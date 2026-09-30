package cmd

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func runEnrichmentReviewCLI(ctx context.Context, args ...string) (string, error) {
	var output bytes.Buffer
	command := newPersonEnrichmentReviewCommand()
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	command.SetContext(ctx)
	command.SilenceUsage = true
	command.SilenceErrors = true
	err := command.Execute()
	return output.String(), err
}

// TestPersonEnrichmentReviewCLIDecidesThroughTheDaemon drives list, accept,
// and reject against the production API server and store adapter.
func TestPersonEnrichmentReviewCLIDecidesThroughTheDaemon(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	_, confirmAttempt := storetest.UncertainEnrichmentAttempt(t, st, "ada@example.test", "Ada Example")
	_, rejectAttempt := storetest.UncertainEnrichmentAttempt(t, st, "bo@example.test", "Bo Example")

	srv := api.NewServerWithOptions(api.ServerOptions{
		Config: &config.Config{}, Store: &storeAPIAdapter{store: st},
		Logger: slog.New(slog.DiscardHandler),
	})
	httpSrv := httptest.NewServer(srv.Router())
	t.Cleanup(httpSrv.Close)
	ctx := withStoreResolverConfig(t, &config.Config{
		Remote: config.RemoteConfig{URL: httpSrv.URL, AllowInsecure: true},
	})

	output, err := runEnrichmentReviewCLI(ctx, "list")
	require.NoError(err, output)
	assert.Contains(output, strconv.FormatInt(confirmAttempt, 10))
	assert.Contains(output, "Ada Example")
	assert.Contains(output, "profiles.example.test")
	assert.Contains(output, "0.70")

	output, err = runEnrichmentReviewCLI(ctx, "accept", strconv.FormatInt(confirmAttempt, 10))
	require.NoError(err, output)
	assert.Contains(output, "confirmed (user_confirmed)")
	assert.Contains(output, "Attempt state: succeeded")
	assert.Contains(output, "Provider identities attached: 1")

	output, err = runEnrichmentReviewCLI(ctx, "reject", strconv.FormatInt(rejectAttempt, 10))
	require.NoError(err, output)
	assert.Contains(output, "rejected (user_rejected)")
	assert.Contains(output, "Negatives recorded: 1")

	_, err = runEnrichmentReviewCLI(ctx, "reject", strconv.FormatInt(rejectAttempt, 10))
	require.Error(err)
	assert.Contains(err.Error(), "no longer awaiting an identity decision")

	output, err = runEnrichmentReviewCLI(ctx, "list", "--json")
	require.NoError(err, output)
	assert.Contains(output, `"reviews":[]`)
}
