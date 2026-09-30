package cleanupsuggest_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/cleanupsuggest"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

const ownerAddress = "owner@example.com"

type archive struct {
	t      *testing.T
	f      *storetest.Fixture
	owner  int64
	labels map[string]int64
	next   int
	sentAt time.Time
}

func newArchive(t *testing.T) *archive {
	t.Helper()
	f := storetest.New(t)
	require.NoError(t, f.Store.AddAccountIdentity(f.Source.ID, ownerAddress, "manual"))
	owner := f.EnsureParticipant(ownerAddress, "Owner Example", "example.com")
	labels := f.EnsureLabels(map[string]string{
		"SPAM": "SPAM", "INBOX": "INBOX", "CATEGORY_PROMOTIONS": "CATEGORY_PROMOTIONS", "Label_1": "Private folder",
	}, "system")
	return &archive{t: t, f: f, owner: owner, labels: labels, sentAt: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)}
}

type mail struct {
	from    int64
	to      int64
	subject string
	headers string
	body    string
	labels  []string
	thread  string
}

func (a *archive) send(m mail) int64 {
	a.t.Helper()
	a.next++
	a.sentAt = a.sentAt.Add(time.Hour)
	thread := m.thread
	if thread == "" {
		thread = fmt.Sprintf("thread-%d", a.next)
	}
	conversation, err := a.f.Store.EnsureConversation(a.f.Source.ID, thread, thread)
	require.NoError(a.t, err)
	id, err := a.f.Store.UpsertMessage(&store.Message{
		ConversationID: conversation, SourceID: a.f.Source.ID,
		SourceMessageID: fmt.Sprintf("cleanup-%d", a.next), MessageType: "email",
		SentAt:   sql.NullTime{Time: a.sentAt, Valid: true},
		SenderID: sql.NullInt64{Int64: m.from, Valid: true}, IsFromMe: m.from == a.owner,
		Subject: sql.NullString{String: m.subject, Valid: m.subject != ""},
	})
	require.NoError(a.t, err)
	require.NoError(a.t, a.f.Store.ReplaceMessageRecipients(id, "from", []int64{m.from}, []string{""}))
	if m.to > 0 {
		require.NoError(a.t, a.f.Store.ReplaceMessageRecipients(id, "to", []int64{m.to}, []string{""}))
	}
	raw := "From: sender@example.com\r\nSubject: " + m.subject + "\r\n" + m.headers + "\r\n" + m.body + "\r\n"
	require.NoError(a.t, a.f.Store.UpsertMessageRaw(id, []byte(raw)))
	require.NoError(a.t, a.f.Store.UpsertMessageBody(id, sql.NullString{String: m.body, Valid: true}, sql.NullString{}))
	ids := []int64{}
	for _, label := range m.labels {
		ids = append(ids, a.labels[label])
	}
	if len(ids) > 0 {
		require.NoError(a.t, a.f.Store.AddMessageLabels(id, ids))
	}
	return id
}

type fakeJev struct {
	mu     sync.Mutex
	bodies []map[string]any
	answer func(message map[string]any) (impersonation, pressure float64, category string, probabilities map[string]float64)
}

func (j *fakeJev) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			State struct {
				Messages []map[string]any `json:"messages"`
			} `json:"state"`
		}
		var recorded map[string]any
		if !assert.NoError(t, json.Unmarshal(raw, &body)) || !assert.NoError(t, json.Unmarshal(raw, &recorded)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		j.mu.Lock()
		j.bodies = append(j.bodies, recorded)
		j.mu.Unlock()
		answers := map[string]any{}
		for i, message := range body.State.Messages {
			impersonation, pressure, category, probabilities := j.answer(message)
			answers[cleanupsuggest.ImpersonationID(i)] = map[string]any{"type": "noul", "noul": impersonation}
			answers[cleanupsuggest.PressureID(i)] = map[string]any{"type": "noul", "noul": pressure}
			answers[cleanupsuggest.CategoryID(i)] = map[string]any{
				"type": "choice", "choice": category, "probabilities": probabilities, "confidence": probabilities[category],
			}
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"model": jev.DefaultModel, "answers": answers,
			"usage": map[string]any{"input_tokens": 1200, "output_tokens": 60},
		}))
	}))
	t.Cleanup(server.Close)
	return server
}

func (j *fakeJev) requests() []map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]map[string]any(nil), j.bodies...)
}

func jevService(t *testing.T, endpoint string, st *store.Store) (*jev.Service, jev.Config) {
	t.Helper()
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	cfg.CleanupSuggestions = jev.CleanupSuggestionsConfig{Enabled: true}
	service, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg, nil },
		Consents:   st,
		Ledger:     st,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
	})
	require.NoError(t, err)
	return service, cfg
}

func grantConsent(t *testing.T, st *store.Store, cfg jev.Config) {
	t.Helper()
	policy, err := cleanupsuggest.JevFeature().Policy(cfg)
	require.NoError(t, err)
	_, _, err = st.GrantJevFeatureConsent(t.Context(), jev.FeatureCleanupSuggestions, policy.Fingerprint, "test")
	require.NoError(t, err)
}

func keys(values map[string]any) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}

const phishHeaders = "Authentication-Results: attacker.example; spf=pass; dkim=pass; dmarc=pass\r\n" +
	"Authentication-Results: mx.google.com; spf=fail smtp.mailfrom=bank.example;" +
	" dkim=fail; dmarc=fail header.from=bank.example\r\nReply-To: <refunds@collector.example.org>\r\n"

func TestRunJudgesThePoolAndOnlyListsSuggestions(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newArchive(t)
	st := a.f.Store
	bank := a.f.EnsureParticipant("alerts@bank.example", "Example Bank Security", "bank.example")
	shop := a.f.EnsureParticipant("news@shop.example", "Example Shop", "shop.example")
	friend := a.f.EnsureParticipant("casey@example.net", "Casey Example", "example.net")
	_, err := st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: friend, Kind: correspondentkind.Person,
	})
	require.NoError(err)

	phish := a.send(mail{from: bank, subject: "Your account is locked", headers: phishHeaders,
		body: "Verify now at https://login.bank-verify.example/unlock or lose access.", labels: []string{"SPAM", "Label_1"}})
	promo := a.send(mail{from: shop, to: a.owner, subject: "Weekend sale",
		headers: "Authentication-Results: mx.google.com; spf=pass; dkim=pass; dmarc=pass\r\n",
		body:    "See the sale at https://links.shop.example/sale " + strings.Repeat("deals ", 200),
		labels:  []string{"CATEGORY_PROMOTIONS"}})
	personalLooking := a.send(mail{from: shop, to: a.owner, subject: "Photos from the trip",
		body: "Here are the photos https://photos.example.org/album", labels: []string{"CATEGORY_PROMOTIONS"}})
	fromPerson := a.send(mail{from: friend, to: a.owner, subject: "Party",
		body: "Invite at https://invite.example.org/x", labels: []string{"CATEGORY_PROMOTIONS"}})
	noLinks := a.send(mail{from: shop, to: a.owner, subject: "Plain text", body: "No links here.",
		labels: []string{"SPAM"}})
	replied := a.send(mail{from: shop, to: a.owner, subject: "Question", thread: "replied",
		body: "Reply at https://shop.example/q", labels: []string{"CATEGORY_PROMOTIONS"}})
	a.send(mail{from: a.owner, to: shop, subject: "Re: Question", thread: "replied", body: "Answer."})
	inbox := a.send(mail{from: shop, to: a.owner, subject: "Receipt", body: "https://shop.example/r", labels: []string{"INBOX"}})

	fake := &fakeJev{answer: func(message map[string]any) (float64, float64, string, map[string]float64) {
		switch message["subject"] {
		case "Your account is locked":
			return 0.9, 0.95, cleanupsuggest.CategoryPhishing, map[string]float64{cleanupsuggest.CategoryPhishing: 0.9, cleanupsuggest.CategoryOtherJunk: 0.1}
		case "Photos from the trip":
			return 0.05, 0.02, cleanupsuggest.CategoryPersonal, map[string]float64{cleanupsuggest.CategoryPersonal: 0.7, cleanupsuggest.CategoryMarketing: 0.3}
		default:
			return 0.05, 0.3, cleanupsuggest.CategoryMarketing, map[string]float64{cleanupsuggest.CategoryMarketing: 0.95, cleanupsuggest.CategoryOtherJunk: 0.05}
		}
	}}
	server := fake.server(t)
	service, cfg := jevService(t, server.URL, st)

	report, err := cleanupsuggest.Run(t.Context(), st, cleanupsuggest.Options{Judge: service})
	require.NoError(err)
	assert.Equal("consent_required", report.Skipped, "nothing leaves without consent")
	assert.Empty(fake.requests())
	assert.Equal(3, report.Eligible)
	assert.Equal(1, report.PersonSenders)
	assert.Equal(1, report.NoLinks)

	grantConsent(t, st, cfg)
	report, err = cleanupsuggest.Run(t.Context(), st, cleanupsuggest.Options{Judge: service})
	require.NoError(err)
	assert.Empty(report.Skipped)
	assert.Equal(1, report.Requests, "three messages fit one request of four")
	assert.Equal(3, report.Judged)
	assert.Equal(1, report.Suspected)
	assert.Equal(1, report.Keep)

	requests := fake.requests()
	require.Len(requests, 1)
	state, ok := requests[0]["state"].(map[string]any)
	require.True(ok)
	messages, ok := state["messages"].([]any)
	require.True(ok)
	require.Len(messages, 3)
	byID := map[string]map[string]any{}
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		require.True(ok)
		assert.Equal([]string{"addressed_as", "authentication", "body_start", "from_domain", "from_name",
			"labels", "link_hosts", "reply_to_domain", "sender_kind", "subject", "thread_replied"}, keys(message))
		bodyStart, _ := message["body_start"].(string)
		assert.LessOrEqual(len([]rune(bodyStart)), cleanupsuggest.BodyChars)
		subject, _ := message["subject"].(string)
		byID[subject] = message
	}
	phishState := byID["Your account is locked"]
	assert.Equal("collector.example.org", phishState["reply_to_domain"])
	assert.Equal(map[string]any{"spf": "fail", "dkim": "fail", "dmarc": "fail"}, phishState["authentication"])
	assert.Equal([]any{"SPAM"}, phishState["labels"], "the owner's own label names are never sent")
	assert.Equal("bcc_or_undisclosed", phishState["addressed_as"])
	assert.Equal("to_or_cc", byID["Weekend sale"]["addressed_as"])
	questions, ok := requests[0]["questions"].(map[string]any)
	require.True(ok)
	assert.Len(questions, 9, "a short batch asks only its own questions")

	suspected, err := st.ListCleanupSuggestionsContext(t.Context(), store.CleanupSuggestionFilter{
		MinScore: cleanupsuggest.SuspectThreshold, Limit: 10,
	})
	require.NoError(err)
	require.Len(suspected, 1)
	assert.Equal(phish, suspected[0].MessageID)
	assert.Equal(cleanupsuggest.CategoryPhishing, suspected[0].Category)
	assert.Contains(suspected[0].Signals, cleanupsuggest.SignalDMARCFail)
	assert.Equal("Example Bank Security", suspected[0].FromName)

	all, err := st.ListCleanupSuggestionsContext(t.Context(), store.CleanupSuggestionFilter{Limit: 10})
	require.NoError(err)
	judged := []int64{}
	for _, row := range all {
		judged = append(judged, row.MessageID)
	}
	slices.Sort(judged)
	assert.Equal([]int64{phish, promo, personalLooking}, judged)
	for _, left := range []int64{fromPerson, noLinks, replied, inbox} {
		assert.NotContains(judged, left)
	}

	protections, err := st.DeletionProtectionsContext(t.Context(), []int64{phish})
	require.NoError(err)
	assert.Empty(protections, "a suggestion never stages, deletes, or protects anything")

	again, err := cleanupsuggest.Run(t.Context(), st, cleanupsuggest.Options{Judge: service})
	require.NoError(err)
	assert.Zero(again.Eligible, "judged messages are not sent again")
	assert.Len(fake.requests(), 1)
}

func TestRunStopsJevOnRevokedConsentAndWithoutAJudge(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newArchive(t)
	st := a.f.Store
	bank := a.f.EnsureParticipant("alerts@bank.example", "Example Bank Security", "bank.example")
	for i := range 6 {
		a.send(mail{from: bank, subject: fmt.Sprintf("Locked %d", i), headers: phishHeaders,
			body: "https://login.bank-verify.example/" + strconv.Itoa(i), labels: []string{"SPAM"}})
	}

	report, err := cleanupsuggest.Run(t.Context(), st, cleanupsuggest.Options{})
	require.NoError(err)
	assert.Equal(6, report.Eligible)
	assert.Zero(report.Requests, "without a judge the run only reports the pool")

	fake := &fakeJev{answer: func(map[string]any) (float64, float64, string, map[string]float64) {
		return 0.9, 0.9, cleanupsuggest.CategoryPhishing, map[string]float64{cleanupsuggest.CategoryPhishing: 1}
	}}
	server := fake.server(t)
	service, cfg := jevService(t, server.URL, st)
	grantConsent(t, st, cfg)
	_, err = st.RevokeJevFeatureConsent(t.Context(), jev.FeatureCleanupSuggestions, "test")
	require.NoError(err)

	report, err = cleanupsuggest.Run(t.Context(), st, cleanupsuggest.Options{Judge: service, Limit: 5})
	require.NoError(err)
	assert.Equal("consent_required", report.Skipped)
	assert.Equal(5, report.Eligible)
	assert.Empty(fake.requests(), "revoked consent never sends")
}
