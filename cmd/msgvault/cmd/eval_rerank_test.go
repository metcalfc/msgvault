//go:build fts5 && sqlite_vec

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/eval"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/vector"
	"go.kenn.io/msgvault/internal/vector/embed"
	"go.kenn.io/msgvault/internal/vector/rerank"
	"go.kenn.io/msgvault/internal/vector/sqlitevec"
)

type evalRerankRecorder struct {
	requests     map[string][]rerank.Request
	failAt       map[string]int
	failUsage    rerank.Usage
	promoteText  string
	factoryCalls int
}

type recordingReranker struct {
	shape    string
	recorder *evalRerankRecorder
}

func (r *evalRerankRecorder) makeReranker(shape, _ string, _ *rerank.Budget) (evalReranker, error) {
	r.factoryCalls++
	if r.requests == nil {
		r.requests = make(map[string][]rerank.Request)
	}
	return &recordingReranker{shape: shape, recorder: r}, nil
}

func (r *recordingReranker) Rerank(ctx context.Context, request rerank.Request) (rerank.Result, error) {
	request.Candidates = append([]string(nil), request.Candidates...)
	r.recorder.requests[r.shape] = append(r.recorder.requests[r.shape], request)
	if r.recorder.failAt[r.shape] == len(r.recorder.requests[r.shape]) {
		return rerank.Result{Usage: r.recorder.failUsage}, errors.New("fake provider failure")
	}
	scores := make([]float64, len(request.Candidates))
	for i := range scores {
		if r.recorder.promoteText != "" {
			if strings.Contains(strings.ToLower(request.Candidates[i]), r.recorder.promoteText) {
				scores[i] = 1
			}
			continue
		}
		scores[i] = float64(len(scores)-i) / float64(len(scores))
	}
	input, output := int64(5), int64(2)
	requests := 1
	if r.shape == "per-candidate" {
		requests = len(request.Candidates)
	}
	return rerank.Result{Scores: scores, Usage: rerank.Usage{
		Requests: requests, InputTokens: &input, OutputTokens: &output, Complete: true,
	}}, nil
}

func preserveEvalRerankGlobals(t *testing.T) {
	t.Helper()
	oldQrels, oldTopics, oldModes, oldDocKey := evalQrels, evalTopics, evalModes, evalDocKey
	oldLimit, oldJSON := evalLimit, evalJSON
	oldJev, oldTop, oldMaxRequests := evalRerankJev, evalRerankTop, evalRerankMaxRequests
	oldCost, oldInput, oldOutput := evalRerankCostStopUSD, evalRerankInputUSDPerM, evalRerankOutputUSDPerM
	t.Cleanup(func() {
		evalQrels, evalTopics, evalModes, evalDocKey = oldQrels, oldTopics, oldModes, oldDocKey
		evalLimit, evalJSON = oldLimit, oldJSON
		evalRerankJev, evalRerankTop, evalRerankMaxRequests = oldJev, oldTop, oldMaxRequests
		evalRerankCostStopUSD, evalRerankInputUSDPerM, evalRerankOutputUSDPerM = oldCost, oldInput, oldOutput
	})
}

func newEvalRerankTestCommand(t *testing.T, out *bytes.Buffer, inputPrice, outputPrice bool) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().Float64("rerank-input-usd-per-million", 0, "")
	cmd.Flags().Float64("rerank-output-usd-per-million", 0, "")
	if inputPrice {
		require.NoError(t, cmd.Flags().Set("rerank-input-usd-per-million", "1"))
	}
	if outputPrice {
		require.NoError(t, cmd.Flags().Set("rerank-output-usd-per-million", "1"))
	}
	cmd.SetContext(t.Context())
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

func prepareEvalRerankRun(t *testing.T, shapes string, topicCount int) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	preserveEvalRerankGlobals(t)
	dir := t.TempDir()
	seedRankingDivergenceArchiveIn(t, dir)
	cfg := config.NewDefaultConfig()
	cfg.Data.DataDir = dir
	evalModes, evalDocKey, evalLimit, evalJSON = "fts", "message", 10, true
	evalRerankJev, evalRerankTop, evalRerankMaxRequests = shapes, 2, 1000
	evalRerankCostStopUSD, evalRerankInputUSDPerM, evalRerankOutputUSDPerM = 5, 1, 1
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	var topics, qrels strings.Builder
	for i := range topicCount {
		qid := fmt.Sprintf("q%d", i+1)
		fmt.Fprintf(&topics, "%s\trenewal\n", qid)
		fmt.Fprintf(&qrels, "%s 0 <m1@example.com> 1\n", qid)
	}
	evalQrels = writeEvalFile(t, dir, "qrels.txt", qrels.String())
	evalTopics = writeEvalFile(t, dir, "topics.tsv", topics.String())
	out := &bytes.Buffer{}
	cmd := newEvalRerankTestCommand(t, out, true, true)
	cmd.SetContext(testInvocationContext(cmd.Context(), cfg, invocationOptions{}))
	return cmd, out
}

func TestRunEvalReranksFTSCandidates(t *testing.T) {
	assert := assert.New(t)
	cmd, out := prepareEvalRerankRun(t, "batched,per-candidate", 2)
	recorder := &evalRerankRecorder{}
	require.NoError(t, runEvalWithRerankerFactory(cmd, nil, recorder.makeReranker))
	for _, shape := range []string{"batched", "per-candidate"} {
		require.Len(t, recorder.requests[shape], 2)
		for _, request := range recorder.requests[shape] {
			assert.Equal("renewal", request.Query)
			require.Len(t, request.Candidates, 2)
			candidateText := strings.ToLower(strings.Join(request.Candidates, "\n"))
			assert.Contains(request.Candidates, "Subject: Lease renewal terms\nFrom: \nDate: 2020-01-01\n\nSigned and returned.",
				"the eval sends the production candidate text")
			assert.Contains(candidateText, "lease renewal terms")
			assert.Contains(candidateText, "signed and returned")
			assert.NotContains(candidateText, "<m1@example.com>")
			assert.NotContains(candidateText, "me@example.com")
		}
	}
	var report struct {
		Rerank struct {
			Complete            bool    `json:"complete"`
			MaxRequests         int     `json:"max_requests"`
			CostStopUSD         float64 `json:"cost_stop_usd"`
			InputUSDPerMillion  float64 `json:"input_usd_per_million"`
			OutputUSDPerMillion float64 `json:"output_usd_per_million"`
			Results             map[string]map[string]struct {
				Status               string  `json:"status"`
				Requests             int     `json:"requests"`
				Topics               int     `json:"topics"`
				RequestsPerQuery     float64 `json:"requests_per_query"`
				InputTokensPerQuery  float64 `json:"input_tokens_per_query"`
				OutputTokensPerQuery float64 `json:"output_tokens_per_query"`
				CostPerQueryUSD      float64 `json:"cost_per_query_usd"`
				Latency              struct {
					P95MS *float64 `json:"p95_ms"`
				} `json:"latency"`
			} `json:"results"`
		} `json:"rerank_results"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	assert.True(report.Rerank.Complete)
	assert.Equal(1000, report.Rerank.MaxRequests)
	assert.InDelta(5.0, report.Rerank.CostStopUSD, 1e-9)
	assert.InDelta(1.0, report.Rerank.InputUSDPerMillion, 1e-9)
	assert.InDelta(1.0, report.Rerank.OutputUSDPerMillion, 1e-9)
	for shape, requests := range map[string]int{"batched": 2, "per-candidate": 4} {
		arm := report.Rerank.Results["fts"][shape]
		assert.Equal("complete", arm.Status)
		assert.Equal(2, arm.Topics)
		assert.Equal(requests, arm.Requests)
		assert.InDelta(float64(requests)/2, arm.RequestsPerQuery, 1e-9)
		assert.InDelta(5.0, arm.InputTokensPerQuery, 1e-9)
		assert.InDelta(2.0, arm.OutputTokensPerQuery, 1e-9)
		assert.InDelta(0.000007, arm.CostPerQueryUSD, 1e-12)
		assert.NotNil(arm.Latency.P95MS)
	}
}

func TestRunEvalJevSlowRequestWaves(t *testing.T) {
	cmd, out := prepareEvalRerankRun(t, "per-candidate", 2)
	state := invocationFromContext(cmd.Context())
	cfg := state.cfg
	evalLimit, evalRerankTop = 30, 30
	s, err := store.Open(cfg.DatabaseDSN())
	require.NoError(t, err)
	for i := 4; i <= 31; i++ {
		_, err := s.DB().Exec(`INSERT INTO messages
			(id, conversation_id, source_id, source_message_id, message_type, subject, size_estimate)
			VALUES (?, 1, 1, ?, 'email', 'renewal', 100)`, i, fmt.Sprintf("<m%d@example.com>", i))
		require.NoError(t, err)
	}
	_, err = s.BackfillFTS(nil)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	response := `{"model":"jev-1.13.0","answers":{"matches":{"type":"noul","noul":0.15}},"usage":{"input_tokens":342,"output_tokens":20}}`
	factory := func(shape, key string, budget *rerank.Budget) (evalReranker, error) {
		return rerank.NewJev(shape, key, budget, testTransport(func(request *http.Request) (*http.Response, error) {
			select {
			case <-time.After(3 * time.Second):
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}))
	}
	synctest.Test(t, func(t *testing.T) {
		cmd.SetContext(withInvocation(t.Context(), state))
		require.NoError(t, runEvalWithRerankerFactory(cmd, nil, factory))
	})
	var report struct {
		Rerank struct {
			Complete bool `json:"complete"`
			Results  map[string]map[string]struct {
				Topics   int `json:"topics"`
				Requests int `json:"requests"`
				Latency  struct {
					P95MS float64 `json:"p95_ms"`
				} `json:"latency"`
			} `json:"results"`
		} `json:"rerank_results"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	assert.True(t, report.Rerank.Complete)
	arm := report.Rerank.Results["fts"]["per-candidate"]
	assert.Equal(t, 2, arm.Topics)
	assert.Equal(t, 60, arm.Requests)
	assert.GreaterOrEqual(t, arm.Latency.P95MS, 12000.0)
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeReranker struct{}

func (fakeReranker) Rerank(_ context.Context, _ rerank.Request) (rerank.Result, error) {
	return rerank.Result{Scores: []float64{0.1, 0.9}, Usage: rerank.Usage{Complete: true}}, nil
}

func TestEvalRerankShortlist(t *testing.T) {
	assert := assert.New(t)
	keys, result, err := rerankEvalKeys(context.Background(), fakeReranker{}, "query",
		[]string{"first", "second", "tail"}, []string{"one", "two"})
	require.NoError(t, err)
	assert.Equal([]string{"second", "first", "tail"}, keys)
	assert.Equal([]float64{0.1, 0.9}, result.Scores)
	for _, shortlist := range [][]string{nil, {"only"}} {
		keys, result, err := rerankEvalKeys(t.Context(), nil, "query", shortlist, shortlist)
		require.NoError(t, err)
		assert.Equal(shortlist, keys)
		assert.Equal(rerank.Usage{InputTokens: new(int64(0)), OutputTokens: new(int64(0)), Complete: true}, result.Usage)
	}
}

func TestRunEvalRerankFailureKeepsCompleteBaseline(t *testing.T) {
	cmd, out := prepareEvalRerankRun(t, "batched,per-candidate", 3)
	input, output := int64(5), int64(2)
	recorder := &evalRerankRecorder{
		failAt:    map[string]int{"batched": 2},
		failUsage: rerank.Usage{Requests: 1, InputTokens: &input, OutputTokens: &output},
	}
	err := runEvalWithRerankerFactory(cmd, nil, recorder.makeReranker)
	require.ErrorContains(t, err, "provider request failed")
	require.Len(t, recorder.requests["batched"], 2, "provider work stops after the failed request")
	require.Len(t, recorder.requests["per-candidate"], 1)

	var report struct {
		TopicsEvaluated int `json:"topics_evaluated"`
		Results         map[string]struct {
			Topics int `json:"topics"`
		} `json:"results"`
		Rerank struct {
			Complete bool                                             `json:"complete"`
			Results  map[string]map[string]map[string]json.RawMessage `json:"results"`
		} `json:"rerank_results"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	assert.Equal(t, 3, report.TopicsEvaluated)
	assert.Equal(t, 3, report.Results["fts"].Topics)
	assert.False(t, report.Rerank.Complete)
	arm := report.Rerank.Results["fts"]["batched"]
	var status string
	require.NoError(t, json.Unmarshal(arm["status"], &status))
	assert.Equal(t, "failed", status)
	assert.NotContains(t, arm, "Hit@1")
	var requests int
	require.NoError(t, json.Unmarshal(arm["requests"], &requests))
	assert.Equal(t, 2, requests)
	var inputTokens int64
	require.NoError(t, json.Unmarshal(arm["input_tokens"], &inputTokens))
	assert.Equal(t, int64(10), inputTokens)
	var outputTokens int64
	require.NoError(t, json.Unmarshal(arm["output_tokens"], &outputTokens))
	assert.Equal(t, int64(4), outputTokens)
	assert.Contains(t, string(arm["cost_usd"]), "null")
	var incompleteStatus string
	var incompleteTopics int
	incomplete := report.Rerank.Results["fts"]["per-candidate"]
	require.NoError(t, json.Unmarshal(incomplete["status"], &incompleteStatus))
	require.NoError(t, json.Unmarshal(incomplete["topics"], &incompleteTopics))
	assert.Equal(t, "incomplete", incompleteStatus)
	assert.Equal(t, 1, incompleteTopics)
	assert.NotContains(t, incomplete, "Hit@1")
	assert.NotContains(t, incomplete, "latency")
}

func TestRunEvalRerankQualityMetricsFollowProviderScoresAcrossModes(t *testing.T) {
	cmd, out := prepareEvalRerankRun(t, "batched", 1)
	cfg := invocationFromContext(cmd.Context()).cfg
	dataDir := cfg.Data.DataDir
	evalQrels = writeEvalFile(t, dataDir, "quality-qrels.txt", "q1 0 <m2@example.com> 1\n")
	evalModes = "fts,vector,hybrid"
	c := evalVectorConfig(t, vector.APIFormatOpenAI, "test-model")
	c.Data.DataDir = dataDir
	c.Vector.Embeddings.Dimension = 3
	_, endpoint := embedTestServer(t, `{"data":[{"index":0,"embedding":[1,0,0]}]}`)
	c.Vector.Embeddings.Endpoint = endpoint
	invocationFromContext(cmd.Context()).cfg = c
	cfg = c
	cmd.SetContext(testInvocationContext(cmd.Context(), cfg, invocationOptions{}))
	s, err := store.Open(c.DatabaseDSN())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.InitSchema())
	seedEmbeddedGeneration(t, dataDir, c.DatabaseDSN(), s, c.Vector, 1, 2)
	require.NoError(t, sqlitevec.RegisterExtension())
	backend, err := sqlitevec.Open(context.Background(), sqlitevec.Options{
		Path: filepath.Join(dataDir, "vectors.db"), MainPath: c.DatabaseDSN(),
		Dimension: 3, MainDB: s.DB(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	generation, err := backend.ActiveGeneration(context.Background())
	require.NoError(t, err)
	require.NoError(t, backend.Upsert(context.Background(), generation.ID, []vector.Chunk{
		{MessageID: 1, Vector: []float32{1, 0, 0}, SourceCharLen: 32},
		{MessageID: 2, Vector: []float32{0, 1, 0}, SourceCharLen: 32},
	}))

	recorder := &evalRerankRecorder{promoteText: "weekly digest"}
	require.NoError(t, runEvalWithRerankerFactory(cmd, nil, recorder.makeReranker))
	var report struct {
		Results      map[string]json.RawMessage `json:"results"`
		RerankResult struct {
			Results map[string]map[string]map[string]json.RawMessage `json:"results"`
		} `json:"rerank_results"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	for _, mode := range []string{"fts", "vector", "hybrid"} {
		var baseline struct {
			Hit1 float64 `json:"Hit@1"`
		}
		require.NoError(t, json.Unmarshal(report.Results[mode], &baseline))
		var reranked struct {
			Hit1   float64 `json:"Hit@1"`
			Status string  `json:"status"`
		}
		require.NoError(t, json.Unmarshal(report.RerankResult.Results[mode]["batched"]["Hit@1"], &reranked.Hit1))
		require.NoError(t, json.Unmarshal(report.RerankResult.Results[mode]["batched"]["status"], &reranked.Status))
		assert.InDelta(t, 0, baseline.Hit1, 1e-9, mode)
		assert.InDelta(t, 1, reranked.Hit1, 1e-9, mode)
		assert.Equal(t, "complete", reranked.Status, mode)
	}
	assert.Len(t, recorder.requests["batched"], 3)
}

func TestValidateJevRequestEstimate(t *testing.T) {
	shapes := []string{"per-candidate", "batched"}
	require.NoError(t, validateJevRequestEstimate(10, 3, shapes, 30, 1000))
	require.ErrorContains(t, validateJevRequestEstimate(11, 3, shapes, 30, 1000), "conservative request estimate")
}

func TestRunEvalPreflightsJevRequestEstimateBeforeOpeningArchive(t *testing.T) {
	preserveEvalRerankGlobals(t)
	dir := t.TempDir()
	cfg := config.NewDefaultConfig()
	cfg.Data.DataDir = dir
	evalModes, evalDocKey, evalLimit, evalJSON = "fts,vector,hybrid", "message", 100, true
	evalRerankJev, evalRerankTop, evalRerankMaxRequests = "per-candidate,batched", 30, 1000
	evalRerankCostStopUSD, evalRerankInputUSDPerM, evalRerankOutputUSDPerM = 5, 1, 1
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	var topics, qrels strings.Builder
	for i := range 11 {
		qid := fmt.Sprintf("q%d", i+1)
		fmt.Fprintf(&topics, "%s\trenewal\n", qid)
		fmt.Fprintf(&qrels, "%s 0 <m1@example.com> 1\n", qid)
	}
	evalQrels = writeEvalFile(t, dir, "qrels.txt", qrels.String())
	evalTopics = writeEvalFile(t, dir, "topics.tsv", topics.String())
	cmd := newEvalRerankTestCommand(t, &bytes.Buffer{}, true, true)
	cmd.SetContext(testInvocationContext(cmd.Context(), cfg, invocationOptions{}))
	recorder := &evalRerankRecorder{}
	err := runEvalWithRerankerFactory(cmd, nil, recorder.makeReranker)
	require.ErrorContains(t, err, "conservative request estimate")
	assert.Zero(t, recorder.factoryCalls)
	_, statErr := os.Stat(filepath.Join(dir, "msgvault.db"))
	assert.True(t, os.IsNotExist(statErr), "the rejected estimate must not open the archive")
}

func gateAggregate(t *testing.T, relevantAt int, samples int) *eval.Aggregate {
	t.Helper()
	aggregate := &eval.Aggregate{}
	ranked := make([]string, 20)
	for i := range ranked {
		ranked[i] = fmt.Sprintf("m%d", i)
	}
	relevant := map[string]struct{}{fmt.Sprintf("m%d", relevantAt): {}}
	for range samples {
		aggregate.Add(eval.Evaluate(ranked, relevant, eval.CutoffsForDepth(20)))
	}
	return aggregate
}

func gateReport(t *testing.T, latencies map[string]time.Duration) *evalRerankReport {
	t.Helper()
	report := newEvalRerankReport(evalRerankOptions{Shapes: []string{"batched", "per-candidate"}, Top: 30})
	for shape, latency := range latencies {
		arm := report.arm("hybrid", shape)
		arm.Agg = gateAggregate(t, 0, 4)
		for range 4 {
			arm.Lat.Add(latency)
		}
	}
	return report
}

func TestEvalRerankGateThresholds(t *testing.T) {
	cutoffs := eval.CutoffsForDepth(20)
	baseline := map[string]*eval.Aggregate{"hybrid": gateAggregate(t, 15, 4)}

	t.Run("one shape passing passes the gate", func(t *testing.T) {
		assert := assert.New(t)
		gate := gateReport(t, map[string]time.Duration{
			"batched": 900 * time.Millisecond, "per-candidate": 2500 * time.Millisecond,
		}).gate(baseline, cutoffs)
		assert.Equal(rerankGatePass, gate.Status)
		assert.InDelta(0.05, gate.MinHit10Gain, 1e-12)
		assert.InDelta(2000.0, gate.MaxP95MS, 1e-12)
		batched := gate.Shapes["batched"]
		assert.Equal(rerankGatePass, batched.Status)
		require.NotNil(t, batched.Hit10Gain)
		assert.InDelta(1.0, *batched.Hit10Gain, 1e-12)
		perCandidate := gate.Shapes["per-candidate"]
		assert.Equal(rerankGateFail, perCandidate.Status, "p95 at or above 2 s fails")
		assert.True(perCandidate.GainPasses)
		assert.False(perCandidate.LatencyPasses)
	})

	t.Run("no gain fails", func(t *testing.T) {
		report := gateReport(t, map[string]time.Duration{"batched": time.Millisecond})
		gate := report.gate(map[string]*eval.Aggregate{"hybrid": gateAggregate(t, 0, 4)}, cutoffs)
		assert.Equal(t, rerankGateFail, gate.Status)
		assert.False(t, gate.Shapes["batched"].GainPasses)
		assert.Equal(t, rerankGateNotEvaluated, gate.Shapes["per-candidate"].Status, "an unrun shape is not judged")
	})

	t.Run("gate needs the hybrid mode and Hit@10", func(t *testing.T) {
		report := gateReport(t, map[string]time.Duration{"batched": time.Millisecond})
		assert.Equal(t, rerankGateNotEvaluated, report.gate(map[string]*eval.Aggregate{"fts": gateAggregate(t, 0, 1)}, cutoffs).Status)
		shallow := report.gate(baseline, eval.CutoffsForDepth(5))
		assert.Equal(t, rerankGateNotEvaluated, shallow.Status)
		assert.Contains(t, shallow.Reason, "--limit")
	})

	t.Run("table names the verdict", func(t *testing.T) {
		report := gateReport(t, map[string]time.Duration{"batched": 900 * time.Millisecond})
		var table bytes.Buffer
		require.NoError(t, report.gate(baseline, cutoffs).table(&table, report.Shapes))
		assert.Contains(t, table.String(), "Jev rerank gate (hybrid: Hit@10 gain >= 0.05 and p95 < 2000 ms): pass")
		assert.Contains(t, table.String(), "batched\tpass: Hit@10 0.000 -> 1.000 (gain +1.000, pass)")
	})
}

// TestRunEvalJevHarnessThroughFakeProvider runs the whole eval with the real
// Jev client against a local fake System One endpoint: the provider API is
// an external contract no test can reach, and the fake proves the harness
// sends production candidate text and reports the gate.
func TestRunEvalJevHarnessThroughFakeProvider(t *testing.T) {
	cmd, out := prepareEvalRerankRun(t, "batched", 1)
	cfg := invocationFromContext(cmd.Context()).cfg
	dataDir := cfg.Data.DataDir
	evalQrels = writeEvalFile(t, dataDir, "gate-qrels.txt", "q1 0 <m2@example.com> 1\n")
	evalModes = "hybrid"
	c := evalVectorConfig(t, vector.APIFormatOpenAI, "test-model")
	c.Data.DataDir = dataDir
	c.Vector.Embeddings.Dimension = 3
	_, endpoint := embedTestServer(t, `{"data":[{"index":0,"embedding":[1,0,0]}]}`)
	c.Vector.Embeddings.Endpoint = endpoint
	invocationFromContext(cmd.Context()).cfg = c
	cmd.SetContext(testInvocationContext(cmd.Context(), c, invocationOptions{}))
	s, err := store.Open(c.DatabaseDSN())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.InitSchema())
	seedEmbeddedGeneration(t, dataDir, c.DatabaseDSN(), s, c.Vector, 1, 2)
	require.NoError(t, sqlitevec.RegisterExtension())
	backend, err := sqlitevec.Open(context.Background(), sqlitevec.Options{
		Path: filepath.Join(dataDir, "vectors.db"), MainPath: c.DatabaseDSN(),
		Dimension: 3, MainDB: s.DB(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	generation, err := backend.ActiveGeneration(context.Background())
	require.NoError(t, err)
	require.NoError(t, backend.Upsert(context.Background(), generation.ID, []vector.Chunk{
		{MessageID: 1, Vector: []float32{1, 0, 0}, SourceCharLen: 32},
		{MessageID: 2, Vector: []float32{0, 1, 0}, SourceCharLen: 32},
	}))

	type fakeRequest struct {
		State struct {
			Candidates []string `json:"candidates"`
		} `json:"state"`
	}
	var bodies []fakeRequest
	factory := func(shape, key string, budget *rerank.Budget) (evalReranker, error) {
		return rerank.NewJev(shape, key, budget, testTransport(func(request *http.Request) (*http.Response, error) {
			raw, readErr := io.ReadAll(request.Body)
			require.NoError(t, readErr)
			var body fakeRequest
			require.NoError(t, json.Unmarshal(raw, &body))
			bodies = append(bodies, body)
			answers := map[string]any{}
			for i, candidate := range body.State.Candidates {
				score := 0.1
				if strings.Contains(candidate, "Weekly digest") {
					score = 0.9
				}
				answers[fmt.Sprintf("candidate_%d", i)] = map[string]any{"type": "noul", "noul": score}
			}
			encoded, encodeErr := json.Marshal(map[string]any{
				"model": "jev-1.13.0", "answers": answers,
				"usage": map[string]any{"input_tokens": 400, "output_tokens": 12},
			})
			require.NoError(t, encodeErr)
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(bytes.NewReader(encoded))}, nil
		}))
	}
	require.NoError(t, runEvalWithRerankerFactory(cmd, nil, factory))

	require.Len(t, bodies, 1)
	candidates := bodies[0].State.Candidates
	require.Len(t, candidates, 2)
	for _, candidate := range candidates {
		assert.Regexp(t, `^Subject: .+\nFrom: .*\nDate: \d{4}-\d{2}-\d{2}\n\n`, candidate)
	}
	var report struct {
		Gate evalRerankGate `json:"rerank_gate"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	assert.Equal(t, "hybrid", report.Gate.Mode)
	batched := report.Gate.Shapes["batched"]
	require.NotNil(t, batched.Hit10Gain)
	assert.InDelta(t, 0, *batched.Hit10Gain, 1e-12, "both messages were already in the top ten")
	assert.Equal(t, rerankGateFail, report.Gate.Status, "no gain cannot pass the gate")
}

func TestEvalRerankOptIn(t *testing.T) {
	assert := assert.New(t)
	old := evalRerankJev
	t.Cleanup(func() { evalRerankJev = old })
	evalRerankJev = ""
	options, err := readEvalRerankOptions(nil)
	require.NoError(t, err)
	assert.Empty(options.Shapes)
}

func TestReadEvalRerankOptionsRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name        string
		inputPrice  bool
		outputPrice bool
		key         string
		wantError   string
		mutate      func()
	}{
		{name: "conversation key", inputPrice: true, outputPrice: true, key: "test-key", wantError: "--rerank-jev requires --doc-key=message", mutate: func() { evalDocKey = "conversation" }},
		{name: "top outside provider bound", inputPrice: true, outputPrice: true, key: "test-key", wantError: "--rerank-top must be between", mutate: func() { evalRerankTop = rerank.MaxCandidates + 1 }},
		{name: "top cannot rerank", inputPrice: true, outputPrice: true, key: "test-key", wantError: "--rerank-top must be between", mutate: func() { evalRerankTop = 1 }},
		{name: "top exceeds retrieval depth", inputPrice: true, outputPrice: true, key: "test-key", wantError: "cannot exceed --limit", mutate: func() { evalRerankTop = evalLimit + 1 }},
		{name: "nonpositive request limit", inputPrice: true, outputPrice: true, key: "test-key", wantError: "--rerank-max-requests must be positive", mutate: func() { evalRerankMaxRequests = 0 }},
		{name: "invalid cost stop", inputPrice: true, outputPrice: true, key: "test-key", wantError: "must be a positive finite number", mutate: func() { evalRerankCostStopUSD = math.NaN() }},
		{name: "missing input price", outputPrice: true, key: "test-key", wantError: "--rerank-input-usd-per-million is required"},
		{name: "missing output price", inputPrice: true, key: "test-key", wantError: "--rerank-output-usd-per-million is required"},
		{name: "negative input price", inputPrice: true, outputPrice: true, key: "test-key", wantError: "must be a finite nonnegative number", mutate: func() { evalRerankInputUSDPerM = -1 }},
		{name: "nonfinite output price", inputPrice: true, outputPrice: true, key: "test-key", wantError: "must be a finite nonnegative number", mutate: func() { evalRerankOutputUSDPerM = math.Inf(1) }},
		{name: "unknown shape", inputPrice: true, outputPrice: true, key: "test-key", wantError: "invalid --rerank-jev value", mutate: func() { evalRerankJev = "unknown" }},
		{name: "empty shapes", inputPrice: true, outputPrice: true, key: "test-key", wantError: "must name per-candidate or batched", mutate: func() { evalRerankJev = ",," }},
		{name: "missing API key", inputPrice: true, outputPrice: true, key: " ", wantError: "TYPESAFE_API_KEY is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preserveEvalRerankGlobals(t)
			cfg := config.NewDefaultConfig()
			evalDocKey, evalLimit = "message", 10
			evalRerankJev, evalRerankTop, evalRerankMaxRequests = "batched", 2, 100
			evalRerankCostStopUSD, evalRerankInputUSDPerM, evalRerankOutputUSDPerM = 1, 1, 1
			t.Setenv("TYPESAFE_API_KEY", tc.key)
			cmd := newEvalRerankTestCommand(t, &bytes.Buffer{}, tc.inputPrice, tc.outputPrice)
			cmd.SetContext(testInvocationContext(cmd.Context(), cfg, invocationOptions{}))
			if tc.mutate != nil {
				tc.mutate()
			}
			_, err := readEvalRerankOptions(cmd)
			require.ErrorContains(t, err, tc.wantError)
		})
	}
}

func TestPrepareEvalCandidatesRequiresMessageIdentity(t *testing.T) {
	_, err := prepareEvalCandidates(t.Context(), nil, []string{"<message@example.com>"}, nil, embed.PreprocessConfig{}, 1)
	require.ErrorContains(t, err, "has no message identity")
}

func TestHitDepthLabel(t *testing.T) {
	assert := assert.New(t)
	report := evalReport{cutoffs: eval.CutoffsForDepth(5), diag: &runDiagnostics{}}
	var output bytes.Buffer
	require.NoError(t, report.table(&output))
	assert.Contains(output.String(), "Hit@5")
}

type evalRerankFailingWriter struct{ err error }

func (w evalRerankFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestEvalReportTablesReturnWriterErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	writer := evalRerankFailingWriter{err: writeErr}

	require.ErrorIs(t, (evalReport{}).table(writer), writeErr)
	require.ErrorIs(t, newEvalRerankReport(evalRerankOptions{}).table(writer, eval.StandardCutoffs), writeErr)
}

func TestEvalRerankReport(t *testing.T) {
	assert := assert.New(t)
	inputPrice, outputPrice := 1.0, 2.0
	report := newEvalRerankReport(evalRerankOptions{
		Shapes: []string{"batched"}, Top: 30, MaxRequests: 1000,
		CostStopUSD: 2, InputUSDPerM: inputPrice, OutputUSDPerM: outputPrice,
	})
	arm := report.arm("hybrid", "batched")
	arm.addUsage(rerank.Usage{Requests: 1}, inputPrice, outputPrice)
	arm.addQuality([]string{"relevant", "other"}, map[string]struct{}{"relevant": {}}, eval.CutoffsForDepth(5), time.Millisecond)
	var output bytes.Buffer
	err := evalReport{modes: []string{"hybrid"}, aggs: map[string]*eval.Aggregate{"hybrid": {}},
		lats:    map[string]*eval.LatencyTracker{"hybrid": &eval.LatencyTracker{}},
		catAggs: map[string]map[string]*eval.Aggregate{}, cutoffs: eval.CutoffsForDepth(5),
		diag: &runDiagnostics{}, rerank: report}.json(&output)
	require.NoError(t, err)
	assert.Contains(output.String(), `"usage_complete": false`)
	assert.Contains(output.String(), `"input_tokens": 0`)
	assert.Contains(output.String(), `"output_tokens": 0`)
	assert.Contains(output.String(), `"cost_usd": null`)
	assert.Contains(output.String(), `"input_tokens_per_query": 0`)
	assert.Contains(output.String(), `"output_tokens_per_query": 0`)
	assert.Contains(output.String(), `"cost_per_query_usd": null`)
	assert.Contains(output.String(), `"Hit@5"`)
}

func TestEvalRerankFailure(t *testing.T) {
	report := newEvalRerankReport(evalRerankOptions{
		Shapes: []string{"batched"}, Top: 30, MaxRequests: 1000,
		CostStopUSD: 2, InputUSDPerM: 1, OutputUSDPerM: 2,
	})
	arm := report.arm("hybrid", "batched")
	input, outputTokens := int64(5), int64(2)
	arm.addUsage(rerank.Usage{Requests: 1, InputTokens: &input, OutputTokens: &outputTokens}, 1, 2)
	arm.addQuality([]string{"relevant", "other"}, map[string]struct{}{"relevant": {}}, eval.CutoffsForDepth(5), time.Millisecond)
	arm.Status = "failed"
	arm.Complete = false
	arm.Error = "provider request failed"
	report.Complete = false
	report.Failure = "provider request failed"

	base := &eval.Aggregate{}
	base.Add(eval.Evaluate([]string{"relevant"}, map[string]struct{}{"relevant": {}}, eval.CutoffsForDepth(5)))
	var output bytes.Buffer
	err := (evalReport{
		modes: []string{"hybrid"}, aggs: map[string]*eval.Aggregate{"hybrid": base},
		lats:    map[string]*eval.LatencyTracker{"hybrid": &eval.LatencyTracker{}},
		catAggs: map[string]map[string]*eval.Aggregate{}, cutoffs: eval.CutoffsForDepth(5),
		diag: &runDiagnostics{}, rerank: report,
	}).json(&output)
	require.NoError(t, err)
	assert.Contains(t, output.String(), `"Hit@5"`)
	assert.Contains(t, output.String(), `"status": "failed"`)
	assert.Contains(t, output.String(), `"failure": "provider request failed"`)
	assert.Contains(t, output.String(), `"usage_complete": false`)
	assert.Contains(t, output.String(), `"input_tokens": 5`)
	assert.Contains(t, output.String(), `"output_tokens": 2`)
	assert.Contains(t, output.String(), `"cost_usd": null`)
	var document map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(output.Bytes(), &document))
	var rerankJSON struct {
		Results map[string]map[string]map[string]json.RawMessage `json:"results"`
	}
	require.NoError(t, json.Unmarshal(document["rerank_results"], &rerankJSON))
	assert.NotContains(t, rerankJSON.Results["hybrid"]["batched"], "Hit@1")
	assert.NotContains(t, rerankJSON.Results["hybrid"]["batched"], "Hit@5")

	var table bytes.Buffer
	require.NoError(t, report.table(&table, eval.CutoffsForDepth(5)))
	var failedRow string
	for line := range strings.SplitSeq(table.String(), "\n") {
		if strings.Contains(line, "hybrid") && strings.Contains(line, "batched") && strings.Contains(line, "failed") {
			failedRow = line
			break
		}
	}
	fields := strings.Fields(failedRow)
	require.GreaterOrEqual(t, len(fields), 16)
	assert.Equal(t, []string{"hybrid", "batched", "failed", "false", "1", "-", "-", "-", "-"}, fields[:9])
	assert.Equal(t, []string{"1", "-", "5", "-", "2", "-", "unknown"}, fields[9:16])
}

func TestEvalJSONOutputIsDeterministic(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, printJSONTo(&output, map[string]any{
		"z": 1,
		"a": map[string]int{"y": 2, "b": 1},
	}))
	text := output.String()
	assert.Less(t, strings.Index(text, `"a"`), strings.Index(text, `"z"`))
	_, inner, found := strings.Cut(text, `"a"`)
	require.True(t, found)
	assert.Less(t, strings.Index(inner, `"b"`), strings.Index(inner, `"y"`))
}
