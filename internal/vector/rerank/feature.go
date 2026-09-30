package rerank

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/msgvault/internal/jev"
)

// JevFeature is the exact policy the search_rerank feature consents to. It
// holds both request shapes' wording: one "matches" question per candidate
// for per_candidate, and candidate_0..candidate_29 for batched, each worded
// exactly as encodeJevCalls sends it. A request asks only the questions its
// candidates need. Changing any wording or field changes the fingerprint and
// requires new consent.
func JevFeature() jev.FeatureSpec {
	questions := make([]jev.Question, 0, MaxCandidates+1)
	questions = append(questions, rankingQuestion(perCandidateQuestionID, "candidate"))
	for i := range MaxCandidates {
		questions = append(questions, rankingQuestion(batchedQuestionID(i), fmt.Sprintf("candidates[%d]", i)))
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureSearchRerank,
		Title: "Hybrid search reranking",
		Purpose: "Reorder the leading results of your own hybrid searches by asking Jev which messages " +
			"contain the information your query asks for. Full-text searches and automatic searches never use it.",
		Questions: questions,
		StateFields: []string{
			"query",
			"candidate (per_candidate shape): Subject, From (sender name, or address when there is no name), " +
				"Date, and preprocessed body text of one message, at most 2 KiB in total",
			"candidates[] (batched shape): the same text for each reranked message, at most 30",
		},
		BodyNotice: "each reranked message sends up to 2 KiB of its body text after quote, signature, " +
			"and HTML cleaning, together with its subject, sender, and date.",
	}
}

// ShapeFromConfig maps a [jev.rerank] shape to this package's spelling.
func ShapeFromConfig(shape string) (string, error) {
	switch shape {
	case jev.RerankShapeBatched:
		return ShapeBatched, nil
	case jev.RerankShapePerCandidate:
		return ShapePerCandidate, nil
	default:
		return "", fmt.Errorf("unknown [jev.rerank] shape %q", shape)
	}
}

// Judge is the shared Jev door the service scorer asks through.
// *jev.Service implements it.
type Judge interface {
	Admit(ctx context.Context, spec jev.FeatureSpec, automatic bool) (string, error)
	JudgeAll(ctx context.Context, spec jev.FeatureSpec, automatic bool, judgments []jev.Judgment, deadline time.Time) (jev.BatchResult, error)
}

// ServiceScorer scores candidates through the gated Jev service, so every
// request rechecks [jev], [jev.rerank], the credential, and consent for the
// search_rerank policy, and is charged to that feature's daily counters.
// It never runs as an automatic caller.
type ServiceScorer struct {
	judge Judge
	shape string
	spec  jev.FeatureSpec
}

// NewServiceScorer wires a scorer for one shape (ShapeBatched or
// ShapePerCandidate).
func NewServiceScorer(judge Judge, shape string) (*ServiceScorer, error) {
	if judge == nil {
		return nil, errors.New("rerank service scorer requires a Jev service")
	}
	if shape != ShapeBatched && shape != ShapePerCandidate {
		return nil, fmt.Errorf("unknown Jev request shape %q", shape)
	}
	return &ServiceScorer{judge: judge, shape: shape, spec: JevFeature()}, nil
}

// Admit runs the search_rerank gate for one caller as a non-automatic
// caller and returns the admitted policy fingerprint.
func (s *ServiceScorer) Admit(ctx context.Context) (string, error) {
	return s.judge.Admit(ctx, s.spec, false)
}

// Rerank scores every candidate against the query. On failure the returned
// usage still counts every attempted request.
func (s *ServiceScorer) Rerank(ctx context.Context, request Request) (Result, error) {
	calls, err := encodeJevCalls(request.Query, request.Candidates, s.shape)
	if err != nil {
		return emptyJevResult(), err
	}
	judgments := make([]jev.Judgment, len(calls))
	for i, call := range calls {
		ids := make([]string, len(call.Questions))
		for k, question := range call.Questions {
			ids[k] = question.ID
		}
		judgments[i] = jev.Judgment{State: call.State, QuestionIDs: ids}
	}
	deadline, _ := ctx.Deadline()
	batch, err := s.judge.JudgeAll(ctx, s.spec, false, judgments, deadline)
	return resultFromBatch(batch, err, s.shape, len(request.Candidates))
}
