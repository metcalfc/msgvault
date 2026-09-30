// Package persondedup proposes identity clusters that may be one person and
// asks Jev (feature person_duplicates) whether each pair is the same human
// being. Code proposes the pairs; Jev only returns a probability; a pair at
// or above CandidateThreshold becomes a reviewable identity match candidate
// that only a person's explicit accept ever applies.
package persondedup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/store"
)

// Judgment bounds and threshold.
const (
	// PairsPerRequest is how many pairs one request asks about.
	PairsPerRequest = 20
	// CandidateThreshold is the lowest same-person probability that writes
	// a reviewable candidate. Below it the judgment is only remembered.
	CandidateThreshold = 0.30
	maxNameRunes       = 120
	maxAddressRunes    = 254
)

// PairKey is the state key of the i-th (zero-based) pair in a request.
func PairKey(i int) string { return "pair_" + strconv.Itoa(i+1) }

// QuestionID asks whether the i-th pair is one person.
func QuestionID(i int) string { return "same_person_" + strconv.Itoa(i+1) }

// Feature is the exact policy the person_duplicates feature consents to.
// Every request carries the questions for the pairs it sends, worded
// exactly as here.
func Feature() jev.FeatureSpec {
	questions := make([]jev.Question, PairsPerRequest)
	for i := range questions {
		pair := "pairs." + PairKey(i)
		questions[i] = jev.Question{
			ID: QuestionID(i), Type: jev.QuestionNoul,
			Instructions: fmt.Sprintf("Are `%s.first` and `%s.second` the same human being? `%s.signals` "+
				"says what they share.", pair, pair, pair),
			Criteria: jev.NoulCriteria{
				True: "The names and addresses belong to one person, for example the same full name at a " +
					"personal and a work address, or the same distinctive address name at two domains.",
				False: "Different people who share a common name or address name, a person and a team, " +
					"company, or service address, or not enough to tell.",
			},
		}
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureDuplicatePeople,
		Title: "Duplicate people",
		Purpose: "Find correspondents who are probably one person writing from several addresses. Code " +
			"proposes pairs that share a display name or a distinctive address name; Jev says how likely " +
			"each pair is one person, and likely pairs appear in Reviews for you to accept or reject. " +
			"Nothing is linked or merged unless you accept it.",
		Questions: questions,
		StateFields: []string{
			"pairs.pair_N.first.names[] and .second.names[]: up to three display names each side uses, " +
				"cut to 120 characters; a name with an email address or phone number is not sent",
			"pairs.pair_N.first.addresses[] and .second.addresses[]: up to five email addresses each side " +
				"uses (the full addresses)",
			"pairs.pair_N.signals[]: same_display_name and/or same_local_part",
		},
	}
}

// Judge is the shared Jev door. *jev.Service implements it.
type Judge interface {
	JudgeQuestions(
		ctx context.Context, spec jev.FeatureSpec, automatic bool, state any, questionIDs []string, deadline time.Time,
	) (jev.Response, error)
}

// Store is the archive authority a run needs. *store.Store implements it.
type Store interface {
	PersonDuplicateProposalsContext(ctx context.Context, limit int) ([]store.PersonDuplicateProposal, error)
	RecordPersonDuplicateJudgmentsContext(
		ctx context.Context, judgments []store.PersonDuplicateJudgment,
	) (store.PersonDuplicateWriteResult, error)
}

// Options configure one run.
type Options struct {
	// Limit caps how many pairs one run judges; zero means no cap.
	Limit int
	// Judge is nil when Jev is off; the run then only counts proposals.
	Judge     Judge
	Automatic bool
	Logger    *slog.Logger
}

// Report summarizes a run. It never contains names or addresses.
type Report struct {
	Proposals  int `json:"proposals"`
	Requests   int `json:"requests"`
	Judged     int `json:"judged"`
	Candidates int `json:"candidates"`
	Existing   int `json:"existing"`
	// Dropped counts judged pairs that no longer qualified when written.
	Dropped int    `json:"dropped"`
	Skipped string `json:"skipped,omitempty"`
}

// IdentityState is one side of a pair as sent.
type IdentityState struct {
	Names     []string `json:"names"`
	Addresses []string `json:"addresses"`
}

// PairState is one pair as sent.
type PairState struct {
	First   IdentityState `json:"first"`
	Second  IdentityState `json:"second"`
	Signals []string      `json:"signals"`
}

type requestState struct {
	Pairs map[string]PairState `json:"pairs"`
}

// Run proposes pairs and judges them PairsPerRequest at a time. Any gate,
// budget, or provider failure stops Jev for the rest of the run and leaves
// the remaining pairs for a later run; only a store failure fails the run.
func Run(ctx context.Context, st Store, options Options) (Report, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	var report Report
	proposals, err := st.PersonDuplicateProposalsContext(ctx, options.Limit)
	if err != nil {
		return report, fmt.Errorf("propose duplicate people: %w", err)
	}
	report.Proposals = len(proposals)
	if options.Judge == nil {
		return report, nil
	}
	spec := Feature()
	pairs := make([]PairState, len(proposals))
	for i, proposal := range proposals {
		pairs[i] = pairState(proposal)
	}
	build := func(start, end int) any {
		state := requestState{Pairs: make(map[string]PairState, end-start)}
		for i, pair := range pairs[start:end] {
			state.Pairs[PairKey(i)] = pair
		}
		return state
	}
	// Up to PairsPerRequest pairs per request, fewer when identities carry
	// enough names and addresses to overrun the shared Jev token budget.
	var storeErr error
	jevErr := jev.JudgeSpans(len(proposals), PairsPerRequest, spec.Questions, build, func(span jev.Span) error {
		chunk := proposals[span.Start:span.End]
		ids := make([]string, len(chunk))
		for i := range chunk {
			ids[i] = QuestionID(i)
		}
		response, err := options.Judge.JudgeQuestions(ctx, spec, options.Automatic, build(span.Start, span.End), ids, time.Time{})
		if !errors.Is(err, jev.ErrRequestBounds) {
			// A request refused before sending is not counted.
			report.Requests++
		}
		if err == nil {
			err = checkAnswers(response, len(chunk))
		}
		if err != nil {
			return err
		}
		storeErr = recordJudgments(ctx, st, chunk, response, &report)
		return storeErr
	})
	if storeErr != nil {
		return report, storeErr
	}
	if jevErr != nil {
		report.Skipped = jev.Skipped(jevErr)
		options.Logger.Info("duplicate people: jev skipped",
			"feature", jev.FeatureDuplicatePeople, "category", report.Skipped)
	}
	return report, nil
}

// recordJudgments stores one request's answers.
func recordJudgments(
	ctx context.Context, st Store, chunk []store.PersonDuplicateProposal, response jev.Response, report *Report,
) error {
	judgments := make([]store.PersonDuplicateJudgment, len(chunk))
	for i, proposal := range chunk {
		probability := min(1, max(0, response.Answers[QuestionID(i)].Noul))
		judgments[i] = store.PersonDuplicateJudgment{
			Proposal: proposal, Probability: probability, Model: response.Model,
			Propose: probability >= CandidateThreshold,
		}
	}
	written, err := st.RecordPersonDuplicateJudgmentsContext(ctx, judgments)
	if err != nil {
		return fmt.Errorf("record duplicate people judgments: %w", err)
	}
	report.Judged += written.Recorded
	report.Candidates += written.Candidates
	report.Existing += written.Existing
	report.Dropped += written.Dropped
	return nil
}

func checkAnswers(response jev.Response, count int) error {
	if strings.TrimSpace(response.Model) == "" {
		return fmt.Errorf("%w: no model", jev.ErrInvalidResponse)
	}
	for i := range count {
		if _, ok := response.Answers[QuestionID(i)]; !ok {
			return fmt.Errorf("%w: answer %d missing", jev.ErrInvalidResponse, i)
		}
	}
	return nil
}

func pairState(proposal store.PersonDuplicateProposal) PairState {
	signals := make([]string, len(proposal.Signals))
	for i, signal := range proposal.Signals {
		signals[i] = string(signal)
	}
	return PairState{
		First: identityState(proposal.Left), Second: identityState(proposal.Right), Signals: signals,
	}
}

// identityState keeps names that carry no address or phone number, and the
// email addresses themselves.
func identityState(identity store.PersonDuplicateIdentity) IdentityState {
	state := IdentityState{Names: []string{}, Addresses: []string{}}
	for _, name := range identity.Names {
		name = strings.TrimSpace(name)
		if name == "" || meetingjudge.RedactText(name) != name {
			continue
		}
		state.Names = append(state.Names, truncateRunes(name, maxNameRunes))
	}
	for _, address := range identity.Addresses {
		state.Addresses = append(state.Addresses, truncateRunes(address, maxAddressRunes))
	}
	return state
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
