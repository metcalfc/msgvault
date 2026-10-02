package profilejudge_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/profilejudge"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/jevtest"
)

// fieldDefinition defines a single-value text person attribute shown with
// the given widget.
func fieldDefinition(t *testing.T, st *store.Store, slug string, field store.AttributeFieldType) string {
	t.Helper()
	_, err := st.CreateAttributeDefinitionContext(t.Context(), store.AttributeDefinitionInput{
		UniversalID: "test-" + slug, ObjectType: store.AttributeObjectPerson, Slug: slug,
		Label: "Test " + slug, ValueType: store.AttributeValueText, FieldType: field,
		Cardinality: store.AttributeCardinalitySingle, Ownership: store.AttributeOwnershipUser,
		UICreatable: true, UIEditable: true, APIMutable: true, IsAudited: true, IsDeletable: true,
	})
	require.NoError(t, err)
	return slug
}

// candidate returns the merge's one review candidate as stored now.
func candidate(t *testing.T, st *store.Store, merged *store.PersonMergeResult) store.PersonMergeReviewCandidate {
	t.Helper()
	detail, err := st.GetPersonMergeContext(t.Context(), merged.Merge.ID)
	require.NoError(t, err)
	require.Len(t, detail.ReviewCandidates, 1)
	return detail.ReviewCandidates[0]
}

// currentValue is the person's current value of slug and its source.
func currentValue(t *testing.T, st *store.Store, personID int64, slug string) (string, store.Provenance) {
	t.Helper()
	values, err := st.ListPersonAttributeValuesContext(t.Context(), personID,
		store.PersonAttributeQuery{DefinitionSlug: slug})
	require.NoError(t, err)
	require.Len(t, values, 1)
	text, err := values[0].Value.CanonicalString()
	require.NoError(t, err)
	return text, values[0].Source
}

// alwaysSame would settle any conflict it saw, so a settled conflict
// proves nothing unless the server also received no request.
func alwaysSame(string, map[string]any, map[string]any) map[string]any { return jevtest.Noul(0.99) }

func intValue(value int64) store.AttributeValue {
	return store.AttributeValue{Type: store.AttributeValueInteger, Integer: &value}
}

func TestRunSettlesValuesEqualAfterTheirFieldsNormalizationWithoutAsking(t *testing.T) {
	st := testutil.NewTestStore(t)
	email := fieldDefinition(t, st, "work_email", store.AttributeFieldEmail)
	phone := fieldDefinition(t, st, "desk_phone", store.AttributeFieldPhone)
	url := fieldDefinition(t, st, "homepage", store.AttributeFieldURL)
	notes := fieldDefinition(t, st, "bio", store.AttributeFieldTextarea)
	cases := []struct {
		name, slug, survivor, absorbed string
	}{
		{"free text folds case and punctuation", store.AttributeSlugLocation, "San Francisco, CA", "san francisco  ca."},
		{"free text folds full-width forms", store.AttributeSlugLocation, "Ｌｉｓｂｏｎ", "Lisbon"},
		{"free text treats dashes between words as space", notes, "Long-time cyclist", "long time cyclist"},
		{"free text treats a hyphenated name as two words", store.AttributeSlugLocation,
			"Smith-Jones Farm", "Smith Jones Farm"},
		{"free text drops an abbreviation's period", store.AttributeSlugLocation, "St. Louis", "St Louis"},
		{"email compares as a lower-cased address", email, "Robin.Example@Example.COM", "robin.example@example.com"},
		{"phone compares as E.164", phone, "(415) 555-0100", "+1 415 555 0100"},
		{"url compares as a canonical public URL", url,
			"HTTPS://Example.com:443/in/robin/#about", "https://example.com/in/robin?utm_source=newsletter"},
	}
	merges := make([]*store.PersonMergeResult, len(cases))
	for i, tc := range cases {
		merges[i] = mergeWithValues(t, st, tc.slug, fmt.Sprintf("equal%d", i), tc.survivor, tc.absorbed)
	}

	server := jevtest.NewServer(t, alwaysSame)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, profilejudge.Feature())
	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(t, err)
	assert.Equal(t, profilejudge.Report{SettledInCode: len(cases)}, report)
	assert.Empty(t, server.Requests(), "equal values never leave the machine")

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settled := candidate(t, st, merges[i])
			assert.Equal(t, "rejected", settled.State, "the survivor's value is kept")
			require.NotNil(t, settled.ReviewedBy)
			assert.Equal(t, store.PersonMergeConflictNormalizedActor, *settled.ReviewedBy)
			value, _ := currentValue(t, st, merges[i].Person.ID, tc.slug)
			assert.Equal(t, tc.survivor, value)
		})
	}
}

func TestRunKeepsMeaningfulDifferencesForJevOrTheUser(t *testing.T) {
	st := testutil.NewTestStore(t)
	email := fieldDefinition(t, st, "work_email", store.AttributeFieldEmail)
	url := fieldDefinition(t, st, "homepage", store.AttributeFieldURL)

	// Sent: free text and URLs that still differ after normalization.
	distinct := [][2]string{
		{"Floor 50%", "Floor 50"}, {"José Street", "Jose Street"}, {"Level -5", "Level 5"},
		{"Unit 1.000", "Unit 1,000"}, {"Unit 1.5", "Unit 1,5"}, {"Gate 3.14", "Gate 3:14"},
		{"Box 2⁵", "Box 25"}, {"Lot (5)", "Lot 5"}, {"Weiß Street", "Weiss Street"},
	}
	kept := []*store.PersonMergeResult{}
	for i, pair := range distinct {
		kept = append(kept, mergeWithValues(t, st, store.AttributeSlugLocation, fmt.Sprintf("distinct%d", i),
			pair[0], pair[1]))
	}
	kept = append(kept,
		mergeWithValues(t, st, url, "path", "https://example.com/in/Robin", "https://example.com/in/robin"),
		// Never sent: typed values, select options, and email addresses.
		mergeTyped(t, st, store.AttributeSlugContactFrequency, "frequency", intValue(30), intValue(31),
			store.ProvenanceExtraction, store.ProvenanceExtraction),
		mergeWithValues(t, st, store.AttributeSlugPrimaryChannel, "channel", "email", "sms"),
		mergeWithValues(t, st, email, "address", "robin@example.com", "robin@example.org"),
	)

	server := jevtest.NewServer(t, func(string, map[string]any, map[string]any) map[string]any {
		return jevtest.Noul(0.5)
	})
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, profilejudge.Feature())
	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(t, err)
	assert.Equal(t, profilejudge.Report{Requests: 2, MergeConflicts: 10}, report)

	sent := []string{}
	for _, request := range server.Requests() {
		state, ok := request["state"].(map[string]any)
		require.True(t, ok)
		conflicts, ok := state["conflicts"].(map[string]any)
		require.True(t, ok)
		for _, conflict := range conflicts {
			pair, ok := conflict.(map[string]any)
			require.True(t, ok)
			sent = append(sent, fmt.Sprint(pair["first"], " | ", pair["second"]))
		}
	}
	want := []string{"https://example.com/in/Robin | https://example.com/in/robin"}
	for _, pair := range distinct {
		want = append(want, pair[0]+" | "+pair[1])
	}
	assert.ElementsMatch(t, want, sent)

	for _, merged := range kept {
		assert.Equal(t, "pending", candidate(t, st, merged).State, "a real difference stays with the user")
	}
	remaining, err := st.MergeConflictCandidatesContext(t.Context(), 0)
	require.NoError(t, err)
	assert.Empty(t, remaining, "every conflict is recorded so it is not listed again")
}

func TestRunSettlesEqualValuesInFavorOfTheUsersOwn(t *testing.T) {
	st := testutil.NewTestStore(t)
	location := store.AttributeSlugLocation
	userAbsorbed := mergeTyped(t, st, location, "user-absorbed",
		textValue("san francisco"), textValue("San Francisco"), store.ProvenanceExtraction, store.ProvenanceUser)
	userSurvivor := mergeTyped(t, st, location, "user-survivor",
		textValue("San Francisco"), textValue("san francisco"), store.ProvenanceUser, store.ProvenanceExtraction)
	bothUser := mergeTyped(t, st, location, "both-user",
		textValue("San Francisco"), textValue("San Francisco."), store.ProvenanceUser, store.ProvenanceVCardImport)
	unequalUser := mergeTyped(t, st, location, "unequal-user",
		textValue("Lisbon"), textValue("Porto"), store.ProvenanceExtraction, store.ProvenanceUser)

	server := jevtest.NewServer(t, alwaysSame)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, profilejudge.Feature())
	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(t, err)
	assert.Equal(t, profilejudge.Report{SettledInCode: 3}, report)
	assert.Empty(t, server.Requests(), "a user-declared absorbed value is never sent")

	assert.Equal(t, "accepted", candidate(t, st, userAbsorbed).State)
	value, source := currentValue(t, st, userAbsorbed.Person.ID, location)
	assert.Equal(t, "San Francisco", value, "only the absorbed value is yours, so it replaces the survivor's")
	assert.Equal(t, store.ProvenanceUser, source)

	for _, merged := range []*store.PersonMergeResult{userSurvivor, bothUser} {
		assert.Equal(t, "rejected", candidate(t, st, merged).State)
		value, source = currentValue(t, st, merged.Person.ID, location)
		assert.Equal(t, "San Francisco", value, "your survivor value is never replaced")
		assert.Equal(t, store.ProvenanceUser, source)
	}
	assert.Equal(t, "pending", candidate(t, st, unequalUser).State, "different values stay with you")
}

func TestRunWithJevOffSettlesOnlyWhatNormalizationDecides(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	equal := mergeWithLocations(t, st, "equal", "Berlin, Germany", "berlin germany")
	residual := mergeWithLocations(t, st, "residual", "Lisbon", "Porto")
	robin := promoted(t, st, "robin@example.com", "Robin Example")
	board := employment(t, st, robin.ID, "Example Foundation", "Board Member")
	employment(t, st, robin.ID, "Example Labs", "CEO")

	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{})
	require.NoError(err)
	assert.Equal(profilejudge.Report{SettledInCode: 1}, report)
	assert.Equal("rejected", candidate(t, st, equal).State)
	assert.Equal("pending", candidate(t, st, residual).State)
	assert.Equal(board, primaryID(t, st, robin.ID), "the rule's primary role stays")

	remaining, err := st.MergeConflictCandidatesContext(t.Context(), 0)
	require.NoError(err)
	require.Len(remaining, 1, "the residual conflict waits for Jev to be turned on")
	roles, err := st.PrimaryRoleCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Len(roles, 1, "the role question waits for Jev to be turned on")

	server := jevtest.NewServer(t, answerByContent)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, profilejudge.Feature())
	report, err = profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(err)
	assert.Equal(profilejudge.Report{
		Requests: 2, PrimaryRoles: 1, PrimaryRolesSet: 1, MergeConflicts: 1,
	}, report)
}

func TestRunAsksNoChoiceBetweenOptionsEqualAfterNormalization(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)

	robin := promoted(t, st, "robin@example.com", "Robin Example")
	first := employment(t, st, robin.ID, "Example Labs, Inc.", "CEO")
	employment(t, st, robin.ID, "Example Labs Inc", "ceo")

	link := func(prefix string, names ...string) int64 {
		ids := make([]int64, len(names))
		for i, name := range names {
			id, err := st.EnsureParticipant(fmt.Sprintf("%s-%d@example.com", prefix, i), name, "example.com")
			require.NoError(err)
			ids[i] = id
			if i > 0 {
				_, err = st.LinkParticipants(ids[0], id)
				require.NoError(err)
			}
		}
		return ids[0]
	}
	sameID := link("jane", "Jane Doe", "Jane Doe.")
	same, _, err := st.CreatePersonFromParticipantContext(t.Context(), sameID)
	require.NoError(err)
	mixedID := link("sam", "Sam Roe", "sam roe.", "sroe")
	mixed, _, err := st.CreatePersonFromParticipantContext(t.Context(), mixedID)
	require.NoError(err)

	server := jevtest.NewServer(t, answerByContent)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, profilejudge.Feature())
	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(err)
	assert.Equal(profilejudge.Report{Requests: 1, DisplayNames: 1, SettledInCode: 2}, report)
	assert.Equal(first, primaryID(t, st, robin.ID), "the rule's primary role stays")

	requests := server.Requests()
	require.Len(requests, 1, "only the person with two different names is asked")
	assert.Equal(map[string]any{"names": map[string]any{"name_1": "Sam Roe", "name_2": "sroe"}},
		requests[0]["state"], "equal names are sent once, in the rule's spelling")

	for _, person := range []*store.Person{same, mixed} {
		current, err := st.GetPersonContext(t.Context(), person.ID)
		require.NoError(err)
		assert.Equal(person.DisplayName, current.DisplayName, "the rule's name stays")
	}

	again, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(err)
	assert.Equal(profilejudge.Report{}, again, "settled choices are not revisited")
}

func TestRunSettlesEqualItemsBeyondTheLimitAndAfterAJevFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	// Items that need Jev come first in every listing.
	waiting := []*store.PersonMergeResult{}
	for i, pair := range [][2]string{{"Lisbon", "Porto"}, {"Oslo", "Bergen"}, {"Rome", "Milan"}} {
		waiting = append(waiting, mergeWithLocations(t, st, fmt.Sprintf("waiting%d", i), pair[0], pair[1]))
	}
	for i := range 3 {
		person := promoted(t, st, fmt.Sprintf("role%d@example.com", i), fmt.Sprintf("Role Person %d", i))
		employment(t, st, person.ID, fmt.Sprintf("Example Foundation %d", i), "Board Member")
		employment(t, st, person.ID, fmt.Sprintf("Example Labs %d", i), "CEO")
	}
	equal := mergeWithLocations(t, st, "equal", "Berlin, Germany", "berlin germany")
	robin := promoted(t, st, "robin@example.com", "Robin Example")
	first := employment(t, st, robin.ID, "Example Labs, Inc.", "CEO")
	employment(t, st, robin.ID, "Example Labs Inc", "CEO")

	// Jev fails on the first request: consent was never given.
	server := jevtest.NewServer(t, alwaysSame)
	service, _ := server.Service(t, st, enabled)
	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service, Limit: 2})
	require.NoError(err)
	assert.Equal("consent_required", report.Skipped)
	assert.Equal(2, report.SettledInCode, "a limit of 2 and a failed request still settle the equal items")
	assert.Equal("rejected", candidate(t, st, equal).State)
	assert.Equal(first, primaryID(t, st, robin.ID))
	for _, merged := range waiting {
		assert.Equal("pending", candidate(t, st, merged).State)
	}
	assert.Empty(server.Requests())
}

func TestRunSettlesAConflictRecordedEarlierAsNeverSent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	email := fieldDefinition(t, st, "work_email", store.AttributeFieldEmail)
	merged := mergeWithValues(t, st, email, "unsent", "Bob@example.com", "bob@example.com")
	// An earlier version recorded every conflict with an address as never
	// sent, without comparing the values.
	_, err := st.ApplyMergeConflictJudgmentContext(t.Context(), store.MergeConflictJudgment{
		CandidateID: merged.ReviewCandidates[0].ID, PersonID: merged.Person.ID,
		Model: store.MergeConflictNotSentModel,
	})
	require.NoError(err)

	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{})
	require.NoError(err)
	assert.Equal(profilejudge.Report{SettledInCode: 1}, report)
	assert.Equal("rejected", candidate(t, st, merged).State)
	unsent, err := st.UnsentMergeConflictCandidatesContext(t.Context())
	require.NoError(err)
	assert.Empty(unsent, "the settlement replaces the never-sent record")
}

func TestRunPrefersAMixedCaseSpellingOfTheSameName(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	person := func(prefix string, names ...string) *store.Person {
		ids := make([]int64, len(names))
		for i, name := range names {
			id, err := st.EnsureParticipant(fmt.Sprintf("%s-%d@example.com", prefix, i), name, "example.com")
			require.NoError(err)
			ids[i] = id
			if i > 0 {
				_, err = st.LinkParticipants(ids[0], id)
				require.NoError(err)
			}
		}
		created, _, err := st.CreatePersonFromParticipantContext(t.Context(), ids[0])
		require.NoError(err)
		return created
	}
	shouting := person("john", "JOHN SMITH", "John Smith")
	lower := person("ann", "ann lee", "ANN LEE")

	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{})
	require.NoError(err)
	assert.Equal(profilejudge.Report{SettledInCode: 2}, report)
	for want, original := range map[string]*store.Person{"John Smith": shouting, "ann lee": lower} {
		current, err := st.GetPersonContext(t.Context(), original.ID)
		require.NoError(err)
		require.NotNil(current.DisplayName)
		assert.Equal(want, *current.DisplayName, "a mixed-case spelling wins; otherwise the rule's name stays")
	}
}
