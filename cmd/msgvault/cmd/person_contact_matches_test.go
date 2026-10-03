package cmd

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func runContactMatchesCLI(ctx context.Context, args ...string) (string, error) {
	var output bytes.Buffer
	command := newPersonContactMatchesCommand()
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	command.SetContext(ctx)
	command.SilenceUsage = true
	command.SilenceErrors = true
	err := command.Execute()
	return output.String(), err
}

func contactProfile(
	t *testing.T, st *store.Store, uid, name string, kind store.ContactAddressKind, value string,
) int64 {
	t.Helper()
	var personID int64
	require.NoError(t, st.DB().QueryRow(
		`INSERT INTO persons (vcard_uid, display_name) VALUES (?, ?) RETURNING id`, uid, name,
	).Scan(&personID))
	_, err := st.AddPersonContactPointContext(t.Context(), personID, store.PersonContactPointInput{
		AddressKind: kind, OriginalValue: value,
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceCardDAVImport},
	})
	require.NoError(t, err)
	return personID
}

// TestPersonContactMatchesCLIReviewsThroughTheDaemon drives every
// subcommand against the production API server and store adapter.
func TestPersonContactMatchesCLIReviewsThroughTheDaemon(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// Phone matches wait for review; exact email matches are decided by the
	// build: Cy's identity is linked to the contact, and Dee's contact is
	// merged into the person who already has the address.
	st := testutil.NewTestStore(t)
	boundID, err := st.EnsureParticipantByPhone("+15550100182", "Bo Sender", "whatsapp")
	require.NoError(err)
	existing, _, err := st.CreatePersonFromParticipantContext(t.Context(), boundID)
	require.NoError(err)
	adaID, err := st.EnsureParticipantByPhone("+15550100181", "Ada Sender", "whatsapp")
	require.NoError(err)
	cyID, err := st.EnsureParticipant("cy@example.test", "Cy Sender", "example.test")
	require.NoError(err)
	deeID, err := st.EnsureParticipant("dee@example.test", "Dee Sender", "example.test")
	require.NoError(err)
	dee, _, err := st.CreatePersonFromParticipantContext(t.Context(), deeID)
	require.NoError(err)
	adaContact := contactProfile(t, st, "card-ada", "Ada Contact", store.ContactAddressPhone, "+1 555 010 0181")
	contactProfile(t, st, "card-bo", "Bo Contact", store.ContactAddressPhone, "+1 555 010 0182")
	cyContact := contactProfile(t, st, "card-cy", "Cy Contact", store.ContactAddressEmail, "cy@example.test")
	deeContact := contactProfile(t, st, "card-dee", "Dee Contact", store.ContactAddressEmail, "dee@example.test")

	srv := api.NewServerWithOptions(api.ServerOptions{
		Config: &config.Config{},
		Store:  &storeAPIAdapter{store: st},
		Logger: slog.New(slog.DiscardHandler),
	})
	httpSrv := httptest.NewServer(srv.Router())
	t.Cleanup(httpSrv.Close)
	ctx := withStoreResolverConfig(t, &config.Config{
		Remote: config.RemoteConfig{URL: httpSrv.URL, AllowInsecure: true},
	})

	output, err := runContactMatchesCLI(ctx, "build", "--dry-run")
	require.NoError(err, output)
	assert.Contains(output, "Dry run: nothing was written.")
	assert.Contains(output, "Merged automatically: 1")
	assert.Contains(output, fmt.Sprintf("merge: candidate 0, contact person %d", deeContact))
	assert.Contains(output, fmt.Sprintf("into person %d", dee.ID))
	assert.NotContains(output, "Dee", "dry-run output names no one")
	pending, err := st.ListContactMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	assert.Empty(pending, "a dry run writes nothing")

	output, err = runContactMatchesCLI(ctx, "build")
	require.NoError(err, output)
	assert.Contains(output, "Matches: 4 (new 4, existing 0)")
	assert.Contains(output, "Retired: 0")
	assert.Contains(output, "Bind: 2")
	assert.Contains(output, "Merge: 2")
	assert.Contains(output, "Merged automatically: 1")
	assert.Contains(output, "Linked automatically: 1")
	assert.Contains(output, "Left for review: 2")
	cy, err := st.GetPersonContext(t.Context(), cyContact)
	require.NoError(err)
	assert.Equal([]int64{cyID}, cy.ParticipantIDs)
	merges, err := st.ListPersonMergesContext(t.Context(), dee.ID)
	require.NoError(err)
	require.Len(merges, 1)
	assert.Equal("rule:contact_match:email:dee@example.test", merges[0].Merge.Actor)

	output, err = runContactMatchesCLI(ctx, "list")
	require.NoError(err, output)
	assert.Contains(output, "Ada Sender <+15550100181>")
	assert.Contains(output, "Ada Contact <+1 555 010 0181>")
	assert.Contains(output, fmt.Sprintf("[person %d]", existing.ID))
	assert.NotContains(output, "cy@example.test", "decided matches leave the pending list")

	candidates, err := st.ListContactMatchCandidatesContext(t.Context(),
		[]store.IdentityMatchState{store.IdentityMatchStateCandidate}, 100, 0)
	require.NoError(err)
	require.Len(candidates, 2)
	byPerson := map[int64]store.IdentityMatchCandidate{}
	for _, candidate := range candidates {
		byPerson[candidate.RightID] = candidate
	}

	bind := byPerson[adaContact]
	output, err = runContactMatchesCLI(ctx, "accept", strconv.FormatInt(bind.ID, 10), "--notes", "Same address")
	require.NoError(err, output)
	assert.Contains(output, fmt.Sprintf("Contact match %d: accepted", bind.ID))
	person, err := st.GetPersonContext(t.Context(), adaContact)
	require.NoError(err)
	assert.Equal([]int64{adaID}, person.ParticipantIDs)

	var merge store.IdentityMatchCandidate
	for _, candidate := range candidates {
		if candidate.LeftID == boundID {
			merge = candidate
		}
	}
	require.NotZero(merge.ID)
	output, err = runContactMatchesCLI(ctx, "accept", strconv.FormatInt(merge.ID, 10))
	require.Error(err, output)
	assert.Contains(err.Error(), "needs an explicit person merge")
	assert.Contains(err.Error(), "msgvault person merge")
	assert.Contains(err.Error(), fmt.Sprintf("msgvault person contact-matches accept %d", merge.ID))

	output, err = runContactMatchesCLI(ctx, "reject", strconv.FormatInt(merge.ID, 10))
	require.NoError(err, output)
	assert.Contains(output, fmt.Sprintf("Contact match %d: rejected", merge.ID))

	output, err = runContactMatchesCLI(ctx, "list", "--state", "rejected", "--json")
	require.NoError(err, output)
	assert.Contains(output, fmt.Sprintf(`"id":%d`, merge.ID))
}

func TestPersonContactMatchesCLIValidatesCandidateID(t *testing.T) {
	_, err := runContactMatchesCLI(t.Context(), "accept", "zero")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "candidate ID must be a positive integer")
}
