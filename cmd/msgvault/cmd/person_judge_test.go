package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestRunPersonJudgeWithJevOffOnlyCountsProposals(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	_, err := st.EnsureParticipant("jane@example.com", "Jane Doe", "example.com")
	require.NoError(err)
	_, err = st.EnsureParticipant("jdoe@example.org", "Jane Doe", "example.org")
	require.NoError(err)
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir

	report, err := runPersonJudge(t.Context(), cfg, st, 0, false, nil)
	require.NoError(err)
	assert.False(report.DuplicatesJev)
	assert.False(report.ProfilesJev)
	assert.Equal(1, report.Duplicates.Proposals)
	assert.Zero(report.Duplicates.Requests)
	assert.False(personJudgeAutomatic(cfg))

	var out bytes.Buffer
	writePersonJudgeReport(&out, report)
	assert.Equal("Possible duplicate pairs: 1\nDuplicate people: Jev off\nProfile choices: Jev off; 0 settled in code\n", out.String())

	cfg.Jev.Enabled = true
	cfg.Jev.PersonProfileChoices.Enabled = true
	assert.False(personJudgeAutomatic(cfg), "enabled alone is not automatic")
	cfg.Jev.PersonProfileChoices.Automatic = true
	assert.True(personJudgeAutomatic(cfg))
}
