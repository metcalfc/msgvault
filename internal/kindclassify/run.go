package kindclassify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
)

// Store is the archive authority a run needs. *store.Store implements it.
type Store interface {
	CorrespondentKindCandidatesContext(
		ctx context.Context, query store.CorrespondentKindCandidateQuery,
	) ([]store.CorrespondentKindCandidate, error)
	CorrespondentKindEvidenceContext(
		ctx context.Context, members []int64, options store.CorrespondentKindEvidenceOptions,
	) (store.CorrespondentKindEvidence, error)
	WriteDerivedCorrespondentKindsContext(ctx context.Context, kinds []store.DerivedCorrespondentKind) (int, error)
	CorrespondentKindCandidatesCurrentContext(
		ctx context.Context, candidates []store.CorrespondentKindCandidate,
	) (map[int64]bool, error)
	RecordCorrespondentKindEvaluationsContext(ctx context.Context, candidates []store.CorrespondentKindCandidate) error
}

// Judge asks a subset of a feature's consented questions. *jev.Service
// implements it; every gate is rechecked on each call.
type Judge interface {
	JudgeQuestions(
		ctx context.Context, spec jev.FeatureSpec, automatic bool, state any, questionIDs []string, deadline time.Time,
	) (jev.Response, error)
}

// DefaultMinMessages is the activity floor for a cluster to be classified.
const DefaultMinMessages = 5

// DefaultHeaderSample is how many raw messages per cluster have their
// header block read.
const DefaultHeaderSample = 5

// Options configure one run.
type Options struct {
	// MinMessages is the activity floor; zero takes DefaultMinMessages.
	MinMessages int64
	// Limit caps how many clusters one run visits; zero means no cap.
	Limit int
	// Judge is nil when Jev is off; the run then applies rules only.
	Judge Judge
	// Automatic marks unattended callers such as the cache build, which the
	// feature must allow with automatic = true.
	Automatic bool
	Logger    *slog.Logger
}

// Report summarizes a run. It never contains state content.
type Report struct {
	Candidates int                              `json:"candidates"`
	Rule       map[correspondentkind.Kind]int   `json:"rule"`
	RuleByWhy  map[correspondentkind.Reason]int `json:"rule_reasons"`
	Jev        JevReport                        `json:"jev"`
	// Undecided counts clusters no rule decided and Jev did not judge.
	Undecided int `json:"undecided"`
}

// JevReport summarizes the Jev part of a run.
type JevReport struct {
	Requests int                            `json:"requests"`
	Judged   int                            `json:"judged"`
	Kinds    map[correspondentkind.Kind]int `json:"kinds"`
	// Skipped is the safe-failure category that stopped Jev for this run
	// (for example consent_required or breaker_open), empty when every
	// batch was asked.
	Skipped string `json:"skipped,omitempty"`
}

type pending struct {
	candidate store.CorrespondentKindCandidate
	evidence  store.CorrespondentKindEvidence
}

// Run classifies unclassified clusters above the activity floor. Rules run
// first and need nothing outside the machine. When a Judge is present the
// remainder is asked of Jev; any gate, budget, or provider failure stops
// Jev for the rest of the run and leaves those clusters unclassified, as if
// Jev were off. Only a store failure fails the run.
func Run(ctx context.Context, st Store, options Options) (Report, error) {
	if options.MinMessages <= 0 {
		options.MinMessages = DefaultMinMessages
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	report := Report{
		Rule: map[correspondentkind.Kind]int{}, RuleByWhy: map[correspondentkind.Reason]int{},
		Jev: JevReport{Kinds: map[correspondentkind.Kind]int{}},
	}
	candidates, err := st.CorrespondentKindCandidatesContext(ctx, store.CorrespondentKindCandidateQuery{
		MinMessages: options.MinMessages, Limit: options.Limit,
		SkipSources: []correspondentkind.Source{correspondentkind.SourceRule},
		// A cluster Jev already judged is never sent again, but a rule added
		// since may decide it: rules outrank Jev.
		RulesOnlySources: []correspondentkind.Source{correspondentkind.SourceJev},
		RevisitUnchanged: options.Judge != nil,
	})
	if err != nil {
		return report, fmt.Errorf("list correspondent kind candidates: %w", err)
	}
	for _, candidate := range candidates {
		if !candidate.RulesOnly {
			report.Candidates++
		}
	}
	var decided []store.DerivedCorrespondentKind
	var remaining []pending
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if candidate.RulesOnly {
			// Only the identity rules, which read addresses alone, may
			// overrule an earlier judgment; no evidence is gathered.
			if decision, ok := correspondentkind.Classify(signalsFor(candidate, store.CorrespondentKindEvidence{})); ok {
				decided = append(decided, store.DerivedCorrespondentKind{
					ParticipantID: candidate.CanonicalID, Source: correspondentkind.SourceRule,
					Kind: decision.Kind, Actor: "rule:" + string(decision.Reason), ExpectedMembers: candidate.MemberIDs,
				})
				report.Rule[decision.Kind]++
				report.RuleByWhy[decision.Reason]++
			}
			continue
		}
		evidence, err := st.CorrespondentKindEvidenceContext(ctx, candidate.MemberIDs, store.CorrespondentKindEvidenceOptions{
			HeaderSample: DefaultHeaderSample, SubjectsFromThem: maxSubjectsFromThem, SubjectsFromOwner: maxSubjectsFromOwner,
		})
		if err != nil {
			return report, fmt.Errorf("gather correspondent kind evidence: %w", err)
		}
		decision, ok := correspondentkind.Classify(signalsFor(candidate, evidence))
		if !ok {
			remaining = append(remaining, pending{candidate: candidate, evidence: evidence})
			continue
		}
		decided = append(decided, store.DerivedCorrespondentKind{
			ParticipantID: candidate.CanonicalID, Source: correspondentkind.SourceRule,
			Kind: decision.Kind, Actor: "rule:" + string(decision.Reason), ExpectedMembers: candidate.MemberIDs,
		})
		report.Rule[decision.Kind]++
		report.RuleByWhy[decision.Reason]++
	}
	if _, err := st.WriteDerivedCorrespondentKindsContext(ctx, decided); err != nil {
		return report, fmt.Errorf("write rule correspondent kinds: %w", err)
	}
	judged := map[int64]struct{}{}
	if options.Judge != nil {
		for start := 0; start < len(remaining); start += BatchSize {
			batch := remaining[start:min(start+BatchSize, len(remaining))]
			if err := judgeBatch(ctx, st, options, batch, &report.Jev, judged); err != nil {
				if isStoreError(err) {
					return report, err
				}
				report.Jev.Skipped = jev.Skipped(err)
				options.Logger.Info("correspondent kind: jev skipped",
					"feature", jev.FeatureCorrespondentKind, "category", report.Jev.Skipped)
				break
			}
		}
	}
	undecided := make([]store.CorrespondentKindCandidate, 0, len(remaining))
	for _, item := range remaining {
		if _, ok := judged[item.candidate.CanonicalID]; !ok {
			undecided = append(undecided, item.candidate)
		}
	}
	report.Undecided = len(undecided)
	if err := st.RecordCorrespondentKindEvaluationsContext(ctx, undecided); err != nil {
		return report, fmt.Errorf("record correspondent kind evaluations: %w", err)
	}
	return report, nil
}

// storeError marks a failure writing results, which fails the run.
type storeError struct{ err error }

func (e storeError) Error() string { return e.err.Error() }
func (e storeError) Unwrap() error { return e.err }

func isStoreError(err error) bool {
	var target storeError
	return errors.As(err, &target)
}

// judgeBatch asks about one batch and stores the answers. A batch the
// client or the provider rejects as too large (over the shared Jev token
// budget) is halved until it fits.
func judgeBatch(
	ctx context.Context, st Store, options Options, batch []pending, report *JevReport, judged map[int64]struct{},
) error {
	batch, err := stillCurrent(ctx, st, batch)
	if err != nil || len(batch) == 0 {
		return err
	}
	state, ids := stateFor(batch)
	response, err := options.Judge.JudgeQuestions(ctx, JevFeature(), options.Automatic, state, ids, time.Time{})
	if jev.Oversize(err) && len(batch) > 1 {
		if !errors.Is(err, jev.ErrRequestBounds) {
			// The provider refused a request that was sent (max_tokens_exceeded).
			report.Requests++
		}
		half := len(batch) / 2
		if err := judgeBatch(ctx, st, options, batch[:half], report, judged); err != nil {
			return err
		}
		return judgeBatch(ctx, st, options, batch[half:], report, judged)
	}
	report.Requests++
	if err != nil {
		return err
	}
	results := make([]store.DerivedCorrespondentKind, 0, len(batch))
	for i, item := range batch {
		answer, ok := response.Answers[QuestionID(i)]
		if !ok {
			return fmt.Errorf("%w: answer %d missing", jev.ErrInvalidResponse, i)
		}
		kind := KindForAnswer(answer)
		confidence := answer.Confidence
		results = append(results, store.DerivedCorrespondentKind{
			ParticipantID: item.candidate.CanonicalID, Source: correspondentkind.SourceJev, Kind: kind,
			Confidence: &confidence, Probabilities: answer.Probabilities, Actor: "jev:" + response.Model,
			ExpectedMembers: item.candidate.MemberIDs,
		})
		report.Kinds[kind]++
	}
	if _, err := st.WriteDerivedCorrespondentKindsContext(ctx, results); err != nil {
		return storeError{fmt.Errorf("write jev correspondent kinds: %w", err)}
	}
	for _, item := range batch {
		judged[item.candidate.CanonicalID] = struct{}{}
	}
	report.Judged += len(results)
	return nil
}

// stillCurrent drops identities that became the owner's, gained a user
// decision, or changed membership since their evidence was read. It runs
// right before a batch leaves the machine.
func stillCurrent(ctx context.Context, st Store, batch []pending) ([]pending, error) {
	candidates := make([]store.CorrespondentKindCandidate, len(batch))
	for i, item := range batch {
		candidates[i] = item.candidate
	}
	current, err := st.CorrespondentKindCandidatesCurrentContext(ctx, candidates)
	if err != nil {
		return nil, storeError{fmt.Errorf("recheck correspondent kind candidates: %w", err)}
	}
	kept := make([]pending, 0, len(batch))
	for _, item := range batch {
		if current[item.candidate.CanonicalID] {
			kept = append(kept, item)
		}
	}
	return kept, nil
}

func signalsFor(candidate store.CorrespondentKindCandidate, evidence store.CorrespondentKindEvidence) correspondentkind.Signals {
	return correspondentkind.Signals{
		Emails: candidate.Emails, Phones: candidate.Phones, ProviderBot: candidate.ProviderBot,
		Sent: candidate.Sent, Received: candidate.Received,
		ListIDMessages: evidence.ListIDMessages, ListIDs: evidence.ListIDs,
		Categories: evidence.Categories, Headers: evidence.Headers,
	}
}

// Identity is the state sent about one cluster. Its fields are exactly
// JevFeature's StateFields.
type Identity struct {
	Label             string             `json:"label"`
	Addresses         []Address          `json:"addresses"`
	Counts            Counts             `json:"counts"`
	ListIDShare       float64            `json:"list_id_share"`
	CategoryShares    map[string]float64 `json:"category_shares"`
	HeaderCounts      HeaderCounts       `json:"header_counts"`
	SubjectsFromThem  []string           `json:"subjects_from_them"`
	SubjectsFromOwner []string           `json:"subjects_from_owner"`
}

// Address is an email address split into its local part and domain.
type Address struct {
	LocalPart string `json:"local_part"`
	Domain    string `json:"domain"`
}

// Counts are the cluster's message counts from the owner's point of view.
type Counts struct {
	Sent     int64 `json:"sent"`
	Received int64 `json:"received"`
	Meetings int64 `json:"meetings"`
}

// HeaderCounts are header presence counts over sampled raw messages.
type HeaderCounts struct {
	Sampled         int `json:"sampled"`
	ListUnsubscribe int `json:"list_unsubscribe"`
	AutoSubmitted   int `json:"auto_submitted"`
	PrecedenceBulk  int `json:"precedence_bulk"`
	ListID          int `json:"list_id"`
}

// State is one request's state.
type State struct {
	Identities []Identity `json:"identities"`
}

func stateFor(batch []pending) (State, []string) {
	state := State{Identities: make([]Identity, 0, len(batch))}
	ids := make([]string, 0, len(batch))
	for i, item := range batch {
		state.Identities = append(state.Identities, identityFor(item))
		ids = append(ids, QuestionID(i))
	}
	return state, ids
}

func identityFor(item pending) Identity {
	candidate, evidence := item.candidate, item.evidence
	identity := Identity{
		Label:             truncateRunes(strings.Join(strings.Fields(candidate.DisplayName), " "), maxLabelRunes),
		Addresses:         []Address{},
		Counts:            Counts{Sent: candidate.Sent, Received: candidate.Received, Meetings: candidate.Meetings},
		CategoryShares:    map[string]float64{},
		HeaderCounts:      HeaderCounts(evidence.Headers),
		SubjectsFromThem:  capped(evidence.SubjectsFromThem, maxSubjectsFromThem),
		SubjectsFromOwner: capped(evidence.SubjectsFromOwner, maxSubjectsFromOwner),
	}
	for _, email := range candidate.Emails {
		if len(identity.Addresses) == maxAddresses {
			break
		}
		local, domain := correspondentkind.SplitEmail(email)
		if local == "" || domain == "" {
			continue
		}
		identity.Addresses = append(identity.Addresses, Address{LocalPart: local, Domain: domain})
	}
	if candidate.Sent > 0 {
		identity.ListIDShare = share(evidence.ListIDMessages, candidate.Sent)
		for label, count := range evidence.Categories {
			name := strings.ToLower(strings.TrimPrefix(label, "CATEGORY_"))
			identity.CategoryShares[name] = share(count, candidate.Sent)
		}
	}
	return identity
}

func share(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	value := float64(part) / float64(whole)
	if value > 1 {
		value = 1
	}
	// Two decimals are enough to judge from and keep requests small.
	return float64(int64(value*100+0.5)) / 100
}

func capped(values []string, limit int) []string {
	if len(values) > limit {
		values = values[:limit]
	}
	if values == nil {
		return []string{}
	}
	return values
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
