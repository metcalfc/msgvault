package personenrichment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/jev"
)

// Semantic identity thresholds. The non-exact field must score at least
// IdentityAcceptThreshold with a name conflict no higher than
// IdentityConflictCeiling to accept; from IdentityUncertainThreshold up the
// attempt is recorded as identity_uncertain for a person to look at; below
// that it is rejected exactly as an exact-rule mismatch is.
const (
	IdentityAcceptThreshold    = 0.90
	IdentityUncertainThreshold = 0.50
	IdentityConflictCeiling    = 0.20
	// SemanticIdentityScore is the identity score an accepted semantic match
	// carries: the same as an exact name-and-company match, so the resolver's
	// MinimumIdentityScore treats both alike.
	SemanticIdentityScore = 900
	// SemanticIdentityReason is the accepted assessment reason.
	SemanticIdentityReason = "semantic_name_company"
	// IdentityUncertainReason is the not-accepted assessment reason whose
	// attempt state is identity_uncertain.
	IdentityUncertainReason = "identity_uncertain"

	maxReturnedRoles         = 5
	maxReturnedPastCompanies = 10
)

// ReturnedRole is one current position on the returned profile.
type ReturnedRole struct {
	Title   string `json:"title,omitempty"`
	Company string `json:"company,omitempty"`
}

// ReturnedIdentity is the provider-side identity a review compares against.
// It is transient adapter output like IdentityMatch.Value: the worker reads
// it and the sink never receives it.
type ReturnedIdentity struct {
	Name           string         `json:"name,omitempty"`
	FirstName      string         `json:"first_name,omitempty"`
	LastName       string         `json:"last_name,omitempty"`
	Location       string         `json:"location,omitempty"`
	CurrentRoles   []ReturnedRole `json:"current_roles,omitempty"`
	PastCompanies  []string       `json:"past_companies,omitempty"`
	ProfileURLHost string         `json:"profile_url_host,omitempty"`
}

// RequestedIdentity is the archive-side identity a review discloses: the
// name, the current company, and only the domain of the email.
type RequestedIdentity struct {
	Name        string `json:"name,omitempty"`
	Company     string `json:"company,omitempty"`
	EmailDomain string `json:"email_domain,omitempty"`
}

// IdentityReview is the state object for one semantic identity check. Exact
// names the class that matched exactly; the other is the one under review.
type IdentityReview struct {
	Requested RequestedIdentity `json:"requested"`
	Returned  ReturnedIdentity  `json:"returned"`
	Exact     IdentifierClass   `json:"-"`
}

// IdentityJudgmentOutcome is what the thresholds decided.
type IdentityJudgmentOutcome string

const (
	IdentityJudgmentAccepted  IdentityJudgmentOutcome = "accepted"
	IdentityJudgmentUncertain IdentityJudgmentOutcome = "uncertain"
	IdentityJudgmentRejected  IdentityJudgmentOutcome = "rejected"
)

// IdentityJudgment is a stored judgment: the three probabilities, the
// decision, and which class was exact. It carries no names.
type IdentityJudgment struct {
	Outcome        IdentityJudgmentOutcome `json:"outcome"`
	ExactClass     IdentifierClass         `json:"exact_class"`
	NameCompatible float64                 `json:"name_compatible"`
	CompanySame    float64                 `json:"company_same"`
	NameConflict   float64                 `json:"name_conflict"`
	Model          string                  `json:"model"`
}

// Validate checks a judgment is well formed and consistent with the
// thresholds, so a stored judgment cannot claim an outcome its numbers do
// not support.
func (j IdentityJudgment) Validate() error {
	if j.ExactClass != IdentifierName && j.ExactClass != IdentifierCurrentCompany {
		return errors.New("identity judgment exact class must be name or current_company")
	}
	for _, value := range []float64{j.NameCompatible, j.CompanySame, j.NameConflict} {
		if value < 0 || value > 1 {
			return errors.New("identity judgment probabilities must be in [0,1]")
		}
	}
	if strings.TrimSpace(j.Model) == "" {
		return errors.New("identity judgment model is required")
	}
	if DecideIdentityJudgment(j.ExactClass, j.NameCompatible, j.CompanySame, j.NameConflict) != j.Outcome {
		return errors.New("identity judgment outcome does not follow from its probabilities")
	}
	return nil
}

// DecideIdentityJudgment applies the thresholds. exact is the class that
// matched exactly; the other class's probability is the one that decides.
func DecideIdentityJudgment(exact IdentifierClass, nameCompatible, companySame, nameConflict float64) IdentityJudgmentOutcome {
	other := companySame
	if exact == IdentifierCurrentCompany {
		other = nameCompatible
	}
	switch {
	case other >= IdentityAcceptThreshold && nameConflict <= IdentityConflictCeiling:
		return IdentityJudgmentAccepted
	case other >= IdentityUncertainThreshold:
		return IdentityJudgmentUncertain
	default:
		return IdentityJudgmentRejected
	}
}

// IdentityJudge decides a review. The Jev-backed implementation returns an
// error whenever no request may be sent; the worker then keeps the exact
// rule's answer.
type IdentityJudge interface {
	JudgeIdentity(ctx context.Context, review IdentityReview) (IdentityJudgment, error)
}

// SemanticIdentityReview builds the review for a result in which exactly one
// of name and current company matched exactly and the other is present on
// both sides. It reports false whenever the exact rule already decided or a
// review would compare against nothing.
func SemanticIdentityReview(request Request, result Result) (IdentityReview, bool) {
	if result.ReturnedIdentity == nil || request.Identity.Name == "" || request.Identity.CurrentCompany == "" {
		return IdentityReview{}, false
	}
	returned := *result.ReturnedIdentity
	nameMatch := false
	companyMatch := false
	for _, match := range result.IdentityMatches {
		switch match.Class {
		case IdentifierName:
			nameMatch = nameMatch || nameIdentifierMatch(request.Identity.Name, match.Value)
		case IdentifierCurrentCompany:
			companyMatch = companyMatch || exactNormalizedIdentifierMatch(match.Class, request.Identity.CurrentCompany, match.Value)
		case IdentifierEmail, IdentifierPhone, IdentifierPublicProfileURL:
			// Strong classes are decided by the exact rule.
		}
	}
	if nameMatch == companyMatch {
		return IdentityReview{}, false
	}
	review := IdentityReview{
		Requested: RequestedIdentity{
			Name: request.Identity.Name, Company: request.Identity.CurrentCompany,
			EmailDomain: emailDomain(request.Identity.Email),
		},
		Returned: boundedReturnedIdentity(returned),
	}
	switch {
	case nameMatch:
		review.Exact = IdentifierName
		if len(review.Returned.CurrentRoles) == 0 {
			return IdentityReview{}, false
		}
	default:
		review.Exact = IdentifierCurrentCompany
		if review.Returned.Name == "" {
			return IdentityReview{}, false
		}
	}
	return review, true
}

func boundedReturnedIdentity(returned ReturnedIdentity) ReturnedIdentity {
	bounded := returned
	bounded.CurrentRoles = slices.Clone(returned.CurrentRoles)
	if len(bounded.CurrentRoles) > maxReturnedRoles {
		bounded.CurrentRoles = bounded.CurrentRoles[:maxReturnedRoles]
	}
	bounded.PastCompanies = slices.Clone(returned.PastCompanies)
	if len(bounded.PastCompanies) > maxReturnedPastCompanies {
		bounded.PastCompanies = bounded.PastCompanies[:maxReturnedPastCompanies]
	}
	return bounded
}

func emailDomain(email string) string {
	_, domain, found := strings.Cut(strings.TrimSpace(email), "@")
	if !found {
		return ""
	}
	return strings.ToLower(domain)
}

// ApplyIdentityJudgment folds a judgment into the exact rule's assessment.
// Accepted becomes a semantic name-and-company match at SemanticIdentityScore;
// uncertain becomes the identity_uncertain reason; rejected keeps the exact
// rule's answer. The judgment travels with the assessment for storage.
func ApplyIdentityJudgment(assessment IdentityAssessment, judgment IdentityJudgment) IdentityAssessment {
	if assessment.Accepted {
		return assessment
	}
	stored := judgment
	switch judgment.Outcome {
	case IdentityJudgmentAccepted:
		return IdentityAssessment{
			Accepted: true, Score: SemanticIdentityScore, Reason: SemanticIdentityReason,
			MatchedClasses: []IdentifierClass{IdentifierName, IdentifierCurrentCompany},
			Judgment:       &stored,
		}
	case IdentityJudgmentUncertain:
		return IdentityAssessment{Reason: IdentityUncertainReason, Judgment: &stored}
	case IdentityJudgmentRejected:
		assessment.Judgment = &stored
		return assessment
	default:
		return assessment
	}
}

// Question IDs and state fields of the enrichment identity feature.
const (
	identityQuestionNameCompatible = "name_compatible"
	identityQuestionCompanySame    = "company_same"
	identityQuestionNameConflict   = "name_conflict"
)

// JevIdentityFeature is the exact policy the enrichment identity check
// consents to: three independent Nouls over the requested and returned
// identity fields. Changing any wording or field here changes the
// fingerprint and requires new consent.
func JevIdentityFeature() jev.FeatureSpec {
	return jev.FeatureSpec{
		Name:  jev.FeatureEnrichmentIdentity,
		Title: "Enrichment identity check",
		Purpose: "When an enrichment provider returns a person whose name or current company " +
			"matches the requested person exactly but the other does not, decide whether " +
			"it is the same person before any claim is applied.",
		Questions: []jev.Question{
			{
				ID: identityQuestionNameCompatible, Type: jev.QuestionNoul,
				Instructions: "Could `returned.name` be the same person as `requested.name`?",
				Criteria: jev.NoulCriteria{
					True:  "The returned name is the requested name, or an initial, abbreviation, nickname, reordering, or transliteration of it.",
					False: "The returned name belongs to a different person or only shares a common first name.",
				},
			},
			{
				ID: identityQuestionCompanySame, Type: jev.QuestionNoul,
				Instructions: "Do `requested.company` and the current employer in `returned.current_roles` name the same organization?",
				Criteria: jev.NoulCriteria{
					True:  "The same organization, allowing a legal suffix, an accelerator batch tag, a former name, or a parent and its well-known product.",
					False: "A different organization, a competitor, or only a similar-sounding name.",
				},
			},
			{
				ID: identityQuestionNameConflict, Type: jev.QuestionNoul,
				Instructions: "Is `returned.name` clearly a different person from `requested.name`?",
				Criteria: jev.NoulCriteria{
					True:  "A given name or surname differs in a way an initial, nickname, or reordering cannot explain.",
					False: "Nothing in the returned name rules out the requested person.",
				},
			},
		},
		StateFields: []string{
			"requested.name", "requested.company", "requested.email_domain",
			"returned.name", "returned.first_name", "returned.last_name", "returned.location",
			"returned.current_roles[].title", "returned.current_roles[].company",
			"returned.past_companies[]", "returned.profile_url_host",
		},
	}
}

// JevIdentityJudge asks the enrichment identity feature through the shared
// Jev service. automatic marks unattended callers such as scheduled runs.
type JevIdentityJudge struct {
	service   *jev.Service
	automatic bool
}

// NewJevIdentityJudge wires the judge; a nil service means no judge.
func NewJevIdentityJudge(service *jev.Service, automatic bool) *JevIdentityJudge {
	if service == nil {
		return nil
	}
	return &JevIdentityJudge{service: service, automatic: automatic}
}

// JudgeIdentity sends exactly the review's fields and applies the
// thresholds. Any gate, budget, or provider failure is returned as is so the
// worker keeps the exact rule's answer and reports the category.
func (j *JevIdentityJudge) JudgeIdentity(ctx context.Context, review IdentityReview) (IdentityJudgment, error) {
	if j == nil || j.service == nil {
		return IdentityJudgment{}, jev.ErrDisabled
	}
	if review.Exact != IdentifierName && review.Exact != IdentifierCurrentCompany {
		return IdentityJudgment{}, errors.New("identity review must name the exact class")
	}
	// No caller deadline: the client's configured request timeout bounds the
	// exchange, so a slow provider is judged by the operator's timeout alone.
	response, err := j.service.Judge(ctx, JevIdentityFeature(), j.automatic, review, time.Time{})
	if err != nil {
		return IdentityJudgment{}, err
	}
	judgment := IdentityJudgment{
		ExactClass:     review.Exact,
		NameCompatible: response.Answers[identityQuestionNameCompatible].Noul,
		CompanySame:    response.Answers[identityQuestionCompanySame].Noul,
		NameConflict:   response.Answers[identityQuestionNameConflict].Noul,
		Model:          response.Model,
	}
	judgment.Outcome = DecideIdentityJudgment(judgment.ExactClass, judgment.NameCompatible, judgment.CompanySame, judgment.NameConflict)
	if err := judgment.Validate(); err != nil {
		return IdentityJudgment{}, fmt.Errorf("%w: %w", jev.ErrInvalidResponse, err)
	}
	return judgment, nil
}
