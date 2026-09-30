package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An archive that already ran the first catalog seed gains the profile
// services on upgrade, without replacing a service the user registered
// under the same name or claiming a name that is already another
// service's alias.
func TestCommunicationServiceSeedV2UpgradesWithoutOverridingUserServices(t *testing.T) {
	if IsPostgresURL(os.Getenv("MSGVAULT_TEST_DB")) {
		t.Skip("SQLite file-path migration test")
	}
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st, err := OpenForTest(filepath.Join(t.TempDir(), "upgrade.db"))
	require.NoError(err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(st.InitSchema())

	// Recreate a pre-v2 archive: the v2 services are absent and the ledger
	// has no v2 entry.
	_, err = st.db.Exec(`DELETE FROM applied_migrations WHERE name = ?`, communicationServicesSeedV2)
	require.NoError(err)
	_, err = st.db.Exec(`DELETE FROM communication_services
		WHERE slug IN ('github', 'youtube', 'threads', 'mastodon')`)
	require.NoError(err)

	userGitHub, _, err := st.EnsureCommunicationServiceContext(ctx, CommunicationServiceInput{
		Slug: "github", DisplayLabel: "Code Host", ScopePolicy: ScopePolicyNone,
		Normalization: NormalizationLower, NormalizationVersion: 1,
		ProfileURLTemplate: new("https://code.example.com/{username}"),
	})
	require.NoError(err)
	video, _, err := st.EnsureCommunicationServiceContext(ctx, CommunicationServiceInput{
		Slug: "example-video", DisplayLabel: "Example Video", Aliases: []string{"youtube"},
		ScopePolicy: ScopePolicyNone, Normalization: NormalizationLower, NormalizationVersion: 1,
	})
	require.NoError(err)

	require.NoError(st.InitSchema())

	github, err := st.ResolveCommunicationServiceContext(ctx, "github")
	require.NoError(err)
	assert.Equal(userGitHub.ID, github.ID, "the user's github service must survive the seed")
	assert.Equal("Code Host", github.DisplayLabel)
	require.NotNil(github.ProfileURLTemplate)
	assert.Equal("https://code.example.com/{username}", *github.ProfileURLTemplate)

	youtube, err := st.ResolveCommunicationServiceContext(ctx, "youtube")
	require.NoError(err)
	assert.Equal(video.ID, youtube.ID, "an existing alias keeps resolving to its service")

	threads, err := st.ResolveCommunicationServiceContext(ctx, "threads")
	require.NoError(err)
	assert.True(threads.IsSystem)
	require.NotNil(threads.ProfileURLTemplate)
	assert.Equal("https://www.threads.com/@{username}", *threads.ProfileURLTemplate)

	mastodon, err := st.ResolveCommunicationServiceContext(ctx, "mastodon")
	require.NoError(err)
	assert.Nil(mastodon.ProfileURLTemplate, "a Mastodon handle names its own server")

	applied, err := st.IsMigrationApplied(communicationServicesSeedV2)
	require.NoError(err)
	assert.True(applied)

	// Deleting a seeded service after the seed ran is a user decision the
	// next startup respects.
	_, err = st.db.Exec(`DELETE FROM communication_services WHERE slug = 'threads'`)
	require.NoError(err)
	require.NoError(st.InitSchema())
	_, err = st.ResolveCommunicationServiceContext(ctx, "threads")
	require.ErrorIs(err, ErrServiceNotFound)
}
