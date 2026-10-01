package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRejectsLegacyDatabaseConfiguration(t *testing.T) {
	for _, body := range []string{
		"[data]\ndatabase_url = 'postgres://alice:secret@example.test/archive'\n",
		"[data]\ndatabase_url = 'host=example.test password=secret dbname=archive'\n",
		"[vector]\nbackend = 'pgvector'\nenabled = false\n",
		"[vector]\nskip_extension_create = true\nenabled = false\n",
		"[vector]\ndb_path = 'postgres://alice:secret@example.test/archive'\n",
	} {
		t.Run(body, func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			home := t.TempDir()
			path := filepath.Join(home, "config.toml")
			requirements.NoError(os.WriteFile(path, []byte(body), 0600))
			cfg, err := Load(path, home)
			requirements.Error(err)
			assertions.Nil(cfg)
			assertions.NotContains(err.Error(), "secret")
		})
	}
}
