package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// This catches a selection rule that lets a phone or handle outrank an email
// address, or that is not deterministic within a class.
func TestSelectPrimaryIdentifierPrefersEmailThenPhoneThenHandle(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []store.PrimaryIdentifierCandidate
		want       *store.PrimaryIdentifier
	}{
		{
			name: "email beats a better-ranked phone and handle",
			candidates: []store.PrimaryIdentifierCandidate{
				{RawKind: "username", Value: "synthetic.handle", Rank: []int64{0}},
				{RawKind: "phone", Value: "+15550100001", Rank: []int64{0}},
				{RawKind: "EMAIL", Value: " later@example.com ", Rank: []int64{9}},
			},
			want: &store.PrimaryIdentifier{Kind: store.PrimaryIdentifierEmail, Value: "later@example.com"},
		},
		{
			name: "phone beats a handle",
			candidates: []store.PrimaryIdentifierCandidate{
				{RawKind: "imessage", Value: "chat-id-1", Rank: []int64{0}},
				{RawKind: "phone", Value: "+15550100002", Rank: []int64{5}},
			},
			want: &store.PrimaryIdentifier{Kind: store.PrimaryIdentifierPhone, Value: "+15550100002"},
		},
		{
			name: "handle is the last resort and names or URLs never qualify",
			candidates: []store.PrimaryIdentifierCandidate{
				{RawKind: "name", Value: "Synthetic Person", Rank: []int64{0}},
				{RawKind: "url", Value: "https://example.com/profile", Rank: []int64{0}},
				{RawKind: "impp", Value: "xmpp:person@example.org", Rank: []int64{1}},
			},
			want: &store.PrimaryIdentifier{Kind: store.PrimaryIdentifierHandle, Value: "xmpp:person@example.org"},
		},
		{
			name: "rank then value break ties within a class",
			candidates: []store.PrimaryIdentifierCandidate{
				{RawKind: "email", Value: "b@example.com", Rank: []int64{1, 0}},
				{RawKind: "email", Value: "c@example.com", Rank: []int64{0, 2}},
				{RawKind: "email", Value: "a@example.com", Rank: []int64{0, 2}},
			},
			want: &store.PrimaryIdentifier{Kind: store.PrimaryIdentifierEmail, Value: "a@example.com"},
		},
		{
			name: "blank values and non-identifiers yield nothing",
			candidates: []store.PrimaryIdentifierCandidate{
				{RawKind: "email", Value: "  "},
				{RawKind: "social", Value: "https://example.com/social"},
			},
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, store.SelectPrimaryIdentifier(tc.candidates))
		})
	}
}

// This catches a Directory row that shows a phone or handle while an email
// exists, prefers an observed address over the person's curated one, or omits
// the identifier for people known only by a phone number or handle.
func TestDirectoryPeoplePageContextReturnsPrimaryIdentifier(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()

	curated := directoryPersonFromParticipant(t, st, func() (int64, error) {
		return st.EnsureParticipant("observed-a@example.com", "Person A", "example.com")
	})
	addDirectoryContactPoint(t, st, curated.ID, store.ContactAddressPhone, "+15550100010")
	addDirectoryContactPoint(t, st, curated.ID, store.ContactAddressEmail, "curated-a@example.com")

	observedEmail := directoryPersonFromParticipant(t, st, func() (int64, error) {
		return st.EnsureParticipant("observed-b@example.com", "Person B", "example.com")
	})
	addDirectoryContactPoint(t, st, observedEmail.ID, store.ContactAddressPhone, "+15550100011")

	phoneOnly := directoryPersonFromParticipant(t, st, func() (int64, error) {
		return st.EnsureParticipantByPhone("+15550100012", "Person C", "phone")
	})

	handleOnly := directoryPersonFromParticipant(t, st, func() (int64, error) {
		return st.EnsureParticipantByIdentifier("username", "synthetic.person.d", "Person D")
	})

	page, err := st.DirectoryPeoplePageContext(ctx, store.DirectoryPeopleQuery{})
	require.NoError(err)
	byID := make(map[int64]*store.PrimaryIdentifier, len(page.People))
	for _, person := range page.People {
		byID[person.ID] = person.PrimaryIdentifier
	}
	require.Len(byID, 4)
	assert.Equal(&store.PrimaryIdentifier{Kind: "email", Value: "curated-a@example.com"}, byID[curated.ID],
		"a curated email outranks the observed one and the curated phone")
	assert.Equal(&store.PrimaryIdentifier{Kind: "email", Value: "observed-b@example.com"}, byID[observedEmail.ID],
		"an observed email outranks a curated phone")
	assert.Equal(&store.PrimaryIdentifier{Kind: "phone", Value: "+15550100012"}, byID[phoneOnly.ID])
	assert.Equal(&store.PrimaryIdentifier{Kind: "handle", Value: "synthetic.person.d"}, byID[handleOnly.ID])
}

// This catches name and activity filters applied after pagination (which
// would return short pages or skip rows) and cursors that ignore the filter.
func TestDirectoryPeoplePageContextFiltersNameAndActivityAcrossPages(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()

	named := make([]int64, 0, 3)
	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		named = append(named, createDirectoryPerson(t, st, name+" Example", name+"@example.com", "friend", "active", "Acme").ID)
	}
	quiet := createDirectoryPerson(t, st, "Delta Example", "delta@example.com", "friend", "inactive", "Acme").ID
	unnamed := make([]int64, 0, 3)
	for index, phone := range []string{"+15550100020", "+15550100021", "+15550100022"} {
		person := directoryPersonFromParticipant(t, st, func() (int64, error) {
			return st.EnsureParticipantByPhone(phone, "", "phone")
		})
		if index == 0 {
			_, err := st.DB().ExecContext(ctx, st.Rebind(`INSERT INTO person_contact_state (
				person_id, last_contact_at, interaction_count) VALUES (?, CURRENT_TIMESTAMP, 1)`), person.ID)
			require.NoError(err)
		}
		unnamed = append(unnamed, person.ID)
	}
	yes, no := true, false

	collect := func(query store.DirectoryPeopleQuery) []int64 {
		query.Limit = 1
		ids := []int64{}
		for {
			page, err := st.DirectoryPeoplePageContext(ctx, query)
			require.NoError(err)
			require.LessOrEqual(len(page.People), 1)
			ids = append(ids, directoryPersonIDs(page.People)...)
			if page.NextCursor == "" {
				return ids
			}
			query.Cursor = page.NextCursor
		}
	}

	assert.Equal(append(append([]int64{}, named...), quiet), collect(store.DirectoryPeopleQuery{HasName: &yes}))
	assert.ElementsMatch(unnamed, collect(store.DirectoryPeopleQuery{HasName: &no}))
	assert.ElementsMatch(append(append([]int64{}, named...), unnamed[0]), collect(store.DirectoryPeopleQuery{HasActivity: &yes}))
	assert.ElementsMatch([]int64{quiet, unnamed[1], unnamed[2]}, collect(store.DirectoryPeopleQuery{HasActivity: &no}))
	assert.Equal(named, collect(store.DirectoryPeopleQuery{HasName: &yes, HasActivity: &yes}))
	assert.ElementsMatch(unnamed[1:], collect(store.DirectoryPeopleQuery{HasName: &no, HasActivity: &no}))

	first, err := st.DirectoryPeoplePageContext(ctx, store.DirectoryPeopleQuery{HasName: &yes, Limit: 1})
	require.NoError(err)
	require.NotEmpty(first.NextCursor)
	_, err = st.DirectoryPeoplePageContext(ctx, store.DirectoryPeopleQuery{HasName: &no, Limit: 1, Cursor: first.NextCursor})
	require.ErrorIs(err, store.ErrInvalidDirectoryCursor, "a cursor is bound to its name filter")
	_, err = st.DirectoryPeoplePageContext(ctx, store.DirectoryPeopleQuery{Limit: 1, Cursor: first.NextCursor})
	require.ErrorIs(err, store.ErrInvalidDirectoryCursor, "a filtered cursor does not resume an unfiltered listing")
}

func directoryPersonFromParticipant(t *testing.T, st *store.Store, ensure func() (int64, error)) *store.Person {
	t.Helper()
	participantID, err := ensure()
	require.NoError(t, err)
	person, _, err := st.CreatePersonFromParticipantContext(t.Context(), participantID)
	require.NoError(t, err)
	return person
}

func addDirectoryContactPoint(t *testing.T, st *store.Store, personID int64, kind store.ContactAddressKind, value string) {
	t.Helper()
	_, err := st.AddPersonContactPointContext(t.Context(), personID, store.PersonContactPointInput{
		AddressKind: kind, OriginalValue: value,
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(t, err)
}
