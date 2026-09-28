package carddavserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/carddav"
	"go.kenn.io/msgvault/internal/carddavserver"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/vcard"
	"go.kenn.io/msgvault/internal/vcardmap"
)

type fixture struct {
	store  *store.Store
	server *httptest.Server
	alice  *store.Person
	bob    *store.Person
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	st := testutil.NewTestStore(t)
	alice := seedPerson(t, st, "alice@example.test", "Alice Example")
	bob := seedPerson(t, st, "bob@example.test", "Bob Example")
	handler, err := carddavserver.New(carddavserver.Options{Store: st, DisplayName: "Test Book"})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(carddavserver.WellKnownPath, handler.WellKnown())
	mux.Handle(handler.Prefix()+"/", handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return fixture{store: st, server: server, alice: alice, bob: bob}
}

func seedPerson(t *testing.T, st *store.Store, email, name string) *store.Person {
	t.Helper()
	participantID, err := st.EnsureParticipant(email, name, "example.test")
	require.NoError(t, err)
	person, _, err := st.CreatePersonFromParticipant(participantID)
	require.NoError(t, err)
	_, err = st.AddPersonContactPointContext(t.Context(), person.ID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: email,
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(t, err)
	return person
}

// reply carries what the assertions need from a response; the body has
// already been read and closed.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (f fixture) do(t *testing.T, method, path string, headers map[string]string, body string) (reply, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	require.NoError(t, err)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	return reply{StatusCode: response.StatusCode, Header: response.Header}, string(raw)
}

func propfind(props ...carddav.PropertyName) string {
	body, err := carddav.PropfindBody(props)
	if err != nil {
		panic(err)
	}
	return string(body)
}

func parse(t *testing.T, body string) carddav.MultiStatus {
	t.Helper()
	parsed, err := carddav.ParseMultiStatus([]byte(body), carddav.DefaultXMLLimits())
	require.NoError(t, err)
	return parsed
}

func TestWellKnownRedirectsToDAVRootWithRelativeLocation(t *testing.T) {
	f := newFixture(t)
	response, _ := f.do(t, http.MethodGet, carddavserver.WellKnownPath, nil, "")
	assert.Equal(t, http.StatusMovedPermanently, response.StatusCode)
	assert.Equal(t, "/dav/", response.Header.Get("Location"))
}

func TestOptionsAdvertisesReadOnlyAddressbook(t *testing.T) {
	f := newFixture(t)
	response, _ := f.do(t, http.MethodOptions, "/dav/addressbooks/me/msgvault/", nil, "")
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "1, addressbook", response.Header.Get("DAV"))
	assert.NotContains(t, response.Header.Get("Allow"), "PUT")
}

func TestPropfindDiscoveryChain(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newFixture(t)

	response, body := f.do(t, "PROPFIND", "/dav/", map[string]string{"Depth": "0"}, propfind(carddav.CurrentUserPrincipalProperty))
	require.Equal(http.StatusMultiStatus, response.StatusCode)
	root := parse(t, body)
	require.Len(root.Responses, 1)
	assert.Equal("/dav/", root.Responses[0].Href)
	assert.Equal("/dav/principals/me/", root.Responses[0].PropStats[0].Properties.CurrentUserPrincipal)

	_, body = f.do(t, "PROPFIND", "/dav/principals/me/", map[string]string{"Depth": "0"}, propfind(carddav.AddressbookHomeSetProperty))
	principal := parse(t, body)
	require.Len(principal.Responses, 1)
	assert.Equal([]string{"/dav/addressbooks/me/"}, principal.Responses[0].PropStats[0].Properties.AddressbookHomeSet)

	_, body = f.do(t, "PROPFIND", "/dav/addressbooks/me/", map[string]string{"Depth": "1"}, propfind(
		carddav.ResourceTypeProperty, carddav.DisplayNameProperty, carddav.SupportedReportSetProperty,
		carddav.CurrentUserPrivilegesProperty, carddav.SupportedAddressDataProperty,
	))
	home := parse(t, body)
	require.Len(home.Responses, 2)
	assert.Equal("/dav/addressbooks/me/", home.Responses[0].Href)
	assert.True(home.Responses[0].PropStats[0].Properties.IsCollection)
	book := home.Responses[1]
	assert.Equal("/dav/addressbooks/me/msgvault/", book.Href)
	props := book.PropStats[0].Properties
	assert.True(props.IsAddressBook)
	assert.Equal("Test Book", props.DisplayName)
	assert.True(props.SupportsMultiget)
	assert.False(props.SupportsSync)
	assert.ElementsMatch([]string{"3.0", "4.0"}, props.SupportedVCard)
	assert.True(props.PrivilegesPresent)
	assert.Equal([]string{"read", "read-current-user-privilege-set"}, props.Privileges)
}

func TestPropfindBookListsMembersWithETagsAndUnknownPropsAs404(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newFixture(t)
	body := `<?xml version="1.0"?><D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/" xmlns:X="urn:example:nope">` +
		`<D:prop><D:resourcetype/><D:getetag/><CS:getctag/><X:mystery/></D:prop></D:propfind>`
	response, raw := f.do(t, "PROPFIND", "/dav/addressbooks/me/msgvault/", map[string]string{"Depth": "1"}, body)
	require.Equal(http.StatusMultiStatus, response.StatusCode)
	assert.Contains(raw, "<CS:getctag>")
	assert.Contains(raw, `<mystery xmlns="urn:example:nope"/>`)
	assert.Contains(raw, "HTTP/1.1 404 Not Found")
	listing := parse(t, raw)
	require.Len(listing.Responses, 3)
	hrefs := map[string]bool{}
	for _, member := range listing.Responses[1:] {
		hrefs[member.Href] = true
		require.NotEmpty(member.PropStats)
		assert.NotEmpty(member.PropStats[0].Properties.GetETag)
		assert.False(member.PropStats[0].Properties.IsCollection)
	}
	assert.True(hrefs["/dav/addressbooks/me/msgvault/"+f.alice.VCardUID+".vcf"])
	assert.True(hrefs["/dav/addressbooks/me/msgvault/"+f.bob.VCardUID+".vcf"])
}

func TestGetMemberReturnsVCard30WithETagAndHonorsIfNoneMatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newFixture(t)
	path := "/dav/addressbooks/me/msgvault/" + f.alice.VCardUID + ".vcf"
	response, body := f.do(t, http.MethodGet, path, nil, "")
	require.Equal(http.StatusOK, response.StatusCode)
	assert.Equal("text/vcard; charset=utf-8", response.Header.Get("Content-Type"))
	assert.Contains(body, "VERSION:3.0")
	assert.Contains(body, "UID:"+f.alice.VCardUID)
	assert.Contains(body, "alice@example.test")
	etag := response.Header.Get("ETag")
	require.NotEmpty(etag)

	response, _ = f.do(t, http.MethodGet, path, map[string]string{"If-None-Match": etag}, "")
	assert.Equal(http.StatusNotModified, response.StatusCode)

	response, _ = f.do(t, http.MethodGet, "/dav/addressbooks/me/msgvault/nobody.vcf", nil, "")
	assert.Equal(http.StatusNotFound, response.StatusCode)
}

func TestReportMultigetAndQuery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newFixture(t)
	alicePath := "/dav/addressbooks/me/msgvault/" + f.alice.VCardUID + ".vcf"
	multiget, err := carddav.AddressbookMultigetBody(
		[]carddav.PropertyName{carddav.GetETagProperty, carddav.AddressDataProperty},
		[]string{alicePath, f.server.URL + "/dav/addressbooks/me/msgvault/missing.vcf", "/dav/elsewhere.vcf"},
	)
	require.NoError(err)
	response, raw := f.do(t, "REPORT", "/dav/addressbooks/me/msgvault/", map[string]string{"Depth": "0"}, string(multiget))
	require.Equal(http.StatusMultiStatus, response.StatusCode)
	parsed := parse(t, raw)
	require.Len(parsed.Responses, 3)
	assert.Equal(alicePath, parsed.Responses[0].Href)
	assert.Contains(parsed.Responses[0].PropStats[0].Properties.AddressData, "FN:Alice Example")
	assert.Equal(http.StatusNotFound, parsed.Responses[1].StatusCode)
	assert.Equal(http.StatusNotFound, parsed.Responses[2].StatusCode)

	query, err := carddav.AddressbookQueryBody([]carddav.PropertyName{carddav.GetETagProperty, carddav.AddressDataProperty})
	require.NoError(err)
	response, raw = f.do(t, "REPORT", "/dav/addressbooks/me/msgvault/", map[string]string{"Depth": "1"}, string(query))
	require.Equal(http.StatusMultiStatus, response.StatusCode)
	parsed = parse(t, raw)
	assert.Len(parsed.Responses, 2)

	filtered := `<C:addressbook-query xmlns:C="urn:ietf:params:xml:ns:carddav" xmlns:D="DAV:"><D:prop><D:getetag/></D:prop>` +
		`<C:filter><C:prop-filter name="FN"/></C:filter></C:addressbook-query>`
	response, raw = f.do(t, "REPORT", "/dav/addressbooks/me/msgvault/", nil, filtered)
	assert.Equal(http.StatusForbidden, response.StatusCode)
	assert.Contains(raw, "<C:supported-filter/>")

	v4 := `<C:addressbook-multiget xmlns:C="urn:ietf:params:xml:ns:carddav" xmlns:D="DAV:">` +
		`<D:prop><D:getetag/><C:address-data content-type="text/vcard" version="4.0"/></D:prop>` +
		`<D:href>` + alicePath + `</D:href></C:addressbook-multiget>`
	_, raw = f.do(t, "REPORT", "/dav/addressbooks/me/msgvault/", nil, v4)
	parsed = parse(t, raw)
	require.Len(parsed.Responses, 1)
	assert.Contains(parsed.Responses[0].PropStats[0].Properties.AddressData, "VERSION:4.0")
}

func TestWriteMethodsAreRefusedWithNeedPrivileges(t *testing.T) {
	f := newFixture(t)
	path := "/dav/addressbooks/me/msgvault/" + f.alice.VCardUID + ".vcf"
	for _, method := range []string{http.MethodPut, http.MethodDelete, "PROPPATCH", "MKCOL", "MOVE"} {
		response, raw := f.do(t, method, path, nil, "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:X\r\nEND:VCARD\r\n")
		assert.Equal(t, http.StatusForbidden, response.StatusCode, method)
		assert.Contains(t, raw, "<D:need-privileges/>", method)
	}
	response, _ := f.do(t, http.MethodGet, "/dav/addressbooks/me/msgvault/", nil, "")
	assert.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
}

func TestCTagMovesOnEditAndDelete(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newFixture(t)
	body := `<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop><CS:getctag/></D:prop></D:propfind>`
	ctag := func() string {
		_, raw := f.do(t, "PROPFIND", "/dav/addressbooks/me/msgvault/", map[string]string{"Depth": "0"}, body)
		start := strings.Index(raw, "<CS:getctag>")
		end := strings.Index(raw, "</CS:getctag>")
		require.True(start >= 0 && end > start, raw)
		return raw[start+len("<CS:getctag>") : end]
	}
	initial := ctag()
	require.NotEmpty(initial)
	assert.Equal(initial, ctag())

	_, err := f.store.AddPersonContactPointContext(t.Context(), f.alice.ID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: "alice.work@example.test",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(err)
	afterEdit := ctag()
	assert.NotEqual(initial, afterEdit)

	bob, err := f.store.GetPersonContext(t.Context(), f.bob.ID)
	require.NoError(err)
	require.NoError(f.store.DeletePersonContext(t.Context(), bob.ID, bob.Revision))
	assert.NotEqual(afterEdit, ctag())
}

func TestServedCardOmitsInferredEmployment(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newFixture(t)
	ctx := t.Context()
	current, primary := true, true
	inferredOrg, err := f.store.CreateOrganizationContext(ctx, store.OrganizationInput{Name: "Guessed Corp", Kind: store.OrganizationKindCompany})
	require.NoError(err)
	declaredOrg, err := f.store.CreateOrganizationContext(ctx, store.OrganizationInput{Name: "Declared Inc", Kind: store.OrganizationKindCompany})
	require.NoError(err)
	_, err = f.store.AddEmploymentContext(ctx, store.EmploymentInput{
		PersonID: f.alice.ID, OrganizationID: inferredOrg.ID, Source: store.ProvenanceEnrichment,
		IsCurrent: &current, IsPrimary: &primary,
	})
	require.NoError(err)
	_, err = f.store.AddEmploymentContext(ctx, store.EmploymentInput{
		PersonID: f.bob.ID, OrganizationID: declaredOrg.ID, Source: store.ProvenanceUser,
		IsCurrent: &current, IsPrimary: &primary,
	})
	require.NoError(err)

	// The publication renderer, given the unfiltered snapshot, includes the
	// inferred employer; the served book leaves it out.
	snapshot, err := f.store.LoadPersonVCardSnapshotContext(ctx, f.alice.ID)
	require.NoError(err)
	seed, err := vcardmap.SeedEnvelope(f.alice.VCardUID, "Alice Example", "test", "alice.vcf")
	require.NoError(err)
	unfiltered, err := vcardmap.RenderPersonCard(*snapshot, seed, vcard.Version30)
	require.NoError(err)
	assert.Contains(string(unfiltered.StoredBody), "Guessed Corp")

	_, body := f.do(t, http.MethodGet, "/dav/addressbooks/me/msgvault/"+f.alice.VCardUID+".vcf", nil, "")
	assert.NotContains(body, "Guessed Corp")
	_, body = f.do(t, http.MethodGet, "/dav/addressbooks/me/msgvault/"+f.bob.VCardUID+".vcf", nil, "")
	assert.Contains(body, "ORG:Declared Inc")
}

func TestRequestBodyRejectsDoctypeAndOversize(t *testing.T) {
	f := newFixture(t)
	doctype := `<!DOCTYPE propfind [<!ENTITY x "y">]><D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`
	response, _ := f.do(t, "PROPFIND", "/dav/", nil, doctype)
	assert.Equal(t, http.StatusBadRequest, response.StatusCode)

	huge := `<D:propfind xmlns:D="DAV:"><D:prop>` + strings.Repeat("<D:getetag/>", 200_000) + `</D:prop></D:propfind>`
	response, _ = f.do(t, "PROPFIND", "/dav/", nil, huge)
	assert.Equal(t, http.StatusBadRequest, response.StatusCode)
}
