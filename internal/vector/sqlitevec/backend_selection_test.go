//go:build sqlite_vec

package sqlitevec

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenRejectsServerDatabasePaths(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, opts := range []Options{
		{Path: "postgres://alice:secret@example.test/archive", Dimension: 3},
		{Path: "vectors.db", MainPath: "host=example.test password=secret", Dimension: 3},
	} {
		t.Run(opts.Path, func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			backend, err := Open(t.Context(), opts)
			requirements.Error(err)
			assertions.Nil(backend)
			assertions.NotContains(err.Error(), "secret")
			entries, err := os.ReadDir(".")
			requirements.NoError(err)
			assertions.Empty(entries)
		})
	}
}
