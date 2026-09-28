package carddavserver

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

const (
	davNS     = "DAV:"
	cardDAVNS = "urn:ietf:params:xml:ns:carddav"
	// calendarServerNS carries getctag, which predates RFC 6578 and is what
	// Apple clients poll.
	calendarServerNS = "http://calendarserver.org/ns/"

	maxRequestDepth    = 32
	maxRequestElements = 5000
)

var errBadRequest = errors.New("malformed DAV request")

type propName struct {
	ns    string
	local string
}

func (p propName) String() string { return p.ns + " " + p.local }

// Property names the server understands.
var (
	propResourceType         = propName{davNS, "resourcetype"}
	propDisplayName          = propName{davNS, "displayname"}
	propCurrentUserPrincipal = propName{davNS, "current-user-principal"}
	propPrincipalURL         = propName{davNS, "principal-URL"}
	propOwner                = propName{davNS, "owner"}
	propGetETag              = propName{davNS, "getetag"}
	propGetContentType       = propName{davNS, "getcontenttype"}
	propGetContentLength     = propName{davNS, "getcontentlength"}
	propSupportedReportSet   = propName{davNS, "supported-report-set"}
	propCurrentUserPrivilege = propName{davNS, "current-user-privilege-set"}
	propAddressbookHomeSet   = propName{cardDAVNS, "addressbook-home-set"}
	propSupportedAddressData = propName{cardDAVNS, "supported-address-data"}
	propMaxResourceSize      = propName{cardDAVNS, "max-resource-size"}
	propAddressbookDesc      = propName{cardDAVNS, "addressbook-description"}
	propAddressData          = propName{cardDAVNS, "address-data"}
	propGetCTag              = propName{calendarServerNS, "getctag"}
)

// davRequest is the parsed body of a PROPFIND or REPORT.
type davRequest struct {
	root propName
	// allprop is set for PROPFIND allprop or an empty body.
	allprop  bool
	propname bool
	props    []propName
	// addressDataVersion is the version attribute on CARDDAV:address-data,
	// empty when the element was absent or carried none.
	addressDataVersion     string
	addressDataContentType string
	hrefs                  []string
	// filterChildren counts elements inside CARDDAV:filter. Anything other
	// than an empty filter is unsupported.
	filterChildren int
}

// parseRequestBody reads a bounded XML body without external entities or
// DOCTYPE and walks it as tokens, collecting only what the handlers need.
// An empty PROPFIND body means allprop.
func parseRequestBody(r *http.Request) (davRequest, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxRequestBody))
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			return davRequest{}, fmt.Errorf("%w: body exceeds %d bytes", errBadRequest, maxRequestBody)
		}
		return davRequest{}, fmt.Errorf("%w: %w", errBadRequest, err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return davRequest{allprop: true}, nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.Strict = true
	// No entity expansion beyond the predefined five and no external DTDs.
	decoder.Entity = map[string]string{}

	var parsed davRequest
	var stack []xml.Name
	elements := 0
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return davRequest{}, fmt.Errorf("%w: %w", errBadRequest, err)
		}
		switch typed := token.(type) {
		case xml.Directive:
			return davRequest{}, fmt.Errorf("%w: directives are not accepted", errBadRequest)
		case xml.StartElement:
			elements++
			if elements > maxRequestElements || len(stack) >= maxRequestDepth {
				return davRequest{}, fmt.Errorf("%w: request too large", errBadRequest)
			}
			text.Reset()
			parsed.startElement(typed, stack)
			stack = append(stack, typed.Name)
		case xml.CharData:
			text.Write(typed)
		case xml.EndElement:
			if len(stack) == 0 {
				return davRequest{}, fmt.Errorf("%w: unbalanced element", errBadRequest)
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if name == (xml.Name{Space: davNS, Local: "href"}) && len(stack) == 1 {
				parsed.hrefs = append(parsed.hrefs, strings.TrimSpace(text.String()))
			}
			text.Reset()
		}
	}
	if parsed.root == (propName{}) {
		return davRequest{}, fmt.Errorf("%w: missing root element", errBadRequest)
	}
	return parsed, nil
}

func (d *davRequest) startElement(element xml.StartElement, stack []xml.Name) {
	name := propName{ns: element.Name.Space, local: element.Name.Local}
	switch len(stack) {
	case 0:
		d.root = name
	case 1:
		switch name {
		case propName{davNS, "allprop"}:
			d.allprop = true
		case propName{davNS, "propname"}:
			d.propname = true
		}
	case 2:
		switch stack[1] {
		case xml.Name{Space: davNS, Local: "prop"}:
			d.props = append(d.props, name)
			if name == propAddressData {
				for _, attr := range element.Attr {
					switch attr.Name.Local {
					case "version":
						d.addressDataVersion = strings.TrimSpace(attr.Value)
					case "content-type":
						d.addressDataContentType = strings.TrimSpace(attr.Value)
					}
				}
			}
		case xml.Name{Space: cardDAVNS, Local: "filter"}:
			d.filterChildren++
		}
	}
}

// wants reports whether the request asked for the property, or asked for
// everything.
func (d *davRequest) wants(name propName) bool {
	return d.allprop || slices.Contains(d.props, name)
}
