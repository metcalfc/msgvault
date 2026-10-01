package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestOrganizationProfileUsesStoredCanonicalAddressAndMediaValuesAsKeys(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{
		Name: "Example Org",
	})
	require.NoError(err)

	street := " 1 Example St "
	locality := " Exampletown "
	uri := " https://example.test/logo.png "
	input := store.OrganizationProfileInput{
		Addresses: []store.OrganizationAddressInput{{
			AddressKind:   store.PersonAddressPostal,
			StreetAddress: &street,
			Locality:      &locality,
			Envelope:      store.ValueEnvelopeInput{Source: store.ProvenanceUser},
		}},
		Media: []store.OrganizationMediaInput{{
			MediaKind: store.PersonMediaLogo,
			URI:       &uri,
			Envelope:  store.ValueEnvelopeInput{Source: store.ProvenanceUser},
		}},
	}
	first, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, organization.Revision, input)
	require.NoError(err)
	require.Len(first.Addresses, 1)
	require.Len(first.Media, 1)
	assert.Equal(";;1 Example St;Exampletown;;;", first.Addresses[0].OriginalValue)
	assert.Equal("https://example.test/logo.png", first.Media[0].OriginalValue)
	require.NotNil(first.Media[0].URI)
	assert.Equal("https://example.test/logo.png", *first.Media[0].URI)

	second, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, first.Organization.Revision, input)
	require.NoError(err)
	require.Len(second.Addresses, 1)
	require.Len(second.Media, 1)
	assert.Equal(first.Addresses[0].Envelope.ID, second.Addresses[0].Envelope.ID,
		"omitted address original_value must not churn a stable row")
	assert.Equal(first.Media[0].Envelope.ID, second.Media[0].Envelope.ID,
		"omitted media original_value must not churn a stable row")
}

func TestOrganizationContactScopePartsAreTrimmedBeforeValidationAndStorage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{Name: "Example Org"})
	require.NoError(err)

	scopeKind := " workspace "
	scopeValue := " T0EXAMPLE "
	input := store.OrganizationProfileInput{ContactPoints: []store.OrganizationContactPointInput{{
		AddressKind:   store.ContactAddressUsername,
		ServiceSlug:   new("slack"),
		ScopeKind:     &scopeKind,
		ScopeValue:    &scopeValue,
		OriginalValue: "alice",
		Envelope:      store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	}}}
	first, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, organization.Revision, input)
	require.NoError(err)
	require.Len(first.ContactPoints, 1)
	require.NotNil(first.ContactPoints[0].ScopeKind)
	require.NotNil(first.ContactPoints[0].ScopeValue)
	assert.Equal("workspace", *first.ContactPoints[0].ScopeKind)
	assert.Equal("T0EXAMPLE", *first.ContactPoints[0].ScopeValue)

	second, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, first.Organization.Revision, input)
	require.NoError(err)
	require.Len(second.ContactPoints, 1)
	assert.Equal(first.ContactPoints[0].Envelope.ID, second.ContactPoints[0].Envelope.ID,
		"padded scope parts must not create a second identity key")
}

func TestOrganizationProfileReconcilesWritableMetadataWithDurableVCardIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{Name: "Example Org"})
	require.NoError(err)

	sourceRef := "resource-1"
	propID := "org-1"
	first, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, organization.Revision,
		store.OrganizationProfileInput{Names: []store.OrganizationNameInput{{
			Name: "Example Org", NameKind: store.OrganizationNameKindAlias,
			Envelope: store.ValueEnvelopeInput{
				Pref: new(1), Source: store.ProvenanceVCardImport, SourceRef: &sourceRef,
				VCard: store.VCardIdentity{Property: "ORG", PropID: &propID},
			},
		}}})
	require.NoError(err)
	require.Len(first.Names, 1)

	second, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, first.Organization.Revision,
		store.OrganizationProfileInput{Names: []store.OrganizationNameInput{{
			Name: "Example Org", NameKind: store.OrganizationNameKindAlias,
			Envelope: store.ValueEnvelopeInput{
				Pref: new(2), Source: store.ProvenanceVCardImport, SourceRef: &sourceRef,
				VCard: store.VCardIdentity{Property: "ORG", PropID: &propID},
			},
		}}})
	require.NoError(err)
	require.Len(second.Names, 1)
	assert.NotEqual(first.Names[0].Envelope.ID, second.Names[0].Envelope.ID,
		"a writable metadata change must create a new historized row")
	require.NotNil(second.Names[0].Envelope.Pref)
	assert.Equal(2, *second.Names[0].Envelope.Pref)

	history, err := st.GetOrganizationProfileContext(ctx, organization.ID, true)
	require.NoError(err)
	require.Len(history.Names, 2)
	for _, name := range history.Names {
		if name.Envelope.ID == first.Names[0].Envelope.ID {
			assert.False(name.Envelope.IsCurrent())
		}
	}
}

func TestOrganizationProfileMatchesDurableVCardIdentityWhenBusinessValueChanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{Name: "Example Org"})
	require.NoError(err)

	sourceRef := "resource-2"
	propID := "domain-1"
	envelope := func() store.ValueEnvelopeInput {
		return store.ValueEnvelopeInput{
			Source: store.ProvenanceVCardImport, SourceRef: &sourceRef,
			VCard: store.VCardIdentity{Property: "EMAIL", PropID: &propID},
		}
	}
	first, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, organization.Revision,
		store.OrganizationProfileInput{Identifiers: []store.OrganizationIdentifierInput{{
			IdentifierKind: store.OrganizationIdentifierKindDomain, Value: "old.example",
			Envelope: envelope(),
		}}})
	require.NoError(err)
	require.Len(first.Identifiers, 1)

	second, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, first.Organization.Revision,
		store.OrganizationProfileInput{Identifiers: []store.OrganizationIdentifierInput{{
			IdentifierKind: store.OrganizationIdentifierKindDomain, Value: "new.example",
			Envelope: envelope(),
		}}})
	require.NoError(err)
	require.Len(second.Identifiers, 1)
	assert.Equal("new.example", second.Identifiers[0].Value)
	assert.NotEqual(first.Identifiers[0].Envelope.ID, second.Identifiers[0].Envelope.ID,
		"the durable vCard identity must follow a changed business value")

	history, err := st.GetOrganizationProfileContext(ctx, organization.ID, true)
	require.NoError(err)
	require.Len(history.Identifiers, 2)
	for _, identifier := range history.Identifiers {
		if identifier.Envelope.ID == first.Identifiers[0].Envelope.ID {
			assert.False(identifier.Envelope.IsCurrent())
		}
	}
}

func TestOrganizationProfileScopesDuplicateValuesToVCardResources(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{Name: "Example Org"})
	require.NoError(err)

	sourceRef := "address-book-1"
	propID := "adr-1"
	envelope := func(resourceUID string) store.ValueEnvelopeInput {
		return store.ValueEnvelopeInput{
			Source: store.ProvenanceVCardImport, SourceRef: &sourceRef,
			SourceResourceUID: &resourceUID,
			VCard:             store.VCardIdentity{Property: "ADR", PropID: &propID},
		}
	}
	input := store.OrganizationProfileInput{Addresses: []store.OrganizationAddressInput{
		{
			AddressKind: store.PersonAddressPostal, StreetAddress: new("1 Example St"),
			OriginalValue: "1 Example St", Envelope: envelope("card-1"),
		},
		{
			AddressKind: store.PersonAddressPostal, StreetAddress: new("1 Example St"),
			OriginalValue: "1 Example St", Envelope: envelope("card-2"),
		},
	}}
	first, err := st.ReplaceOrganizationProfileContext(
		ctx, organization.ID, organization.Revision, input)
	require.NoError(err)
	require.Len(first.Addresses, 2)

	firstIDs := []int64{first.Addresses[0].Envelope.ID, first.Addresses[1].Envelope.ID}
	second, err := st.ReplaceOrganizationProfileContext(
		ctx, organization.ID, first.Organization.Revision, input)
	require.NoError(err)
	require.Len(second.Addresses, 2)
	secondIDs := []int64{second.Addresses[0].Envelope.ID, second.Addresses[1].Envelope.ID}
	assert.ElementsMatch(firstIDs, secondIDs)
}

func TestOrganizationProfileHistorizesSourceResourceUIDChanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{Name: "Example Org"})
	require.NoError(err)

	sourceRef := "address-book-1"
	propID := "domain-1"
	identifier := func(resourceUID string) store.OrganizationIdentifierInput {
		return store.OrganizationIdentifierInput{
			IdentifierKind: store.OrganizationIdentifierKindDomain,
			Value:          "example.test",
			Envelope: store.ValueEnvelopeInput{
				Source: store.ProvenanceVCardImport, SourceRef: &sourceRef,
				SourceResourceUID: &resourceUID,
				VCard:             store.VCardIdentity{Property: "EMAIL", PropID: &propID},
			},
		}
	}
	first, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, organization.Revision,
		store.OrganizationProfileInput{Identifiers: []store.OrganizationIdentifierInput{
			identifier("card-1"),
		}})
	require.NoError(err)
	require.Len(first.Identifiers, 1)

	second, err := st.ReplaceOrganizationProfileContext(ctx, organization.ID, first.Organization.Revision,
		store.OrganizationProfileInput{Identifiers: []store.OrganizationIdentifierInput{
			identifier("card-2"),
		}})
	require.NoError(err)
	require.Len(second.Identifiers, 1)
	assert.NotEqual(first.Identifiers[0].Envelope.ID, second.Identifiers[0].Envelope.ID)
	require.NotNil(second.Identifiers[0].Envelope.SourceResourceUID)
	assert.Equal("card-2", *second.Identifiers[0].Envelope.SourceResourceUID)

	history, err := st.GetOrganizationProfileContext(ctx, organization.ID, true)
	require.NoError(err)
	require.Len(history.Identifiers, 2)
}
