package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestDerivedCorrespondentKindsStayBelowUserDecisionsAndOwners(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)
	st := f.Store
	require.NoError(st.AddAccountIdentity(f.Source.ID, "owner@example.com", "manual"))
	owner := f.EnsureParticipant("owner@example.com", "Owner Example", "example.com")
	ownerAlias := f.EnsureParticipant("owner-alias@example.com", "", "example.com")
	_, err := st.LinkParticipants(owner, ownerAlias)
	require.NoError(err)
	alerts := f.EnsureParticipant("alerts@example.com", "Example Alerts", "example.com")
	alertsAlias := f.EnsureParticipant("alerts-eu@example.com", "", "example.com")
	_, err = st.LinkParticipants(alerts, alertsAlias)
	require.NoError(err)
	decided := f.EnsureParticipant("casey@example.com", "Casey Example", "example.com")
	_, err = st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: decided, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	before, err := st.CorrespondentKindRevision()
	require.NoError(err)

	confidence := 0.9
	written, err := st.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{
		{ParticipantID: alertsAlias, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated, Actor: "rule:noreply_address"},
		{ParticipantID: ownerAlias, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated},
		{ParticipantID: decided, Source: correspondentkind.SourceJev, Kind: correspondentkind.MailingList,
			Confidence: &confidence, Probabilities: map[string]float64{"mailing_list_or_group": 0.9}},
	})
	require.NoError(err)
	assert.Equal(1, written, "owner clusters and user decisions are never overwritten")
	after, err := st.CorrespondentKindRevision()
	require.NoError(err)
	assert.Equal(before+1, after)

	record, err := st.GetCorrespondentKindContext(t.Context(), alerts)
	require.NoError(err)
	assert.Equal(correspondentkind.Automated, record.Kind)
	assert.Equal([]int64{alerts, alertsAlias}, record.MemberIDs)
	ids, err := st.ParticipantsWithCorrespondentKindContext(t.Context(), correspondentkind.Automated)
	require.NoError(err)
	assert.Equal([]int64{alerts, alertsAlias}, ids)
	_, err = st.ParticipantsWithCorrespondentKindContext(t.Context(), correspondentkind.Person)
	require.ErrorIs(err, store.ErrCorrespondentKindInvalid)

	rows, err := st.CorrespondentKindExportRowsContext(t.Context())
	require.NoError(err)
	assert.Equal([]store.CorrespondentKindExportRow{
		{ParticipantID: alerts, Kind: correspondentkind.Automated, Source: correspondentkind.SourceRule},
		{ParticipantID: alertsAlias, Kind: correspondentkind.Automated, Source: correspondentkind.SourceRule},
		{ParticipantID: decided, Kind: correspondentkind.Person, Source: correspondentkind.SourceUser},
	}, rows)

	// The user overrides the rule; the derived row stays underneath.
	_, err = st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: alerts, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	record, err = st.GetCorrespondentKindContext(t.Context(), alerts)
	require.NoError(err)
	assert.Equal(correspondentkind.Person, record.Kind)
}

func TestDerivedCorrespondentKindsRejectKindsTheirSourceMayNotWrite(t *testing.T) {
	f := storetest.New(t)
	participant := f.EnsureParticipant("desk@example.com", "Desk", "example.com")
	cases := map[string]store.DerivedCorrespondentKind{
		"user source":         {ParticipantID: participant, Source: correspondentkind.SourceUser, Kind: correspondentkind.Automated},
		"organization":        {ParticipantID: participant, Source: correspondentkind.SourceJev, Kind: correspondentkind.Organization},
		"rule unclear":        {ParticipantID: participant, Source: correspondentkind.SourceRule, Kind: correspondentkind.Unclear},
		"unknown kind":        {ParticipantID: participant, Source: correspondentkind.SourceRule, Kind: "robot"},
		"missing participant": {ParticipantID: 0, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated},
	}
	for name, kind := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.Store.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{kind})
			require.ErrorIs(t, err, store.ErrDerivedCorrespondentKindInvalid)
		})
	}
}

func TestUnclearJudgmentNeverHoldsBackAnOwnerIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)
	later := f.EnsureParticipant("later-me@example.com", "Later Me", "example.com")
	_, err := f.Store.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{{
		ParticipantID: later, Source: correspondentkind.SourceJev, Kind: correspondentkind.Unclear,
	}})
	require.NoError(err)
	hidden, err := f.Store.RankingHiddenParticipantsContext(t.Context())
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{later: correspondentkind.Unclear}, hidden)

	// The address turns out to be the owner's own.
	require.NoError(f.Store.AddAccountIdentity(f.Source.ID, "later-me@example.com", "manual"))
	hidden, err = f.Store.RankingHiddenParticipantsContext(t.Context())
	require.NoError(err)
	assert.Empty(hidden)
}
