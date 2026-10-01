package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personfacts"
)

func TestPersonFactEmploymentCrossedOrganizationChainsSerialize(t *testing.T) {
	var st *Store

	{
		st, _, _ = newPersonFactProjectionStore(t)
	}
	requirements := require.New(t)
	assertions := assert.New(t)

	firstPersonID := createTrackedPersonFactPerson(t, st, "crossed-org-first")
	secondPersonID := createTrackedPersonFactPerson(t, st, "crossed-org-second")
	target := projectionTargetBySlug(t, st, "employment")
	firstSource := createPersonFactOrganization(t, st, "First Source", "first-source.example")
	firstRoot := createPersonFactOrganization(t, st, "First Root", "first-root.example")
	secondSource := createPersonFactOrganization(t, st, "Second Source", "second-source.example")
	secondRoot := createPersonFactOrganization(t, st, "Second Root", "second-root.example")
	secondAlternate := createPersonFactOrganization(
		t, st, "Second Alternate", "second-alternate.example")
	firstAlternate := createPersonFactOrganization(
		t, st, "First Alternate", "first-alternate.example")
	mergedFirst, err := st.MergeOrganizationsContext(
		t.Context(), firstRoot.ID, firstRoot.Revision, firstSource.ID, firstSource.Revision)
	requirements.NoError(err)
	_, err = st.MergeOrganizationsContext(
		t.Context(), mergedFirst.ID, mergedFirst.Revision, firstAlternate.ID, firstAlternate.Revision)
	requirements.NoError(err)
	mergedSecond, err := st.MergeOrganizationsContext(
		t.Context(), secondRoot.ID, secondRoot.Revision, secondSource.ID, secondSource.Revision)
	requirements.NoError(err)
	_, err = st.MergeOrganizationsContext(
		t.Context(), mergedSecond.ID, mergedSecond.Revision,
		secondAlternate.ID, secondAlternate.Revision)
	requirements.NoError(err)

	firstInput := crossedPersonFactOrganizationGeneration(
		t, firstPersonID, target, firstSource, secondSource, firstSource.ID, "first")
	secondInput := crossedPersonFactOrganizationGeneration(
		t, secondPersonID, target, firstAlternate, secondAlternate,
		secondAlternate.ID, "second")
	{
		first, firstErr := st.ApplyPersonFactGenerationContext(t.Context(), firstInput, nil)
		second, secondErr := st.ApplyPersonFactGenerationContext(t.Context(), secondInput, nil)
		requirements.NoError(firstErr)
		requirements.NoError(secondErr)
		requirements.Len(first.Projections, 2)
		requirements.Len(second.Projections, 2)
	}

	assertions.Equal(int64(2), personFactProjectionRowCount(t, st, "person_fact_generations"))
	assertions.Equal(int64(4), personFactProjectionRowCount(t, st, "employments"))

	firstEvidence, err := st.ListPersonFactEvidenceContext(t.Context(), firstPersonID,
		personfacts.EvidenceFilter{Limit: 10})
	requirements.NoError(err)
	requirements.Len(firstEvidence, 2)
	secondEvidence, err := st.ListPersonFactEvidenceContext(t.Context(), secondPersonID,
		personfacts.EvidenceFilter{Limit: 10})
	requirements.NoError(err)
	requirements.Len(secondEvidence, 2)
	firstReplay := personFactProjectionInput(firstPersonID, "crossed-first-replay", nil,
		[]personfacts.EvidenceStatusChange{{
			EvidenceKey: firstEvidence[0].Key, SourceVersion: firstEvidence[0].Input.SourceVersion,
			Supported: false, Reason: personfacts.EvidenceStatusSourceDeleted,
		}})
	secondReplay := personFactProjectionInput(secondPersonID, "crossed-second-replay", nil,
		[]personfacts.EvidenceStatusChange{{
			EvidenceKey: secondEvidence[0].Key, SourceVersion: secondEvidence[0].Input.SourceVersion,
			Supported: false, Reason: personfacts.EvidenceStatusSourceDeleted,
		}})
	{
		_, firstErr := st.ApplyPersonFactGenerationContext(t.Context(), firstReplay, nil)
		_, secondErr := st.ApplyPersonFactGenerationContext(t.Context(), secondReplay, nil)
		requirements.NoError(firstErr)
		requirements.NoError(secondErr)
	}

	assertions.Equal(int64(4), personFactProjectionRowCount(t, st, "person_fact_generations"))
}

func TestPersonFactEmploymentIncomingAndHistoricalOrganizationsShareLockOrder(t *testing.T) {
	var st *Store

	{
		st, _, _ = newPersonFactProjectionStore(t)
	}
	requirements := require.New(t)
	assertions := assert.New(t)

	firstPersonID := createTrackedPersonFactPerson(t, st, "mixed-org-first")
	secondPersonID := createTrackedPersonFactPerson(t, st, "mixed-org-second")
	target := projectionTargetBySlug(t, st, "employment")
	firstOrganization := createPersonFactOrganization(
		t, st, "Mixed First Organization", "mixed-first.example")
	secondOrganization := createPersonFactOrganization(
		t, st, "Mixed Second Organization", "mixed-second.example")
	claim := func(personID int64, organization *Organization, title, suffix string) personfacts.ProposedClaim {
		return personFactProjectionClaim(personID, target, fmt.Sprintf(
			`{"organization":{"id":%d,"name":%q,"domain":%q},"title":%q}`,
			organization.ID, organization.Name, *organization.PrimaryDomain, title), suffix)
	}
	_, err := st.ApplyPersonFactGenerationContext(t.Context(),
		personFactProjectionInput(firstPersonID, "mixed-first-history", []personfacts.ProposedClaim{
			claim(firstPersonID, secondOrganization, "Historical Engineer", "mixed-first-history"),
		}, nil), nil)
	requirements.NoError(err)
	_, err = st.ApplyPersonFactGenerationContext(t.Context(),
		personFactProjectionInput(secondPersonID, "mixed-second-history", []personfacts.ProposedClaim{
			claim(secondPersonID, firstOrganization, "Historical Engineer", "mixed-second-history"),
		}, nil), nil)
	requirements.NoError(err)

	firstIncoming := personFactProjectionInput(
		firstPersonID, "mixed-first-incoming", []personfacts.ProposedClaim{
			claim(firstPersonID, firstOrganization, "Incoming Engineer", "mixed-first-incoming"),
		}, nil)
	secondIncoming := personFactProjectionInput(
		secondPersonID, "mixed-second-incoming", []personfacts.ProposedClaim{
			claim(secondPersonID, secondOrganization, "Incoming Engineer", "mixed-second-incoming"),
		}, nil)

	{
		_, firstErr := st.ApplyPersonFactGenerationContext(t.Context(), firstIncoming, nil)
		_, secondErr := st.ApplyPersonFactGenerationContext(t.Context(), secondIncoming, nil)
		requirements.NoError(firstErr)
		requirements.NoError(secondErr)
	}
	assertions.Equal(int64(4), personFactProjectionRowCount(t, st, "person_fact_generations"))
	assertions.Equal(int64(4), personFactProjectionRowCount(t, st, "employments"))
}

func crossedPersonFactOrganizationGeneration(
	t *testing.T, personID int64, target personfacts.TargetDescriptor,
	firstSource, secondSource *Organization, wantedFirstID int64, suffix string,
) personfacts.GenerationInput {
	t.Helper()
	requirements := require.New(t)
	for attempt := range 100 {
		claims := []personfacts.ProposedClaim{
			personFactProjectionClaim(personID, target, fmt.Sprintf(
				`{"organization":{"id":%d,"name":%q,"domain":%q},"title":%q}`,
				firstSource.ID, firstSource.Name, *firstSource.PrimaryDomain,
				fmt.Sprintf("Engineer %s A %d", suffix, attempt)),
				fmt.Sprintf("crossed-%s-a-%d", suffix, attempt)),
			personFactProjectionClaim(personID, target, fmt.Sprintf(
				`{"organization":{"id":%d,"name":%q,"domain":%q},"title":%q}`,
				secondSource.ID, secondSource.Name, *secondSource.PrimaryDomain,
				fmt.Sprintf("Engineer %s B %d", suffix, attempt)),
				fmt.Sprintf("crossed-%s-b-%d", suffix, attempt)),
		}
		input := personFactProjectionInput(personID,
			fmt.Sprintf("crossed-%s-%d", suffix, attempt), claims, nil)
		prepared, err := personfacts.PreparePersonFactGeneration(t.Context(), input, nil)
		requirements.NoError(err)
		preparedClaims := prepared.Claims()
		requirements.Len(preparedClaims, 2)
		var value personfacts.EmploymentValue
		requirements.NoError(json.Unmarshal(preparedClaims[0].Normalized.JSON, &value))
		requirements.NotNil(value.Organization.ID)
		claimKeys := make([]string, len(preparedClaims))
		for index := range preparedClaims {
			claimKeys[index], err = personfacts.ClaimKey(prepared.GenerationKey(), preparedClaims[index])
			requirements.NoError(err)
		}
		storedFirst := 0
		if claimKeys[1] < claimKeys[0] {
			storedFirst = 1
		}
		var storedValue personfacts.EmploymentValue
		requirements.NoError(json.Unmarshal(
			preparedClaims[storedFirst].Normalized.JSON, &storedValue))
		requirements.NotNil(storedValue.Organization.ID)
		if *value.Organization.ID == wantedFirstID &&
			*storedValue.Organization.ID == wantedFirstID {
			return input
		}
	}
	requirements.FailNow("could not construct crossed canonical claim order")
	return personfacts.GenerationInput{}
}

func createTrackedPersonFactPerson(t *testing.T, st *Store, suffix string) int64 {
	t.Helper()
	participantID, err := st.EnsureParticipant(
		suffix+"@example.invalid", "Crossed Organization Person", "example.invalid")
	require.NoError(t, err)
	person, _, err := st.CreatePersonFromParticipant(participantID)
	require.NoError(t, err)
	_, err = st.SetPersonTrackingContext(t.Context(), person.ID, true)
	require.NoError(t, err)
	return person.ID
}
