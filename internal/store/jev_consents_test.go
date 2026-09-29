package store_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil"
)

const (
	jevTestFingerprintA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	jevTestFingerprintB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestJevFeatureConsentLifecycle(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()

	active, err := st.HasActiveJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintA)
	require.NoError(err)
	assert.False(active)

	consent, created, err := st.GrantJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintA, "cli")
	require.NoError(err)
	require.True(created)
	assert.Equal("enrichment_identity", consent.Feature)
	assert.Equal(jevTestFingerprintA, consent.PolicyFingerprint)
	assert.Equal("cli", consent.GrantedBy)
	assert.NotEmpty(consent.GrantedAt)
	assert.Nil(consent.RevokedAt)

	again, created, err := st.GrantJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintA, "cli")
	require.NoError(err)
	assert.False(created, "an active grant is idempotent")
	assert.Equal(consent.ID, again.ID)

	active, err = st.HasActiveJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintA)
	require.NoError(err)
	assert.True(active)

	// A new policy for the same feature supersedes the old grant.
	replacement, created, err := st.GrantJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintB, "cli")
	require.NoError(err)
	require.True(created)
	assert.NotEqual(consent.ID, replacement.ID)
	active, err = st.HasActiveJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintA)
	require.NoError(err)
	assert.False(active, "the old fingerprint loses authority when the policy changes")
	status, err := st.GetJevFeatureConsentStatus(ctx, "enrichment_identity", jevTestFingerprintA)
	require.NoError(err)
	assert.False(status.Active)
	assert.True(status.Superseded)
	require.NotNil(status.LastRevoked)
	assert.Equal(consent.ID, status.LastRevoked.ID)

	status, err = st.GetJevFeatureConsentStatus(ctx, "enrichment_identity", jevTestFingerprintB)
	require.NoError(err)
	assert.True(status.Active)
	require.NotNil(status.Consent)
	assert.Equal(replacement.ID, status.Consent.ID)

	revoked, err := st.RevokeJevFeatureConsent(ctx, "enrichment_identity", "cli")
	require.NoError(err)
	assert.Equal(int64(1), revoked)
	active, err = st.HasActiveJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintB)
	require.NoError(err)
	assert.False(active)
	revoked, err = st.RevokeJevFeatureConsent(ctx, "enrichment_identity", "cli")
	require.NoError(err)
	assert.Zero(revoked, "nothing active is left to revoke")

	_, _, err = st.GrantJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintA, "cli")
	require.NoError(err)
	_, _, err = st.GrantJevFeatureConsent(ctx, "search_rerank", jevTestFingerprintA, "cli")
	require.NoError(err)
	revoked, err = st.RevokeAllJevFeatureConsents(ctx, "cli")
	require.NoError(err)
	assert.Equal(int64(2), revoked, "revoke all covers every feature")
}

func TestJevFeatureConsentRejectsInvalidInput(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()
	_, _, err := st.GrantJevFeatureConsent(ctx, "Bad Feature", jevTestFingerprintA, "cli")
	require.Error(err)
	_, _, err = st.GrantJevFeatureConsent(ctx, "enrichment_identity", strings.ToUpper(jevTestFingerprintA), "cli")
	require.Error(err, "fingerprints are lowercase SHA-256 hex")
	_, _, err = st.GrantJevFeatureConsent(ctx, "enrichment_identity", jevTestFingerprintA, " ")
	require.Error(err)
	_, err = st.HasActiveJevFeatureConsent(ctx, "enrichment_identity", "short")
	require.Error(err)
	_, err = st.RevokeJevFeatureConsent(ctx, "enrichment_identity", "")
	require.Error(err)
	_, err = st.RevokeAllJevFeatureConsents(ctx, "")
	require.Error(err)
}
