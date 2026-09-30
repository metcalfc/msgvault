package orgresolution_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/orgresolution"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

var fixtureNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// fakeJev answers org_ref with the configured candidate probabilities (the
// rest going to new_organization) and every title_same_role question with
// the configured probability. It records every request body.
type fakeJev struct {
	mu        sync.Mutex
	server    *httptest.Server
	bodies    []map[string]any
	orgRef    map[string]float64
	titleSame float64
}

func newFakeJev(t *testing.T, orgRef map[string]float64, titleSame float64) *fakeJev {
	t.Helper()
	fake := &fakeJev{orgRef: orgRef, titleSame: titleSame}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		fake.mu.Lock()
		fake.bodies = append(fake.bodies, body)
		fake.mu.Unlock()
		questions, _ := body["questions"].(map[string]any)
		answers := make(map[string]any, len(questions))
		for id := range questions {
			if id == orgresolution.QuestionOrgRef {
				answers[id] = fake.choice()
				continue
			}
			answers[id] = map[string]any{"type": "noul", "noul": fake.titleSame}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": jev.DefaultModel, "answers": answers,
			"usage": map[string]any{"input_tokens": 300, "output_tokens": 20},
		})
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeJev) choice() map[string]any {
	probabilities := map[string]any{}
	rest := 1.0
	best, bestProbability := orgresolution.OptionNewOrganization, 0.0
	keys := make([]string, 0, len(f.orgRef))
	for key := range f.orgRef {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		probability := f.orgRef[key]
		probabilities[key] = probability
		rest -= probability
		if probability > bestProbability {
			best, bestProbability = key, probability
		}
	}
	probabilities[orgresolution.OptionNewOrganization] = max(rest, 0)
	if rest > bestProbability {
		best = orgresolution.OptionNewOrganization
	}
	return map[string]any{"type": "choice", "choice": best, "probabilities": probabilities, "confidence": 0.8}
}

func (f *fakeJev) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...)
}

// fixture is a real archive with one tracked person, a Jev service bound to
// the fake endpoint, and consent granted for the feature's current policy.
type fixture struct {
	store    *store.Store
	personID int64
	target   personfacts.TargetDescriptor
	jev      *fakeJev
	config   jev.Config
	service  *jev.Service
	policy   jev.Policy
}

func newFixture(t *testing.T, fake *fakeJev) *fixture {
	t.Helper()
	st := testutil.NewTestStore(t)
	participantID, err := st.EnsureParticipant("rowan@example.test", "Rowan Example", "example.test")
	require.NoError(t, err)
	person, _, err := st.CreatePersonFromParticipant(participantID)
	require.NoError(t, err)
	_, err = st.SetPersonTrackingContext(t.Context(), person.ID, true)
	require.NoError(t, err)
	catalog, err := st.BuildPersonFactCatalogContext(t.Context(), true)
	require.NoError(t, err)
	var target personfacts.TargetDescriptor
	for _, candidate := range catalog.Targets {
		if candidate.Kind == personfacts.TargetEmployment {
			target = candidate
		}
	}
	require.Equal(t, personfacts.TargetEmployment, target.Kind)

	f := &fixture{store: st, personID: person.ID, target: target, jev: fake}
	f.config = jev.DefaultConfig()
	f.config.Enabled = true
	f.config.Endpoint = fake.server.URL
	f.config.OrganizationResolution = jev.FeatureConfig{Enabled: true}
	f.service, err = jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return f.config, nil },
		Consents:   st,
		Ledger:     st,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
		Now:        func() time.Time { return fixtureNow },
	})
	require.NoError(t, err)
	f.policy, err = orgresolution.Feature().Policy(f.config)
	require.NoError(t, err)
	_, _, err = st.GrantJevFeatureConsent(t.Context(), jev.FeatureOrganizationResolution, f.policy.Fingerprint, "test")
	require.NoError(t, err)
	return f
}

func (f *fixture) preparer() *orgresolution.Preparer {
	return orgresolution.NewPreparer(f.service, f.store, false, nil)
}

func (f *fixture) organization(t *testing.T, name, domain string) *store.Organization {
	t.Helper()
	input := store.OrganizationInput{Name: name, Kind: store.OrganizationKindCompany}
	if domain != "" {
		input.PrimaryDomain = &domain
	}
	organization, err := f.store.CreateOrganizationContext(t.Context(), input)
	require.NoError(t, err)
	return organization
}

// claim is a strongly evidenced current employment claim.
func (f *fixture) claim(organizationJSON, title, suffix string) personfacts.ProposedClaim {
	subject := f.personID
	return personfacts.ProposedClaim{
		Target: f.target, Relation: personfacts.RelationSupport,
		SubmittedValue: json.RawMessage(`{"organization":` + organizationJSON + `,"title":"` + title + `"}`),
		Evidence: []personfacts.EvidenceInput{{
			PersonID: f.personID, SourceClass: personfacts.EvidencePublic,
			Directness: personfacts.DirectSelf, Authority: personfacts.AuthorityAuthoritative,
			SourceURL: "https://example.com/" + suffix, SubjectPersonID: &subject,
			SubjectRef: "synthetic-person", Excerpt: "Synthetic evidence " + suffix,
			SourceVersion: "source-v1", EventTime: fixtureNow.Add(-time.Hour),
			RecordedTime: fixtureNow, IdentityScore: 990,
		}},
		Origin:     personfacts.OriginExtraction,
		Confidence: personfacts.ConfidenceInputs{ReportedScore: 900},
	}
}

// apply commits a generation the way the enrichment and sweep sinks do.
func (f *fixture) apply(t *testing.T, suffix string, claims ...personfacts.ProposedClaim) *personfacts.GenerationResult {
	t.Helper()
	result, err := f.store.ApplyPersonFactGenerationContext(t.Context(), personfacts.GenerationInput{
		PersonID:      f.personID,
		SourceCursors: []personfacts.SourceCursor{{Lane: "fixture", Start: suffix, End: suffix + "-end"}},
		ProgramID:     "org-resolution-fixture", ProgramVersion: "v1",
		ProgramFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CatalogFingerprint: "catalog-fixture", Provider: "fixture", ProviderVersion: "v1",
		Model: "fixture", ModelVersion: "v1", ResolvedAt: fixtureNow,
		Policy: personfacts.PolicyContext{ProviderPolicyFingerprint: "policy-v1"},
		Claims: claims,
	}, nil)
	require.NoError(t, err)
	return result
}

func (f *fixture) currentEmployments(t *testing.T) []store.Employment {
	t.Helper()
	employments, err := f.store.ListEmploymentsContext(t.Context(), store.EmploymentFilter{
		PersonID: f.personID, CurrentOnly: true,
	})
	require.NoError(t, err)
	return employments
}
