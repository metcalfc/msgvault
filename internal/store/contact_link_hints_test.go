package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestSeededProfileServicesCarryLinkTemplates(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := storetest.New(t).Store
	ctx := context.Background()

	for _, test := range []struct{ slug, template string }{
		{"github", "https://github.com/{username}"},
		{"youtube", "https://www.youtube.com/@{username}"},
		{"threads", "https://www.threads.com/@{username}"},
	} {
		service, err := st.ResolveCommunicationServiceContext(ctx, test.slug)
		require.NoError(err, test.slug)
		assert.True(service.IsSystem, test.slug)
		require.NotNil(service.ProfileURLTemplate, test.slug)
		assert.Equal(test.template, *service.ProfileURLTemplate, test.slug)
		assert.Equal(store.NormalizationStripAtLower, service.Normalization, test.slug)
	}
	mastodon, err := st.ResolveCommunicationServiceContext(ctx, "mastodon")
	require.NoError(err)
	assert.True(mastodon.IsSystem)
	assert.Nil(mastodon.ProfileURLTemplate)
}

func TestPersonContactPointsCarryServiceLinkHints(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := storetest.New(t).Store
	ctx := context.Background()
	personID := newTestPerson(t, st)

	telegram, err := st.AddPersonContactPointContext(ctx, personID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressUsername, ServiceSlug: new("telegram"),
		OriginalValue: "@example_user",
		Envelope:      store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(err)
	require.NotNil(telegram.ProfileURLTemplate)
	assert.Equal("https://t.me/{username}", *telegram.ProfileURLTemplate)
	require.NotNil(telegram.URIScheme)
	assert.Equal("tg", *telegram.URIScheme)

	_, err = st.AddPersonContactPointContext(ctx, personID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: "person@example.com",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(err)

	points, err := st.ListPersonContactPointsContext(ctx, personID, true)
	require.NoError(err)
	require.Len(points, 2)
	for _, point := range points {
		if point.AddressKind == store.ContactAddressEmail {
			assert.Nil(point.ProfileURLTemplate, "a point without a service has no template")
			assert.Nil(point.URIScheme)
			continue
		}
		require.NotNil(point.ProfileURLTemplate)
		assert.Equal("https://t.me/{username}", *point.ProfileURLTemplate)
	}
}

func TestOrganizationContactPointsCarryServiceLinkHints(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{
		Name: "Example Org", Kind: store.OrganizationKindCompany,
	})
	require.NoError(err)

	profile, err := st.ReplaceOrganizationProfileContext(
		ctx, organization.ID, organization.Revision, store.OrganizationProfileInput{
			ContactPoints: []store.OrganizationContactPointInput{{
				AddressKind: store.ContactAddressSocial, ServiceSlug: new("github"),
				OriginalValue: "example-org",
				Envelope:      store.ValueEnvelopeInput{Source: store.ProvenanceUser},
			}},
		})
	require.NoError(err)
	require.Len(profile.ContactPoints, 1)
	require.NotNil(profile.ContactPoints[0].ProfileURLTemplate)
	assert.Equal("https://github.com/{username}", *profile.ContactPoints[0].ProfileURLTemplate)
	assert.Nil(profile.ContactPoints[0].URIScheme)
}

func TestParticipantIdentifierContextCarriesServiceLinkHints(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()
	const key = "example-bridge-user"
	participant, err := st.EnsureParticipantByIdentifier("beeper", key, "Example Person")
	require.NoError(err)
	service, err := st.ResolveCommunicationServiceContext(ctx, "telegram")
	require.NoError(err)
	require.NoError(st.ClassifyParticipantIdentifierServiceContext(
		ctx, "beeper", key, &service.ID, nil, nil))

	details, err := st.GetParticipantIdentityContext(ctx, []int64{participant})
	require.NoError(err)
	assert.Contains(details.Identifiers, store.ParticipantIdentifierContext{
		ParticipantID: participant, Type: "beeper", Value: key,
		ServiceSlug: "telegram", ServiceLabel: "Telegram",
		URIScheme: "tg", ProfileURLTemplate: "https://t.me/{username}",
	})
}
