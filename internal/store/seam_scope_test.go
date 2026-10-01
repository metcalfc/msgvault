package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil"
)

// TestInitSchemaWindowHookFiresOnlyForItsOwnStore verifies that a migration
// hook installed on one Store cannot run during another Store's migration.
// A shared hook could write to the wrong archive or a closed database.
func TestInitSchemaWindowHookFiresOnlyForItsOwnStore(t *testing.T) {
	require := require.New(t)
	owner := testutil.NewTestStore(t)
	other := testutil.NewTestStore(t)

	fired := 0
	restore := owner.SetInitSchemaWindowHookForTest(func() { fired++ })
	defer restore()

	// Another Store's migration must not see this test's hook.
	require.NoError(other.InitSchema())
	require.Zero(fired,
		"a hook installed on one Store fired during another Store's InitSchema: "+
			"concurrent fixture builds can now reach into this test's archive")

	// The hook must still fire for the Store that installed it, or the check
	// above would pass just as well on a seam that never works at all.
	require.NoError(owner.InitSchema())
	require.Equal(1, fired,
		"the hook did not fire on its own Store's InitSchema, so this test proves nothing")

	// And clearing it is scoped the same way.
	restore()
	require.NoError(owner.InitSchema())
	require.Equal(1, fired, "the restore func did not clear the hook")
}
