// Package persondedup proposes identity clusters that may be one person.
// Pairs that share a mailbox, a phone number, or a provider account are
// decided in code and become reviewable candidates without Jev. For pairs
// that only share a display name or an address name, Jev (feature
// person_duplicates) is asked whether the names belong to one human being;
// it sees the names and coarse address context, never an address. A pair at
// or above CandidateThreshold becomes a reviewable identity match candidate.
// Only a person's explicit accept ever applies a candidate.
package persondedup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/store"
	"golang.org/x/net/publicsuffix"
)

// Judgment bounds and threshold.
const (
	// PairsPerRequest is how many pairs one request asks about.
	PairsPerRequest = 20
	// CandidateThreshold is the lowest same-person probability that writes
	// a reviewable candidate. Below it the judgment is only remembered.
	CandidateThreshold = 0.30
	maxNameRunes       = 120
	// ruleBatch bounds how many pairs decided in code one write
	// transaction records.
	ruleBatch = 250
)

// Address kinds sent instead of addresses.
const (
	// AddressPersonal is an address at a consumer mail provider.
	AddressPersonal = "personal"
	// AddressOrganization is an address at any other domain.
	AddressOrganization = "organization"
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
			Instructions: fmt.Sprintf("Do the names in `%s.first` and `%s.second` belong to the same human being? "+
				"`%s.signals` says what the two sides share, `address_kinds` says whether each side writes "+
				"from a personal mail provider or an organization's domain, and "+
				"`%s.same_organization_domain` says whether both sides use one organization's domain.",
				pair, pair, pair, pair),
			Criteria: jev.NoulCriteria{
				True: "The names belong to one person, for example the same full name at a personal and a work " +
					"address, or a name and its initials or nickname on two addresses with the same " +
					"distinctive address name.",
				False: "Different people who share a common name, a person and a team, company, or service, " +
					"or not enough to tell.",
			},
		}
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureDuplicatePeople,
		Title: "Duplicate people",
		Purpose: "Find correspondents who are probably one person writing from several addresses. Pairs " +
			"that share a mailbox, phone number, or provider account are decided in code and never sent. " +
			"For pairs that only share a display name or a distinctive address name, Jev says how likely " +
			"the names are one person, and likely pairs appear in Reviews for you to accept or reject. " +
			"Nothing is linked or merged unless you accept it.",
		Questions: questions,
		StateFields: []string{
			"pairs.pair_N.first.names[] and .second.names[]: up to three display names each side uses, " +
				"cut to 120 characters; a name with an email address or phone number is not sent",
			"pairs.pair_N.first.address_kinds[] and .second.address_kinds[]: personal and/or organization, " +
				"for up to five addresses each side uses; no address, local part, or domain is sent",
			"pairs.pair_N.same_organization_domain: true when both sides use the same organization domain",
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
	RecordPersonDuplicateRulesContext(
		ctx context.Context, proposals []store.PersonDuplicateProposal,
	) (store.PersonDuplicateWriteResult, error)
	RecordPersonDuplicateJudgmentsContext(
		ctx context.Context, judgments []store.PersonDuplicateJudgment,
	) (store.PersonDuplicateWriteResult, error)
}

// Options configure one run.
type Options struct {
	// Limit caps how many name pairs one run sends to Jev; zero means no
	// cap. Pairs decided in code are always all written.
	Limit int
	// Judge is nil when Jev is off; the run then writes only the pairs
	// decided in code.
	Judge     Judge
	Automatic bool
	Logger    *slog.Logger
}

// Report summarizes a run. It never contains names or addresses.
type Report struct {
	Proposals int `json:"proposals"`
	// Matched counts pairs that share a mailbox, phone number, or provider
	// account. They are decided in code, never sent to Jev.
	Matched int `json:"matched"`
	// Unnamed counts name pairs never sent because neither side has a
	// display name to judge. They are remembered and proposed again only
	// when a side changes.
	Unnamed int `json:"unnamed"`
	// OneSided counts name pairs not sent because only one side has a
	// display name. They are not remembered, so a later run takes them up
	// again when the other side gains a name.
	OneSided int `json:"one_sided"`
	Requests int `json:"requests"`
	Judged   int `json:"judged"`
	// Candidates counts new review candidates, from code and from Jev.
	Candidates int `json:"candidates"`
	Existing   int `json:"existing"`
	// Dropped counts pairs that no longer qualified when written.
	Dropped int    `json:"dropped"`
	Skipped string `json:"skipped,omitempty"`
}

// IdentityState is one side of a pair as sent: names and the kind of each
// address, never the addresses.
type IdentityState struct {
	Names        []string `json:"names"`
	AddressKinds []string `json:"address_kinds"`
}

// PairState is one pair as sent.
type PairState struct {
	First                  IdentityState `json:"first"`
	Second                 IdentityState `json:"second"`
	SameOrganizationDomain bool          `json:"same_organization_domain"`
	Signals                []string      `json:"signals"`
}

type requestState struct {
	Pairs map[string]PairState `json:"pairs"`
}

// Run proposes pairs, writes the ones decided in code, and judges the name
// pairs PairsPerRequest at a time. Any gate, budget, or provider failure
// stops Jev for the rest of the run and leaves the remaining name pairs for
// a later run; only a store failure fails the run.
func Run(ctx context.Context, st Store, options Options) (Report, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	var report Report
	// Every proposal is read: the limit bounds only what is sent to Jev, so
	// name pairs that cannot be judged never starve the exact matches.
	proposals, err := st.PersonDuplicateProposalsContext(ctx, 0)
	if err != nil {
		return report, fmt.Errorf("propose duplicate people: %w", err)
	}
	report.Proposals = len(proposals)
	var ruled, named []store.PersonDuplicateProposal
	var pairs []PairState
	for _, proposal := range proposals {
		if _, exact := proposal.ExactSignal(); exact {
			report.Matched++
			ruled = append(ruled, proposal)
			continue
		}
		pair := pairState(proposal)
		if len(pair.First.Names) == 0 && len(pair.Second.Names) == 0 {
			// Without any name there is nothing to judge. The pair is
			// remembered; a name on either side changes its fingerprint,
			// so it is proposed again then.
			report.Unnamed++
			ruled = append(ruled, proposal)
			continue
		}
		if len(pair.First.Names) == 0 || len(pair.Second.Names) == 0 {
			// A name on one side only is not sent and not remembered, so
			// the pair is taken up again once the other side has a name.
			report.OneSided++
			continue
		}
		if options.Limit > 0 && len(named) >= options.Limit {
			continue
		}
		named = append(named, proposal)
		pairs = append(pairs, pair)
	}
	for start := 0; start < len(ruled); start += ruleBatch {
		written, err := st.RecordPersonDuplicateRulesContext(ctx, ruled[start:min(start+ruleBatch, len(ruled))])
		if err != nil {
			return report, fmt.Errorf("record duplicate people decided in code: %w", err)
		}
		addWritten(&report, written)
	}
	if options.Judge == nil || len(named) == 0 {
		return report, nil
	}
	spec := Feature()
	build := func(start, end int) any {
		state := requestState{Pairs: make(map[string]PairState, end-start)}
		for i, pair := range pairs[start:end] {
			state.Pairs[PairKey(i)] = pair
		}
		return state
	}
	// Up to PairsPerRequest pairs per request, fewer when identities carry
	// enough names to overrun the shared Jev token budget.
	var storeErr error
	jevErr := jev.JudgeSpans(len(named), PairsPerRequest, spec.Questions, build, func(span jev.Span) error {
		chunk := named[span.Start:span.End]
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

func addWritten(report *Report, written store.PersonDuplicateWriteResult) {
	report.Candidates += written.Candidates
	report.Existing += written.Existing
	report.Dropped += written.Dropped
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
	addWritten(report, written)
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
		First: identityState(proposal.Left), Second: identityState(proposal.Right),
		SameOrganizationDomain: sameOrganizationDomain(proposal.Left.Addresses, proposal.Right.Addresses),
		Signals:                signals,
	}
}

// identityState keeps names that carry no address or phone number and are
// not an address token, and only the kind of each address. An address token
// is a name without whitespace that equals one of the side's local parts
// ignoring case and separators ("john.smith", "JSmith"); a spaced name such
// as "John Smith" is a name and is sent.
func identityState(identity store.PersonDuplicateIdentity) IdentityState {
	state := IdentityState{Names: []string{}, AddressKinds: []string{}}
	localParts := map[string]struct{}{}
	for _, address := range identity.Addresses {
		if local, _ := correspondentkind.SplitEmail(address); local != "" {
			localParts[nameKey(local)] = struct{}{}
		}
	}
	for _, name := range identity.Names {
		name = strings.TrimSpace(name)
		if name == "" || meetingjudge.RedactText(name) != name {
			continue
		}
		if _, isLocalPart := localParts[nameKey(name)]; isLocalPart && !strings.ContainsFunc(name, unicode.IsSpace) {
			// "john.smith" as a name is the address, not a name.
			continue
		}
		state.Names = append(state.Names, truncateRunes(name, maxNameRunes))
	}
	for _, address := range identity.Addresses {
		_, domain := correspondentkind.SplitEmail(address)
		if domain == "" {
			continue
		}
		kind := AddressOrganization
		if correspondentkind.IsFreemailDomain(domain) {
			kind = AddressPersonal
		}
		if !slices.Contains(state.AddressKinds, kind) {
			state.AddressKinds = append(state.AddressKinds, kind)
		}
	}
	slices.Sort(state.AddressKinds)
	return state
}

// sameOrganizationDomain reports whether both sides have an address at the
// same registrable domain that is not a consumer mail provider.
func sameOrganizationDomain(left, right []string) bool {
	domains := func(addresses []string) map[string]struct{} {
		result := map[string]struct{}{}
		for _, address := range addresses {
			_, domain := correspondentkind.SplitEmail(address)
			if domain == "" || correspondentkind.IsFreemailDomain(domain) {
				continue
			}
			if registrable, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil {
				domain = registrable
			}
			result[domain] = struct{}{}
		}
		return result
	}
	rightDomains := domains(right)
	for domain := range domains(left) {
		if _, ok := rightDomains[domain]; ok {
			return true
		}
	}
	return false
}

// nameKey compares a name with a local part: lowercase letters and digits
// only, so case and separators such as dots, dashes, underscores, and
// spaces do not matter.
func nameKey(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
