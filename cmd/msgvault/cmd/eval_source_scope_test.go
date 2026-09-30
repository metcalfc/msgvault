//go:build fts5 && sqlite_vec

package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/store"
)

// seedTwoSourceArchiveIn builds an archive with two connected accounts whose
// source-assigned ids overlap, which is the shape a multi-source archiver
// routinely ends up in and no single-account benchmark corpus ever has.
//
//	source 1 (gmail)     thread-1 / <shared@example.com>, thread-1 / <s1-only@example.com>
//	source 2 (whatsapp)  thread-1 / <shared@example.com>
//
// Both the message id and the conversation id are shared across the two
// sources, and every message is live, so retrieval can return either copy.
// Passing shareMessageID=false keeps the message ids disjoint while leaving the
// conversation ids shared, so a test can tell the two doc-keys apart.
func seedTwoSourceArchiveIn(t *testing.T, dataDir string, shareMessageID bool) *store.Store {
	t.Helper()
	require := require.New(t)

	s, err := store.Open(filepath.Join(dataDir, "msgvault.db"))
	require.NoError(err, "open store")
	t.Cleanup(func() { require.NoError(s.Close()) })
	require.NoError(s.InitSchema(), "init schema")

	secondMessageID := "<shared@example.com>"
	if !shareMessageID {
		secondMessageID = "<s2-only@example.com>"
	}
	_, err = s.DB().Exec(`
INSERT INTO sources (id, source_type, identifier) VALUES
	(1, 'gmail', 'me@example.com'),
	(2, 'whatsapp', '+15550100');
INSERT INTO conversations (id, source_id, source_conversation_id, conversation_type) VALUES
	(1, 1, 'thread-1', 'email_thread'),
	(2, 2, 'thread-1', 'whatsapp_chat');
INSERT INTO messages
	(id, conversation_id, source_id, source_message_id, message_type, subject, sent_at, size_estimate) VALUES
	(1, 1, 1, '<shared@example.com>',  'email',    'Lease renewal terms', '2020-01-01T00:00:00Z', 100),
	(2, 1, 1, '<s1-only@example.com>', 'email',    'Re: Lease renewal',   '2020-01-02T00:00:00Z', 100),
	(3, 2, 2, ?,                       'whatsapp', 'Lease renewal chat',  '2020-01-03T00:00:00Z', 100);
`, secondMessageID)
	require.NoError(err, "seed two-source archive")

	for id, body := range map[int64]string{
		1: "Signed and returned.",
		2: "Counter-signed.",
		3: "Unrelated chat that happens to reuse the id.",
	} {
		_, err = s.DB().Exec(
			`INSERT INTO message_bodies (message_id, body_text) VALUES (?, ?)`, id, body)
		require.NoError(err, "seed body %d", id)
	}
	indexed, err := s.BackfillFTS(nil)
	require.NoError(err, "backfill FTS")
	require.EqualValues(3, indexed, "every message must be indexed")
	return s
}

// TestRankedFTS_CrossSourceIDsCollapseIntoOneKey characterizes the hazard the
// precondition exists to prevent, so the guard below is not asserting the
// absence of a problem nobody has shown to exist.
//
// Message 1 (gmail) and message 3 (whatsapp) are unrelated documents that
// happen to carry the same source-assigned id. Retrieval finds both, and the
// doc-key extraction reduces them to one key: the ranking hands the scoring
// core two hits' worth of evidence under a single id, so a judgment written
// about the gmail message silently grades the whatsapp one as well, and the
// depth quietly loses a rank.
func TestRankedFTS_CrossSourceIDsCollapseIntoOneKey(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := seedTwoSourceArchiveIn(t, t.TempDir(), true)
	const topic = "renewal"

	found, _, err := s.SearchMessagesQueryContext(t.Context(), evalTestQuery(t, topic), 0, 10)
	require.NoError(err, "production store search")
	require.Len(found, 3, "all three messages are live and match the topic")

	ev, _ := newTestEvaluator(t, s, "message")
	ev.limit = 10
	ranked, err := ev.rankedFTS(evalTestQuery(t, topic))
	require.NoError(err, "rankedFTS")

	assert.Len(ranked, 2,
		"three retrieved messages reduce to two keys: the two sources' ids collided")
	assert.Contains(ranked, "<shared@example.com>",
		"and the surviving key names a document in each source at once")
}

// TestRankedFTS_UnjudgedSharedIDsStayPerSource is the fix for that collapse
// when the shared id is one no judgment names. Once the run knows the judged
// ids, an unjudged id is qualified by its source, so the two copies stay two
// ranked documents, as a search shows them, while a judged id stays bare and
// still matches --qrels.
func TestRankedFTS_UnjudgedSharedIDsStayPerSource(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := seedTwoSourceArchiveIn(t, t.TempDir(), true)
	ev, _ := newTestEvaluator(t, s, "message")
	ev.limit = 10
	ev.judged = map[string]struct{}{"<s1-only@example.com>": {}}

	ranked, err := ev.rankedFTS(evalTestQuery(t, "renewal"))
	require.NoError(err, "rankedFTS")

	assert.Len(ranked, 3, "each account's copy of the unjudged shared id keeps its own rank")
	assert.Contains(ranked, "<s1-only@example.com>", "a judged id is left bare so qrels still match it")
	assert.NotContains(ranked, "<shared@example.com>", "an unjudged id never takes the bare key")
}

// TestRequireDisjointSourceIDs_RejectsJudgedCollidingMessageIDs is the guard
// for that collapse under --doc-key=message. When a judged id is shared, the
// run must stop with an error that names the id, the sources holding it, the
// key, and the column whose uniqueness does not hold.
func TestRequireDisjointSourceIDs_RejectsJudgedCollidingMessageIDs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := seedTwoSourceArchiveIn(t, t.TempDir(), true)
	registry := newDocKeyRegistry()

	_, err := requireDisjointSourceIDs(t.Context(), s, "message", registry["message"],
		[]string{"<s1-only@example.com>", "<shared@example.com>"})
	require.Error(err, "a judged id held by two sources must stop the run, not be scored")
	assert.Contains(err.Error(), "<shared@example.com> (sources: #1 gmail me@example.com; #2 whatsapp +15550100)",
		"the offending id is named with the sources holding it")
	assert.Contains(err.Error(), "source_message_id", "and so is the column whose uniqueness failed")
	assert.NotContains(err.Error(), "<s1-only@example.com>",
		"a judged id held by one source only is not a collision")
}

// TestRequireDisjointSourceIDs_AllowsUnjudgedCollidingIDs is the relaxation:
// the shared id is one no judgment names, so it scores as non-relevant under
// both accounts and cannot leak relevance. The run proceeds, and reports the
// several sources so the evaluator keeps the copies apart.
func TestRequireDisjointSourceIDs_AllowsUnjudgedCollidingIDs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := seedTwoSourceArchiveIn(t, t.TempDir(), true)
	registry := newDocKeyRegistry()

	multi, err := requireDisjointSourceIDs(t.Context(), s, "message", registry["message"],
		[]string{"<s1-only@example.com>", "<absent@example.com>"})
	require.NoError(err, "only unjudged ids are shared")
	assert.True(multi, "the archive holds two sources")
}

// TestRequireDisjointSourceIDs_ConversationKeyChecksThreadIDs pins the same
// guard for the coarser key, whose id lives on conversations rather than
// messages. Here the message ids are disjoint and only the thread ids overlap,
// so the two keys must reach opposite verdicts on the same judged id set — a
// check that quietly looked at one column for both keys would pass this.
func TestRequireDisjointSourceIDs_ConversationKeyChecksThreadIDs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := seedTwoSourceArchiveIn(t, t.TempDir(), false)
	registry := newDocKeyRegistry()
	judged := []string{"thread-1", "<shared@example.com>"}

	_, err := requireDisjointSourceIDs(t.Context(), s, "message", registry["message"], judged)
	require.NoError(err, "the message ids are disjoint in this archive")

	_, err = requireDisjointSourceIDs(t.Context(), s, "conversation", registry["conversation"], judged)
	require.Error(err, "the judged thread id is not")
	assert.Contains(err.Error(), "thread-1 (sources: #1 gmail me@example.com; #2 whatsapp +15550100)")
	assert.Contains(err.Error(), "source_conversation_id")

	_, err = requireDisjointSourceIDs(t.Context(), s, "conversation", registry["conversation"],
		[]string{"thread-9"})
	assert.NoError(err, "a shared thread id nobody judged does not stop the run")
}

// TestRequireDisjointSourceIDs_ChunksLargeJudgedSets keeps the lookup correct
// past one IN list: the colliding id sorts after the first chunk, so a check
// that only ran the first chunk would miss it.
func TestRequireDisjointSourceIDs_ChunksLargeJudgedSets(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := seedTwoSourceArchiveIn(t, t.TempDir(), true)
	judged := make([]string, 0, evalCollisionChunk+2)
	for i := range evalCollisionChunk + 1 {
		judged = append(judged, fmt.Sprintf("<filler-%04d@example.com>", i))
	}
	judged = append(judged, "<shared@example.com>")

	_, err := requireDisjointSourceIDs(t.Context(), s, "message", newDocKeyRegistry()["message"], judged)
	require.Error(err)
	assert.Contains(err.Error(), "<shared@example.com>")
}

// TestRequireDisjointSourceIDs_AllowsSeveralSourcesWithDisjointIDs is the other
// half of the decision: the precondition is disjointness, not single-source.
// msgvault archives normally hold several accounts, and refusing to score one
// because it has two sources — when nothing in it can collide — would be a wall
// built for a hazard that is not there.
func TestRequireDisjointSourceIDs_AllowsSeveralSourcesWithDisjointIDs(t *testing.T) {
	require := require.New(t)
	s := seedTwoSourceArchiveIn(t, t.TempDir(), false)

	var sources int
	require.NoError(s.DB().QueryRow(
		`SELECT COUNT(DISTINCT source_id) FROM messages`).Scan(&sources))
	require.Equal(2, sources, "the archive really does hold two connected accounts")

	_, err := requireDisjointSourceIDs(t.Context(), s, "message", newDocKeyRegistry()["message"],
		[]string{"<shared@example.com>", "<s2-only@example.com>"})
	require.NoError(err, "two accounts that share no message id are scorable")
}

// TestRequireDisjointSourceIDs_IgnoresUnretrievableCopies pins the population
// the check runs over. A dedup-hidden or source-deleted copy is not something
// any search here returns, so it cannot reach a ranking and cannot collide with
// anything — counting it would refuse to score an archive that is fine.
func TestRequireDisjointSourceIDs_IgnoresUnretrievableCopies(t *testing.T) {
	require := require.New(t)
	s := seedTwoSourceArchiveIn(t, t.TempDir(), true)

	// Hide the second source's copy the way dedup does, leaving the row in
	// place.
	_, err := s.DB().Exec(
		`UPDATE messages SET deleted_at = '2023-01-01T00:00:00Z' WHERE id = 3`)
	require.NoError(err, "hide the duplicate")

	_, err = requireDisjointSourceIDs(t.Context(), s, "message", newDocKeyRegistry()["message"],
		[]string{"<shared@example.com>"})
	require.NoError(err, "the surviving copy is the only retrievable one, so the id names one document")
}

// TestRequireDisjointSourceIDs_RejectsAKeyWithNoArchiveColumn keeps the check
// from being skipped by omission. A doc-key added without saying where its ids
// live would otherwise sail past the guard and reintroduce the collapse; it has
// to fail loudly instead, naming itself.
func TestRequireDisjointSourceIDs_RejectsAKeyWithNoArchiveColumn(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := seedTwoSourceArchiveIn(t, t.TempDir(), false)
	unbacked := docKeySpec{extract: func(evalHit) string { return "x" }}

	_, err := requireDisjointSourceIDs(t.Context(), s, "thread", unbacked, []string{"x"})
	require.Error(err, "an unbacked key must not silently skip the check")
	assert.Contains(err.Error(), `"thread"`)
}

// TestRunEval_StopsOnJudgedCrossSourceIDCollisions drives the whole command, so
// the guard is pinned at the call site and not just in its own helper. Without
// it the run completes and reports a perfect MRR for q1: the whatsapp message
// carries the gmail message's id, so its hit is scored against the gmail
// message's judgment.
func TestRunEval_StopsOnJudgedCrossSourceIDCollisions(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	seedTwoSourceArchiveIn(t, dir, true)
	testCtx := configureEvalRun(t, dir,
		"q1 0 <shared@example.com> 1\n",
		"q1\trenewal\n")

	cmd := &cobra.Command{}
	cmd.SetContext(testCtx)

	err := runEval(cmd, nil)
	require.Error(err, "the run must stop rather than print a number it cannot justify")
	assert.Contains(err.Error(), "more than one connected source")
	assert.Contains(err.Error(), "--doc-key=message")
}

// seedSharedCalendarArchiveIn builds two accounts that both hold the same
// unjudged event (same message id, same thread id), plus one judged message in
// the first account that ranks below both copies:
//
//	source 1  cal-evt / <evt@example.com>       "Renewal renewal renewal"
//	source 2  cal-evt / <evt@example.com>       "Renewal renewal renewal"
//	source 1  thread-judged / <judged@example.com>  a longer, weaker match
//
// If the two event copies folded into one ranked entry, the judged message
// would move from rank 3 to rank 2 and MRR would read 0.5 instead of 1/3.
func seedSharedCalendarArchiveIn(t *testing.T, dataDir string) *store.Store {
	t.Helper()
	require := require.New(t)

	s, err := store.Open(filepath.Join(dataDir, "msgvault.db"))
	require.NoError(err, "open store")
	t.Cleanup(func() { require.NoError(s.Close()) })
	require.NoError(s.InitSchema(), "init schema")

	_, err = s.DB().Exec(`
INSERT INTO sources (id, source_type, identifier) VALUES
	(1, 'gmail', 'me@example.com'),
	(2, 'gmail', 'work@example.com');
INSERT INTO conversations (id, source_id, source_conversation_id, conversation_type) VALUES
	(1, 1, 'cal-evt', 'email_thread'),
	(2, 2, 'cal-evt', 'email_thread'),
	(3, 1, 'thread-judged', 'email_thread');
INSERT INTO messages
	(id, conversation_id, source_id, source_message_id, message_type, subject, sent_at, size_estimate) VALUES
	(1, 1, 1, '<evt@example.com>',    'email', 'Renewal renewal renewal', '2020-01-01T00:00:00Z', 100),
	(2, 2, 2, '<evt@example.com>',    'email', 'Renewal renewal renewal', '2020-01-01T00:00:00Z', 100),
	(3, 3, 1, '<judged@example.com>', 'email',
		'Quarterly planning notes covering the office move, budget review and renewal', '2020-01-02T00:00:00Z', 100);
`)
	require.NoError(err, "seed shared-calendar archive")
	for id := int64(1); id <= 3; id++ {
		_, err = s.DB().Exec(
			`INSERT INTO message_bodies (message_id, body_text) VALUES (?, ?)`, id, "See attached.")
		require.NoError(err, "seed body %d", id)
	}
	_, err = s.BackfillFTS(nil)
	require.NoError(err, "backfill FTS")
	return s
}

// TestRunEval_ScoresPastUnjudgedSharedIDs drives the whole command over an
// archive whose accounts share an unjudged id under both doc-keys. The run
// must proceed, and must score the judged document at the rank a search shows
// it — third, behind both accounts' copies of the event.
func TestRunEval_ScoresPastUnjudgedSharedIDs(t *testing.T) {
	for _, tc := range []struct {
		docKey string
		judged string
	}{
		{"message", "<judged@example.com>"},
		{"conversation", "thread-judged"},
	} {
		t.Run(tc.docKey, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			dir := t.TempDir()
			s := seedSharedCalendarArchiveIn(t, dir)
			found, _, err := s.SearchMessagesQueryContext(t.Context(), evalTestQuery(t, "renewal"), 0, 10)
			require.NoError(err, "production store search")
			require.Len(found, 3)
			require.Equal("<judged@example.com>", found[2].SourceMessageID,
				"precondition: both event copies outrank the judged message")

			testCtx := configureEvalRun(t, dir, "q1 0 "+tc.judged+" 1\n", "q1\trenewal\n")
			evalDocKey = tc.docKey
			cmd := &cobra.Command{}
			cmd.SetContext(testCtx)

			done := captureStdout(t)
			err = runEval(cmd, nil)
			out := done()
			require.NoError(err, "an unjudged shared id must not stop the run")

			var report struct {
				Results map[string]struct {
					MRR float64 `json:"MRR@10"`
					P   float64 `json:"P@10"`
				} `json:"results"`
			}
			require.NoError(json.Unmarshal([]byte(out), &report), "parse report: %s", out)
			scored := report.Results[evalTestMode]
			assert.InDelta(1.0/3, scored.MRR, 1e-9,
				"each account's copy keeps its rank; folding them would report 0.5")
			assert.InDelta(0.1, scored.P, 1e-9)
		})
	}
}
