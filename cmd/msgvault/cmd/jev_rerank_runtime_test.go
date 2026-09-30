//go:build sqlite_vec

package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestNewJevSearchRerankerIsNilUntilJevAndRerankAreOn(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir

	reranker, err := newJevSearchReranker(cfg, st, cfg.Vector)
	require.NoError(err)
	assert.Nil(reranker, "reranking is off by default")
	cfg.Jev.Enabled = true
	reranker, err = newJevSearchReranker(cfg, st, cfg.Vector)
	require.NoError(err)
	assert.Nil(reranker, "[jev.rerank] is separate from the [jev] switch")
	cfg.Jev.Rerank.Enabled = true
	cfg.Jev.Rerank.Shape = jev.RerankShapePerCandidate
	cfg.Jev.Rerank.Top = 12
	reranker, err = newJevSearchReranker(cfg, st, cfg.Vector)
	require.NoError(err)
	require.NotNil(reranker)
	assert.Equal(12, reranker.Top())
	assert.Contains(reranker.Identity(), "per-candidate")
}
