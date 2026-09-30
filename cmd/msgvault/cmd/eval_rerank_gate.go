//go:build sqlite_vec

package cmd

import (
	"fmt"
	"io"

	"go.kenn.io/msgvault/internal/eval"
)

// The Jev rerank eval gate (commit 01ea4543, docs/usage/jev-judgments.md):
// hybrid search reranking is recommended only when one complete hybrid shape
// gains at least 0.05 absolute Hit@10 over the fused hybrid ranking with a
// p95 latency under 2 s. The run must use the target collection (matched
// TREC Legal 2010 messages); the gate cannot check which collection it saw.
const (
	rerankGateMode         = "hybrid"
	rerankGateMinHit10Gain = 0.05
	rerankGateMaxP95MS     = 2000.0
	rerankGateHitDepth     = 10
	// rerankGateEpsilon absorbs float error in the gain comparison.
	rerankGateEpsilon = 1e-9
)

// Gate verdicts.
const (
	rerankGatePass         = "pass"
	rerankGateFail         = "fail"
	rerankGateNotEvaluated = "not_evaluated"
)

type evalRerankGateShape struct {
	Status        string   `json:"status"`
	Reason        string   `json:"reason,omitempty"`
	BaselineHit10 *float64 `json:"baseline_hit10"`
	RerankedHit10 *float64 `json:"reranked_hit10"`
	Hit10Gain     *float64 `json:"hit10_gain"`
	P95MS         *float64 `json:"p95_ms"`
	GainPasses    bool     `json:"gain_passes"`
	LatencyPasses bool     `json:"latency_passes"`
}

type evalRerankGate struct {
	Status       string                         `json:"status"`
	Reason       string                         `json:"reason,omitempty"`
	Mode         string                         `json:"mode"`
	MinHit10Gain float64                        `json:"min_hit10_gain"`
	MaxP95MS     float64                        `json:"max_p95_ms"`
	Shapes       map[string]evalRerankGateShape `json:"shapes"`
}

// gate judges every shape of the hybrid arm against the fused hybrid
// baseline. The whole gate passes when any complete shape passes both
// thresholds.
func (r *evalRerankReport) gate(baselines map[string]*eval.Aggregate, cutoffs eval.Cutoffs) evalRerankGate {
	gate := evalRerankGate{
		Status: rerankGateNotEvaluated, Mode: rerankGateMode,
		MinHit10Gain: rerankGateMinHit10Gain, MaxP95MS: rerankGateMaxP95MS,
		Shapes: make(map[string]evalRerankGateShape, len(r.Shapes)),
	}
	baseline := baselines[rerankGateMode]
	switch {
	case baseline == nil || baseline.N == 0:
		gate.Reason = "the hybrid mode was not evaluated; run with --modes including hybrid"
		return gate
	case eval.HitDepth(cutoffs) < rerankGateHitDepth:
		gate.Reason = fmt.Sprintf("Hit@%d needs a retrieval depth of at least %d; raise --limit", rerankGateHitDepth, rerankGateHitDepth)
		return gate
	}
	baseHit10 := baseline.Mean().Hit10
	evaluated := false
	for _, shape := range r.Shapes {
		verdict := evalRerankGateShape{Status: rerankGateNotEvaluated, BaselineHit10: new(baseHit10)}
		arm := r.Results[rerankGateMode][shape]
		if arm == nil || !arm.Complete || arm.Status != "complete" || arm.Agg == nil || arm.Agg.N == 0 {
			verdict.Reason = "the reranked arm did not complete"
			gate.Shapes[shape] = verdict
			continue
		}
		evaluated = true
		reranked := arm.Agg.Mean().Hit10
		gain := reranked - baseHit10
		p95 := arm.Lat.Summary().P95MS
		verdict.RerankedHit10, verdict.Hit10Gain, verdict.P95MS = new(reranked), new(gain), new(p95)
		verdict.GainPasses = gain+rerankGateEpsilon >= rerankGateMinHit10Gain
		verdict.LatencyPasses = p95 < rerankGateMaxP95MS
		verdict.Status = rerankGateFail
		if verdict.GainPasses && verdict.LatencyPasses {
			verdict.Status = rerankGatePass
			gate.Status = rerankGatePass
		}
		gate.Shapes[shape] = verdict
	}
	if gate.Status != rerankGatePass {
		if evaluated {
			gate.Status = rerankGateFail
		} else {
			gate.Reason = "no reranked hybrid arm completed"
		}
	}
	return gate
}

func (g evalRerankGate) table(w io.Writer, shapes []string) error {
	if _, err := fmt.Fprintf(w, "\nJev rerank gate (%s: Hit@10 gain >= %.2f and p95 < %.0f ms): %s\n",
		g.Mode, g.MinHit10Gain, g.MaxP95MS, g.Status); err != nil {
		return fmt.Errorf("write rerank gate: %w", err)
	}
	if g.Reason != "" {
		if _, err := fmt.Fprintf(w, "  %s\n", g.Reason); err != nil {
			return fmt.Errorf("write rerank gate: %w", err)
		}
		return nil
	}
	for _, shape := range shapes {
		verdict, ok := g.Shapes[shape]
		if !ok {
			continue
		}
		if verdict.Hit10Gain == nil {
			if _, err := fmt.Fprintf(w, "  %s\t%s: %s\n", shape, verdict.Status, verdict.Reason); err != nil {
				return fmt.Errorf("write rerank gate: %w", err)
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "  %s\t%s: Hit@10 %.3f -> %.3f (gain %+.3f, %s), p95 %.1f ms (%s)\n",
			shape, verdict.Status, *verdict.BaselineHit10, *verdict.RerankedHit10, *verdict.Hit10Gain,
			passWord(verdict.GainPasses), *verdict.P95MS, passWord(verdict.LatencyPasses)); err != nil {
			return fmt.Errorf("write rerank gate: %w", err)
		}
	}
	return nil
}

func passWord(passed bool) string {
	if passed {
		return rerankGatePass
	}
	return rerankGateFail
}
