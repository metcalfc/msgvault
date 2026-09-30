package orgresolution_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/orgresolution"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
)

func TestPreparerNeverAsksWhenAGateIsClosedAndCreatesAsBefore(t *testing.T) {
	tests := []struct {
		name     string
		close    func(*testing.T, *fixture)
		category string
	}{
		{"consent revoked", func(t *testing.T, f *fixture) {
			t.Helper()
			_, err := f.store.RevokeJevFeatureConsent(t.Context(), jev.FeatureOrganizationResolution, "test")
			require.NoError(t, err)
		}, "consent_required"},
		{"jev disabled", func(_ *testing.T, f *fixture) { f.config.Enabled = false }, "disabled"},
		{"feature disabled", func(_ *testing.T, f *fixture) {
			f.config.OrganizationResolution.Enabled = false
		}, "feature_disabled"},
		{"policy changed since consent", func(_ *testing.T, f *fixture) {
			f.config.Model = "jev-9.9.9"
		}, "consent_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fake := newFakeJev(t, map[string]float64{"candidate_1": 0.99}, 0.99)
			f := newFixture(t, fake)
			labs := f.organization(t, "Example Labs", "")
			test.close(t, f)
			claims := []personfacts.ProposedClaim{f.claim(`{"name":"Example Labs, Inc."}`, "Engineer", "gate")}

			results, err := f.preparer().Prepare(t.Context(), f.personID, claims, nil)
			require.NoError(err)
			require.Len(results, 1)
			assert.Equal(orgresolution.OutcomeSkipped, results[0].Outcome)
			assert.Equal(test.category, results[0].Skipped)
			assert.Empty(fake.requests(), "nothing leaves the machine")

			f.apply(t, "gate", claims...)
			employments := f.currentEmployments(t)
			require.Len(employments, 1)
			assert.NotEqual(labs.ID, employments[0].OrganizationID, "the exact lookup alone creates as today")
		})
	}
}

func TestPreparerWithoutAKeyNeverAsks(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.99}, 0)
	f := newFixture(t, fake)
	f.organization(t, "Example Labs", "")
	keyless, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return f.config, nil },
		Consents:   f.store,
		Ledger:     f.store,
		Credential: func(string, string) (string, bool, error) { return "", false, nil },
	})
	require.NoError(err)

	results, err := orgresolution.NewPreparer(keyless, f.store, false, nil).Prepare(t.Context(), f.personID,
		[]personfacts.ProposedClaim{f.claim(`{"name":"Example Labs, Inc."}`, "Engineer", "keyless")}, nil)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal("credential_missing", results[0].Skipped)
	assert.Empty(fake.requests())
}

func TestPreparerScheduledRunsNeedAutomaticUse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.99}, 0)
	f := newFixture(t, fake)
	f.organization(t, "Example Labs", "")
	claims := []personfacts.ProposedClaim{f.claim(`{"name":"Example Labs, Inc."}`, "Engineer", "scheduled")}
	scheduled := orgresolution.NewPreparer(f.service, f.store, true, nil)

	results, err := scheduled.Prepare(t.Context(), f.personID, claims, nil)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal("manual_only", results[0].Skipped)
	assert.Empty(fake.requests())

	f.config.OrganizationResolution.Automatic = true
	results, err = scheduled.Prepare(t.Context(), f.personID, claims, nil)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal(orgresolution.OutcomeAlias, results[0].Outcome)
	assert.Len(fake.requests(), 1)
}

func TestPreparerStopsAtTheDailyRequestLimit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.2}, 0)
	f := newFixture(t, fake)
	f.config.MaxRequestsPerDay = 1
	f.organization(t, "Example Labs", "")
	f.organization(t, "Northwind Traders", "")
	claims := []personfacts.ProposedClaim{
		f.claim(`{"name":"Example Labs Europe"}`, "Engineer", "first"),
		f.claim(`{"name":"Northwind Trading"}`, "Engineer", "second"),
	}

	results, err := f.preparer().Prepare(t.Context(), f.personID, claims, nil)
	require.NoError(err)
	require.Len(results, 2)
	assert.True(results[0].Asked)
	assert.Equal("request_limit", results[1].Skipped)
	assert.Len(fake.requests(), 1, "the day's budget is spent after one request")
	counters, err := f.store.JevDayCounters(t.Context(), jev.FeatureOrganizationResolution, jev.UTCDay(fixtureNow))
	require.NoError(err)
	assert.Equal(int64(1), counters.Requests)
}

func TestReplayWithAStoredAliasResolvesWithoutAJudgment(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.9}, 0)
	f := newFixture(t, fake)
	labs := f.organization(t, "Example Labs", "")
	claims := []personfacts.ProposedClaim{f.claim(`{"name":"Example Labs, Inc."}`, "Engineer", "replay")}
	_, err := f.preparer().Prepare(t.Context(), f.personID, claims, nil)
	require.NoError(err)
	require.Len(fake.requests(), 1)

	// Revoke consent and turn the feature off: replay must not need either.
	_, err = f.store.RevokeJevFeatureConsent(t.Context(), jev.FeatureOrganizationResolution, "test")
	require.NoError(err)
	f.config.Enabled = false
	first := f.apply(t, "replay", claims...)
	replayed := f.apply(t, "replay", claims...)
	assert.Equal(first.Decisions, replayed.Decisions, "the same generation replays identically")
	later := f.apply(t, "later", f.claim(`{"name":"Example Labs, Inc."}`, "Engineer", "later"))
	assert.NotEmpty(later.Decisions)

	employments := f.currentEmployments(t)
	require.Len(employments, 1)
	assert.Equal(labs.ID, employments[0].OrganizationID)
	assert.Len(fake.requests(), 1, "stored aliases, not live calls, feed resolution")
}

func TestPreparerWritesNothingOnceTheLeaseIsLost(t *testing.T) {
	tests := []struct {
		name   string
		orgRef float64
	}{
		{"confident alias", 0.95},
		{"review", 0.7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fake := newFakeJev(t, map[string]float64{"candidate_1": test.orgRef}, 0.95)
			f := newFixture(t, fake)
			labs := f.organization(t, "Example Labs", "")
			// No sweep lease row holds this fence: the lease is gone.
			lost := &personfacts.WriteFence{
				Kind: personfacts.FencePersonSweep, PersonID: f.personID, Owner: "sweep-worker", Fence: 1,
			}

			_, err := f.preparer().Prepare(t.Context(), f.personID,
				[]personfacts.ProposedClaim{f.claim(`{"name":"Example Labs, Inc."}`, "Engineer", "lost")}, lost)
			require.ErrorIs(err, store.ErrOrganizationWriteFenced)
			assert.Len(fake.requests(), 1, "asking is not a write")

			profile, err := f.store.GetOrganizationProfileContext(t.Context(), labs.ID, false)
			require.NoError(err)
			assert.Empty(profile.Names, "no alias for a lost lease")
			reviews, err := f.store.ListOrganizationMatchReviewsContext(t.Context(), 10)
			require.NoError(err)
			assert.Empty(reviews, "no review for a lost lease")
		})
	}
}
