package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestCleanupSuggestionsUpsertAndNameKeepCandidatesByProviderID(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newProtectionArchive(t)
	st := a.f.Store
	sender := a.f.EnsureParticipant("casey@example.net", "Casey Example", "example.net")
	personal := a.message(sender, false)
	work := a.message(sender, false)
	junk := a.message(sender, false)
	unjudged := a.message(sender, false)

	suggestion := func(id int64, keep float64) store.CleanupSuggestion {
		return store.CleanupSuggestion{
			MessageID: id, Score: 0.1, Category: "personal", Model: "jev-test", KeepProbability: keep,
			CategoryProbabilities: map[string]float64{"personal": keep},
		}
	}
	written, err := st.WriteCleanupSuggestionsContext(t.Context(), []store.CleanupSuggestion{
		suggestion(personal, 0.9), suggestion(work, 0.3), suggestion(junk, 0.05), suggestion(999999, 0.9),
	})
	require.NoError(err)
	assert.Equal(3, written, "a message that no longer exists is skipped")
	written, err = st.WriteCleanupSuggestionsContext(t.Context(), []store.CleanupSuggestion{suggestion(work, 0.6)})
	require.NoError(err)
	assert.Equal(1, written, "a rejudged message replaces its suggestion")
	_, err = st.WriteCleanupSuggestionsContext(t.Context(), []store.CleanupSuggestion{suggestion(junk, 1.5)})
	require.ErrorIs(err, store.ErrCleanupSuggestionInvalid)

	providerIDs := []string{"protect-1", "protect-2", "protect-3", "protect-4", "protect-4", "not-archived"}
	rows, err := st.KeepCandidatesForSourceMessagesContext(t.Context(), a.f.Source.ID, providerIDs, 0.5)
	require.NoError(err)
	require.Len(rows, 2)
	assert.Equal(personal, rows[0].MessageID, "most likely first")
	assert.Equal("protect-1", rows[0].SourceMessageID)
	assert.Equal("Casey Example", rows[0].FromName)
	assert.Equal("casey@example.net", rows[0].FromEmail)
	assert.Equal(work, rows[1].MessageID)
	assert.InDelta(0.6, rows[1].KeepProbability, 1e-9)
	assert.NotEqual(unjudged, rows[1].MessageID)

	other, err := st.KeepCandidatesForSourceMessagesContext(t.Context(), a.f.Source.ID+1, providerIDs, 0.5)
	require.NoError(err)
	assert.Empty(other, "provider IDs are scoped to the manifest's source")
}
