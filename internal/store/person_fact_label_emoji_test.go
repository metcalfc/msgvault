package store

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/personfacts"
)

// A claim stored before label normalization removed emoji and the same value
// submitted after it resolve as one fact, and replaying the newer generation
// changes nothing.
func TestPersonFactClaimStoredWithEmojiResolvesWithCleanedClaim(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st, personID, _ := newPersonFactProjectionStore(t)
	definition, err := st.GetAttributeDefinitionBySlugContext(t.Context(), AttributeObjectPerson, AttributeSlugLocation)
	require.NoError(err)
	target := projectionTargetBySlug(t, st, definition.Slug)

	_, err = st.ApplyPersonFactGenerationContext(t.Context(), personFactProjectionInput(personID, "old",
		[]personfacts.ProposedClaim{personFactProjectionClaim(personID, target, `"Ana ✨"`, "old")}, nil), nil)
	require.NoError(err)
	// Rewrite the stored claim and projection as an earlier release wrote them.
	oldJSON := `"Ana ✨"`
	_, err = st.db.Exec(`UPDATE person_fact_claims SET normalized_value_json = ?, value_fingerprint = ?`,
		oldJSON, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(oldJSON))))
	require.NoError(err)
	_, err = st.db.Exec(`UPDATE person_attribute_values SET value_text = 'Ana ✨' WHERE definition_id = ?`,
		definition.ID)
	require.NoError(err)
	require.NoError(st.stripStoredLabelEmoji(t.Context()))

	newer := personFactProjectionInput(personID, "new",
		[]personfacts.ProposedClaim{personFactProjectionClaim(personID, target, `"Ana"`, "new")}, nil)
	result, err := st.ApplyPersonFactGenerationContext(t.Context(), newer, nil)
	require.NoError(err)
	actions := make([]personfacts.DecisionAction, 0, len(result.Decisions))
	for _, decision := range result.Decisions {
		actions = append(actions, decision.Action)
	}
	assert.ElementsMatch([]personfacts.DecisionAction{
		personfacts.DecisionApplied, personfacts.DecisionSuperseded,
	}, actions, "the old and new claims are one value, not a competing tie")
	values, err := st.ListPersonAttributeValuesContext(t.Context(), personID,
		PersonAttributeQuery{DefinitionSlug: definition.Slug})
	require.NoError(err)
	require.Len(values, 1)
	assert.Equal("Ana", *values[0].Value.Text)

	before := personFactProjectionCounts(t, st)
	_, err = st.ApplyPersonFactGenerationContext(t.Context(), newer, nil)
	require.NoError(err)
	assert.Equal(before, personFactProjectionCounts(t, st))
}
