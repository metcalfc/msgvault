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
	require.NoError(t, err, "link equivalent addresses")
	assert.False(t, result.Skipped)
	assert.Equal(t, 9, result.Participants)
	assert.Equal(t, 4, result.Linked)
	assert.Equal(t, 1, result.Suggested)
	assert.Equal(t, 0, result.Conflicts)

	assert.True(t, linkedPair(t, st, plain, tagged), "plus tag links on any domain")
	assert.True(t, linkedPair(t, st, plain, doubleTagged), "everything after the first plus is a tag")
	assert.True(t, linkedPair(t, st, gmail, gmailDotless), "Gmail ignores dots and plus tags")
	assert.True(t, linkedPair(t, st, gmail, googlemail), "googlemail.com is gmail.com")
	assert.False(t, linkedPair(t, st, dotted, dotless), "non-Gmail dots are never linked automatically")
	assert.False(t, linkedPair(t, st, plain, unrelated))

	auto := equivalenceCandidateFor(t, st, plain, tagged)
	assert.Equal(t, store.IdentityMatchEmailEquivalence, auto.Basis)
	assert.Equal(t, store.IdentityMatchStateAccepted, auto.State)
	require.NotNil(t, auto.DecidedBy)
	assert.Equal(t, "system", *auto.DecidedBy)
	require.NotNil(t, auto.NormalizedValue)
	assert.Equal(t, "pat@example.com", *auto.NormalizedValue)
	require.Len(t, auto.Evidence, 1, "the link carries a plain-language reason")
	assert.Equal(t, "email_equivalence", auto.Evidence[0].EvidenceKind)

	gmailCandidate := equivalenceCandidateFor(t, st, gmail, googlemail)
	require.NotNil(t, gmailCandidate.NormalizedValue)
	assert.Equal(t, "samdoe@gmail.com", *gmailCandidate.NormalizedValue)

	suggestion := equivalenceCandidateFor(t, st, dotted, dotless)
	assert.Equal(t, store.IdentityMatchEmailDotVariant, suggestion.Basis)
	assert.Equal(t, store.IdentityMatchStateCandidate, suggestion.State,
		"a non-Gmail dot variant waits in Reviews")
	assert.Nil(t, suggestion.DecidedBy)

	// Each automatic link is an ordinary edge, so the cluster exposes it for
	// the person page's unlink control.
	edges, err := st.ClusterEdges(plain)
	require.NoError(t, err, "cluster edges")
	assert.ElementsMatch(t, []store.LinkEdge{
		{A: plain, B: tagged}, {A: plain, B: doubleTagged},
	}, edges)
}

func TestLinkEquivalentEmailAddressesIsIdempotent(t *testing.T) {
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	ensureEmailParticipant(t, st, "pat@example.com")
	ensureEmailParticipant(t, st, "pat+news@example.com")
	ensureEmailParticipant(t, st, "lee.roe@example.org")
	ensureEmailParticipant(t, st, "leeroe@example.org")

	first, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "first pass")
	assert.Equal(t, 1, first.Linked)
	assert.Equal(t, 1, first.Suggested)
	revision, err := st.IdentityRevision()
	require.NoError(t, err)
	candidates := equivalenceCandidates(t, st)

	unchanged, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "second pass")
	assert.True(t, unchanged.Skipped, "no new participants since the last complete pass")

	forced, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(t, err, "forced pass")
	assert.False(t, forced.Skipped)
	assert.Equal(t, store.EmailEquivalenceResult{Participants: 4}, *forced)

	after, err := st.IdentityRevision()
	require.NoError(t, err)
	assert.Equal(t, revision, after, "a repeat pass writes nothing")
	assert.Len(t, equivalenceCandidates(t, st), len(candidates))
}

func TestLinkEquivalentEmailAddressesUnlinkIsFinal(t *testing.T) {
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	news := ensureEmailParticipant(t, st, "pat+news@example.com")
	shop := ensureEmailParticipant(t, st, "pat+shop@example.com")

	_, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "first pass")
	require.True(t, linkedPair(t, st, primary, news))

	_, err = st.UnlinkParticipants(primary, news)
	require.NoError(t, err, "unlink automatic edge")
	rejected := equivalenceCandidateFor(t, st, primary, news)
	assert.Equal(t, store.IdentityMatchStateRejected, rejected.State,
		"unlinking records the user's decision")

	again, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(t, err, "forced pass after unlink")
	assert.Equal(t, 0, again.Linked)
	assert.False(t, linkedPair(t, st, primary, news), "an unlinked equivalence is not recreated")
	assert.True(t, linkedPair(t, st, primary, shop), "other addresses keep their link")

	// A later address for the same mailbox joins the primary identity without
	// bridging the address the user split off.
	later := ensureEmailParticipant(t, st, "pat+later@example.com")
	next, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "pass after new participant")
	assert.Equal(t, 1, next.Linked)
	assert.True(t, linkedPair(t, st, primary, later))
	assert.False(t, linkedPair(t, st, primary, news))
}

func TestLinkEquivalentEmailAddressesKeepsSplitSideApart(t *testing.T) {
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	news := ensureEmailParticipant(t, st, "pat+news@example.com")
	_, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "first pass")
	_, err = st.UnlinkParticipants(primary, news)
	require.NoError(t, err, "unlink automatic edge")

	// The user then links a new tag of the same mailbox to the split-off
	// address by hand. Linking that tag to the primary address would pull the
	// split side back in, so the pass leaves it alone.
	again := ensureEmailParticipant(t, st, "pat+again@example.com")
	_, err = st.LinkParticipants(news, again)
	require.NoError(t, err, "manual link")
	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "pass after new participant")
	assert.Equal(t, 0, result.Linked)
	assert.Equal(t, 1, result.Suppressed)
	assert.False(t, linkedPair(t, st, primary, news))
	assert.False(t, linkedPair(t, st, primary, again))
}

func TestLinkEquivalentEmailAddressesRejectedLinkIsRemovedAndStays(t *testing.T) {
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	news := ensureEmailParticipant(t, st, "pat+news@example.com")
	_, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "first pass")
	candidate := equivalenceCandidateFor(t, st, primary, news)

	// Rejecting the accepted candidate from Reviews withdraws the edge it owns.
	_, err = st.DecideIdentityMatchCandidateContext(
		ctx, candidate.ID, store.IdentityMatchStateRejected, "user", nil)
	require.NoError(t, err, "reject automatic link")
	assert.False(t, linkedPair(t, st, primary, news))

	_, err = st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(t, err, "forced pass")
	assert.False(t, linkedPair(t, st, primary, news), "a rejected equivalence stays rejected")
}

func TestLinkEquivalentEmailAddressesDifferentPeopleBecomeReview(t *testing.T) {
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+club@example.com")
	first, _, err := st.CreatePersonFromParticipant(primary)
	require.NoError(t, err, "promote primary")
	second, _, err := st.CreatePersonFromParticipant(tagged)
	require.NoError(t, err, "promote tagged")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "link equivalent addresses")
	assert.Equal(t, 0, result.Linked)
	assert.Equal(t, 1, result.Conflicts)
	assert.False(t, linkedPair(t, st, primary, tagged), "two people are never merged automatically")

	candidate := equivalenceCandidateFor(t, st, primary, tagged)
	assert.Equal(t, store.IdentityMatchStateConflict, candidate.State, "the pair waits in Reviews")
	for _, person := range []*store.Person{first, second} {
		current, err := st.GetPerson(person.ID)
		require.NoError(t, err, "person %d still exists", person.ID)
		assert.Equal(t, person.ID, current.ID)
	}

	// Accepting in Reviews asks for an explicit person merge rather than
	// linking, and leaves the candidate reviewable.
	_, _, err = st.AcceptIdentityMatchCandidateContext(ctx, candidate.ID, "user", nil)
	require.ErrorIs(t, err, store.ErrPersonBindingConflict)
	after := equivalenceCandidateFor(t, st, primary, tagged)
	assert.Equal(t, store.IdentityMatchStateConflict, after.State)

	again, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(t, err, "forced pass")
	assert.Equal(t, store.EmailEquivalenceResult{Participants: 2}, *again)
}

func TestLinkEquivalentEmailAddressesBindsToTheOnePerson(t *testing.T) {
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+club@example.com")
	person, _, err := st.CreatePersonFromParticipant(primary)
	require.NoError(t, err, "promote primary")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "link equivalent addresses")
	assert.Equal(t, 1, result.Linked)

	bound, err := st.PersonForParticipants([]int64{tagged})
	require.NoError(t, err, "person for tagged address")
	require.NotNil(t, bound, "the new address joins the existing person")
	assert.Equal(t, person.ID, bound.ID)
}

func TestLinkEquivalentEmailAddressesRecordsExistingLinks(t *testing.T) {
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	primary := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+club@example.com")
	_, err := st.LinkParticipants(primary, tagged)
	require.NoError(t, err, "manual link")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "link equivalent addresses")
	assert.Equal(t, 0, result.Linked)
	assert.Equal(t, 1, result.AlreadyLinked)
	candidate := equivalenceCandidateFor(t, st, primary, tagged)
	assert.Equal(t, store.IdentityMatchStateAccepted, candidate.State)

	// Undoing the manual link also undoes the recorded equivalence, so the
	// pass does not relink what the user just separated.
	_, err = st.UnlinkParticipants(primary, tagged)
	require.NoError(t, err, "unlink manual edge")
	_, err = st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(t, err, "forced pass")
	assert.False(t, linkedPair(t, st, primary, tagged))
}

func TestCompleteSyncLinksNewEquivalentAddresses(t *testing.T) {
	st := testutil.NewTestStore(t)

	source, err := st.GetOrCreateSource("mbox", "archive@example.com")
	require.NoError(t, err, "create source")
	syncID, err := st.StartSync(source.ID, "import-mbox")
	require.NoError(t, err, "start sync")
	primary := ensureEmailParticipant(t, st, "pat@example.com")
	tagged := ensureEmailParticipant(t, st, "pat+receipts@example.com")
	require.NoError(t, st.CompleteSync(syncID, "done"), "complete sync")

	assert.True(t, linkedPair(t, st, primary, tagged), "an import links new equivalent addresses")
}

func TestLinkEquivalentEmailAddressesRespectsNotAPerson(t *testing.T) {
	st := testutil.NewTestStore(t)
	ctx := context.Background()

	inbox := ensureEmailParticipant(t, st, "reply@example.net")
	thread := ensureEmailParticipant(t, st, "reply+thread1@example.net")
	_, err := st.SetCorrespondentKindContext(ctx, store.SetCorrespondentKindInput{
		ParticipantID: thread, Kind: correspondentkind.Automated,
	})
	require.NoError(t, err, "classify as automated")

	result, err := st.LinkEquivalentEmailAddressesContext(ctx, false)
	require.NoError(t, err, "link equivalent addresses")
	assert.Equal(t, 0, result.Linked)
	assert.Equal(t, 1, result.Suppressed)
	assert.False(t, linkedPair(t, st, inbox, thread))
	suppressed := equivalenceCandidateFor(t, st, inbox, thread)
	assert.Equal(t, store.IdentityMatchStateRejected, suppressed.State)

	// Clearing the classification restores the equivalence, and the next
	// pass links it.
	_, err = st.SetCorrespondentKindContext(ctx, store.SetCorrespondentKindInput{
		ParticipantID: thread, Kind: correspondentkind.Person,
	})
	require.NoError(t, err, "classify as person")
	restored := equivalenceCandidateFor(t, st, inbox, thread)
	assert.Equal(t, store.IdentityMatchStateCandidate, restored.State)
	again, err := st.LinkEquivalentEmailAddressesContext(ctx, true)
	require.NoError(t, err, "forced pass")
	assert.Equal(t, 1, again.Linked)
	assert.True(t, linkedPair(t, st, inbox, thread))
}
