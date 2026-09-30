// Package sweepjudge holds the Jev judgments the people sweep asks around its
// chat model: which retrieved messages bear on a fact before extraction
// (sweep_evidence_rerank), and whether each extracted claim is stated and
// still current (sweep_claim_grounding). Both send message excerpts the
// person wrote on a source that authenticates its sender, and nothing else
// about the person or the archive owner.
package sweepjudge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/peoplesweep"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/vector/rerank"
)

// Judge is the shared Jev door. *jev.Service implements it.
type Judge interface {
	JudgeQuestions(
		ctx context.Context, spec jev.FeatureSpec, automatic bool, state any, questionIDs []string, deadline time.Time,
	) (jev.Response, error)
}

// Candidate and query bounds for the context judgment.
const (
	// ContextCandidatesPerRequest is how many context items one request
	// asks about: the shared reranker's batched limit.
	ContextCandidatesPerRequest = rerank.MaxCandidates
	// MaxContextCandidateBytes bounds one candidate's text, date included.
	MaxContextCandidateBytes = rerank.MaxCandidateBytes
	maxContextQueryRunes     = 400
)

// EvidenceRerankFeature is the exact policy the sweep_evidence_rerank
// feature consents to: the shared search reranker's batched Noul wording
// over the fact being looked for and the retrieved excerpts.
func EvidenceRerankFeature() jev.FeatureSpec {
	return jev.FeatureSpec{
		Name:  jev.FeatureSweepEvidenceRerank,
		Title: "People sweep evidence relevance",
		Purpose: "Before the people sweep sends a person's older messages to its chat model, ask Jev " +
			"which of them bear on each fact it looks for (for example employment), and leave out the " +
			"ones that do not, so the chat model reads less and cites better evidence. Newly changed " +
			"messages are always sent to the chat model; only retrieved older context is judged.",
		Questions: rerank.BatchedQuestions(),
		StateFields: []string{
			"query: the catalog description of the fact being looked for, such as \"Current and historical " +
				"employment\"; never a name or an address",
			"candidates[]: for each retrieved message, its date and up to 2 KiB of excerpt text from a " +
				"message the person sent on a source that authenticates its sender, with email addresses " +
				"and phone numbers replaced by placeholders; at most 30 per request",
		},
		BodyNotice: "each judged message sends up to 2 KiB of excerpt text the person wrote, and its date. " +
			"Your own identities, the person's name, and their addresses are never sent as fields.",
	}
}

// ContextJudge implements peoplesweep.ContextJudge through the gated Jev
// service. It never judges a sensitive target's context.
type ContextJudge struct {
	judge     Judge
	automatic bool
	logger    *slog.Logger
}

var _ peoplesweep.ContextJudge = (*ContextJudge)(nil)

// NewContextJudge wires the context judgment. automatic marks the daemon's
// scheduled sweeps, which the service admits only when the feature allows
// automatic use.
func NewContextJudge(judge Judge, automatic bool, logger *slog.Logger) *ContextJudge {
	if logger == nil {
		logger = slog.Default()
	}
	return &ContextJudge{judge: judge, automatic: automatic, logger: logger}
}

type contextState struct {
	Query      string   `json:"query"`
	Candidates []string `json:"candidates"`
}

// JudgeContext scores each item's relevance to the target, one request per
// ContextCandidatesPerRequest items. Any gate, budget, or provider failure
// returns an error, and the sweep keeps every item.
func (j *ContextJudge) JudgeContext(
	ctx context.Context, target personfacts.TargetDescriptor, items []peoplesweep.EvidenceItem,
) ([]float64, error) {
	if j == nil || j.judge == nil {
		return nil, fmt.Errorf("%w: no jev judge", jev.ErrPolicyUnavailable)
	}
	if target.Sensitive {
		return nil, fmt.Errorf("%w: sensitive targets are never judged", jev.ErrRequestBounds)
	}
	query := contextQuery(target)
	if query == "" {
		return nil, fmt.Errorf("%w: target has no description", jev.ErrRequestBounds)
	}
	spec := EvidenceRerankFeature()
	scores := make([]float64, 0, len(items))
	for start := 0; start < len(items); start += ContextCandidatesPerRequest {
		chunk := items[start:min(start+ContextCandidatesPerRequest, len(items))]
		state := contextState{Query: query, Candidates: make([]string, len(chunk))}
		ids := make([]string, len(chunk))
		for i, item := range chunk {
			state.Candidates[i] = contextCandidate(item)
			ids[i] = rerank.BatchedQuestionID(i)
		}
		response, err := j.judge.JudgeQuestions(ctx, spec, j.automatic, state, ids, time.Time{})
		if err != nil {
			j.logger.Info("people sweep context relevance: jev skipped",
				"feature", jev.FeatureSweepEvidenceRerank, "category", jev.Skipped(err))
			return nil, err
		}
		for i := range chunk {
			answer, ok := response.Answers[rerank.BatchedQuestionID(i)]
			if !ok {
				return nil, fmt.Errorf("%w: answer %d missing", jev.ErrInvalidResponse, i)
			}
			scores = append(scores, min(1, max(0, answer.Noul)))
		}
	}
	return scores, nil
}

// contextQuery is the target's catalog description, falling back to its slug.
func contextQuery(target personfacts.TargetDescriptor) string {
	query := strings.Join(strings.Fields(target.Description), " ")
	if query == "" {
		query = strings.ReplaceAll(strings.TrimSpace(target.Slug), "_", " ")
	}
	return truncateRunes(query, maxContextQueryRunes)
}

// contextCandidate renders one item as its date and redacted excerpt,
// within MaxContextCandidateBytes.
func contextCandidate(item peoplesweep.EvidenceItem) string {
	header := "Date: "
	if !item.EventTime.IsZero() {
		header += item.EventTime.UTC().Format(time.DateOnly)
	}
	return rerank.TruncateUTF8Bytes(header+"\n\n"+meetingjudge.RedactText(item.Excerpt), MaxContextCandidateBytes)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
