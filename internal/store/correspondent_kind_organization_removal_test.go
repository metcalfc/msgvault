package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
)

func TestClearingRemovesTheOrganizationTheClassificationCreatedOnlyWhenUnused(t *testing.T) {
	cases := []struct {
		name        string
		shareWith   bool
		otherOrg    bool
		wantRemoved bool
	}{
		{name: "created and unused", wantRemoved: true},
		{name: "another cluster still grouped under it", shareWith: true},
		{name: "the cluster was grouped under a different organization", otherOrg: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			f := newContactMatchFixture(t)

			orders := f.emailParticipant("orders@shop.example.test", "Example Shop")
			marked, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
				ParticipantID: orders, Kind: correspondentkind.Organization,
			})
			require.NoError(err)
			require.True(marked.OrganizationCreated)
			organizationID := *marked.Record.OrganizationID
			if tc.shareWith {
				billing := f.emailParticipant("billing@shop.example.test", "")
				_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
					ParticipantID: billing, Kind: correspondentkind.Organization, OrganizationID: &organizationID,
				})
				require.NoError(err)
			}
			remove := organizationID
			if tc.otherOrg {
				other, err := f.st.CreateOrganizationContext(t.Context(), store.OrganizationInput{
					Name: "Other Example", Kind: store.OrganizationKindCompany,
				})
				require.NoError(err)
				remove = other.ID
			}

			cleared, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
				ParticipantID: orders, Kind: correspondentkind.Person, RemoveOrganizationID: &remove,
			})
			require.NoError(err)
			assert.Equal(tc.wantRemoved, cleared.OrganizationRemoved)
			_, err = f.st.GetOrganizationContext(t.Context(), organizationID)
			if tc.wantRemoved {
				assert.ErrorIs(err, store.ErrOrganizationNotFound)
			} else {
				assert.NoError(err)
			}
			if tc.otherOrg {
				_, err = f.st.GetOrganizationContext(t.Context(), remove)
				assert.NoError(err, "an organization the cluster was not grouped under is never removed")
			}
		})
	}
}

func TestClearingKeepsAnOrganizationWithItsOwnProfileData(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	orders := f.emailParticipant("orders@shop.example.test", "Example Shop")
	marked, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: orders, Kind: correspondentkind.Organization,
	})
	require.NoError(err)
	organizationID := *marked.Record.OrganizationID
	_, err = f.st.DB().ExecContext(t.Context(), f.st.Rebind(
		`INSERT INTO organization_categories (organization_id, original_value, normalized_value, source)
		 VALUES (?, 'Retail', 'retail', 'user')`), organizationID)
	require.NoError(err)

	cleared, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: orders, Kind: correspondentkind.Person, RemoveOrganizationID: &organizationID,
	})
	require.NoError(err)
	assert.False(cleared.OrganizationRemoved)
	_, err = f.st.GetOrganizationContext(t.Context(), organizationID)
	assert.NoError(err)
}

func TestClearingKeepsAnOrganizationWithASupersededUserContactPoint(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	orders := f.emailParticipant("orders@shop.example.test", "Example Shop")
	marked, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: orders, Kind: correspondentkind.Organization,
	})
	require.NoError(err)
	organizationID := *marked.Record.OrganizationID
	// A contact point the user added and later replaced is still the
	// organization's own history, unlike the ones the classification added.
	_, err = f.st.DB().ExecContext(t.Context(), f.st.Rebind(`
		INSERT INTO organization_contact_points (
			organization_id, address_kind, original_value, normalized_value, source,
			active_until, superseded_at
		) VALUES (?, 'email', 'sales@shop.example.test', 'sales@shop.example.test', 'user',
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`), organizationID)
	require.NoError(err)

	cleared, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: orders, Kind: correspondentkind.Person, RemoveOrganizationID: &organizationID,
	})
	require.NoError(err)
	assert.False(cleared.OrganizationRemoved)
	_, err = f.st.GetOrganizationContext(t.Context(), organizationID)
	assert.NoError(err)
}

func TestRemovingAnOrganizationRequiresClearingTheKind(t *testing.T) {
	f := newContactMatchFixture(t)
	orders := f.emailParticipant("orders@shop.example.test", "Example Shop")
	id := int64(1)
	_, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: orders, Kind: correspondentkind.Ignored, RemoveOrganizationID: &id,
	})
	assert.ErrorIs(t, err, store.ErrCorrespondentKindInvalid)
}
