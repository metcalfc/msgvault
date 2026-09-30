package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestEntityLabelsParticipants(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)

	named, err := st.EnsureParticipant("named@example.com", "  Named Person  ", "example.com")
	require.NoError(err)
	emailOnly, err := st.EnsureParticipant("email.only@example.com", "", "example.com")
	require.NoError(err)
	phoneOnly, err := st.EnsureParticipantByPhone("+15550100001", "", "phone")
	require.NoError(err)
	var identifierOnly, bare int64
	require.NoError(st.DB().QueryRowContext(ctx, st.Rebind(
		`INSERT INTO participants (display_name) VALUES ('') RETURNING id`)).Scan(&identifierOnly))
	_, err = st.DB().ExecContext(ctx, st.Rebind(
		`INSERT INTO participant_identifiers (participant_id, identifier_type, identifier_value, display_value)
		 VALUES (?, 'apple_id', 'handle@example.net', 'Handle@Example.net')`), identifierOnly)
	require.NoError(err)
	require.NoError(st.DB().QueryRowContext(ctx, st.Rebind(
		`INSERT INTO participants (display_name) VALUES (NULL) RETURNING id`)).Scan(&bare))

	labels, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{
		ParticipantIDs: []int64{named, emailOnly, phoneOnly, identifierOnly, bare, named, 999_999},
	})
	require.NoError(err)
	assert.Equal(map[int64]string{
		named:          "Named Person",
		emailOnly:      "email.only@example.com",
		phoneOnly:      "+15550100001",
		identifierOnly: "Handle@Example.net",
	}, labels.Participants, "an unnamed or missing participant is absent, never its ID")
	assert.Equal(map[int64]string{
		named:          "Named Person · named@example.com",
		emailOnly:      "email.only@example.com",
		phoneOnly:      "+15550100001",
		identifierOnly: "Handle@Example.net",
	}, labels.ParticipantIdentities, "an identity is the participant's own name and address")
	assert.Empty(labels.People)
	assert.Empty(labels.Organizations)
}

func TestEntityLabelsPeopleAndOrganizations(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)

	curatedParticipant, err := st.EnsureParticipant("curated@example.com", "Curated Name", "example.com")
	require.NoError(err)
	curated, _, err := st.CreatePersonFromParticipant(curatedParticipant)
	require.NoError(err)

	memberParticipant, err := st.EnsureParticipant("member@example.com", "Member Name", "example.com")
	require.NoError(err)
	member, _, err := st.CreatePersonFromParticipant(memberParticipant)
	require.NoError(err)
	_, err = st.UpdatePersonDisplayNameContext(ctx, member.ID, member.Revision, nil)
	require.NoError(err)

	addressParticipant, err := st.EnsureParticipant("address@example.com", "", "example.com")
	require.NoError(err)
	address, _, err := st.CreatePersonFromParticipant(addressParticipant)
	require.NoError(err)

	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{
		Name: "Example Org", Kind: store.OrganizationKindCompany,
	})
	require.NoError(err)

	labels, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{
		PersonIDs:       []int64{curated.ID, member.ID, address.ID, 888_888},
		OrganizationIDs: []int64{organization.ID, 777_777},
	})
	require.NoError(err)
	assert.Equal(map[int64]string{
		curated.ID: "Curated Name",
		member.ID:  "Member Name",
		address.ID: "address@example.com",
	}, labels.People)
	assert.Equal(map[int64]string{organization.ID: "Example Org"}, labels.Organizations)
}

func TestEntityLabelsNamesMergedAwayPersonFromSnapshot(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)

	survivorParticipant, err := st.EnsureParticipant("survivor@example.com", "Survivor Name", "example.com")
	require.NoError(err)
	survivor, _, err := st.CreatePersonFromParticipant(survivorParticipant)
	require.NoError(err)
	absorbedParticipant, err := st.EnsureParticipant("absorbed@example.com", "Absorbed Name", "example.com")
	require.NoError(err)
	absorbed, _, err := st.CreatePersonFromParticipant(absorbedParticipant)
	require.NoError(err)

	_, err = st.MergePersonsContext(ctx, store.PersonMergeRequest{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision,
		ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey:           "entity-labels-merge", Actor: "test",
	})
	require.NoError(err)

	labels, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{
		PersonIDs: []int64{survivor.ID, absorbed.ID},
	})
	require.NoError(err)
	assert.Equal(map[int64]string{
		survivor.ID: "Survivor Name",
		absorbed.ID: "Absorbed Name",
	}, labels.People)
}

func TestEntityLabelsNamesUnnamedMergedAwayPersonByItsParticipants(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)

	survivorParticipant, err := st.EnsureParticipant("kept@example.com", "Kept Name", "example.com")
	require.NoError(err)
	survivor, _, err := st.CreatePersonFromParticipant(survivorParticipant)
	require.NoError(err)
	absorbedParticipant, err := st.EnsureParticipant("unnamed@example.com", "", "example.com")
	require.NoError(err)
	absorbed, _, err := st.CreatePersonFromParticipant(absorbedParticipant)
	require.NoError(err)
	require.Nil(absorbed.DisplayName)

	_, err = st.MergePersonsContext(ctx, store.PersonMergeRequest{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision,
		ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey:           "entity-labels-unnamed-merge", Actor: "test",
	})
	require.NoError(err)

	labels, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{PersonIDs: []int64{absorbed.ID}})
	require.NoError(err)
	assert.Equal(map[int64]string{absorbed.ID: "unnamed@example.com"}, labels.People)
}

func TestEntityLabelsRejectsOversizedRequest(t *testing.T) {
	st := testutil.NewTestStore(t)
	ids := make([]int64, 0, store.MaxEntityLabelIDs+1)
	for id := range int64(store.MaxEntityLabelIDs + 1) {
		ids = append(ids, id+1)
	}
	_, err := st.EntityLabelsContext(context.Background(), store.EntityLabelRequest{ParticipantIDs: ids})
	require.ErrorIs(t, err, store.ErrEntityLabelRequestTooLarge)

	duplicates := slices.Repeat([]int64{1}, store.MaxEntityLabelIDs+1)
	_, err = st.EntityLabelsContext(context.Background(), store.EntityLabelRequest{PersonIDs: duplicates})
	assert.NoError(t, err, "the cap counts distinct IDs")
}

// TestEntityLabelsParticipantPrefersBoundPersonName pins that a participant
// bound to a renamed person reads the curated person name, as the analytics
// labels do, whether or not the participant was observed with a name.
func TestEntityLabelsParticipantPrefersBoundPersonName(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)

	observed, err := st.EnsureParticipant("jdoe@example.com", "jdoe", "example.com")
	require.NoError(err)
	renamed, _, err := st.CreatePersonFromParticipant(observed)
	require.NoError(err)
	_, err = st.UpdatePersonDisplayNameContext(ctx, renamed.ID, renamed.Revision, new("Jane Doe"))
	require.NoError(err)

	unnamed, err := st.EnsureParticipant("quiet@example.com", "", "example.com")
	require.NoError(err)
	quiet, _, err := st.CreatePersonFromParticipant(unnamed)
	require.NoError(err)
	_, err = st.UpdatePersonDisplayNameContext(ctx, quiet.ID, quiet.Revision, new("Quinn Quiet"))
	require.NoError(err)

	unboundNamed, err := st.EnsureParticipant("unbound@example.com", "Unbound Name", "example.com")
	require.NoError(err)
	blankPersonParticipant, err := st.EnsureParticipant("blank@example.com", "Observed Blank", "example.com")
	require.NoError(err)
	blank, _, err := st.CreatePersonFromParticipant(blankPersonParticipant)
	require.NoError(err)
	_, err = st.UpdatePersonDisplayNameContext(ctx, blank.ID, blank.Revision, nil)
	require.NoError(err)

	labels, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{
		ParticipantIDs: []int64{observed, unnamed, unboundNamed, blankPersonParticipant},
	})
	require.NoError(err)
	assert.Equal(map[int64]string{
		observed:               "Jane Doe",
		unnamed:                "Quinn Quiet",
		unboundNamed:           "Unbound Name",
		blankPersonParticipant: "Observed Blank",
	}, labels.Participants)
}

// TestEntityLabelsPersonPrefersCurrentPersonName pins that a person with no
// display name is named by its current formatted or structured person name
// before any bound participant, and that superseded names are ignored.
func TestEntityLabelsPersonPrefersCurrentPersonName(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	envelope := store.ValueEnvelopeInput{Source: store.ProvenanceUser}
	unnamedPerson := func(email string) *store.Person {
		participantID, err := st.EnsureParticipant(email, "", "example.com")
		require.NoError(err)
		person, _, err := st.CreatePersonFromParticipant(participantID)
		require.NoError(err)
		return person
	}

	structured := unnamedPerson("rob@example.com")
	_, err := st.AddPersonNameContext(ctx, structured.ID, store.PersonNameInput{
		NameKind: store.PersonNameStructured, GivenName: new("Robert"), FamilyName: new("Example"),
		Envelope: envelope,
	})
	require.NoError(err)

	formatted := unnamedPerson("fmt@example.com")
	_, err = st.AddPersonNameContext(ctx, formatted.ID, store.PersonNameInput{
		NameKind: store.PersonNameStructured, GivenName: new("Structured"), FamilyName: new("Only"),
		Envelope: envelope,
	})
	require.NoError(err)
	_, err = st.AddPersonNameContext(ctx, formatted.ID, store.PersonNameInput{
		NameKind: store.PersonNameFormatted, Formatted: new("Formatted Name"), Envelope: envelope,
	})
	require.NoError(err)

	superseded := unnamedPerson("old@example.com")
	oldName, err := st.AddPersonNameContext(ctx, superseded.ID, store.PersonNameInput{
		NameKind: store.PersonNameFormatted, Formatted: new("Retired Name"), Envelope: envelope,
	})
	require.NoError(err)
	require.NoError(st.SupersedePersonNameContext(ctx, superseded.ID, oldName.Envelope.ID, nil))

	for _, person := range []*store.Person{structured, formatted, superseded} {
		current, err := st.GetPersonContext(ctx, person.ID)
		require.NoError(err)
		require.Nil(current.DisplayName, "the fixture needs a person without a display name")
	}

	labels, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{
		PersonIDs: []int64{structured.ID, formatted.ID, superseded.ID},
	})
	require.NoError(err)
	assert.Equal(map[int64]string{
		structured.ID: "Robert Example",
		formatted.ID:  "Formatted Name",
		superseded.ID: "old@example.com",
	}, labels.People)
}

// TestEntityLabelsParticipantIdentitiesTellOnePersonsIdentitiesApart pins
// that participants bound to one person share its name as their label but
// keep distinct identities built from their own names and addresses.
func TestEntityLabelsParticipantIdentitiesTellOnePersonsIdentitiesApart(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)

	work, err := st.EnsureParticipant("jane@example.com", "Jane D", "example.com")
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipant(work)
	require.NoError(err)
	_, err = st.UpdatePersonDisplayNameContext(ctx, person.ID, person.Revision, new("Jane Doe"))
	require.NoError(err)
	home, err := st.EnsureParticipant("jane.home@example.org", "", "example.org")
	require.NoError(err)
	phone, err := st.EnsureParticipantByPhone("+15550100002", "Jane Mobile", "phone")
	require.NoError(err)
	for _, participantID := range []int64{home, phone} {
		_, err = st.DB().ExecContext(ctx, st.Rebind(
			`INSERT INTO person_participants (person_id, participant_id) VALUES (?, ?)`), person.ID, participantID)
		require.NoError(err)
	}

	labels, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{ParticipantIDs: []int64{work, home, phone}})
	require.NoError(err)
	assert.Equal(map[int64]string{work: "Jane Doe", home: "Jane Doe", phone: "Jane Doe"}, labels.Participants)
	assert.Equal(map[int64]string{
		work:  "Jane D · jane@example.com",
		home:  "jane.home@example.org",
		phone: "Jane Mobile · +15550100002",
	}, labels.ParticipantIdentities)
}

// TestEntityLabelsParticipantMarkedNotAPersonKeepsItsOwnName pins that a
// participant in a cluster marked as not a person reads as its own name or
// address rather than the person it is still bound to, while that person
// keeps its own label.
func TestEntityLabelsParticipantMarkedNotAPersonKeepsItsOwnName(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)

	desk, err := st.EnsureParticipant("desk@example.com", "Help Desk", "example.com")
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipant(desk)
	require.NoError(err)
	_, err = st.UpdatePersonDisplayNameContext(ctx, person.ID, person.Revision, new("Avery Stone"))
	require.NoError(err)
	orders, err := st.EnsureParticipant("orders@example.com", "", "example.com")
	require.NoError(err)
	shop, _, err := st.CreatePersonFromParticipant(orders)
	require.NoError(err)
	_, err = st.UpdatePersonDisplayNameContext(ctx, shop.ID, shop.Revision, new("Blair Example"))
	require.NoError(err)

	before, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{ParticipantIDs: []int64{desk, orders}})
	require.NoError(err)
	assert.Equal(map[int64]string{desk: "Avery Stone", orders: "Blair Example"}, before.Participants)

	_, err = st.SetCorrespondentKindContext(ctx, store.SetCorrespondentKindInput{
		ParticipantID: desk, Kind: correspondentkind.SharedMailbox,
	})
	require.NoError(err)
	_, err = st.SetCorrespondentKindContext(ctx, store.SetCorrespondentKindInput{
		ParticipantID: orders, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)

	after, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{
		ParticipantIDs: []int64{desk, orders}, PersonIDs: []int64{person.ID},
	})
	require.NoError(err)
	assert.Equal(map[int64]string{desk: "Help Desk", orders: "orders@example.com"}, after.Participants)
	assert.Equal(map[int64]string{person.ID: "Avery Stone"}, after.People)
}

// TestEntityLabelsScopeNotAPersonToTheRequestedClusters pins that the
// not-a-person check follows links from the requested participants: a
// participant linked into a classified cluster reads as its own name, and
// participants outside every classified cluster keep their bound person's
// name while another cluster is classified. The scoped lookup agrees with
// the whole-archive one for the requested participants.
func TestEntityLabelsScopeNotAPersonToTheRequestedClusters(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)

	named := func(address, name, personName string) int64 {
		id, err := st.EnsureParticipant(address, name, "example.com")
		require.NoError(err)
		if personName != "" {
			person, _, err := st.CreatePersonFromParticipant(id)
			require.NoError(err)
			_, err = st.UpdatePersonDisplayNameContext(ctx, person.ID, person.Revision, new(personName))
			require.NoError(err)
		}
		return id
	}
	desk := named("desk@example.com", "Help Desk", "")
	middle := named("desk-alias@example.com", "", "")
	alias := named("desk-other@example.com", "Desk Alias", "Avery Stone")
	friend := named("friend@example.com", "Blair", "Blair Example")
	_, err := st.LinkParticipants(desk, middle)
	require.NoError(err)
	_, err = st.LinkParticipants(middle, alias)
	require.NoError(err)
	_, err = st.SetCorrespondentKindContext(ctx, store.SetCorrespondentKindInput{
		ParticipantID: desk, Kind: correspondentkind.SharedMailbox,
	})
	require.NoError(err)

	labels, err := st.EntityLabelsContext(ctx, store.EntityLabelRequest{ParticipantIDs: []int64{alias, friend}})
	require.NoError(err)
	assert.Equal(map[int64]string{alias: "Desk Alias", friend: "Blair Example"}, labels.Participants)

	scoped, err := st.NotPersonParticipantsForContext(ctx, []int64{alias, friend})
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{alias: correspondentkind.SharedMailbox}, scoped)
	whole, err := st.NotPersonParticipantsContext(ctx)
	require.NoError(err)
	assert.Equal(whole[alias], scoped[alias])
	assert.NotContains(whole, friend)

	outside, err := st.NotPersonParticipantsForContext(ctx, []int64{friend})
	require.NoError(err)
	assert.Empty(outside)
}
