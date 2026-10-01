package store_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestDocumentIndexSchemaStoresCanonicalTextAndRebuildableFTS(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	profileID := "profile-" + strings.Repeat("a", 64)
	extractionID := "extraction-synthetic"
	hash := strings.Repeat("b", 64)

	_, err := st.DB().Exec(st.Rebind(`
		INSERT INTO document_extraction_profiles
			(id, fingerprint, provider, endpoint, region, model,
			 retention_posture, training_posture, allowed_media_types,
			 policy_json, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		profileID, strings.Repeat("a", 64), "mistral",
		"https://api.mistral.ai/v1/ocr", "eu", "mistral-ocr-4-0",
		"standard", "opted-out", `["application/pdf"]`, `{}`, true,
	)
	require.NoError(err)
	_, err = st.DB().Exec(st.Rebind(`
		INSERT INTO document_extractions
			(id, profile_id, canonical_blob_hash, extraction_input_key,
			 state, local_bytes)
		VALUES (?, ?, ?, 'original', 'ready', ?)`),
		extractionID, profileID, hash, 128,
	)
	require.NoError(err)
	_, err = st.DB().Exec(st.Rebind(`
		INSERT INTO document_units
			(extraction_id, unit_index, unit_kind, text, checksum,
			 char_count, truncated)
		VALUES (?, 0, 'page', ?, ?, ?, ?)`),
		extractionID, "canonical unit text", strings.Repeat("c", 64), 19, false,
	)
	require.NoError(err)
	_, err = st.DB().Exec(st.Rebind(`
		INSERT INTO document_chunks
			(extraction_id, chunk_key, ordinal, text, heading_path,
			 first_unit_index, last_unit_index, checksum, char_count)
		VALUES (?, 'chunk-0', 0, ?, ?, 0, 0, ?, ?)`),
		extractionID, "quasar invoice total", `[]`, strings.Repeat("d", 64), 20,
	)
	require.NoError(err)
	_, err = st.DB().Exec(st.Rebind(`
		INSERT INTO document_chunk_spans
			(extraction_id, chunk_key, span_ordinal, unit_index,
			 start_char, end_char, synthetic)
		VALUES (?, 'chunk-0', 0, 0, 0, 19, ?)`),
		extractionID, false,
	)
	require.NoError(err)

	assert.Equal(t, 1, documentFTSMatchCount(t, st, "quasar"))
	var revision int64
	require.NoError(st.DB().QueryRow(st.Rebind(
		`SELECT revision FROM document_index_state WHERE singleton = ?`), 1).Scan(&revision))
	assert.Zero(t, revision)

	_, err = st.DB().Exec(st.Rebind(`DELETE FROM document_extractions WHERE id = ?`), extractionID)
	require.NoError(err)
	assert.Zero(t, documentFTSMatchCount(t, st, "quasar"))
}

func TestInitSchemaAddsDocumentRebuildIDToLegacyExtractionTable(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	drop := `ALTER TABLE document_extractions DROP COLUMN rebuild_id`

	_, err := st.DB().Exec(drop)
	require.NoError(err)

	require.NoError(st.InitSchema())
	var columnCount int
	query := `SELECT COUNT(*) FROM pragma_table_info('document_extractions') WHERE name = 'rebuild_id'`

	require.NoError(st.DB().QueryRow(query).Scan(&columnCount))
	assert.Equal(t, 1, columnCount)
}

func TestInitSchemaAddsDocumentTargetProfileToLegacyIndexState(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	_, err := st.DB().Exec(`ALTER TABLE document_index_state DROP COLUMN target_profile_id`)
	require.NoError(err)

	require.NoError(st.InitSchema())
	var columnCount int
	query := `SELECT COUNT(*) FROM pragma_table_info('document_index_state') WHERE name = 'target_profile_id'`

	require.NoError(st.DB().QueryRow(query).Scan(&columnCount))
	assert.Equal(t, 1, columnCount)
}

func TestInitSchemaAddsDocumentProviderAccountingToLegacyExtractions(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	for _, column := range []string{"retry_count", "request_count", "provider_latency_ms"} {
		_, err := st.DB().Exec(`ALTER TABLE document_extractions DROP COLUMN ` + column)
		require.NoError(err)
	}

	require.NoError(st.InitSchema())
	for _, column := range []string{"request_count", "retry_count", "provider_latency_ms"} {
		var columnCount int
		query := `SELECT COUNT(*) FROM pragma_table_info('document_extractions') WHERE name = ?`

		require.NoError(st.DB().QueryRow(st.Rebind(query), column).Scan(&columnCount))
		assert.Equal(t, 1, columnCount, column)
	}
}

func documentFTSMatchCount(t *testing.T, st *store.Store, term string) int {
	t.Helper()
	var count int

	require.NoError(t, st.DB().QueryRow(`
		SELECT COUNT(*) FROM document_chunks_fts
		WHERE document_chunks_fts MATCH ?`, term).Scan(&count))
	return count
}
