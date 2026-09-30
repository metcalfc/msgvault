package store_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

type protectionArchive struct {
	t      *testing.T
	f      *storetest.Fixture
	labels map[string]int64
	next   int
}

func newProtectionArchive(t *testing.T) *protectionArchive {
	t.Helper()
	f := storetest.New(t)
	labels := f.EnsureLabels(map[string]string{
		"STARRED": "STARRED", "SPAM": "SPAM", "TRASH": "TRASH", "INBOX": "INBOX",
	}, "system")
	return &protectionArchive{t: t, f: f, labels: labels}
}

func (a *protectionArchive) message(sender int64, fromMe bool, labels ...string) int64 {
	a.t.Helper()
	a.next++
	id, err := a.f.Store.UpsertMessage(&store.Message{
		ConversationID: a.f.ConvID, SourceID: a.f.Source.ID,
		SourceMessageID: fmt.Sprintf("protect-%d", a.next), MessageType: "email",
		SenderID: sql.NullInt64{Int64: sender, Valid: sender > 0}, IsFromMe: fromMe,
		Subject: sql.NullString{String: "Synthetic subject", Valid: true},
	})
	require.NoError(a.t, err)
	ids := make([]int64, 0, len(labels))
	for _, label := range labels {
		ids = append(ids, a.labels[label])
	}
	if len(ids) > 0 {
		require.NoError(a.t, a.f.Store.AddMessageLabels(id, ids))
	}
	return id
}

func TestDeletionProtectionsNameStarredOwnerSentAndPersonSenders(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newProtectionArchive(t)
	st := a.f.Store
	owner := a.f.EnsureParticipant("owner@example.com", "Owner Example", "example.com")
	decided := a.f.EnsureParticipant("casey@example.com", "Casey Example", "example.com")
	decidedAlias := a.f.EnsureParticipant("casey.alt@example.org", "Casey Example", "example.org")
	_, err := st.LinkParticipants(decided, decidedAlias)
	require.NoError(err)
	judged := a.f.EnsureParticipant("riley@example.net", "Riley Example", "example.net")
	unsure := a.f.EnsureParticipant("sam@example.net", "Sam Example", "example.net")
	alerts := a.f.EnsureParticipant("alerts@example.com", "Example Alerts", "example.com")
	unknown := a.f.EnsureParticipant("pat@example.org", "Pat Example", "example.org")
	overruled := a.f.EnsureParticipant("jordan@example.org", "Jordan Example", "example.org")

	_, err = st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: decided, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	high, low := 0.82, 0.41
	_, err = st.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{
		{ParticipantID: judged, Source: correspondentkind.SourceJev, Kind: correspondentkind.Person,
			Confidence: &high, Probabilities: map[string]float64{"individual_person": high}},
		{ParticipantID: unsure, Source: correspondentkind.SourceJev, Kind: correspondentkind.Unclear,
			Confidence: &low, Probabilities: map[string]float64{"individual_person": low}},
		{ParticipantID: alerts, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated},
		{ParticipantID: overruled, Source: correspondentkind.SourceJev, Kind: correspondentkind.Person,
			Confidence: &high, Probabilities: map[string]float64{"individual_person": high}},
	})
	require.NoError(err)
	_, err = st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: overruled, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)

	starred := a.message(alerts, false, "STARRED", "INBOX")
	mine := a.message(owner, true)
	fromDecidedAlias := a.message(decidedAlias, false)
	fromJudged := a.message(judged, false, "STARRED")
	fromUnsure := a.message(unsure, false)
	fromAlerts := a.message(alerts, false)
	fromUnknown := a.message(unknown, false)
	fromOverruled := a.message(overruled, false)

	protections, err := st.DeletionProtectionsContext(t.Context(), []int64{
		starred, mine, fromDecidedAlias, fromJudged, fromUnsure, fromAlerts, fromUnknown, fromOverruled,
	})
	require.NoError(err)

	assert.Equal([]string{store.ProtectionStarred}, protections[starred].Reasons())
	assert.Equal([]string{store.ProtectionOwnerSent}, protections[mine].Reasons())
	assert.Equal([]string{store.ProtectionPersonSender}, protections[fromDecidedAlias].Reasons(),
		"a user decision covers every member of the cluster")
	assert.Nil(protections[fromDecidedAlias].PersonProbability)
	assert.Equal([]string{store.ProtectionStarred, store.ProtectionPersonSender}, protections[fromJudged].Reasons())
	require.NotNil(protections[fromJudged].PersonProbability)
	assert.InDelta(high, *protections[fromJudged].PersonProbability, 1e-9)
	for name, id := range map[string]int64{
		"an unclear judgment below 0.60": fromUnsure,
		"an automated sender":            fromAlerts,
		"an unclassified sender":         fromUnknown,
		"a user decision over Jev":       fromOverruled,
	} {
		_, found := protections[id]
		assert.False(found, name)
	}
	assert.Len(protections, 4)

	empty, err := st.DeletionProtectionsContext(t.Context(), nil)
	require.NoError(err)
	assert.Empty(empty)
}

func TestRemoteImagesAreBlockedForSpamAndTrash(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newProtectionArchive(t)
	st := a.f.Store
	sender := a.f.EnsureParticipant("news@example.com", "Example News", "example.com")
	inbox := a.message(sender, false, "INBOX")
	spam := a.message(sender, false, "SPAM")
	trash := a.message(sender, false, "TRASH")

	for id, want := range map[int64]bool{inbox: false, spam: true, trash: true} {
		blocked, err := st.MessageRemoteImagesBlockedContext(t.Context(), id)
		require.NoError(err)
		assert.Equal(want, blocked, id)
	}
	_, err := st.MessageRemoteImagesBlockedContext(t.Context(), 999999)
	require.ErrorIs(err, sql.ErrNoRows)

	ids, err := st.RemoteImageBackfillMessageIDs(t.Context(), 0, 0, 100)
	require.NoError(err)
	assert.Equal([]int64{inbox}, ids, "backfill never visits spam or trash")
}
