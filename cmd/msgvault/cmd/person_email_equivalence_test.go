package cmd

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// TestPersonLinkEquivalentAddressesCLILinksThroughTheDaemon drives the
// command against the production API server and store adapter.
func TestPersonLinkEquivalentAddressesCLILinksThroughTheDaemon(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	st := testutil.NewTestStore(t)
	primary, err := st.EnsureParticipant("pat@example.test", "Pat Sender", "example.test")
	require.NoError(err)
	tagged, err := st.EnsureParticipant("pat+lists@example.test", "", "example.test")
	require.NoError(err)
	_, err = st.EnsureParticipant("lee.roe@example.test", "", "example.test")
	require.NoError(err)
	_, err = st.EnsureParticipant("leeroe@example.test", "", "example.test")
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

	run := func(args ...string) string {
		var output bytes.Buffer
		command := newPersonLinkEquivalentAddressesCommand()
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(args)
		command.SetContext(ctx)
		command.SilenceUsage = true
		command.SilenceErrors = true
		require.NoError(command.Execute(), output.String())
		return output.String()
	}

	output := run()
	assert.Contains(output, "Email participants: 4")
	assert.Contains(output, "Linked: 1")
	assert.Contains(output, "Suggested for review: 1")
	members, err := st.ClusterMembers(primary)
	require.NoError(err)
	assert.Equal([]int64{primary, tagged}, members)

	var repeat store.EmailEquivalenceResult
	require.NoError(json.Unmarshal([]byte(run("--json")), &repeat))
	assert.Equal(store.EmailEquivalenceResult{Participants: 4}, repeat,
		"a repeat run changes nothing")
}
