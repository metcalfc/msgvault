package store_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

// TestEnsureParticipant_Concurrent verifies that concurrent imports of the
// same email converge on one participant without unique-constraint errors.
func TestEnsureParticipant_Concurrent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	const workers = 50
	const email = "concurrent@example.test"
	ids := make([]int64, workers)
	errs := runConcurrentStoreWrites(workers, func(i int) error {
		id, err := st.EnsureParticipant(email, "Concurrent User", "example.test")
		ids[i] = id
		return err
	})
	for i, err := range errs {
		require.NoError(err, "writer %d: EnsureParticipant", i)
	}
	require.Positive(ids[0])
	for i, id := range ids {
		assert.Equal(ids[0], id, "writer %d must observe the same participant", i)
	}

	var count int
	require.NoError(st.DB().QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM participants WHERE email_address = ?", email,
	).Scan(&count))
	assert.Equal(1, count)
}

// TestEnsureParticipantByPhone_Concurrent exercises the same import race for
// phone participants and their partial unique index.
func TestEnsureParticipantByPhone_Concurrent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	const workers = 50
	const phone = "+15555550100"
	ids := make([]int64, workers)
	errs := runConcurrentStoreWrites(workers, func(i int) error {
		id, err := st.EnsureParticipantByPhone(phone, "Concurrent User", "whatsapp")
		ids[i] = id
		return err
	})
	for i, err := range errs {
		require.NoError(err, "writer %d: EnsureParticipantByPhone", i)
	}
	require.Positive(ids[0])
	for i, id := range ids {
		assert.Equal(ids[0], id, "writer %d must observe the same participant", i)
	}

	var count int
	require.NoError(st.DB().QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM participants WHERE phone_number = ?", phone,
	).Scan(&count))
	assert.Equal(1, count)
}

// TestAddAccountIdentity_Concurrent verifies that concurrent confirmations of
// one address preserve the union of signals in exactly one identity row.
func TestAddAccountIdentity_Concurrent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "account@example.test")
	require.NoError(err)
	signals := []string{"manual", "account-identifier", "header"}
	errs := runConcurrentStoreWrites(50, func(i int) error {
		return st.AddAccountIdentity(source.ID, "identity@example.test", signals[i%len(signals)])
	})
	for i, err := range errs {
		require.NoError(err, "writer %d: AddAccountIdentity", i)
	}

	identities, err := st.ListAccountIdentities(source.ID)
	require.NoError(err)
	require.Len(identities, 1)
	assert.ElementsMatch(signals, strings.Split(identities[0].SourceSignal, ","))
}

// TestUpsertAttachment_Concurrent verifies that concurrent saves of the same
// message/content-hash attachment succeed and persist exactly one row.
func TestUpsertAttachment_Concurrent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := storetest.New(t)
	messageID := f.NewMessage().WithSourceMessageID("concurrent-attachment").Create(t, f.Store)
	const contentHash = "abcd1234"
	errs := runConcurrentStoreWrites(20, func(_ int) error {
		return f.Store.UpsertAttachment(
			messageID, "file.pdf", "application/pdf", "ab/abcd1234", contentHash, 1024,
		)
	})
	for i, err := range errs {
		require.NoError(err, "writer %d: UpsertAttachment", i)
	}

	var count int
	require.NoError(f.Store.DB().QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM attachments WHERE message_id = ? AND content_hash = ?",
		messageID, contentHash,
	).Scan(&count))
	assert.Equal(1, count)
}

// runConcurrentStoreWrites releases every worker from one barrier. Each worker
// owns its result slot; assertions run in the test goroutine after all return.
func runConcurrentStoreWrites(workers int, write func(int) error) []error {
	errs := make([]error, workers)
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(workers)
	for i := range workers {
		done.Go(func() {
			ready.Done()
			<-start
			errs[i] = write(i)
		})
	}
	ready.Wait()
	close(start)
	done.Wait()
	return errs
}
