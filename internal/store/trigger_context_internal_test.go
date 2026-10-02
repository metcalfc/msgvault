package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelDuringTriggersDialect cancels the initialisation the moment trigger
// replacement begins, then delegates to the SQLite dialect. It changes only
// when cancellation lands.
type cancelDuringTriggersDialect struct {
	Dialect

	cancel func()
}

func (d cancelDuringTriggersDialect) EnsureTriggers(q querier) error {
	d.cancel()
	return d.Dialect.EnsureTriggers(q)
}

// TestInitSchema_TriggerReplacementStopsWhenTheContextIsCancelled verifies
// that trigger replacement uses the migration context for its DDL. Backfill
// cancellation tests run later and cannot detect a context-free trigger querier.
func TestInitSchema_TriggerReplacementStopsWhenTheContextIsCancelled(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	st, err := Open(filepath.Join(t.TempDir(), "triggers.db"))
	require.NoError(err, "open store")
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st.dialect = cancelDuringTriggersDialect{Dialect: st.dialect, cancel: cancel}

	err = st.InitSchemaContext(ctx)

	require.Error(err, "a cancelled initialisation must report failure, not a silent partial upgrade")
	require.ErrorIs(err, context.Canceled,
		"and report it as cancellation, so the daemon exits on the signal rather than "+
			"treating an operator's Ctrl-C as a corrupt archive")
	assert.Contains(err.Error(), "ensure message watermark triggers",
		"the cancellation has to stop trigger replacement itself: an error raised by a "+
			"LATER step means the DROP/CREATE ran to completion with the context "+
			"already cancelled")
}
