package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The classification probe runs on every identity revision bump. It may treat
// only a missing table as "nothing classified"; any other failure must
// surface.
func TestNotPersonProbeTreatsOnlyAMissingTableAsAbsent(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st, err := Open(filepath.Join(t.TempDir(), "probe.db"))
	require.NoError(err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(st.InitSchema())

	probe := func() (bool, error) {
		var classified bool
		err := st.withTxContext(t.Context(), func(tx *loggedTx) error {
			var probeErr error
			classified, probeErr = st.anyNotPersonClassificationTx(t.Context(), tx)
			return probeErr
		})
		return classified, err
	}

	classified, err := probe()
	require.NoError(err)
	assert.False(classified)

	_, err = st.DB().Exec(`DROP TABLE correspondent_kinds`)
	require.NoError(err)
	classified, err = probe()
	require.NoError(err, "a missing table means nothing is classified")
	assert.False(classified)

	// A table that exists but cannot answer the probe is an error.
	_, err = st.DB().Exec(`CREATE TABLE correspondent_kinds (participant_id INTEGER)`)
	require.NoError(err)
	_, err = probe()
	assert.ErrorContains(err, "probe correspondent kinds")
}
