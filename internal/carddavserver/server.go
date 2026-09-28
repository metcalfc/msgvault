// Package carddavserver serves msgvault people as a read-only CardDAV address
// book so iOS and macOS Contacts can subscribe to the daemon directly. It is
// the inverse of internal/carddav, which is the client msgvault uses to talk
// to other people's servers. Both render a person through
// vcardmap.RenderPersonCard, so a device sees the same card a remote book
// would receive, minus facts msgvault only inferred.
//
// The package owns the DAV surface only. Authentication, throttling, and
// transport security belong to whoever mounts the Handler.
package carddavserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/vcard"
)

const (
	// DefaultPrefix is the path the DAV tree is mounted under.
	DefaultPrefix = "/dav"
	// WellKnownPath is where RFC 6764 discovery starts.
	WellKnownPath = "/.well-known/carddav"

	bookSlug        = "msgvault"
	maxRequestBody  = 1 << 20
	maxMultigetHref = 500
	// maxResourceSize is advertised as CARDDAV:max-resource-size and caps a
	// served card; a PHOTO that would push a card past it is dropped.
	maxResourceSize = 1 << 20
)

// Options configures a Handler.
type Options struct {
	Store *store.Store
	// DisplayName is the address book name shown by the device.
	DisplayName string
	// Prefix is the mount path; DefaultPrefix when empty.
	Prefix string
	Logger *slog.Logger
}

// Handler answers CardDAV requests for the served address book.
type Handler struct {
	store       *store.Store
	displayName string
	prefix      string
	logger      *slog.Logger
	renderer    *renderer
}

// New validates options and returns a Handler. Mount it at Prefix and
// WellKnown at WellKnownPath.
func New(options Options) (*Handler, error) {
	if options.Store == nil {
		return nil, errors.New("carddavserver: store is required")
	}
	prefix := options.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if !strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || path.Clean(prefix) != prefix {
		return nil, fmt.Errorf("carddavserver: prefix %q must be a clean absolute path without a trailing slash", prefix)
	}
	displayName := strings.TrimSpace(options.DisplayName)
	if displayName == "" {
		displayName = "msgvault"
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		store: options.Store, displayName: displayName, prefix: prefix, logger: logger,
		renderer: newRenderer(options.Store),
	}, nil
}

// Prefix returns the mount path.
func (h *Handler) Prefix() string { return h.prefix }

// WellKnown redirects RFC 6764 discovery to the DAV root. The Location is a
// relative path so the scheme and host come from whatever the client used,
// which matters behind a TLS-terminating proxy.
func (h *Handler) WellKnown() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", h.prefix+"/")
		w.WriteHeader(http.StatusMovedPermanently)
	})
}

// Paths relative to the prefix.
func (h *Handler) rootPath() string      { return h.prefix + "/" }
func (h *Handler) principalPath() string { return h.prefix + "/principals/me/" }
func (h *Handler) homePath() string      { return h.prefix + "/addressbooks/me/" }
func (h *Handler) bookPath() string      { return h.homePath() + bookSlug + "/" }
func (h *Handler) memberPath(uid string) string {
	return h.bookPath() + url.PathEscape(uid) + ".vcf"
}

type resourceKind int

const (
	kindNone resourceKind = iota
	kindRoot
	kindPrincipals
	kindPrincipal
	kindAddressbooks
	kindHome
	kindBook
	kindMember
)

type resource struct {
	kind resourceKind
	// href is the canonical path of the resource.
	href string
	// uid is set for members.
	uid string
}

// resolve maps a request path to a served resource. Collections are
// recognized with or without a trailing slash; the canonical href always has
// one.
func (h *Handler) resolve(requestPath string) (resource, bool) {
	if requestPath != h.prefix && !strings.HasPrefix(requestPath, h.prefix+"/") {
		return resource{}, false
	}
	rel := strings.TrimPrefix(requestPath, h.prefix)
	cleaned := path.Clean("/" + rel)
	switch cleaned {
	case "/":
		return resource{kind: kindRoot, href: h.rootPath()}, true
	case "/principals":
		return resource{kind: kindPrincipals, href: h.prefix + "/principals/"}, true
	case "/principals/me":
		return resource{kind: kindPrincipal, href: h.principalPath()}, true
	case "/addressbooks":
		return resource{kind: kindAddressbooks, href: h.prefix + "/addressbooks/"}, true
	case "/addressbooks/me":
		return resource{kind: kindHome, href: h.homePath()}, true
	case "/addressbooks/me/" + bookSlug:
		return resource{kind: kindBook, href: h.bookPath()}, true
	}
	memberPrefix := "/addressbooks/me/" + bookSlug + "/"
	if !strings.HasPrefix(cleaned, memberPrefix) || strings.HasSuffix(requestPath, "/") {
		return resource{}, false
	}
	name := strings.TrimPrefix(cleaned, memberPrefix)
	if strings.Contains(name, "/") || !strings.HasSuffix(name, ".vcf") {
		return resource{}, false
	}
	uid, err := url.PathUnescape(strings.TrimSuffix(name, ".vcf"))
	if err != nil || uid == "" {
		return resource{}, false
	}
	return resource{kind: kindMember, href: h.memberPath(uid), uid: uid}, true
}

// ServeHTTP dispatches by method. Write methods are refused with 403 so a
// device treats the book as read-only rather than broken.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolve(r.URL.EscapedPath())
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodOptions:
		h.writeDAVHeaders(w)
		w.WriteHeader(http.StatusOK)
	case "PROPFIND":
		h.handlePropfind(w, r, res)
	case "REPORT":
		h.handleReport(w, r, res)
	case http.MethodGet, http.MethodHead:
		h.handleGet(w, r, res)
	case http.MethodPut, http.MethodDelete, http.MethodPost, http.MethodPatch,
		"MKCOL", "MOVE", "COPY", "PROPPATCH", "LOCK", "UNLOCK", "MKCALENDAR":
		h.writeDAVHeaders(w)
		writeDAVError(w, `<D:need-privileges/>`)
	default:
		h.writeDAVHeaders(w)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) writeDAVHeaders(w http.ResponseWriter) {
	w.Header().Set("DAV", "1, addressbook")
	w.Header().Set("Allow", "OPTIONS, PROPFIND, REPORT, GET, HEAD")
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request, res resource) {
	if res.kind != kindMember {
		h.writeDAVHeaders(w)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	card, err := h.memberCard(r.Context(), res.uid, vcard.Version30)
	if err != nil {
		h.writeLookupError(w, r, err)
		return
	}
	w.Header().Set("ETag", card.etag)
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	if matches := r.Header.Get("If-None-Match"); matches != "" && etagMatches(matches, card.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(card.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(card.body)
}

func etagMatches(header, etag string) bool {
	for candidate := range strings.SplitSeq(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// memberCard resolves a UID to a live person and renders it. A retired UID
// (a merged-away person) is not a redirect: the card is gone and the survivor
// appears under its own UID.
func (h *Handler) memberCard(ctx context.Context, uid string, version vcard.Version) (renderedCard, error) {
	person, err := h.store.ResolvePersonByVCardUIDContext(ctx, uid)
	if err != nil {
		return renderedCard{}, err
	}
	return h.renderer.render(ctx, *person, version)
}

func (h *Handler) writeLookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrPersonNotFound) {
		http.NotFound(w, r)
		return
	}
	h.logger.ErrorContext(r.Context(), "carddav server request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// ctag digests the persons table. Any add, delete, merge, rename, or
// projected edit changes it; devices re-list the book when it moves.
func (h *Handler) ctag(ctx context.Context) (string, error) {
	digest, err := h.store.PersonCatalogDigestContext(ctx)
	if err != nil {
		return "", err
	}
	return vcard.ContentHash([]byte(fmt.Sprintf("%d:%d:%d:%d",
		digest.Count, digest.MaxID, digest.RevisionSum, digest.ProjectionRevisionSum))), nil
}
