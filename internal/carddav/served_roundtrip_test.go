package carddav

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/carddavserver"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// The served address book is exercised by msgvault's own CardDAV client:
// discovery from the root through /.well-known/carddav, a full pull through
// addressbook-query and multiget, then incremental pulls after a profile edit
// and a person deletion. Production code runs on both sides.
func TestServedAddressBookRoundTripsThroughOwnClient(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	serverStore := testutil.NewTestStore(t)
	alice := seedServedPerson(t, serverStore, "alice@example.test", "Alice Example")
	bob := seedServedPerson(t, serverStore, "bob@example.test", "Bob Example")
	handler, err := carddavserver.New(carddavserver.Options{Store: serverStore, DisplayName: "Served"})
	require.NoError(err)
	mux := http.NewServeMux()
	mux.Handle(carddavserver.WellKnownPath, handler.WellKnown())
	mux.Handle(handler.Prefix()+"/", handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := NewClient(ClientOptions{
		CredentialOrigin: mustParseURL(t, server.URL), Username: "device", Password: "secret",
		AllowInsecureCredentials: true,
	})
	require.NoError(err)
	client.allowPrivateOrigin = true

	discovery, err := Discover(ctx, client, server.URL+"/")
	require.NoError(err)
	assert.Equal(server.URL+"/dav/principals/me/", discovery.PrincipalURL.String())
	assert.Equal(server.URL+"/dav/addressbooks/me/", discovery.HomeURL.String())
	require.Len(discovery.Books, 1)
	book := discovery.Books[0]
	assert.Equal(server.URL+"/dav/addressbooks/me/msgvault/", book.URL.String())
	assert.Equal("Served", book.DisplayName)
	assert.True(book.SupportsMultiget)
	assert.False(book.SupportsSyncCollection)
	assert.ElementsMatch([]string{"3.0", "4.0"}, book.SupportedVCardVersions)
	assert.Equal(BookCapabilities{CreateKnown: true, UpdateKnown: true, DeleteKnown: true}, book.Capabilities)

	clientStore := testutil.NewTestStore(t)
	service := NewService(clientStore, client)
	require.NoError(service.PersistDiscovery(ctx, server.URL, "device", discovery, false))
	books, err := service.ListBooks(ctx)
	require.NoError(err)
	require.Len(books, 1)
	assert.False(books[0].IsWriteTarget, "a read-only book must not become the write target")

	first, err := service.Sync(ctx, SyncOptions{Full: true})
	require.NoError(err)
	assert.Equal(2, first.Created)
	resources, err := clientStore.ListCardDAVResourcesContext(ctx, books[0].ID)
	require.NoError(err)
	require.Len(resources, 2)
	byUID := map[string]store.CardDAVResource{}
	for _, resource := range resources {
		byUID[resource.RemoteUID] = resource
	}
	require.Contains(byUID, alice.VCardUID)
	require.Contains(byUID, bob.VCardUID)
	assert.Contains(string(byUID[alice.VCardUID].RemoteBody), "EMAIL:alice@example.test")
	assert.Equal(server.URL+"/dav/addressbooks/me/msgvault/"+alice.VCardUID+".vcf", byUID[alice.VCardUID].Href)

	_, err = serverStore.AddPersonContactPointContext(ctx, alice.ID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: "alice.work@example.test",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(err)
	second, err := service.Sync(ctx, SyncOptions{})
	require.NoError(err)
	assert.Equal(1, second.Updated)
	assert.Equal(0, second.Created)
	assert.Equal(0, second.Removed)
	updated, err := clientStore.GetCardDAVResourceContext(ctx, books[0].ID, byUID[alice.VCardUID].Href)
	require.NoError(err)
	assert.Contains(string(updated.RemoteBody), "alice.work@example.test")
	assert.NotEqual(byUID[alice.VCardUID].RemoteETag, updated.RemoteETag)

	current, err := serverStore.GetPersonContext(ctx, bob.ID)
	require.NoError(err)
	require.NoError(serverStore.DeletePersonContext(ctx, bob.ID, current.Revision))
	third, err := service.Sync(ctx, SyncOptions{})
	require.NoError(err)
	assert.Equal(1, third.Removed)
	remaining, err := clientStore.ListCardDAVResourcesContext(ctx, books[0].ID)
	require.NoError(err)
	require.Len(remaining, 1)
	assert.Equal(alice.VCardUID, remaining[0].RemoteUID)
}

func seedServedPerson(t *testing.T, st *store.Store, email, name string) *store.Person {
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
