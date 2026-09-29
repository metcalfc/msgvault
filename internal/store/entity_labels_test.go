package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
