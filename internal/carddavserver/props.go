package carddavserver

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/vcard"
)

// propSet is the live properties of one resource, each as pre-rendered inner
// XML with the D:, C:, and CS: prefixes declared on the multistatus root.
type propSet map[propName]string

func escape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func hrefElement(href string) string {
	return "<D:href>" + escape(href) + "</D:href>"
}

// commonProps are present on every served resource.
func (h *Handler) commonProps() propSet {
	return propSet{
		propCurrentUserPrincipal: hrefElement(h.principalPath()),
		propOwner:                hrefElement(h.principalPath()),
	}
}

func (h *Handler) collectionProps(displayName string) propSet {
	props := h.commonProps()
	props[propResourceType] = "<D:collection/>"
	props[propDisplayName] = escape(displayName)
	return props
}

func (h *Handler) principalProps() propSet {
	props := h.collectionProps(h.displayName)
	props[propResourceType] = "<D:collection/><D:principal/>"
	props[propPrincipalURL] = hrefElement(h.principalPath())
	props[propAddressbookHomeSet] = hrefElement(h.homePath())
	return props
}

func (h *Handler) bookProps(ctx context.Context) (propSet, error) {
	ctag, err := h.ctag(ctx)
	if err != nil {
		return nil, err
	}
	props := h.collectionProps(h.displayName)
	props[propResourceType] = "<D:collection/><C:addressbook/>"
	props[propAddressbookDesc] = escape("People in msgvault. Read-only; edit in msgvault.")
	props[propGetCTag] = escape(ctag)
	props[propSupportedReportSet] = "<D:supported-report><D:report><C:addressbook-multiget/></D:report></D:supported-report>" +
		"<D:supported-report><D:report><C:addressbook-query/></D:report></D:supported-report>"
	props[propSupportedAddressData] = `<C:address-data-type content-type="text/vcard" version="3.0"/>` +
		`<C:address-data-type content-type="text/vcard" version="4.0"/>`
	props[propMaxResourceSize] = strconv.Itoa(maxResourceSize)
	props[propCurrentUserPrivilege] = "<D:privilege><D:read/></D:privilege>" +
		"<D:privilege><D:read-current-user-privilege-set/></D:privilege>"
	return props, nil
}

func (h *Handler) memberProps(card renderedCard) propSet {
	props := h.commonProps()
	props[propResourceType] = ""
	props[propGetETag] = escape(card.etag)
	props[propGetContentType] = "text/vcard; charset=utf-8"
	props[propGetContentLength] = strconv.Itoa(len(card.body))
	props[propAddressData] = escape(string(card.body))
	return props
}

// multistatusWriter accumulates D:response elements.
type multistatusWriter struct {
	buffer bytes.Buffer
}

func newMultistatusWriter() *multistatusWriter {
	w := &multistatusWriter{}
	w.buffer.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	w.buffer.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/">` + "\n")
	return w
}

func prefixed(name propName) (string, bool) {
	switch name.ns {
	case davNS:
		return "D:" + name.local, true
	case cardDAVNS:
		return "C:" + name.local, true
	case calendarServerNS:
		return "CS:" + name.local, true
	default:
		return "", false
	}
}

func sortedNames(props propSet) []propName {
	names := make([]propName, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i].String() < names[j].String() })
	return names
}

func writeProp(buffer *bytes.Buffer, name propName, inner string, present bool) {
	qualified, ok := prefixed(name)
	if !ok {
		// An unknown namespace is declared inline on the element itself.
		qualified = name.local
		buffer.WriteString("<" + qualified + ` xmlns="` + escape(name.ns) + `"`)
		if !present || inner == "" {
			buffer.WriteString("/>")
			return
		}
		buffer.WriteString(">" + inner + "</" + qualified + ">")
		return
	}
	if !present || inner == "" {
		buffer.WriteString("<" + qualified + "/>")
		return
	}
	buffer.WriteString("<" + qualified + ">" + inner + "</" + qualified + ">")
}

// response writes one D:response. Requested properties the resource has go in
// a 200 propstat; the rest go in a 404 propstat so a client learns exactly
// which of its questions were unanswered.
func (w *multistatusWriter) response(href string, props propSet, request davRequest) {
	w.buffer.WriteString("<D:response>" + hrefElement(href))
	var found, missing bytes.Buffer
	if request.allprop || request.propname {
		for _, name := range sortedNames(props) {
			inner := props[name]
			if name == propAddressData {
				continue
			}
			if request.propname {
				inner = ""
			}
			writeProp(&found, name, inner, true)
		}
	} else {
		for _, name := range request.props {
			inner, ok := props[name]
			if ok {
				writeProp(&found, name, inner, true)
			} else {
				writeProp(&missing, name, "", false)
			}
		}
	}
	if found.Len() > 0 || missing.Len() == 0 {
		w.buffer.WriteString("<D:propstat><D:prop>")
		w.buffer.Write(found.Bytes())
		w.buffer.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>")
	}
	if missing.Len() > 0 {
		w.buffer.WriteString("<D:propstat><D:prop>")
		w.buffer.Write(missing.Bytes())
		w.buffer.WriteString("</D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>")
	}
	w.buffer.WriteString("</D:response>\n")
}

// notFound writes a D:response whose whole resource is missing.
func (w *multistatusWriter) notFound(href string) {
	w.buffer.WriteString("<D:response>" + hrefElement(href) + "<D:status>HTTP/1.1 404 Not Found</D:status></D:response>\n")
}

func (w *multistatusWriter) finish(rw http.ResponseWriter) {
	w.buffer.WriteString("</D:multistatus>\n")
	rw.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	rw.Header().Set("Content-Length", strconv.Itoa(w.buffer.Len()))
	rw.WriteHeader(http.StatusMultiStatus)
	_, _ = rw.Write(w.buffer.Bytes())
}

// writeDAVError answers 403 with an RFC 4918 error body naming the failed
// precondition.
func writeDAVError(w http.ResponseWriter, condition string) {
	body := `<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<D:error xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">` + condition + `</D:error>` + "\n"
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(body))
}

func (h *Handler) writeParseError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errBadRequest) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.writeLookupError(w, r, err)
}

// depth reads the Depth header. RFC 4918 defaults to infinity, which this
// server treats as 1: its deepest collection holds only members.
func depth(r *http.Request) int {
	switch strings.TrimSpace(r.Header.Get("Depth")) {
	case "0":
		return 0
	default:
		return 1
	}
}

// resourceProps returns the live properties of a resource plus, at depth 1,
// its children. Members are rendered only when the request asks for a
// property that needs the card.
func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request, res resource) {
	request, err := parseRequestBody(r)
	if err != nil {
		h.writeParseError(w, r, err)
		return
	}
	if request.root != (propName{}) && request.root != (propName{davNS, "propfind"}) {
		http.Error(w, "expected DAV:propfind", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	writer := newMultistatusWriter()
	children := depth(r) == 1
	switch res.kind {
	case kindRoot:
		writer.response(res.href, h.collectionProps(h.displayName), request)
		if children {
			writer.response(h.prefix+"/principals/", h.collectionProps("Principals"), request)
			writer.response(h.prefix+"/addressbooks/", h.collectionProps("Address books"), request)
		}
	case kindPrincipals:
		writer.response(res.href, h.collectionProps("Principals"), request)
		if children {
			writer.response(h.principalPath(), h.principalProps(), request)
		}
	case kindPrincipal:
		writer.response(res.href, h.principalProps(), request)
	case kindAddressbooks:
		writer.response(res.href, h.collectionProps("Address books"), request)
		if children {
			writer.response(h.homePath(), h.collectionProps(h.displayName), request)
		}
	case kindHome:
		writer.response(res.href, h.collectionProps(h.displayName), request)
		if children {
			props, err := h.bookProps(ctx)
			if err != nil {
				h.writeLookupError(w, r, err)
				return
			}
			writer.response(h.bookPath(), props, request)
		}
	case kindBook:
		props, err := h.bookProps(ctx)
		if err != nil {
			h.writeLookupError(w, r, err)
			return
		}
		writer.response(res.href, props, request)
		if children {
			if err := h.listMembers(ctx, writer, request); err != nil {
				h.writeLookupError(w, r, err)
				return
			}
		}
	case kindMember:
		card, err := h.memberCard(ctx, res.uid, vcard.Version30)
		if err != nil {
			h.writeLookupError(w, r, err)
			return
		}
		writer.response(res.href, h.memberProps(card), request)
	default:
		http.NotFound(w, r)
		return
	}
	writer.finish(w)
}

// listMembers writes one response per person. A person that fails to render
// is left out and logged; the rest of the book still lists.
func (h *Handler) listMembers(ctx context.Context, writer *multistatusWriter, request davRequest) error {
	persons, err := h.store.ListPersonsContext(ctx)
	if err != nil {
		return err
	}
	needsCard := request.allprop || request.wants(propGetETag) || request.wants(propGetContentLength)
	for _, person := range persons {
		if !needsCard {
			props := h.commonProps()
			props[propResourceType] = ""
			props[propGetContentType] = "text/vcard; charset=utf-8"
			writer.response(h.memberPath(person.VCardUID), props, request)
			continue
		}
		card, err := h.renderer.render(ctx, person, vcard.Version30)
		if err != nil {
			h.logger.WarnContext(ctx, "carddav server skipped person", "person_id", person.ID, "error", err)
			continue
		}
		props := h.memberProps(card)
		delete(props, propAddressData)
		writer.response(h.memberPath(person.VCardUID), props, request)
	}
	return nil
}

// handleReport answers addressbook-multiget and addressbook-query on the book.
func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request, res resource) {
	if res.kind != kindBook {
		writeDAVError(w, "<D:supported-report/>")
		return
	}
	request, err := parseRequestBody(r)
	if err != nil {
		h.writeParseError(w, r, err)
		return
	}
	version, ok := requestedVersion(request)
	if !ok {
		writeDAVError(w, "<C:supported-address-data/>")
		return
	}
	ctx := r.Context()
	writer := newMultistatusWriter()
	switch request.root {
	case propName{cardDAVNS, "addressbook-multiget"}:
		if len(request.hrefs) == 0 {
			http.Error(w, "addressbook-multiget requires at least one href", http.StatusBadRequest)
			return
		}
		if len(request.hrefs) > maxMultigetHref {
			writeDAVError(w, "<D:number-of-matches-within-limits/>")
			return
		}
		for _, href := range request.hrefs {
			h.multigetResponse(ctx, writer, request, href, version)
		}
	case propName{cardDAVNS, "addressbook-query"}:
		if request.filterChildren > 0 {
			writeDAVError(w, "<C:supported-filter/>")
			return
		}
		persons, err := h.store.ListPersonsContext(ctx)
		if err != nil {
			h.writeLookupError(w, r, err)
			return
		}
		for _, person := range persons {
			card, err := h.renderer.render(ctx, person, version)
			if err != nil {
				h.logger.WarnContext(ctx, "carddav server skipped person", "person_id", person.ID, "error", err)
				continue
			}
			writer.response(h.memberPath(person.VCardUID), h.memberProps(card), request)
		}
	default:
		writeDAVError(w, "<D:supported-report/>")
		return
	}
	writer.finish(w)
}

// requestedVersion reads the address-data version a REPORT asked for. The
// default is 3.0, which Apple clients request and round-trip.
func requestedVersion(request davRequest) (vcard.Version, bool) {
	contentType := strings.ToLower(request.addressDataContentType)
	if contentType != "" && contentType != "text/vcard" && contentType != "text/x-vcard" {
		return "", false
	}
	switch request.addressDataVersion {
	case "", "3.0":
		return vcard.Version30, true
	case "4.0":
		return vcard.Version40, true
	default:
		return "", false
	}
}

// multigetResponse resolves one requested href against the book. Hrefs may be
// absolute paths or absolute URLs; only members of this book are honored, and
// anything else is a 404 response rather than an error for the whole report.
func (h *Handler) multigetResponse(
	ctx context.Context, writer *multistatusWriter, request davRequest, href string, version vcard.Version,
) {
	requestPath := href
	if strings.Contains(href, "://") {
		if parsed, err := parseAbsolute(href); err == nil {
			requestPath = parsed
		} else {
			writer.notFound(href)
			return
		}
	}
	res, ok := h.resolve(requestPath)
	if !ok || res.kind != kindMember {
		writer.notFound(href)
		return
	}
	card, err := h.memberCard(ctx, res.uid, version)
	if err != nil {
		if !errors.Is(err, store.ErrPersonNotFound) {
			h.logger.WarnContext(ctx, "carddav server skipped person", "uid", res.uid, "error", err)
		}
		writer.notFound(href)
		return
	}
	writer.response(href, h.memberProps(card), request)
}

func parseAbsolute(href string) (string, error) {
	scheme, rest, ok := strings.Cut(href, "://")
	if !ok || scheme == "" {
		return "", errors.New("not an absolute URL")
	}
	_, requestPath, ok := strings.Cut(rest, "/")
	if !ok {
		return "/", nil
	}
	if query := strings.IndexAny(requestPath, "?#"); query >= 0 {
		requestPath = requestPath[:query]
	}
	return "/" + requestPath, nil
}
