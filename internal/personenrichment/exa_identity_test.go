package personenrichment_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personenrichment"
)

func exaPartialServer(t *testing.T, fixtures ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	queries := make([]string, 0)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(raw, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		query, _ := request["query"].(string)
		queries = append(queries, query)
		index := min(len(queries)-1, len(fixtures)-1)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(exaFixture(t, fixtures[index]))
	}))
	t.Cleanup(server.Close)
	return server, &queries
}

func exaNameCompanyConfig(endpoint string) personenrichment.ProviderConfig {
	cfg := exaConfig(endpoint, "people", 1)
	cfg.AllowedIdentifiers = []personenrichment.IdentifierClass{
		personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany,
	}
	return cfg
}

func TestExaPartialMatchIsRejectedWithoutIdentityReview(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, queries := exaPartialServer(t, "exa_people_partial.json")
	provider, err := personenrichment.NewExaProvider(exaNameCompanyConfig(server.URL+"/search"), "test-key", server.Client())
	require.NoError(err)
	_, err = provider.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "susie singh", CurrentCompany: "heavybit"},
		Targets:  exaTypedTargets(t),
	})
	require.Error(err, "today's exact rule rejects an abbreviated surname at decode")
	var providerErr *personenrichment.ProviderError
	require.ErrorAs(err, &providerErr)
	assert.Equal(personenrichment.FailureInvalidOutput, providerErr.Class)
	assert.Len(*queries, 1, "a partial match is not a missing entity, so no retry")
}

func TestExaPartialMatchPassesDecodeWithReturnedIdentityWhenReviewIsEnabled(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, _ := exaPartialServer(t, "exa_people_partial.json")
	provider, err := personenrichment.NewExaProvider(
		exaNameCompanyConfig(server.URL+"/search"), "test-key", server.Client(),
		personenrichment.WithExaIdentityReview())
	require.NoError(err)
	attempt, err := provider.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "susie singh", CurrentCompany: "heavybit"},
		Targets:  exaTypedTargets(t),
	})
	require.NoError(err)
	require.NoError(attempt.Validate())
	result := attempt.Result
	assert.Zero(result.IdentityConfidence, "a partial match carries no identity confidence of its own")
	assert.Equal([]personenrichment.IdentityMatch{{
		Class: personenrichment.IdentifierCurrentCompany, Value: "Heavybit", Confidence: 900,
	}}, result.IdentityMatches)
	require.NotNil(result.ReturnedIdentity)
	assert.Equal(&personenrichment.ReturnedIdentity{
		Name: "Susie S.", FirstName: "Susie", LastName: "S.", Location: "Example City",
		CurrentRoles:   []personenrichment.ReturnedRole{{Title: "Partner", Company: "Heavybit"}},
		PastCompanies:  []string{"Example Ventures"},
		ProfileURLHost: "profiles.example.test",
	}, result.ReturnedIdentity)
	assert.Equal([]personenrichment.ProviderPersonID{{ID: "person_test_partial_7", Confidence: 0}}, result.ProviderPersonIDs)

	// The exact rule still declines it; only a judge can change that.
	assessment := personenrichment.AssessIdentity(personenrichment.Request{
		Identity: personenrichment.Identity{Name: "susie singh", CurrentCompany: "heavybit"},
	}, *result, nil)
	assert.False(assessment.Accepted)
	assert.Equal("identity_not_verified", assessment.Reason)

	// Neither field matching is still a decode failure with review enabled.
	_, err = provider.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "different person", CurrentCompany: "other corp"},
		Targets:  exaTypedTargets(t),
	})
	require.Error(err)
}

func TestExaRetriesOnceWithACodeBuiltNameVariantWhenNothingIsReturned(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, queries := exaPartialServer(t, "exa_people_empty.json", "exa_people_success.json")
	provider, err := personenrichment.NewExaProvider(exaNameCompanyConfig(server.URL+"/search"), "test-key", server.Client())
	require.NoError(err)
	attempt, err := provider.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "test q. user", CurrentCompany: "example labs"},
		Targets:  exaTypedTargets(t),
	})
	require.NoError(err)
	require.Equal([]string{
		"name: test q. user; company: example labs",
		"name: test user; company: example labs",
	}, *queries, "the middle initial is dropped for exactly one retry")
	assert.Equal(900, attempt.Result.IdentityConfidence, "the variant name counts as the requested name")
	assert.Equal(personenrichment.Cost{Currency: "USD", AmountMicros: 7000, Estimated: true}, attempt.Result.Cost,
		"the empty first response cost nothing, so the retry's charge stands alone")
	assert.Equal("request_test_people_42", attempt.RequestID)

	noVariant, err := personenrichment.NewExaProvider(exaNameCompanyConfig(server.URL+"/search"), "test-key", server.Client())
	require.NoError(err)
	*queries = (*queries)[:0]
	_, err = noVariant.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "plain name", CurrentCompany: "example labs"},
		Targets:  exaTypedTargets(t),
	})
	require.Error(err, "a name with no variant is not retried")
	assert.Equal([]string{"name: plain name; company: example labs"}, *queries)
}

func TestExaRetryAddsBothChargesWhenBothResponsesCost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, queries := exaPartialServer(t, "exa_people_empty_charged.json", "exa_people_success.json")
	provider, err := personenrichment.NewExaProvider(exaNameCompanyConfig(server.URL+"/search"), "test-key", server.Client())
	require.NoError(err)
	attempt, err := provider.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "user, test q.", CurrentCompany: "example labs"},
		Targets:  exaTypedTargets(t),
	})
	require.NoError(err)
	require.Len(*queries, 2)
	assert.True(strings.HasPrefix((*queries)[1], "name: test q. user;"), "Last, First is collapsed first")
	assert.Equal(personenrichment.Cost{Currency: "USD", AmountMicros: 9000, Estimated: true}, attempt.Result.Cost)
}
