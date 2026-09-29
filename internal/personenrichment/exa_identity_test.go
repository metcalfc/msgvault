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
		Identity: personenrichment.Identity{Name: "priya ramanathan", CurrentCompany: "example capital"},
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
		Identity: personenrichment.Identity{Name: "priya ramanathan", CurrentCompany: "example capital"},
		Targets:  exaTypedTargets(t),
	})
	require.NoError(err)
	require.NoError(attempt.Validate())
	result := attempt.Result
	assert.Zero(result.IdentityConfidence, "a partial match carries no identity confidence of its own")
	assert.Equal([]personenrichment.IdentityMatch{{
		Class: personenrichment.IdentifierCurrentCompany, Value: "Example Capital", Confidence: 900,
	}}, result.IdentityMatches)
	require.NotNil(result.ReturnedIdentity)
	assert.Equal(&personenrichment.ReturnedIdentity{
		Name: "Priya R.", FirstName: "Priya", LastName: "R.", Location: "Example City",
		CurrentRoles:   []personenrichment.ReturnedRole{{Title: "Partner", Company: "Example Capital"}},
		PastCompanies:  []string{"Example Ventures"},
		ProfileURLHost: "profiles.example.test",
	}, result.ReturnedIdentity)
	assert.Equal([]personenrichment.ProviderPersonID{{ID: "person_test_partial_7", Confidence: 0}}, result.ProviderPersonIDs)

	// The exact rule still declines it; only a judge can change that.
	assessment := personenrichment.AssessIdentity(personenrichment.Request{
		Identity: personenrichment.Identity{Name: "priya ramanathan", CurrentCompany: "example capital"},
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

func TestExaReportsAnEmptyLookupWithItsChargeInsteadOfRetrying(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, queries := exaPartialServer(t, "exa_people_empty_charged.json", "exa_people_success.json")
	provider, err := personenrichment.NewExaProvider(exaNameCompanyConfig(server.URL+"/search"), "test-key", server.Client())
	require.NoError(err)
	_, err = provider.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "test q. user", CurrentCompany: "example labs"},
		Targets:  exaTypedTargets(t),
	})
	var noEntity *personenrichment.NoEntityError
	require.ErrorAs(err, &noEntity, "an empty lookup is reported to the worker, which owns the retry budget")
	assert.Equal(personenrichment.Cost{Currency: "USD", AmountMicros: 2000, Estimated: true}, noEntity.Cost,
		"the charge the empty call incurred travels with the report")
	var providerErr *personenrichment.ProviderError
	require.ErrorAs(err, &providerErr, "it still unwraps to the failure a caller without a retry would see")
	assert.Equal(personenrichment.FailureInvalidOutput, providerErr.Class)
	assert.Equal("request_test_people_empty_2", providerErr.RequestID)
	assert.Equal([]string{"name: test q. user; company: example labs"}, *queries,
		"the adapter never issues a second paid call on its own")

	freeServer, _ := exaPartialServer(t, "exa_people_empty.json")
	free, err := personenrichment.NewExaProvider(exaNameCompanyConfig(freeServer.URL+"/search"), "test-key", freeServer.Client())
	require.NoError(err)
	_, err = free.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "test q. user", CurrentCompany: "example labs"},
		Targets:  exaTypedTargets(t),
	})
	require.ErrorAs(err, &noEntity)
	assert.Zero(noEntity.Cost.AmountMicros, "an uncharged empty lookup reports a zero cost")
}

// TestExaCarriesTheChargeOfABilledResponseItCannotUse: an HTTP 200 with a
// reported cost that fails result validation (here, no identity match) was
// still billed. The failure the worker persists carries that charge.
func TestExaCarriesTheChargeOfABilledResponseItCannotUse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, _ := exaPartialServer(t, "exa_people_success.json")
	provider, err := personenrichment.NewExaProvider(exaNameCompanyConfig(server.URL+"/search"), "test-key", server.Client())
	require.NoError(err)
	_, err = provider.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "different person", CurrentCompany: "other corp"},
		Targets:  exaTypedTargets(t),
	})
	var providerErr *personenrichment.ProviderError
	require.ErrorAs(err, &providerErr)
	assert.Equal(personenrichment.FailureInvalidOutput, providerErr.Class)
	assert.Equal(personenrichment.Cost{Currency: "USD", AmountMicros: 7000, Estimated: true}, providerErr.Cost,
		"the billed response's charge travels with the validation failure")
}

// TestExaCarriesTheChargeWhenTheRequestIDIsInvalid: a decoded HTTP 200 whose
// requestId fails validation was still billed; the failure keeps the charge.
func TestExaCarriesTheChargeWhenTheRequestIDIsInvalid(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	body := strings.Replace(string(exaFixture(t, "exa_people_success.json")),
		`"requestId": "`, `"requestId": " `, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	provider, err := personenrichment.NewExaProvider(exaNameCompanyConfig(server.URL+"/search"), "test-key", server.Client())
	require.NoError(err)
	_, err = provider.Start(t.Context(), personenrichment.Request{
		Identity: personenrichment.Identity{Name: "test user", CurrentCompany: "example labs"},
		Targets:  exaTypedTargets(t),
	})
	var providerErr *personenrichment.ProviderError
	require.ErrorAs(err, &providerErr)
	assert.Equal(personenrichment.FailureInvalidOutput, providerErr.Class)
	assert.Empty(providerErr.RequestID, "an invalid request id is never reported")
	assert.Equal(personenrichment.Cost{Currency: "USD", AmountMicros: 7000, Estimated: true}, providerErr.Cost,
		"the billed response's charge travels with the failure")
}
