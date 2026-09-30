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
// compare equal: lowercase scheme and host, default port dropped, an empty
// path as "/", dot segments resolved, and percent-encoding made canonical.
// Only unreserved characters are decoded; every other escape, including
// %2F, %5C, %3F, and %23, stays an uppercase-hex escape, so an encoded slash
// never becomes a path separator. %2E counts as a dot for dot segments, as
// in WHATWG. The fragment is dropped.
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
	path := resolveDotSegments(canonicalEscapes(parsed.EscapedPath(), pathLiteral))
	query := canonicalEscapes(parsed.RawQuery, queryLiteral)
	return scheme + "://" + host + path + "?" + query, true
}

const upperHex = "0123456789ABCDEF"

func unreserved(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// pathLiteral and queryLiteral are the bytes left unescaped in a path or
// query: unreserved, sub-delims, ':' and '@', plus '/' in a path and '/'
// and '?' in a query.
func pathLiteral(c byte) bool {
	return unreserved(c) || strings.IndexByte("!$&'()*+,;=:@/", c) >= 0
}

func queryLiteral(c byte) bool {
	return unreserved(c) || strings.IndexByte("!$&'()*+,;=:@/?", c) >= 0
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// canonicalEscapes decodes escapes of unreserved characters, writes every
// other escape in uppercase hex, and escapes bytes that are not literal.
func canonicalEscapes(value string, literal func(byte) bool) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '%' && i+2 < len(value) {
			hi, okHi := unhex(value[i+1])
			lo, okLo := unhex(value[i+2])
			if okHi && okLo {
				decoded := hi<<4 | lo
				if unreserved(decoded) {
					out.WriteByte(decoded)
				} else {
					out.WriteByte('%')
					out.WriteByte(upperHex[decoded>>4])
					out.WriteByte(upperHex[decoded&15])
				}
				i += 2
				continue
			}
		}
		if literal(c) {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(upperHex[c>>4])
		out.WriteByte(upperHex[c&15])
	}
	return out.String()
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
