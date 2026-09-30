package sweepjudge

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/peoplesweep"
	"go.kenn.io/msgvault/internal/personfacts"
)

// Claim grounding bounds.
const (
	// ClaimsPerRequest is how many claims one grounding request asks about.
	ClaimsPerRequest = 8
	// MaxGroundingEvidence is how many of a claim's cited excerpts are sent,
	// newest first.
	MaxGroundingEvidence    = 3
	maxGroundingExcerptRune = 1000
	maxGroundingValueRunes  = 500
	maxGroundingFactRunes   = 300
)

// ClaimKey is the state key of the i-th (zero-based) claim in a request.
func ClaimKey(i int) string { return "claim_" + strconv.Itoa(i+1) }

// StatedQuestionID asks whether the i-th claim's evidence states it.
func StatedQuestionID(i int) string { return "stated_" + strconv.Itoa(i+1) }

// CurrentQuestionID asks whether the i-th claim is still current.
func CurrentQuestionID(i int) string { return "current_" + strconv.Itoa(i+1) }

// ClaimGroundingFeature is the exact policy the sweep_claim_grounding
// feature consents to. Every request carries the questions for the claims
// it sends, worded exactly as here.
func ClaimGroundingFeature() jev.FeatureSpec {
	questions := make([]jev.Question, 0, 2*ClaimsPerRequest)
	for i := range ClaimsPerRequest {
		claim := "claims." + ClaimKey(i)
		questions = append(questions, jev.Question{
			ID: StatedQuestionID(i), Type: jev.QuestionNoul,
			Instructions: fmt.Sprintf("Do the excerpts in `%s.evidence`, written by the person, explicitly "+
				"say what `%s` asserts: that they `relation` (support, contradict, or supersede) `value` "+
				"for `fact`?", claim, claim),
			Criteria: jev.NoulCriteria{
				True: "An excerpt says it directly about the person who wrote it; no guess or inference " +
					"is needed.",
				False: "The assertion is implied, guessed, about someone else, a joke or hypothetical, or " +
					"not in the excerpts.",
			},
		}, jev.Question{
			ID: CurrentQuestionID(i), Type: jev.QuestionNoul,
			Instructions: fmt.Sprintf("Is what `%s` asserts still true as of the newest excerpt date in "+
				"`%s.evidence`?", claim, claim),
			Criteria: jev.NoulCriteria{
				True:  "Nothing in the excerpts says it ended, changed, or was only planned.",
				False: "The excerpts say it ended, changed, was only planned, or describe a past state.",
			},
		})
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureSweepClaimGrounding,
		Title: "People sweep claim grounding",
		Purpose: "After the people sweep's chat model proposes facts about a person, ask Jev whether the " +
			"messages each fact cites actually state it and whether it is still current, and use that as " +
			"the fact's confidence instead of the chat model's own estimate. Nothing is added or removed; " +
			"the fact resolver's rules and your pins still decide what changes.",
		Questions: questions,
		StateFields: []string{
			"claims.claim_N.fact: the catalog description of the fact, such as \"Current and historical " +
				"employment\"",
			"claims.claim_N.relation: support, contradict, or supersede",
			"claims.claim_N.value: the proposed value, such as a job title and organization name",
			"claims.claim_N.evidence[].date and .text: up to three cited excerpts, newest first, each up " +
				"to 1,000 characters of a message the person sent on a source that authenticates its " +
				"sender, with email addresses and phone numbers replaced by placeholders",
		},
		BodyNotice: "each judged claim sends up to three excerpts of messages the person wrote (up to " +
			"1,000 characters each) with their dates, and the proposed value. Your own identities, the " +
			"person's name, and their addresses are never sent as fields.",
	}
}

// Grounder implements peoplesweep.ClaimGrounder through the gated Jev
// service.
type Grounder struct {
	judge     Judge
	automatic bool
	logger    *slog.Logger
}

var _ peoplesweep.ClaimGrounder = (*Grounder)(nil)

// NewGrounder wires claim grounding. automatic marks the daemon's scheduled
// sweeps.
func NewGrounder(judge Judge, automatic bool, logger *slog.Logger) *Grounder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Grounder{judge: judge, automatic: automatic, logger: logger}
}

// ClaimState is one claim as sent.
type ClaimState struct {
	Fact     string          `json:"fact"`
	Relation string          `json:"relation"`
	Value    string          `json:"value"`
	Evidence []EvidenceState `json:"evidence"`
}

// EvidenceState is one cited excerpt as sent.
type EvidenceState struct {
	Date string `json:"date"`
	Text string `json:"text"`
}

type groundingState struct {
	Claims map[string]ClaimState `json:"claims"`
}

// GroundClaims replaces each judgeable claim's reported score with
// round(1000 × P(stated) × P(current)). A sensitive target, a value that
// carries an email address or phone number, or a claim without evidence
// keeps its reported score. The first failed request stops grounding;
// claims already judged keep their new scores.
func (g *Grounder) GroundClaims(
	ctx context.Context, _ int64, claims []personfacts.ProposedClaim,
) []personfacts.ProposedClaim {
	out := slices.Clone(claims)
	if g == nil || g.judge == nil {
		return out
	}
	type pending struct {
		index int
		state ClaimState
	}
	judgeable := make([]pending, 0, len(claims))
	for i, claim := range claims {
		state, ok := claimState(claim)
		if ok {
			judgeable = append(judgeable, pending{index: i, state: state})
		}
	}
	spec := ClaimGroundingFeature()
	grounded := 0
	for start := 0; start < len(judgeable); start += ClaimsPerRequest {
		chunk := judgeable[start:min(start+ClaimsPerRequest, len(judgeable))]
		state := groundingState{Claims: make(map[string]ClaimState, len(chunk))}
		ids := make([]string, 0, 2*len(chunk))
		for i, entry := range chunk {
			state.Claims[ClaimKey(i)] = entry.state
			ids = append(ids, StatedQuestionID(i), CurrentQuestionID(i))
		}
		response, err := g.judge.JudgeQuestions(ctx, spec, g.automatic, state, ids, time.Time{})
		if err != nil {
			g.logger.Info("people sweep claim grounding: jev skipped",
				"feature", jev.FeatureSweepClaimGrounding, "category", jev.Skipped(err), "grounded", grounded)
			return out
		}
		scores := make([]int, len(chunk))
		for i := range chunk {
			stated, statedOK := response.Answers[StatedQuestionID(i)]
			current, currentOK := response.Answers[CurrentQuestionID(i)]
			if !statedOK || !currentOK {
				g.logger.Info("people sweep claim grounding: jev skipped",
					"feature", jev.FeatureSweepClaimGrounding, "category", "invalid_response", "grounded", grounded)
				return out
			}
			scores[i] = GroundedScore(stated.Noul, current.Noul)
		}
		for i, entry := range chunk {
			out[entry.index].Confidence = personfacts.ConfidenceInputs{ReportedScore: scores[i]}
			grounded++
		}
	}
	if grounded > 0 {
		g.logger.Debug("people sweep claims grounded", "feature", jev.FeatureSweepClaimGrounding,
			"claims", len(claims), "grounded", grounded)
	}
	return out
}

// GroundedScore is the reported score a grounded claim carries:
// round(1000 × stated × current), in basis points of 1000.
func GroundedScore(stated, current float64) int {
	stated, current = min(1, max(0, stated)), min(1, max(0, current))
	return int(math.Round(1000 * stated * current))
}

// claimState renders one claim as sent, or reports that it must keep its
// reported score.
func claimState(claim personfacts.ProposedClaim) (ClaimState, bool) {
	if claim.Target.Sensitive || len(claim.Evidence) == 0 {
		return ClaimState{}, false
	}
	value, ok := claimValueText(claim.SubmittedValue)
	if !ok || meetingjudge.RedactText(value) != strings.TrimSpace(value) {
		return ClaimState{}, false
	}
	fact := strings.Join(strings.Fields(claim.Target.Description), " ")
	if fact == "" {
		fact = strings.ReplaceAll(claim.Target.Slug, "_", " ")
	}
	evidence := slices.Clone(claim.Evidence)
	slices.SortStableFunc(evidence, func(a, b personfacts.EvidenceInput) int {
		return b.EventTime.Compare(a.EventTime)
	})
	evidence = evidence[:min(len(evidence), MaxGroundingEvidence)]
	state := ClaimState{
		Fact: truncateRunes(fact, maxGroundingFactRunes), Relation: string(claim.Relation),
		Value: truncateRunes(value, maxGroundingValueRunes), Evidence: make([]EvidenceState, 0, len(evidence)),
	}
	for _, item := range evidence {
		date := ""
		if !item.EventTime.IsZero() {
			date = item.EventTime.UTC().Format(time.DateOnly)
		}
		state.Evidence = append(state.Evidence, EvidenceState{
			Date: date, Text: truncateRunes(meetingjudge.RedactText(item.Excerpt), maxGroundingExcerptRune),
		})
	}
	return state, true
}

// claimValueText renders a submitted value: a JSON string as its text,
// anything else as compact JSON. An empty value is not judgeable.
func claimValueText(value jsontext.Value) (string, bool) {
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		text = strings.TrimSpace(text)
		return text, text != ""
	}
	compact := jsontext.Value(slices.Clone(value))
	if err := compact.Compact(); err != nil {
		return "", false
	}
	rendered := strings.TrimSpace(string(compact))
	return rendered, rendered != "" && rendered != "null"
}
