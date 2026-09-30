package orgresolution

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
)

// maxReferencesPerPreparation bounds how many distinct organizations one
// generation may ask about, so a large sweep cannot spend a day's budget at
// once. The rest resolve exactly as before Jev.
const maxReferencesPerPreparation = 8

// Store is the archive authority the preparer reads shortlists from and
// writes aliases and reviews to. *store.Store implements it.
type Store interface {
	OrganizationShortlistContext(ctx context.Context, ref personfacts.OrganizationReference) (*store.OrganizationShortlist, error)
	GetOrganizationContext(ctx context.Context, id int64) (*store.Organization, error)
	PersonOrganizationTitlesContext(ctx context.Context, personID, organizationID int64) ([]string, error)
	RecordOrganizationResolutionAliasContext(ctx context.Context, input store.OrganizationAliasInput) (store.OrganizationAliasResult, error)
	RecordOrganizationMatchReviewContext(ctx context.Context, input store.OrganizationMatchReviewInput) (bool, error)
	RecordEmploymentTitleAliasContext(ctx context.Context, input store.EmploymentTitleAliasInput) (bool, error)
	EmploymentTitleCanonicalContext(ctx context.Context, organizationID int64, titles []string) (map[string]string, error)
}

// Judge asks a subset of the feature's consented questions. *jev.Service
// implements it and rechecks every gate before each request.
type Judge interface {
	JudgeQuestions(ctx context.Context, spec jev.FeatureSpec, automatic bool, state any,
		questionIDs []string, deadline time.Time) (jev.Response, error)
}

// Preparer resolves the organizations of employment claims before they are
// committed. automatic marks unattended callers such as scheduled runs.
type Preparer struct {
	judge     Judge
	store     Store
	automatic bool
	logger    *slog.Logger
}

// NewPreparer wires a preparer; a nil judge or store means no preparer, and
// callers then keep the exact lookup alone.
func NewPreparer(judge Judge, st Store, automatic bool, logger *slog.Logger) *Preparer {
	if judge == nil || st == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Preparer{judge: judge, store: st, automatic: automatic, logger: logger}
}

// Outcome is what happened to one organization reference.
type Outcome string

// Reference outcomes.
const (
	OutcomeExact     Outcome = "exact"
	OutcomeAmbiguous Outcome = "ambiguous"
	OutcomeNoMatch   Outcome = "no_candidates"
	OutcomeAlias     Outcome = "alias"
	OutcomeReview    Outcome = "review"
	OutcomeNew       Outcome = "new_organization"
	OutcomeSkipped   Outcome = "skipped"
)

// ReferenceResult reports one reference's outcome with numbers only.
type ReferenceResult struct {
	Outcome        Outcome
	OrganizationID int64
	Probability    float64
	TitleAliases   int
	Asked          bool
	// Skipped is the jev.Skipped category when a judgment could not run.
	Skipped string
}

// PrepareEmploymentOrganizations implements personfacts.OrganizationPreparer.
func (p *Preparer) PrepareEmploymentOrganizations(
	ctx context.Context, personID int64, claims []personfacts.ProposedClaim,
) {
	if p == nil {
		return
	}
	_, _ = p.Prepare(ctx, personID, claims)
}

// Prepare resolves every distinct organization the claims name without an
// ID. A failure to ask is not an error: the reference keeps today's exact
// lookup. Only a store failure is returned, after the references before it
// were handled.
func (p *Preparer) Prepare(
	ctx context.Context, personID int64, claims []personfacts.ProposedClaim,
) ([]ReferenceResult, error) {
	if p == nil {
		return nil, nil
	}
	references := employmentReferences(claims)
	if len(references) > maxReferencesPerPreparation {
		references = references[:maxReferencesPerPreparation]
	}
	results := make([]ReferenceResult, 0, len(references))
	for _, reference := range references {
		result, err := p.resolve(ctx, personID, reference)
		if err != nil {
			p.logger.Warn("organization resolution failed",
				"feature", jev.FeatureOrganizationResolution, "error", err.Error())
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// employmentReference is one distinct organization named by the claims,
// with the job titles claimed there.
type employmentReference struct {
	ref    personfacts.OrganizationReference
	titles []string
}

func employmentReferences(claims []personfacts.ProposedClaim) []employmentReference {
	var references []employmentReference
	index := make(map[string]int)
	for _, claim := range claims {
		if claim.Target.Kind != personfacts.TargetEmployment ||
			claim.Relation != personfacts.RelationSupport {
			continue
		}
		normalized, failure, err := personfacts.NormalizeClaimValue(claim.Target, claim.SubmittedValue)
		if err != nil || failure != nil || normalized == nil {
			continue
		}
		var value personfacts.EmploymentValue
		if json.Unmarshal(normalized.JSON, &value) != nil || value.Organization.ID != nil ||
			strings.TrimSpace(value.Organization.Name) == "" {
			continue
		}
		key := store.NormalizeOrganizationName(value.Organization.Name) + "\x00" + value.Organization.Domain
		position, seen := index[key]
		if !seen {
			position = len(references)
			index[key] = position
			references = append(references, employmentReference{ref: value.Organization})
		}
		if value.Title != "" && !containsTitle(references[position].titles, value.Title) {
			references[position].titles = append(references[position].titles, value.Title)
		}
	}
	return references
}

func titleKey(title string) string { return strings.ToLower(strings.Join(strings.Fields(title), " ")) }

func containsTitle(titles []string, title string) bool {
	for _, existing := range titles {
		if titleKey(existing) == titleKey(title) {
			return true
		}
	}
	return false
}

// titlePair is one asked pair: the claimed title and the title already
// known at the organization it would map to.
type titlePair struct {
	organizationID int64
	title          string
	other          string
}

func (p *Preparer) resolve(
	ctx context.Context, personID int64, reference employmentReference,
) (ReferenceResult, error) {
	shortlist, err := p.store.OrganizationShortlistContext(ctx, reference.ref)
	if err != nil {
		return ReferenceResult{}, err
	}
	switch shortlist.Status {
	case store.OrganizationAmbiguous:
		return ReferenceResult{Outcome: OutcomeAmbiguous}, nil
	case store.OrganizationReused:
		organizationID := shortlist.MatchedIDs[0]
		pairs, err := p.titlePairs(ctx, personID, organizationID, reference.titles, nil)
		if err != nil {
			return ReferenceResult{}, err
		}
		result := ReferenceResult{Outcome: OutcomeExact, OrganizationID: organizationID}
		if len(pairs) == 0 {
			return result, nil
		}
		return p.askTitlesOnly(ctx, result, pairs)
	}
	if len(shortlist.Candidates) == 0 {
		return ReferenceResult{Outcome: OutcomeNoMatch}, nil
	}
	return p.askOrganization(ctx, personID, reference, shortlist)
}

// titlePairs pairs each claimed title with every distinct title already
// known for the person at the organization, and later claimed titles with
// earlier ones, up to the room left in the request.
func (p *Preparer) titlePairs(
	ctx context.Context, personID, organizationID int64, titles []string, existing []titlePair,
) ([]titlePair, error) {
	if len(titles) == 0 || len(existing) >= MaxTitlePairs {
		return nil, nil
	}
	known, err := p.store.PersonOrganizationTitlesContext(ctx, personID, organizationID)
	if err != nil {
		return nil, err
	}
	// Compare roles, not spellings: a title already mapped to a known title
	// is that role and needs no question.
	canonical, err := p.store.EmploymentTitleCanonicalContext(ctx, organizationID, append(slices.Clone(titles), known...))
	if err != nil {
		return nil, err
	}
	role := func(title string) string {
		if mapped, ok := canonical[titleKey(title)]; ok {
			return titleKey(mapped)
		}
		return titleKey(title)
	}
	var pairs []titlePair
	room := MaxTitlePairs - len(existing)
	for _, title := range titles {
		sameRole := false
		for _, other := range known {
			if role(title) == role(other) {
				sameRole = true
				break
			}
		}
		if sameRole {
			continue
		}
		for _, other := range known {
			if len(pairs) >= room {
				return pairs, nil
			}
			pairs = append(pairs, titlePair{organizationID: organizationID, title: title, other: other})
		}
		known = append(known, title)
	}
	return pairs, nil
}

func (p *Preparer) askTitlesOnly(
	ctx context.Context, result ReferenceResult, pairs []titlePair,
) (ReferenceResult, error) {
	organizationName, err := p.organizationName(ctx, result.OrganizationID)
	if err != nil {
		return ReferenceResult{}, err
	}
	state := State{TitlePairs: make(map[string]TitlePairState, len(pairs))}
	names := map[int64]string{result.OrganizationID: organizationName}
	questions := addTitlePairs(&state, pairs, names)
	response, err := p.judge.JudgeQuestions(ctx, Feature(), p.automatic, state, questions, time.Time{})
	if err != nil {
		result.Skipped = jev.Skipped(err)
		p.logSkipped(result.Skipped)
		return result, nil
	}
	result.Asked = true
	result.TitleAliases, err = p.applyTitles(ctx, response, pairs, result.OrganizationID)
	p.logOutcome(result, response)
	return result, err
}

func (p *Preparer) organizationName(ctx context.Context, organizationID int64) (string, error) {
	organization, err := p.store.GetOrganizationContext(ctx, organizationID)
	if err != nil {
		return "", err
	}
	return organization.Name, nil
}

func addTitlePairs(state *State, pairs []titlePair, names map[int64]string) []string {
	if state.TitlePairs == nil {
		state.TitlePairs = make(map[string]TitlePairState, len(pairs))
	}
	questions := make([]string, 0, len(pairs))
	for i, pair := range pairs {
		state.TitlePairs[PairKey(i)] = TitlePairState{
			Organization: names[pair.organizationID], Title: pair.title, OtherTitle: pair.other,
		}
		questions = append(questions, TitleQuestion(i))
	}
	return questions
}

func (p *Preparer) askOrganization(
	ctx context.Context, personID int64, reference employmentReference, shortlist *store.OrganizationShortlist,
) (ReferenceResult, error) {
	state := State{
		Reference:  &ReferenceState{Name: shortlist.Reference.Name, Domain: shortlist.Reference.Domain},
		Candidates: make(map[string]CandidateState, len(shortlist.Candidates)),
	}
	names := make(map[int64]string, len(shortlist.Candidates))
	byKey := make(map[string]int64, len(shortlist.Candidates))
	var pairs []titlePair
	for i, candidate := range shortlist.Candidates {
		key := CandidateKey(i)
		state.Candidates[key] = CandidateState{
			Name: candidate.Name, Domains: candidate.Domains, OtherNames: candidate.OtherNames,
		}
		names[candidate.OrganizationID] = candidate.Name
		byKey[key] = candidate.OrganizationID
		more, err := p.titlePairs(ctx, personID, candidate.OrganizationID, reference.titles, pairs)
		if err != nil {
			return ReferenceResult{}, err
		}
		pairs = append(pairs, more...)
	}
	questions := append([]string{QuestionOrgRef}, addTitlePairs(&state, pairs, names)...)
	if len(pairs) == 0 {
		state.TitlePairs = nil
	}
	response, err := p.judge.JudgeQuestions(ctx, Feature(), p.automatic, state, questions, time.Time{})
	if err != nil {
		result := ReferenceResult{Outcome: OutcomeSkipped, Skipped: jev.Skipped(err)}
		p.logSkipped(result.Skipped)
		return result, nil
	}
	result := ReferenceResult{Asked: true, Outcome: OutcomeNew}
	answer := response.Answers[QuestionOrgRef]
	bestKey := ""
	for key := range byKey {
		probability := answer.Probabilities[key]
		if bestKey == "" || probability > result.Probability ||
			(probability == result.Probability && key < bestKey) {
			bestKey, result.Probability = key, probability
		}
	}
	chosen := byKey[bestKey]
	switch {
	case result.Probability >= AliasThreshold:
		result.Outcome, result.OrganizationID = OutcomeAlias, chosen
		if _, err := p.store.RecordOrganizationResolutionAliasContext(ctx, store.OrganizationAliasInput{
			OrganizationID: chosen, Name: shortlist.Reference.Name, Domain: shortlist.Reference.Domain,
			Model: response.Model, Confidence: result.Probability,
		}); err != nil {
			return ReferenceResult{}, err
		}
		// The alias is only an input: rerun the deterministic lookup so the
		// log says what projection will see.
		rerun, err := p.store.OrganizationShortlistContext(ctx, reference.ref)
		if err != nil {
			return ReferenceResult{}, err
		}
		if rerun.Status != store.OrganizationReused || rerun.MatchedIDs[0] != chosen {
			p.logger.Debug("organization alias did not make the lookup unique",
				"feature", jev.FeatureOrganizationResolution, "status", string(rerun.Status))
		}
		chosenPairs := make([]titlePair, 0, len(pairs))
		indexes := make([]int, 0, len(pairs))
		for i, pair := range pairs {
			if pair.organizationID == chosen {
				chosenPairs = append(chosenPairs, pair)
				indexes = append(indexes, i)
			}
		}
		result.TitleAliases, err = p.applyTitlesAt(ctx, response, chosenPairs, indexes, chosen)
		if err != nil {
			return ReferenceResult{}, err
		}
	case result.Probability >= ReviewThreshold:
		result.Outcome, result.OrganizationID = OutcomeReview, chosen
		if _, err := p.store.RecordOrganizationMatchReviewContext(ctx, store.OrganizationMatchReviewInput{
			OrganizationID: chosen, Name: shortlist.Reference.Name, Domain: shortlist.Reference.Domain,
			Model: response.Model, Probability: result.Probability,
		}); err != nil {
			return ReferenceResult{}, err
		}
	}
	p.logOutcome(result, response)
	return result, nil
}

func (p *Preparer) applyTitles(
	ctx context.Context, response jev.Response, pairs []titlePair, organizationID int64,
) (int, error) {
	indexes := make([]int, len(pairs))
	for i := range pairs {
		indexes[i] = i
	}
	return p.applyTitlesAt(ctx, response, pairs, indexes, organizationID)
}

// applyTitlesAt records a title alias for every pair whose question (at the
// matching index) cleared TitleThreshold.
func (p *Preparer) applyTitlesAt(
	ctx context.Context, response jev.Response, pairs []titlePair, indexes []int, organizationID int64,
) (int, error) {
	written := 0
	for i, pair := range pairs {
		answer, ok := response.Answers[TitleQuestion(indexes[i])]
		if !ok || answer.Noul < TitleThreshold {
			continue
		}
		added, err := p.store.RecordEmploymentTitleAliasContext(ctx, store.EmploymentTitleAliasInput{
			OrganizationID: organizationID, Title: pair.title, CanonicalTitle: pair.other,
			Model: response.Model, Confidence: answer.Noul,
		})
		if err != nil {
			return written, fmt.Errorf("record title alias: %w", err)
		}
		if added {
			written++
		}
	}
	return written, nil
}

func (p *Preparer) logSkipped(category string) {
	p.logger.Debug("organization resolution judgment skipped",
		"feature", jev.FeatureOrganizationResolution, "category", category)
}

func (p *Preparer) logOutcome(result ReferenceResult, response jev.Response) {
	p.logger.Debug("organization resolution judgment",
		"feature", jev.FeatureOrganizationResolution, "outcome", string(result.Outcome),
		"probability", result.Probability, "title_aliases", result.TitleAliases,
		"answers", jev.SafeAnswers(response.Answers))
}
