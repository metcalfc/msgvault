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

func contactProfile(t *testing.T, st *store.Store, uid, name, email string) int64 {
	t.Helper()
	var personID int64
	require.NoError(t, st.DB().QueryRow(
		`INSERT INTO persons (vcard_uid, display_name) VALUES (?, ?) RETURNING id`, uid, name,
	).Scan(&personID))
	_, err := st.AddPersonContactPointContext(t.Context(), personID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: email,
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

	st := testutil.NewTestStore(t)
	boundID, err := st.EnsureParticipant("bo@example.test", "Bo Sender", "example.test")
	require.NoError(err)
	existing, _, err := st.CreatePersonFromParticipantContext(t.Context(), boundID)
	require.NoError(err)
	adaID, err := st.EnsureParticipant("ada@example.test", "Ada Sender", "example.test")
	require.NoError(err)
	_, err = st.EnsureParticipant("cy@example.test", "Cy Sender", "example.test")
	require.NoError(err)
	adaContact := contactProfile(t, st, "card-ada", "Ada Contact", "ada@example.test")
	contactProfile(t, st, "card-bo", "Bo Contact", "bo@example.test")
	contactProfile(t, st, "card-cy", "Cy Contact", "cy@example.test")

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

	output, err := runContactMatchesCLI(ctx, "build")
	require.NoError(err, output)
	assert.Contains(output, "Matches: 3 (new 3, existing 0)")
	assert.Contains(output, "Retired: 0")
	assert.Contains(output, "Bind: 2")
	assert.Contains(output, "Merge: 1")

	output, err = runContactMatchesCLI(ctx, "list")
	require.NoError(err, output)
	assert.Contains(output, "Ada Sender <ada@example.test>")
	assert.Contains(output, "Ada Contact <ada@example.test>")
	assert.Contains(output, fmt.Sprintf("[person %d]", existing.ID))

	candidates, err := st.ListContactMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 3)
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
