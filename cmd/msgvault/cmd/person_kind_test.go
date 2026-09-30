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
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/testutil"
)

func runPersonKindCLI(ctx context.Context, args ...string) (string, error) {
	var output bytes.Buffer
	command := newPersonKindCommand()
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	command.SetContext(ctx)
	command.SilenceUsage = true
	command.SilenceErrors = true
	err := command.Execute()
	return output.String(), err
}

// TestPersonKindCLIThroughTheDaemon drives set and list against the
// production API server and store adapter.
func TestPersonKindCLIThroughTheDaemon(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	st := testutil.NewTestStore(t)
	desk, err := st.EnsureParticipant("desk@shop.example.test", "Example Shop Desk", "shop.example.test")
	require.NoError(err)
	news, err := st.EnsureParticipant("news@example.test", "Example News", "example.test")
	require.NoError(err)
	saved, _, err := st.CreatePersonFromParticipantContext(t.Context(), news)
	require.NoError(err)

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

	output, err := runPersonKindCLI(ctx, "set", strconv.FormatInt(desk, 10), "organization",
		"--organization-name", "Example Shop")
	require.NoError(err, output)
	assert.Contains(output, "Organization")
	assert.Contains(output, "Organization: Example Shop (created)")

	output, err = runPersonKindCLI(ctx, "set", strconv.FormatInt(news, 10), "ignored")
	require.NoError(err, output)
	assert.Contains(output, ": Ignored")
	assert.Contains(output, "msgvault person delete "+strconv.FormatInt(saved.ID, 10),
		"a profile only for this identity is kept and the delete command is named")

	output, err = runPersonKindCLI(ctx, "list")
	require.NoError(err, output)
	assert.Contains(output, "organization")
	assert.Contains(output, "Example Shop")
	assert.Contains(output, "ignored")

	output, err = runPersonKindCLI(ctx, "list", "--kind", "ignored")
	require.NoError(err, output)
	assert.NotContains(output, "Example Shop Desk")

	output, err = runPersonKindCLI(ctx, "set", strconv.FormatInt(news, 10), "person")
	require.NoError(err, output)
	assert.Contains(output, ": Person")
	kinds, err := st.NotPersonParticipantsContext(t.Context())
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{desk: correspondentkind.Organization}, kinds)

	for _, args := range [][]string{
		{"set", "0", "ignored"},
		{"set", "1", "robot"},
		{"set", "1", "ignored", "--organization-name", "X"},
		{"set", "1", "organization", "--organization", "3", "--organization-name", "X"},
		{"list", "--kind", "person"},
	} {
		_, err := runPersonKindCLI(ctx, args...)
		assert.Error(err, args)
	}
}
