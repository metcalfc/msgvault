package remoteimage

import (
	"net"
	"net/url"
	"strings"
)

// Referenced reports whether target is an <img src> of one of the message's
// stored bodies. The image proxy fetches only images a message actually
// references, so a message ID cannot be used as a pass for any URL.
//
// The reader serializes URLs with the WHATWG URL parser and the stored HTML
// may escape them differently, so both sides are compared in a normalized
// form: lowercase scheme and host, default port dropped, empty path as "/",
// and unescaped path and query.
func Referenced(target string, bodies ...string) bool {
	want, ok := referenceKey(target)
	if !ok {
		return false
	}
	found := false
	for _, body := range bodies {
		if found || body == "" || len(body) > maxArchiveHTMLBytes {
			continue
		}
		mapImageSources(body, func(source string) string {
			if key, ok := referenceKey(source); ok && key == want {
				found = true
			}
			return ""
		})
	}
	return found
}

// Referenceable reports whether target is a URL an <img src> could name for
// the proxy. Anything else is rejected by Fetch's own validation before any
// network use.
func Referenceable(target string) bool {
	_, ok := referenceKey(target)
	return ok
}

func referenceKey(raw string) (string, bool) {
	normalized := remoteURL(raw)
	if normalized == "" {
		return "", false
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	if port := parsed.Port(); port != "" && (scheme != "http" || port != "80") && (scheme != "https" || port != "443") {
		host = net.JoinHostPort(host, port)
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	query := parsed.RawQuery
	if unescaped, err := url.QueryUnescape(query); err == nil {
		query = unescaped
	}
	return scheme + "://" + host + path + "?" + query, true
}
