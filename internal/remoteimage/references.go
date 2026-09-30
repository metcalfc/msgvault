package remoteimage

import (
	"net"
	"net/url"
	"strings"
)

// References is the set of normalized <img src> URLs a message's stored
// bodies name. The image proxy fetches only images in this set, so a
// message ID cannot be used as a pass for any URL.
type References map[string]struct{}

// ReferencesOf collects the image references of a message's bodies.
func ReferencesOf(bodies ...string) References {
	refs := References{}
	for _, body := range bodies {
		if body == "" || len(body) > maxArchiveHTMLBytes {
			continue
		}
		mapImageSources(body, func(source string) string {
			if key, ok := ReferenceKey(source); ok {
				refs[key] = struct{}{}
			}
			return ""
		})
	}
	return refs
}

// Contains reports whether the references include target.
func (r References) Contains(target string) bool {
	key, ok := ReferenceKey(target)
	if !ok {
		return false
	}
	_, found := r[key]
	return found
}

// Referenced reports whether target is an <img src> of one of the bodies.
func Referenced(target string, bodies ...string) bool {
	return ReferencesOf(bodies...).Contains(target)
}

// Referenceable reports whether target is a URL an <img src> could name for
// the proxy. Anything else is rejected by Fetch's own validation before any
// network use.
func Referenceable(target string) bool {
	_, ok := ReferenceKey(target)
	return ok
}

// ReferenceKey normalizes an image URL the way the reader's WHATWG URL
// parser serializes it, so the stored HTML and the browser's request
// compare equal: lowercase scheme and host, default port dropped, dot
// segments resolved, an empty path as "/", and the path and query compared
// unescaped so percent-encoding differences do not matter. The fragment is
// dropped.
func ReferenceKey(raw string) (string, bool) {
	normalized := remoteURL(raw)
	if normalized == "" {
		return "", false
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return "", false
	}
	if port := parsed.Port(); port != "" && (scheme != "http" || port != "80") && (scheme != "https" || port != "443") {
		host = net.JoinHostPort(host, port)
	}
	query := parsed.RawQuery
	if unescaped, err := url.QueryUnescape(query); err == nil {
		query = unescaped
	}
	return scheme + "://" + host + resolveDotSegments(parsed.Path) + "?" + query, true
}

// resolveDotSegments removes "." and ".." segments as RFC 3986 section
// 5.2.4 and the WHATWG URL parser do, keeping a trailing slash.
func resolveDotSegments(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	out := make([]string, 0, len(segments))
	for i, segment := range segments {
		last := i == len(segments)-1
		switch segment {
		case ".":
			if last {
				out = append(out, "")
			}
		case "..":
			if len(out) > 1 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, segment)
		}
	}
	resolved := strings.Join(out, "/")
	if !strings.HasPrefix(resolved, "/") {
		resolved = "/" + resolved
	}
	return resolved
}
