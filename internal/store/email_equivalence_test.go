package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func ensureEmailParticipant(t *testing.T, st *store.Store, address string) int64 {
	t.Helper()
	id, err := st.EnsureParticipant(address, "", "")
	require.NoError(t, err, "ensure participant %s", address)
	return id
}

// equivalenceCandidates lists every candidate the equivalence pass wrote.
func equivalenceCandidates(t *testing.T, st *store.Store) []store.IdentityMatchCandidate {
	t.Helper()
	all, err := st.ListIdentityMatchCandidatesContext(context.Background(), nil, 500, 0)
	require.NoError(t, err, "list candidates")
	out := make([]store.IdentityMatchCandidate, 0, len(all))
	for _, candidate := range all {
		if candidate.SourceRef != nil && *candidate.SourceRef == store.EmailEquivalenceSourceRef {
			out = append(out, candidate)
		}
	}
	return out
}

func equivalenceCandidateFor(
	t *testing.T, st *store.Store, a, b int64,
) store.IdentityMatchCandidate {
	t.Helper()
	lo, hi := min(a, b), max(a, b)
	for _, candidate := range equivalenceCandidates(t, st) {
		if candidate.LeftID == lo && candidate.RightID == hi {
			return candidate
		}
	}
	require.FailNow(t, "no equivalence candidate", "pair %d-%d", lo, hi)
	return store.IdentityMatchCandidate{}
}

func TestLinkEquivalentEmailAddressesLinksSameMailbox(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	plain := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+news@example.com")
	doubleTagged := ensureEmailParticipant(t, st, "pat+a+b@example.com")
	gmail := ensureEmailParticipant(t, st, "sam.doe@gmail.com")
	gmailDotless := ensureEmailParticipant(t, st, "samdoe+shop@gmail.com")
	googlemail := ensureEmailParticipant(t, st, "Sam.Doe@googlemail.com")
	dotted := ensureEmailParticipant(t, st, "lee.roe@example.org")
	dotless := ensureEmailParticipant(t, st, "leeroe@example.org")
	unrelated := ensureEmailParticipant(t, st, "other@example.com")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "link equivalent addresses")
	assert.False(result.Skipped)
	assert.Equal(9, result.Participants)
	assert.Equal(4, result.Linked)
	assert.Equal(1, result.Suggested)
	assert.Equal(0, result.Conflicts)

	assert.True(linkedPair(t, st, plain, tagged), "plus tag links on any domain")
	assert.True(linkedPair(t, st, plain, doubleTagged), "everything after the first plus is a tag")
	assert.True(linkedPair(t, st, gmail, gmailDotless), "Gmail ignores dots and plus tags")
	assert.True(linkedPair(t, st, gmail, googlemail), "googlemail.com is gmail.com")
	assert.False(linkedPair(t, st, dotted, dotless), "non-Gmail dots are never linked automatically")
	assert.False(linkedPair(t, st, plain, unrelated))

	auto := equivalenceCandidateFor(t, st, plain, tagged)
	assert.Equal(store.IdentityMatchEmailEquivalence, auto.Basis)
	assert.Equal(store.IdentityMatchStateAccepted, auto.State)
	require.NotNil(auto.DecidedBy)
	assert.Equal("system", *auto.DecidedBy)
	require.NotNil(auto.NormalizedValue)
	assert.Equal("pat@example.com", *auto.NormalizedValue)
	require.Len(auto.Evidence, 1, "the link carries a plain-language reason")
	assert.Equal("email_equivalence", auto.Evidence[0].EvidenceKind)

	gmailCandidate := equivalenceCandidateFor(t, st, gmail, googlemail)
	require.NotNil(gmailCandidate.NormalizedValue)
	assert.Equal("samdoe@gmail.com", *gmailCandidate.NormalizedValue)

	suggestion := equivalenceCandidateFor(t, st, dotted, dotless)
	assert.Equal(store.IdentityMatchEmailDotVariant, suggestion.Basis)
	assert.Equal(store.IdentityMatchStateCandidate, suggestion.State,
		"a non-Gmail dot variant waits in Reviews")
	assert.Nil(suggestion.DecidedBy)

	// Each automatic link is an ordinary edge, so the cluster exposes it for
	// the person page's unlink control.
	edges, err := st.ClusterEdges(plain)
	require.NoError(err, "cluster edges")
	assert.ElementsMatch([]store.LinkEdge{
		{A: plain, B: tagged}, {A: plain, B: doubleTagged},
	}, edges)
}

func TestLinkEquivalentEmailAddressesIsIdempotent(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	ensureEmailParticipant(t, st, "pat@example.com")
	ensureEmailParticipant(t, st, "pat+news@example.com")
	ensureEmailParticipant(t, st, "lee.roe@example.org")
	ensureEmailParticipant(t, st, "leeroe@example.org")

	first, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "first pass")
	assert.Equal(1, first.Linked)
	assert.Equal(1, first.Suggested)
	revision, err := st.IdentityRevision()
	require.NoError(err)
	candidates := equivalenceCandidates(t, st)

	unchanged, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "second pass")
	assert.True(unchanged.Skipped, "no new participants since the last complete pass")

	forced, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "forced pass")
	assert.False(forced.Skipped)
	assert.Equal(store.EmailEquivalenceResult{Participants: 4}, *forced)

	after, err := st.IdentityRevision()
	require.NoError(err)
	assert.Equal(revision, after, "a repeat pass writes nothing")
	assert.Len(equivalenceCandidates(t, st), len(candidates))
}

func TestLinkEquivalentEmailAddressesUnlinkIsFinal(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	news := ensureEmailParticipant(t, st, "pat+news@example.com")
	shop := ensureEmailParticipant(t, st, "pat+shop@example.com")

	_, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "first pass")
	require.True(linkedPair(t, st, primary, news))

	_, err = st.UnlinkParticipants(primary, news)
	require.NoError(err, "unlink automatic edge")
	rejected := equivalenceCandidateFor(t, st, primary, news)
	assert.Equal(store.IdentityMatchStateRejected, rejected.State,
		"unlinking records the user's decision")

	again, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "forced pass after unlink")
	assert.Equal(0, again.Linked)
	assert.False(linkedPair(t, st, primary, news), "an unlinked equivalence is not recreated")
	assert.True(linkedPair(t, st, primary, shop), "other addresses keep their link")

	// A later address for the same mailbox joins the primary identity without
	// bridging the address the user split off.
	later := ensureEmailParticipant(t, st, "pat+later@example.com")
	next, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "pass after new participant")
	assert.Equal(1, next.Linked)
	assert.True(linkedPair(t, st, primary, later))
	assert.False(linkedPair(t, st, primary, news))
}

func TestLinkEquivalentEmailAddressesKeepsSplitSideApart(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	news := ensureEmailParticipant(t, st, "pat+news@example.com")
	_, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "first pass")
	_, err = st.UnlinkParticipants(primary, news)
	require.NoError(err, "unlink automatic edge")

	// The user then links a new tag of the same mailbox to the split-off
	// address by hand. Linking that tag to the primary address would pull the
	// split side back in, so the pass leaves it alone.
	again := ensureEmailParticipant(t, st, "pat+again@example.com")
	_, err = st.LinkParticipants(news, again)
	require.NoError(err, "manual link")
	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "pass after new participant")
	assert.Equal(0, result.Linked)
	assert.Equal(1, result.Suppressed)
	assert.False(linkedPair(t, st, primary, news))
	assert.False(linkedPair(t, st, primary, again))
}

func TestLinkEquivalentEmailAddressesRejectedLinkIsRemovedAndStays(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	news := ensureEmailParticipant(t, st, "pat+news@example.com")
	_, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "first pass")
	candidate := equivalenceCandidateFor(t, st, primary, news)

	// Rejecting the accepted candidate from Reviews withdraws the edge it owns.
	_, err = st.DecideIdentityMatchCandidateContext(
		ctx, candidate.ID, store.IdentityMatchStateRejected, "user", nil)
	require.NoError(err, "reject automatic link")
	assert.False(linkedPair(t, st, primary, news))

	_, err = st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "forced pass")
	assert.False(linkedPair(t, st, primary, news), "a rejected equivalence stays rejected")
}

func TestLinkEquivalentEmailAddressesDifferentPeopleBecomeReview(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+club@example.com")
	first, _, err := st.CreatePersonFromParticipant(primary)
	require.NoError(err, "promote primary")
	second, _, err := st.CreatePersonFromParticipant(tagged)
	require.NoError(err, "promote tagged")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "link equivalent addresses")
	assert.Equal(0, result.Linked)
	assert.Equal(1, result.Conflicts)
	assert.False(linkedPair(t, st, primary, tagged), "two people are never merged automatically")

	candidate := equivalenceCandidateFor(t, st, primary, tagged)
	assert.Equal(store.IdentityMatchStateConflict, candidate.State, "the pair waits in Reviews")
	for _, person := range []*store.Person{first, second} {
		current, err := st.GetPerson(person.ID)
		require.NoError(err, "person %d still exists", person.ID)
		assert.Equal(person.ID, current.ID)
	}

	// Accepting in Reviews asks for an explicit person merge rather than
	// linking, and leaves the candidate reviewable.
	_, _, err = st.AcceptIdentityMatchCandidateContext(ctx, candidate.ID, "user", nil)
	require.ErrorIs(err, store.ErrPersonBindingConflict)
	after := equivalenceCandidateFor(t, st, primary, tagged)
	assert.Equal(store.IdentityMatchStateConflict, after.State)

	again, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "forced pass")
	assert.Equal(store.EmailEquivalenceResult{Participants: 2}, *again)
}

func TestLinkEquivalentEmailAddressesBindsToTheOnePerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+club@example.com")
	person, _, err := st.CreatePersonFromParticipant(primary)
	require.NoError(err, "promote primary")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "link equivalent addresses")
	assert.Equal(1, result.Linked)

	bound, err := st.PersonForParticipants([]int64{tagged})
	require.NoError(err, "person for tagged address")
	require.NotNil(bound, "the new address joins the existing person")
	assert.Equal(person.ID, bound.ID)
}

func TestLinkEquivalentEmailAddressesRecordsExistingLinks(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+club@example.com")
	_, err := st.LinkParticipants(primary, tagged)
	require.NoError(err, "manual link")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "link equivalent addresses")
	assert.Equal(0, result.Linked)
	assert.Equal(1, result.AlreadyLinked)
	candidate := equivalenceCandidateFor(t, st, primary, tagged)
	assert.Equal(store.IdentityMatchStateAccepted, candidate.State)

	// Undoing the manual link also undoes the recorded equivalence, so the
	// pass does not relink what the user just separated.
	_, err = st.UnlinkParticipants(primary, tagged)
	require.NoError(err, "unlink manual edge")
	_, err = st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "forced pass")
	assert.False(linkedPair(t, st, primary, tagged))
}

func TestCompleteSyncLinksNewEquivalentAddresses(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)

	source, err := st.GetOrCreateSource("mbox", "archive@example.com")
	require.NoError(err, "create source")
	syncID, err := st.StartSync(source.ID, "import-mbox")
	require.NoError(err, "start sync")
	primary := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+receipts@example.com")
	require.NoError(st.CompleteSync(syncID, "done"), "complete sync")

	assert.True(linkedPair(t, st, primary, tagged), "an import links new equivalent addresses")
}

func TestLinkEquivalentEmailAddressesRespectsNotAPerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	inbox := ensureEmailParticipant(t, st, "desk@example.net")
	thread := ensureEmailParticipant(t, st, "desk+thread1@example.net")
	_, err := st.SetCorrespondentKindContext(ctx, store.SetCorrespondentKindInput{
		ParticipantID: thread, Kind: correspondentkind.Automated,
	})
	require.NoError(err, "classify as automated")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "link equivalent addresses")
	assert.Equal(0, result.Linked)
	assert.Equal(1, result.Suppressed)
	assert.False(linkedPair(t, st, inbox, thread))
	suppressed := equivalenceCandidateFor(t, st, inbox, thread)
	assert.Equal(store.IdentityMatchStateRejected, suppressed.State)

	// Clearing the classification restores the equivalence, and the next
	// pass links it.
	_, err = st.SetCorrespondentKindContext(ctx, store.SetCorrespondentKindInput{
		ParticipantID: thread, Kind: correspondentkind.Person,
	})
	require.NoError(err, "classify as person")
	restored := equivalenceCandidateFor(t, st, inbox, thread)
	assert.Equal(store.IdentityMatchStateCandidate, restored.State)
	again, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "forced pass")
	assert.Equal(1, again.Linked)
	assert.True(linkedPair(t, st, inbox, thread))
}

func TestLinkEquivalentEmailAddressesSkipsAutomatedSenders(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	relay := ensureEmailParticipant(t, st, "reply@reply.example.net")
	threadOne := ensureEmailParticipant(t, st, "reply+t0k3n1@reply.example.net")
	threadTwo := ensureEmailParticipant(t, st, "reply+t0k3n2@reply.example.net")
	bounce := ensureEmailParticipant(t, st, "bounces+4242@mail.example.org")
	bounceOther := ensureEmailParticipant(t, st, "bounces+4243@mail.example.org")
	alerts := ensureEmailParticipant(t, st, "alerts@example.com")
	alertsTagged := ensureEmailParticipant(t, st, "alerts+build@example.com")
	person := ensureEmailParticipant(t, st, "pat@example.com")
	personTagged := ensureEmailParticipant(t, st, "pat+news@example.com")
	// A rule, not the user, marks the tagged alerts address automated.
	_, err := st.WriteDerivedCorrespondentKindsContext(ctx, []store.DerivedCorrespondentKind{{
		ParticipantID: alertsTagged, Source: correspondentkind.SourceRule,
		Kind: correspondentkind.Automated, Actor: "rule:noreply_address",
	}})
	require.NoError(err, "classify alerts by rule")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "link equivalent addresses")
	assert.Equal(1, result.Linked, "only the person's tag links")
	assert.Equal(0, result.Suggested)
	assert.True(linkedPair(t, st, person, personTagged))
	for _, pair := range [][2]int64{
		{relay, threadOne}, {relay, threadTwo}, {threadOne, threadTwo},
		{bounce, bounceOther}, {alerts, alertsTagged},
	} {
		assert.False(linkedPair(t, st, pair[0], pair[1]),
			"automated pair %d-%d stays unlinked", pair[0], pair[1])
	}
	candidates := equivalenceCandidates(t, st)
	require.Len(candidates, 1, "skipped pairs leave nothing in Reviews")
	assert.Equal(min(person, personTagged), candidates[0].LeftID)
}

func TestLinkEquivalentEmailAddressesHonorsDetachment(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	news := ensureEmailParticipant(t, st, "pat+news@example.com")
	_, err := st.LinkParticipants(primary, news)
	require.NoError(err, "manual link")
	person, _, err := st.CreatePersonFromParticipant(primary)
	require.NoError(err, "promote")
	require.Equal([]int64{primary, news}, person.ParticipantIDs)
	detached, err := st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: person.ID, ParticipantIDs: []int64{news},
		ExpectedRevision: person.Revision, Actor: "user",
	})
	require.NoError(err, "detach the tagged address")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "pass after detach")
	assert.Equal(0, result.Linked)
	assert.Equal(1, result.Suppressed)
	assert.False(linkedPair(t, st, primary, news), "the pass never undoes a detachment")
	blocked := equivalenceCandidateFor(t, st, primary, news)
	assert.Equal(store.IdentityMatchStateRejected, blocked.State)
	require.NotNil(blocked.Notes)
	assert.Equal(store.PersonDetachmentNote, *blocked.Notes)

	// A new sibling tag links to the person, but one the user tied to the
	// detached address would pull it back through the sibling, so it is
	// refused too.
	sibling := ensureEmailParticipant(t, st, "pat+again@example.com")
	bridge := ensureEmailParticipant(t, st, "pat+bridge@example.com")
	_, err = st.LinkParticipants(news, bridge)
	require.NoError(err, "manual link to the detached address")
	next, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "pass after new siblings")
	assert.Equal(1, next.Linked)
	assert.True(linkedPair(t, st, primary, sibling))
	assert.False(linkedPair(t, st, primary, news))
	assert.False(linkedPair(t, st, primary, bridge))
	owner, err := st.PersonForParticipants([]int64{news})
	require.NoError(err)
	assert.Nil(owner, "the detached address stays off the person")

	// Undoing the detachment restores the refused pairs, and the pass then
	// links them normally.
	current, err := st.GetPerson(person.ID)
	require.NoError(err)
	_, err = st.ReattachPersonParticipantsContext(ctx, store.PersonParticipantReattachRequest{
		PersonID: person.ID, DetachmentID: detached.Detachment.ID,
		ExpectedRevision: current.Revision, Actor: "user",
	})
	require.NoError(err, "undo the detachment")
	restored := equivalenceCandidateFor(t, st, primary, news)
	assert.NotEqual(store.IdentityMatchStateRejected, restored.State)
	_, err = st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "pass after undo")
	assert.True(linkedPair(t, st, primary, news))
	assert.True(linkedPair(t, st, primary, bridge))
	assert.Equal(store.IdentityMatchStateAccepted, equivalenceCandidateFor(t, st, primary, bridge).State)
}

// An unlinked pair must stay apart even when a pair on a different mailbox
// would join the two sides.
func TestLinkEquivalentEmailAddressesUnlinkHoldsAcrossMailboxes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	work := ensureEmailParticipant(t, st, "pat+work@example.com")
	colleague := ensureEmailParticipant(t, st, "lee@example.org")
	_, err := st.LinkParticipants(work, colleague)
	require.NoError(err, "manual link")
	_, err = st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "first pass")
	require.True(linkedPair(t, st, primary, work))
	_, err = st.UnlinkParticipants(primary, work)
	require.NoError(err, "unlink")

	// A tag of the colleague's mailbox tied to the primary side would bridge
	// the colleague's side, and with it the unlinked tag, back in.
	home := ensureEmailParticipant(t, st, "lee+home@example.org")
	_, err = st.LinkParticipants(primary, home)
	require.NoError(err, "manual link")
	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(err, "pass after new tag")
	assert.Equal(0, result.Linked)
	assert.Equal(1, result.Suppressed)
	assert.False(linkedPair(t, st, primary, work), "the unlink holds across mailboxes")
	assert.False(linkedPair(t, st, home, colleague))
}

// A manual unlink of two addresses of one mailbox is a decision too, even
// when no equivalence candidate recorded the link.
func TestUnlinkSameMailboxAddressesIsRemembered(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	work := ensureEmailParticipant(t, st, "pat+work@example.com")
	_, err := st.LinkParticipants(primary, work)
	require.NoError(err, "manual link")
	_, err = st.UnlinkParticipants(primary, work)
	require.NoError(err, "manual unlink")

	recorded := equivalenceCandidateFor(t, st, primary, work)
	assert.Equal(store.IdentityMatchStateRejected, recorded.State)
	require.NotNil(recorded.DecidedBy)
	assert.Equal("user", *recorded.DecidedBy)

	_, err = st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "pass after unlink")
	assert.False(linkedPair(t, st, primary, work), "the pass keeps a manual unlink")
}

// A manual unlink that separates a mailbox's addresses through another edge
// is remembered for the pair that crosses the cut.
func TestUnlinkSeparatingSameMailboxThroughAnotherEdgeIsRemembered(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	colleague := ensureEmailParticipant(t, st, "lee@example.org")
	work := ensureEmailParticipant(t, st, "pat+work@example.com")
	_, err := st.LinkParticipants(primary, colleague)
	require.NoError(err)
	_, err = st.LinkParticipants(colleague, work)
	require.NoError(err)
	_, err = st.UnlinkParticipants(colleague, work)
	require.NoError(err, "cut the edge between the two mailboxes")

	_, err = st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "pass after unlink")
	assert.False(linkedPair(t, st, primary, work), "the pass keeps the separation")
}

// When the lowest-ID address of a mailbox cannot anchor its group, the other
// addresses still link to each other.
func TestLinkEquivalentEmailAddressesAnchorsOnEligibleMember(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	hub := ensureEmailParticipant(t, st, "pat@example.com")
	first := ensureEmailParticipant(t, st, "pat+a@example.com")
	second := ensureEmailParticipant(t, st, "pat+b@example.com")
	_, err := st.WriteDerivedCorrespondentKindsContext(ctx, []store.DerivedCorrespondentKind{{
		ParticipantID: hub, Source: correspondentkind.SourceRule,
		Kind: correspondentkind.Automated, Actor: "rule:noreply_address",
	}})
	require.NoError(err, "classify the lowest-ID address by rule")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(err, "link equivalent addresses")
	assert.Equal(1, result.Linked)
	assert.True(linkedPair(t, st, first, second), "siblings link without the hub")
	assert.False(linkedPair(t, st, hub, first), "the automated address stays apart")
}
