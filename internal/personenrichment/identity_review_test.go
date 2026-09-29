package personenrichment_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/personenrichment"
)

func TestDecideIdentityJudgmentThresholds(t *testing.T) {
	cases := []struct {
		name                        string
		exact                       personenrichment.IdentifierClass
		nameCompatible, companySame float64
		nameConflict                float64
		want                        personenrichment.IdentityJudgmentOutcome
	}{
		{"abbreviated surname at the same company", personenrichment.IdentifierCurrentCompany, 0.95, 0.99, 0.02, personenrichment.IdentityJudgmentAccepted},
		{"batch tag on the company", personenrichment.IdentifierName, 0.99, 0.97, 0.01, personenrichment.IdentityJudgmentAccepted},
		{"accept threshold is inclusive", personenrichment.IdentifierName, 0.99, 0.90, 0.20, personenrichment.IdentityJudgmentAccepted},
		{"strong but conflicting name", personenrichment.IdentifierCurrentCompany, 0.92, 0.99, 0.35, personenrichment.IdentityJudgmentUncertain},
		{"middle of the road", personenrichment.IdentifierCurrentCompany, 0.70, 0.99, 0.10, personenrichment.IdentityJudgmentUncertain},
		{"uncertain threshold is inclusive", personenrichment.IdentifierName, 0.99, 0.50, 0.0, personenrichment.IdentityJudgmentUncertain},
		{"different person with a common first name", personenrichment.IdentifierCurrentCompany, 0.30, 0.99, 0.85, personenrichment.IdentityJudgmentRejected},
		{"different company", personenrichment.IdentifierName, 0.99, 0.10, 0.0, personenrichment.IdentityJudgmentRejected},
		{"a name conflict blocks acceptance even beside an exact name", personenrichment.IdentifierName, 0.10, 0.95, 0.90, personenrichment.IdentityJudgmentUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := personenrichment.DecideIdentityJudgment(tc.exact, tc.nameCompatible, tc.companySame, tc.nameConflict)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIdentityJudgmentValidateRejectsInconsistentOutcomes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	judgment := personenrichment.IdentityJudgment{
		Outcome: personenrichment.IdentityJudgmentAccepted, ExactClass: personenrichment.IdentifierCurrentCompany,
		NameCompatible: 0.95, CompanySame: 0.99, NameConflict: 0.02, Model: "jev-1.13.0",
	}
	require.NoError(judgment.Validate())
	judgment.Outcome = personenrichment.IdentityJudgmentRejected
	require.Error(judgment.Validate(), "an outcome must follow from its probabilities")
	judgment.Outcome = personenrichment.IdentityJudgmentAccepted
	judgment.ExactClass = personenrichment.IdentifierEmail
	require.Error(judgment.Validate())
	judgment.ExactClass = personenrichment.IdentifierCurrentCompany
	judgment.Model = ""
	require.Error(judgment.Validate())
	judgment.Model = "jev-1.13.0"
	judgment.NameConflict = 1.5
	assert.Error(judgment.Validate())
}

func partialRequest() personenrichment.Request {
	return personenrichment.Request{
		RequestHash: "hash",
		Identity: personenrichment.Identity{
			Name: "susie singh", CurrentCompany: "heavybit", Email: "susie@example.test",
		},
	}
}

func partialResult(name, company string) personenrichment.Result {
	return personenrichment.Result{
		State: personenrichment.ResultComplete,
		IdentityMatches: []personenrichment.IdentityMatch{
			{Class: personenrichment.IdentifierCurrentCompany, Value: company, Confidence: 900},
		},
		ReturnedIdentity: &personenrichment.ReturnedIdentity{
			Name: name, FirstName: "Susie", LastName: "S.", Location: "Example City",
			CurrentRoles:   []personenrichment.ReturnedRole{{Title: "Partner", Company: company}},
			PastCompanies:  []string{"Example Ventures"},
			ProfileURLHost: "profiles.example.test",
		},
	}
}

func TestSemanticIdentityReviewOnlyForExactlyOneMatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	review, ok := personenrichment.SemanticIdentityReview(partialRequest(), partialResult("Susie S.", "Heavybit"))
	require.True(ok)
	assert.Equal(personenrichment.IdentifierCurrentCompany, review.Exact)
	assert.Equal(personenrichment.RequestedIdentity{Name: "susie singh", Company: "heavybit", EmailDomain: "example.test"}, review.Requested)
	assert.Equal("Susie S.", review.Returned.Name)
	assert.Equal("profiles.example.test", review.Returned.ProfileURLHost)

	both := partialResult("Susie Singh", "Heavybit")
	both.IdentityMatches = append(both.IdentityMatches, personenrichment.IdentityMatch{
		Class: personenrichment.IdentifierName, Value: "Susie Singh", Confidence: 900,
	})
	_, ok = personenrichment.SemanticIdentityReview(partialRequest(), both)
	assert.False(ok, "two exact matches need no review")

	neither := partialResult("Someone Else", "Other Corp")
	_, ok = personenrichment.SemanticIdentityReview(partialRequest(), neither)
	assert.False(ok, "no exact match is a plain rejection")

	nameOnly := personenrichment.Result{
		State: personenrichment.ResultComplete,
		IdentityMatches: []personenrichment.IdentityMatch{
			{Class: personenrichment.IdentifierName, Value: "Susie Singh", Confidence: 900},
		},
		ReturnedIdentity: &personenrichment.ReturnedIdentity{
			Name: "Susie Singh", CurrentRoles: []personenrichment.ReturnedRole{{Company: "Dataherald (YC W21)"}},
		},
	}
	dataherald := partialRequest()
	dataherald.Identity.CurrentCompany = "dataherald"
	review, ok = personenrichment.SemanticIdentityReview(dataherald, nameOnly)
	require.True(ok)
	assert.Equal(personenrichment.IdentifierName, review.Exact)

	noReturned := partialResult("Susie S.", "Heavybit")
	noReturned.ReturnedIdentity = nil
	_, ok = personenrichment.SemanticIdentityReview(partialRequest(), noReturned)
	assert.False(ok, "a provider that returned no identity fields cannot be reviewed")

	long := partialResult("Susie S.", "Heavybit")
	for range 20 {
		long.ReturnedIdentity.PastCompanies = append(long.ReturnedIdentity.PastCompanies, "Another Company")
		long.ReturnedIdentity.CurrentRoles = append(long.ReturnedIdentity.CurrentRoles, personenrichment.ReturnedRole{Company: "Heavybit"})
	}
	review, ok = personenrichment.SemanticIdentityReview(partialRequest(), long)
	require.True(ok)
	assert.Len(review.Returned.PastCompanies, 10, "past companies are capped at 10")
	assert.Len(review.Returned.CurrentRoles, 5, "current roles are capped at 5")
}

func TestApplyIdentityJudgmentMapsOutcomesToAssessments(t *testing.T) {
	assert := assert.New(t)
	declined := personenrichment.IdentityAssessment{Reason: "identity_not_verified"}
	accepted := personenrichment.ApplyIdentityJudgment(declined, personenrichment.IdentityJudgment{
		Outcome: personenrichment.IdentityJudgmentAccepted, ExactClass: personenrichment.IdentifierCurrentCompany,
		NameCompatible: 0.95, CompanySame: 0.99, NameConflict: 0.02, Model: "jev-1.13.0",
	})
	assert.True(accepted.Accepted)
	assert.Equal(personenrichment.SemanticIdentityScore, accepted.Score)
	assert.Equal(personenrichment.SemanticIdentityReason, accepted.Reason)
	assert.Equal([]personenrichment.IdentifierClass{personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany}, accepted.MatchedClasses)
	assert.NotNil(accepted.Judgment)

	uncertain := personenrichment.ApplyIdentityJudgment(declined, personenrichment.IdentityJudgment{
		Outcome: personenrichment.IdentityJudgmentUncertain, ExactClass: personenrichment.IdentifierCurrentCompany,
		NameCompatible: 0.70, CompanySame: 0.99, NameConflict: 0.10, Model: "jev-1.13.0",
	})
	assert.False(uncertain.Accepted)
	assert.Equal(personenrichment.IdentityUncertainReason, uncertain.Reason)
	assert.Zero(uncertain.Score)

	rejected := personenrichment.ApplyIdentityJudgment(declined, personenrichment.IdentityJudgment{
		Outcome: personenrichment.IdentityJudgmentRejected, ExactClass: personenrichment.IdentifierCurrentCompany,
		NameCompatible: 0.30, CompanySame: 0.99, NameConflict: 0.85, Model: "jev-1.13.0",
	})
	assert.False(rejected.Accepted)
	assert.Equal("identity_not_verified", rejected.Reason, "a rejection keeps the exact rule's answer")
	assert.NotNil(rejected.Judgment, "the rejection is still auditable")

	already := personenrichment.IdentityAssessment{Accepted: true, Score: 1000, Reason: "strong_identifier_match"}
	assert.Equal(already, personenrichment.ApplyIdentityJudgment(already, personenrichment.IdentityJudgment{Outcome: personenrichment.IdentityJudgmentRejected}))
}

type consentTable struct {
	mu     sync.Mutex
	active map[string]string
}

func (c *consentTable) HasActiveJevFeatureConsent(_ context.Context, feature, fingerprint string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active[feature] == fingerprint, nil
}

func fakeJevServer(t *testing.T, nameCompatible, companySame, nameConflict float64) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	bodies := make([]map[string]any, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": jev.DefaultModel,
			"answers": map[string]any{
				"name_compatible": map[string]any{"type": "noul", "noul": nameCompatible},
				"company_same":    map[string]any{"type": "noul", "noul": companySame},
				"name_conflict":   map[string]any{"type": "noul", "noul": nameConflict},
			},
			"usage": map[string]any{"input_tokens": 420, "output_tokens": 30},
		})
	}))
	t.Cleanup(server.Close)
	return server, &bodies
}

func jevServiceFor(t *testing.T, endpoint string, consents jev.ConsentChecker) (*jev.Service, jev.Config) {
	t.Helper()
	var cfg jev.Config
	cfg.ApplyDefaults()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	cfg.IdentityVerification = jev.FeatureConfig{Enabled: true, Automatic: true}
	service, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg, nil },
		Consents:   consents,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
	})
	require.NoError(t, err)
	return service, cfg
}

func TestJevIdentityJudgeSendsOnlyDisclosedFieldsWithConsentedWording(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, bodies := fakeJevServer(t, 0.95, 0.99, 0.02)
	consents := &consentTable{active: map[string]string{}}
	service, cfg := jevServiceFor(t, server.URL, consents)
	policy, err := personenrichment.JevIdentityFeature().Policy(cfg)
	require.NoError(err)
	judge := personenrichment.NewJevIdentityJudge(service, true)
	review, ok := personenrichment.SemanticIdentityReview(partialRequest(), partialResult("Susie S.", "Heavybit"))
	require.True(ok)

	_, err = judge.JudgeIdentity(t.Context(), review)
	require.ErrorIs(err, jev.ErrConsentRequired)
	assert.Empty(*bodies, "revoked or missing consent never sends")

	consents.active[jev.FeatureEnrichmentIdentity] = policy.Fingerprint
	judgment, err := judge.JudgeIdentity(t.Context(), review)
	require.NoError(err)
	assert.Equal(personenrichment.IdentityJudgment{
		Outcome: personenrichment.IdentityJudgmentAccepted, ExactClass: personenrichment.IdentifierCurrentCompany,
		NameCompatible: 0.95, CompanySame: 0.99, NameConflict: 0.02, Model: jev.DefaultModel,
	}, judgment)
	require.Len(*bodies, 1)
	sent := (*bodies)[0]
	assert.Equal(map[string]any{
		"requested": map[string]any{"name": "susie singh", "company": "heavybit", "email_domain": "example.test"},
		"returned": map[string]any{
			"name": "Susie S.", "first_name": "Susie", "last_name": "S.", "location": "Example City",
			"current_roles":    []any{map[string]any{"title": "Partner", "company": "Heavybit"}},
			"past_companies":   []any{"Example Ventures"},
			"profile_url_host": "profiles.example.test",
		},
	}, sent["state"], "exactly the disclosed fields leave, and the email address itself never does")
	questions, ok := sent["questions"].(map[string]any)
	require.True(ok)
	assert.Equal(map[string]any{
		"type":         "noul",
		"instructions": "Could `returned.name` be the same person as `requested.name`?",
		"criteria": map[string]any{
			"true":  "The returned name is the requested name, or an initial, abbreviation, nickname, reordering, or transliteration of it.",
			"false": "The returned name belongs to a different person or only shares a common first name.",
		},
	}, questions["name_compatible"])
	assert.Equal(map[string]any{
		"type":         "noul",
		"instructions": "Do `requested.company` and the current employer in `returned.current_roles` name the same organization?",
		"criteria": map[string]any{
			"true":  "The same organization, allowing a legal suffix, an accelerator batch tag, a former name, or a parent and its well-known product.",
			"false": "A different organization, a competitor, or only a similar-sounding name.",
		},
	}, questions["company_same"])
	assert.Equal(map[string]any{
		"type":         "noul",
		"instructions": "Is `returned.name` clearly a different person from `requested.name`?",
		"criteria": map[string]any{
			"true":  "A given name or surname differs in a way an initial, nickname, or reordering cannot explain.",
			"false": "Nothing in the returned name rules out the requested person.",
		},
	}, questions["name_conflict"])
	assert.Len(questions, 3)

	delete(consents.active, jev.FeatureEnrichmentIdentity)
	_, err = judge.JudgeIdentity(t.Context(), review)
	require.ErrorIs(err, jev.ErrConsentRequired)
	assert.Len(*bodies, 1, "consent revoked after a grant stops the next request")

	var nilJudge *personenrichment.JevIdentityJudge
	_, err = nilJudge.JudgeIdentity(t.Context(), review)
	require.ErrorIs(err, jev.ErrDisabled)
}
