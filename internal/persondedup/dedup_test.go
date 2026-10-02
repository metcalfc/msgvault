package persondedup_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/persondedup"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/jevtest"
)

func participant(t *testing.T, st *store.Store, email, name string) int64 {
	t.Helper()
	id, err := st.EnsureParticipant(email, name, "")
	require.NoError(t, err)
	return id
}

// samePersonWhenBothSidesHaveNames says a pair is one person when both
// sides carry a display name, and probably not otherwise.
func samePersonWhenBothSidesHaveNames(questionID string, _ map[string]any, state map[string]any) map[string]any {
	pairs, _ := state["pairs"].(map[string]any)
	pair, _ := pairs["pair_"+questionID[strings.LastIndexByte(questionID, '_')+1:]].(map[string]any)
	first, _ := pair["first"].(map[string]any)
	second, _ := pair["second"].(map[string]any)
	firstNames, _ := first["names"].([]any)
	secondNames, _ := second["names"].([]any)
	if len(firstNames) > 0 && len(secondNames) > 0 {
		return jevtest.Noul(0.86)
	}
	return jevtest.Noul(0.12)
}

func enabled(cfg *jev.Config) { cfg.PersonDuplicates = jev.FeatureConfig{Enabled: true} }

func TestRunProposesLikelyDuplicatesForReviewOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "owner@example.com")
	require.NoError(err)
	participant(t, st, "owner@example.com", "Avery Owner")
	participant(t, st, "avery@example.org", "Avery Owner")
	require.NoError(st.AddAccountIdentityContext(t.Context(), source.ID, "owner@example.com", "manual"))
	jane := participant(t, st, "jane@example.com", "Jane Doe")
	janeWork := participant(t, st, "jdoe@example.org", "Doe, Jane")
	participant(t, st, "robin.example@example.com", "")
	participant(t, st, "robin.example@example.net", "")

	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())

	report, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal(persondedup.Report{Proposals: 2, Unnamed: 1, Requests: 1, Judged: 1, Candidates: 1}, report,
		"a pair with no name on one side has nothing to judge and is not sent")

	requests := server.Requests()
	require.Len(requests, 1)
	raw := fmt.Sprint(requests[0])
	assert.NotContains(raw, "owner@example.com", "the owner's identities are never sent")
	assert.NotContains(raw, "Avery Owner")
	assert.NotContains(raw, "robin")
	state, ok := requests[0]["state"].(map[string]any)
	require.True(ok)
	encodedState := fmt.Sprint(state)
	for _, leaked := range []string{"@", "jane@", "jdoe", "example.com", "example.org"} {
		assert.NotContains(encodedState, leaked, "no address, local part, or domain is sent")
	}
	pairs, ok := state["pairs"].(map[string]any)
	require.True(ok)
	require.Len(pairs, 1)
	first, ok := pairs["pair_1"].(map[string]any)
	require.True(ok)
	assert.Equal([]any{"same_display_name"}, first["signals"])
	assert.Equal(false, first["same_organization_domain"])
	left, ok := first["first"].(map[string]any)
	require.True(ok)
	assert.Equal(map[string]any{"names": []any{"Jane Doe"}, "address_kinds": []any{"organization"}}, left)
	right, ok := first["second"].(map[string]any)
	require.True(ok)
	assert.Equal(map[string]any{"names": []any{"Doe, Jane"}, "address_kinds": []any{"organization"}}, right)
	questions, ok := requests[0]["questions"].(map[string]any)
	require.True(ok)
	assert.Len(questions, 1, "only the questions for the sent pairs")

	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)
	assert.Equal(jane, candidates[0].LeftID)
	assert.Equal(janeWork, candidates[0].RightID)
	assert.Equal(store.IdentityMatchDisplayName, candidates[0].Basis)
	assert.Equal(store.IdentityMatchStateCandidate, candidates[0].State)
	require.NotNil(candidates[0].Confidence)
	assert.InDelta(0.86, *candidates[0].Confidence, 1e-9)

	again, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal(persondedup.Report{}, again, "judged and unnamed pairs are not taken up again")
	assert.Len(server.Requests(), 1)
}

func TestRunSendsAddressKindsAndASharedOrganizationDomainOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	work := participant(t, st, "quinn@mail.example.com", "Quinn Example")
	_, err := st.LinkParticipants(work, participant(t, st, "quinn.example@gmail.com", "Quinn Example"))
	require.NoError(err)
	participant(t, st, "qe@example.com", "Quinn Example")

	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())
	_, err = persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)

	requests := server.Requests()
	require.Len(requests, 1)
	state, _ := requests[0]["state"].(map[string]any)
	pairs, _ := state["pairs"].(map[string]any)
	pair, _ := pairs["pair_1"].(map[string]any)
	require.NotNil(pair)
	assert.Equal(true, pair["same_organization_domain"], "mail.example.com and example.com are one organization")
	left, _ := pair["first"].(map[string]any)
	assert.Equal([]any{"organization", "personal"}, left["address_kinds"])
	assert.NotContains(fmt.Sprint(state), "example", "the domain itself is never sent")
}

// sharePhone gives two participants the same number written two ways,
// through the identifiers importers record.
func sharePhone(t *testing.T, st *store.Store, left, right int64) {
	t.Helper()
	require.NoError(t, st.SetParticipantIdentifier(left, "phone", "+15555550100"))
	require.NoError(t, st.SetParticipantIdentifier(right, "imessage", "(555) 555-0100"))
}

func TestRunDecidesSharedMailboxPhoneAndProviderIDInCode(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	// Same mailbox (Gmail dots and googlemail.com), and the same name too:
	// the exact signal decides, so the name is never sent.
	mailboxA := participant(t, st, "jane.doe@gmail.com", "Jane Doe")
	mailboxB := participant(t, st, "janedoe@googlemail.com", "Jane Doe")
	phoneA := participant(t, st, "sam@example.com", "Sam Rivera")
	phoneB := participant(t, st, "srivera@example.net", "S. Rivera")
	sharePhone(t, st, phoneA, phoneB)
	providerA := participant(t, st, "kit@example.com", "")
	providerB := participant(t, st, "kit.work@example.org", "")
	for id, username := range map[int64]string{providerA: "@kit", providerB: "@kit_work"} {
		_, err := st.RecordContactObservationContext(t.Context(), id, store.ParticipantContactObservationInput{
			AddressKind: store.ContactAddressUsername, ServiceSlug: new("x"),
			ProviderUserID: new("x-1001"), OriginalValue: username,
			Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceArchiveObservation},
		})
		require.NoError(err)
	}

	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())

	report, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal(persondedup.Report{Proposals: 3, Matched: 3, Candidates: 3}, report)
	assert.Empty(server.Requests(), "pairs decided in code never reach Jev")

	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 3)
	type decided struct {
		left, right int64
		basis       store.IdentityMatchBasis
		value       string
		reason      string
	}
	got := []decided{}
	for _, candidate := range candidates {
		require.NotNil(candidate.NormalizedValue)
		require.NotNil(candidate.Confidence)
		assert.InDelta(1.0, *candidate.Confidence, 1e-9)
		assert.Equal(store.IdentityMatchStateCandidate, candidate.State, "never accepted automatically")
		require.NotEmpty(candidate.Evidence)
		last := candidate.Evidence[len(candidate.Evidence)-1].Detail
		require.NotNil(last)
		got = append(got, decided{
			candidate.LeftID, candidate.RightID, candidate.Basis, *candidate.NormalizedValue, *last,
		})
	}
	assert.ElementsMatch([]decided{
		{mailboxA, mailboxB, store.IdentityMatchEmail, "janedoe@gmail.com",
			"decided in code: same_mailbox, no Jev judgment"},
		{phoneA, phoneB, store.IdentityMatchPhone, "+15555550100",
			"decided in code: same_phone, no Jev judgment"},
		{providerA, providerB, store.IdentityMatchStableProviderID, "x|||x-1001",
			"decided in code: same_provider_id, no Jev judgment"},
	}, got)
}

func TestRunNeverOverridesARejectionWithAMatchDecidedInCode(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	left := participant(t, st, "alex@example.com", "Alex Example")
	right := participant(t, st, "alex.e@example.org", "Alex Example")
	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())

	_, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)
	_, err = st.DecideIdentityMatchCandidateContext(t.Context(), candidates[0].ID,
		store.IdentityMatchStateRejected, "user", nil)
	require.NoError(err)

	// The two now share a phone number, which on its own would be decided
	// in code. The user's "not the same person" still stands.
	sharePhone(t, st, left, right)
	report, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal(persondedup.Report{}, report)
	assert.Len(server.Requests(), 1, "nothing new is sent")
	after, err := st.ListIdentityMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(after, 1)
	assert.Equal(store.IdentityMatchStateRejected, after[0].State)
}

func TestRunBatchesTwentyPairsPerRequest(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	for i := range persondedup.PairsPerRequest + 1 {
		name := fmt.Sprintf("Person%c Example", 'A'+rune(i))
		participant(t, st, fmt.Sprintf("p%d@example.com", i), name)
		participant(t, st, fmt.Sprintf("p%d@example.org", i), name)
	}
	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())

	report, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal(t, 2, report.Requests)
	assert.Equal(t, persondedup.PairsPerRequest+1, report.Candidates)
	requests := server.Requests()
	require.Len(requests, 2)
	firstQuestions, _ := requests[0]["questions"].(map[string]any)
	assert.Len(t, firstQuestions, persondedup.PairsPerRequest)
}

func TestRunSendsNothingWithoutConsentOrAutomaticUse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	participant(t, st, "jane@example.com", "Jane Doe")
	participant(t, st, "jdoe@example.org", "Jane Doe")
	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)

	report, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal("consent_required", report.Skipped)

	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())
	report, err = persondedup.Run(t.Context(), st, persondedup.Options{Judge: service, Automatic: true})
	require.NoError(err)
	assert.Equal("manual_only", report.Skipped)
	assert.Empty(server.Requests())

	report, err = persondedup.Run(t.Context(), st, persondedup.Options{})
	require.NoError(err)
	assert.Equal(persondedup.Report{Proposals: 1}, report, "without Jev a name pair is only counted")
	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	assert.Empty(candidates)
}

func TestRunWithoutJevStillWritesMatchesDecidedInCode(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	participant(t, st, "jane@example.com", "Jane Doe")
	participant(t, st, "jdoe@example.org", "Jane Doe")
	left := participant(t, st, "sam@example.com", "Sam Rivera")
	right := participant(t, st, "srivera@example.net", "")
	sharePhone(t, st, left, right)

	report, err := persondedup.Run(t.Context(), st, persondedup.Options{})
	require.NoError(err)
	assert.Equal(persondedup.Report{Proposals: 2, Matched: 1, Candidates: 1}, report)
	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)
	assert.Equal(left, candidates[0].LeftID)
	assert.Equal(store.IdentityMatchPhone, candidates[0].Basis)
}
