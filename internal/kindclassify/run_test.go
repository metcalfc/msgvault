package kindclassify_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/kindclassify"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

const ownerAddress = "owner@example.com"

type archive struct {
	t       *testing.T
	f       *storetest.Fixture
	owner   int64
	counter int
	sentAt  time.Time
}

func newArchive(t *testing.T) *archive {
	t.Helper()
	f := storetest.New(t)
	require.NoError(t, f.Store.AddAccountIdentity(f.Source.ID, ownerAddress, "manual"))
	owner := f.EnsureParticipant(ownerAddress, "Owner Example", "example.com")
	return &archive{t: t, f: f, owner: owner, sentAt: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
}

func (a *archive) participant(email, name string) int64 {
	_, domain := correspondentkind.SplitEmail(email)
	return a.f.EnsureParticipant(email, name, domain)
}

type mail struct {
	from    int64
	to      int64
	subject string
	listID  string
	headers string
}

func (a *archive) send(m mail) int64 {
	a.t.Helper()
	a.counter++
	a.sentAt = a.sentAt.Add(time.Hour)
	message := &store.Message{
		ConversationID: a.f.ConvID, SourceID: a.f.Source.ID,
		SourceMessageID: fmt.Sprintf("kind-%d", a.counter), MessageType: "email",
		SentAt:   sql.NullTime{Time: a.sentAt, Valid: true},
		SenderID: sql.NullInt64{Int64: m.from, Valid: true}, IsFromMe: m.from == a.owner,
		Subject: sql.NullString{String: m.subject, Valid: m.subject != ""},
		ListID:  sql.NullString{String: m.listID, Valid: m.listID != ""},
	}
	id, err := a.f.Store.UpsertMessage(message)
	require.NoError(a.t, err)
	require.NoError(a.t, a.f.Store.ReplaceMessageRecipients(id, "from", []int64{m.from}, []string{""}))
	require.NoError(a.t, a.f.Store.ReplaceMessageRecipients(id, "to", []int64{m.to}, []string{""}))
	raw := "From: sender@example.com\r\nSubject: " + m.subject + "\r\n" + m.headers + "\r\nBody text.\r\n"
	require.NoError(a.t, a.f.Store.UpsertMessageRaw(id, []byte(raw)))
	return id
}

func (a *archive) sendMany(count int, m mail) {
	for i := range count {
		copyOf := m
		copyOf.subject = fmt.Sprintf("%s %d", m.subject, i+1)
		a.send(copyOf)
	}
}

func (a *archive) kind(participant int64) *store.CorrespondentKindRecord {
	record, err := a.f.Store.GetCorrespondentKindContext(a.t.Context(), participant)
	require.NoError(a.t, err)
	return record
}

func TestRunAppliesDeterministicRulesWithoutJev(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newArchive(t)
	receipts := a.participant("no-reply@shop.example.com", "Example Shop")
	news := a.participant("news@letters.example.org", "Example Letters")
	list := a.participant("builders@lists.example.net", "Builders")
	casey := a.participant("casey@example.com", "Casey Example")
	quiet := a.participant("quiet@example.com", "Quiet Example")
	a.sendMany(6, mail{from: receipts, to: a.owner, subject: "Your receipt"})
	a.sendMany(6, mail{from: news, to: a.owner, subject: "Weekly letter",
		headers: "List-Unsubscribe: <mailto:leave@letters.example.org>\r\nPrecedence: bulk\r\n"})
	a.sendMany(6, mail{from: list, to: a.owner, subject: "Builders digest", listID: "Builders <builders.lists.example.net>"})
	a.sendMany(5, mail{from: casey, to: a.owner, subject: "Lunch"})
	a.send(mail{from: a.owner, to: casey, subject: "Re: Lunch"})
	a.sendMany(2, mail{from: quiet, to: a.owner, subject: "Hello"})

	report, err := kindclassify.Run(t.Context(), a.f.Store, kindclassify.Options{})
	require.NoError(err)
	assert.Equal(4, report.Candidates, "the owner and clusters below the floor are never candidates")
	assert.Equal(map[correspondentkind.Kind]int{correspondentkind.Automated: 2, correspondentkind.MailingList: 1}, report.Rule)
	assert.Equal(1, report.Undecided)
	assert.Empty(report.Jev.Skipped)

	for participant, want := range map[int64]struct {
		kind  correspondentkind.Kind
		actor string
	}{
		receipts: {correspondentkind.Automated, "rule:noreply_address"},
		news:     {correspondentkind.Automated, "rule:bulk_unsubscribe"},
		list:     {correspondentkind.MailingList, "rule:list_address"},
	} {
		record := a.kind(participant)
		assert.Equal(want.kind, record.Kind)
		require.NotNil(record.Source)
		assert.Equal(correspondentkind.SourceRule, *record.Source)
		require.NotNil(record.Actor)
		assert.Equal(want.actor, *record.Actor)
	}
	assert.Nil(a.kind(casey).Source, "a person no rule decides stays unclassified")
	assert.Nil(a.kind(a.owner).Source)

	again, err := kindclassify.Run(t.Context(), a.f.Store, kindclassify.Options{})
	require.NoError(err)
	assert.Equal(1, again.Candidates, "a rerun visits only unclassified clusters")
}

type fakeJev struct {
	mu     sync.Mutex
	bodies []map[string]any
	fail   bool
	answer func(identity map[string]any) (string, map[string]float64)
}

func (j *fakeJev) server(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		j.mu.Lock()
		j.bodies = append(j.bodies, body)
		fail := j.fail
		j.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		identities := body["state"].(map[string]any)["identities"].([]any)
		answers := map[string]any{}
		for i, identity := range identities {
			choice, probabilities := j.answer(identity.(map[string]any))
			answers[kindclassify.QuestionID(i)] = map[string]any{
				"type": "choice", "choice": choice, "probabilities": probabilities, "confidence": probabilities[choice],
			}
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"model": jev.DefaultModel, "answers": answers,
			"usage": map[string]any{"input_tokens": 900, "output_tokens": 40},
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
	cfg.CorrespondentKind = jev.FeatureConfig{Enabled: true}
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
	policy, err := kindclassify.JevFeature().Policy(cfg)
	require.NoError(t, err)
	_, _, err = st.GrantJevFeatureConsent(t.Context(), jev.FeatureCorrespondentKind, policy.Fingerprint, "test")
	require.NoError(t, err)
}

func localPart(identity map[string]any) string {
	addresses := identity["addresses"].([]any)
	if len(addresses) == 0 {
		return ""
	}
	return addresses[0].(map[string]any)["local_part"].(string)
}

// The motivating case: a team alias the owner replied to once looks like a
// person to every rule. Jev calls it a mailing list, so relationship
// rankings leave it out and enrichment skips a profile made of it.
func TestJevClassifiesTeamAliasAsMailingList(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newArchive(t)
	team := a.participant("team@example.com", "Example Team")
	casey := a.participant("casey@example.com", "Casey Example")
	a.sendMany(8, mail{from: team, to: a.owner, subject: "Standup notes"})
	a.send(mail{from: a.owner, to: team, subject: "Re: Standup notes"})
	a.sendMany(6, mail{from: casey, to: a.owner, subject: "Trip"})
	teamPerson, _, err := a.f.Store.CreatePersonFromParticipantContext(t.Context(), team)
	require.NoError(err)

	fake := &fakeJev{answer: func(identity map[string]any) (string, map[string]float64) {
		if localPart(identity) == "team" {
			return kindclassify.OptionMailingList, map[string]float64{
				kindclassify.OptionMailingList: 0.86, kindclassify.OptionIndividualPerson: 0.06, kindclassify.OptionSharedMailbox: 0.08,
			}
		}
		return kindclassify.OptionIndividualPerson, map[string]float64{
			kindclassify.OptionIndividualPerson: 0.93, kindclassify.OptionUnclear: 0.07,
		}
	}}
	service, cfg := jevService(t, fake.server(t).URL, a.f.Store)
	options := kindclassify.Options{Judge: service}

	report, err := kindclassify.Run(t.Context(), a.f.Store, options)
	require.NoError(err)
	assert.Equal("consent_required", report.Jev.Skipped)
	assert.Empty(fake.requests(), "nothing leaves the machine without consent")
	assert.Nil(a.kind(team).Source)

	grantConsent(t, a.f.Store, cfg)
	automatic := options
	automatic.Automatic = true
	report, err = kindclassify.Run(t.Context(), a.f.Store, automatic)
	require.NoError(err)
	assert.Equal("manual_only", report.Jev.Skipped, "unattended runs need automatic = true")
	assert.Empty(fake.requests())

	report, err = kindclassify.Run(t.Context(), a.f.Store, options)
	require.NoError(err)
	assert.Empty(report.Jev.Skipped)
	assert.Equal(1, report.Jev.Requests)
	assert.Equal(map[correspondentkind.Kind]int{correspondentkind.MailingList: 1, correspondentkind.Person: 1}, report.Jev.Kinds)

	requests := fake.requests()
	require.Len(requests, 1)
	policy, err := kindclassify.JevFeature().Policy(cfg)
	require.NoError(err)
	questions := requests[0]["questions"].(map[string]any)
	assert.Len(questions, 2, "one question per identity in the batch")
	for i := range 2 {
		question := questions[kindclassify.QuestionID(i)].(map[string]any)
		assert.Equal("choice", question["type"])
		assert.Equal(policy.Questions[i].Instructions, question["instructions"])
	}
	identities := requests[0]["state"].(map[string]any)["identities"].([]any)
	require.Len(identities, 2)
	teamState := identities[0].(map[string]any)
	assert.ElementsMatch([]string{"label", "addresses", "counts", "list_id_share", "category_shares",
		"header_counts", "subjects_from_them", "subjects_from_owner"}, keys(teamState))
	assert.Equal("Example Team", teamState["label"])
	assert.Equal([]any{map[string]any{"local_part": "team", "domain": "example.com"}}, teamState["addresses"])
	assert.Equal(map[string]any{"sent": 8.0, "received": 1.0, "meetings": 0.0}, teamState["counts"])
	assert.Len(teamState["subjects_from_them"], 5)
	assert.Equal([]any{"Re: Standup notes"}, teamState["subjects_from_owner"])
	encoded, err := json.Marshal(requests[0])
	require.NoError(err)
	assert.NotContains(string(encoded), "Body text", "message bodies never leave the machine")
	assert.NotContains(string(encoded), ownerAddress, "owner identities are never sent")

	record := a.kind(team)
	assert.Equal(correspondentkind.MailingList, record.Kind)
	require.NotNil(record.Source)
	assert.Equal(correspondentkind.SourceJev, *record.Source)
	require.NotNil(record.Confidence)
	assert.InDelta(0.86, *record.Confidence, 1e-9)
	assert.InDelta(0.86, record.Probabilities[kindclassify.OptionMailingList], 1e-9)
	assert.Equal(correspondentkind.Person, a.kind(casey).Kind)

	hidden, err := a.f.Store.RankingHiddenParticipantsContext(t.Context())
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{team: correspondentkind.MailingList}, hidden)
	notAPerson, err := a.f.Store.PersonIsNotAPersonContext(t.Context(), teamPerson.ID)
	require.NoError(err)
	assert.True(notAPerson, "enrichment skips the team alias with outcome not_a_person")
	input, err := a.f.Store.LoadRequestInput(t.Context(), personenrichment.WorkLease{
		PersonID: teamPerson.ID,
		Trigger:  personenrichment.Trigger{Kind: personenrichment.TriggerManual, Generation: "manual:kinds"},
	})
	require.NoError(err)
	assert.True(input.NotAPerson)

	// A user override outranks the judgment.
	_, err = a.f.Store.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: team, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	hidden, err = a.f.Store.RankingHiddenParticipantsContext(t.Context())
	require.NoError(err)
	assert.Empty(hidden)
}

func keys(values map[string]any) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}

func TestJevUnclearJudgmentsWaitInReview(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newArchive(t)
	desk := a.participant("frontdesk@example.com", "Front Desk")
	a.sendMany(6, mail{from: desk, to: a.owner, subject: "Visitor"})
	fake := &fakeJev{answer: func(map[string]any) (string, map[string]float64) {
		return kindclassify.OptionIndividualPerson, map[string]float64{
			kindclassify.OptionIndividualPerson: 0.45, kindclassify.OptionSharedMailbox: 0.40, kindclassify.OptionUnclear: 0.15,
		}
	}}
	service, cfg := jevService(t, fake.server(t).URL, a.f.Store)
	grantConsent(t, a.f.Store, cfg)

	_, err := kindclassify.Run(t.Context(), a.f.Store, kindclassify.Options{Judge: service})
	require.NoError(err)
	assert.Equal(correspondentkind.Unclear, a.kind(desk).Kind)

	unclear, err := a.f.Store.ListCorrespondentKindsContext(t.Context(), store.CorrespondentKindListFilter{Kind: correspondentkind.Unclear})
	require.NoError(err)
	require.Len(unclear, 1)
	assert.Equal(desk, unclear[0].CanonicalID)
	all, err := a.f.Store.ListCorrespondentKindsContext(t.Context(), store.CorrespondentKindListFilter{})
	require.NoError(err)
	assert.Empty(all, "unclear is a review queue, not a record marked as not a person")
	notPerson, err := a.f.Store.NotPersonParticipantsContext(t.Context())
	require.NoError(err)
	assert.Empty(notPerson, "unclear never removes anyone from People lists or enrichment")
	hidden, err := a.f.Store.RankingHiddenParticipantsContext(t.Context())
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{desk: correspondentkind.Unclear}, hidden,
		"rankings keep only clusters with individual_person at or above the threshold")
}

func TestJevBatchesTenIdentitiesAndStopsOnProviderFailure(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newArchive(t)
	for i := range 12 {
		sender := a.participant(fmt.Sprintf("writer%02d@example.com", i), fmt.Sprintf("Writer %02d", i))
		a.sendMany(5, mail{from: sender, to: a.owner, subject: "Note"})
	}
	fake := &fakeJev{fail: true, answer: func(map[string]any) (string, map[string]float64) {
		return kindclassify.OptionIndividualPerson, map[string]float64{kindclassify.OptionIndividualPerson: 0.9, kindclassify.OptionUnclear: 0.1}
	}}
	endpoint := fake.server(t).URL
	service, cfg := jevService(t, endpoint, a.f.Store)
	grantConsent(t, a.f.Store, cfg)

	report, err := kindclassify.Run(t.Context(), a.f.Store, kindclassify.Options{Judge: service})
	require.NoError(err, "a provider failure never fails the run")
	assert.Equal("provider_error", report.Jev.Skipped)
	assert.Equal(12, report.Undecided)
	require.Len(fake.requests(), 1, "the run stops asking after a failure")

	fake.mu.Lock()
	fake.fail = false
	fake.bodies = nil
	fake.mu.Unlock()
	// A fresh service stands in for a later run once the breaker cooled.
	service, _ = jevService(t, endpoint, a.f.Store)
	report, err = kindclassify.Run(t.Context(), a.f.Store, kindclassify.Options{Judge: service})
	require.NoError(err)
	assert.Empty(report.Jev.Skipped)
	assert.Equal(12, report.Jev.Judged)
	sizes := []int{}
	for _, request := range fake.requests() {
		sizes = append(sizes, len(request["state"].(map[string]any)["identities"].([]any)))
	}
	assert.Equal([]int{kindclassify.BatchSize, 2}, sizes)
	assert.Zero(report.Undecided)
	for _, request := range fake.requests() {
		assert.False(strings.Contains(fmt.Sprint(request), "Body text"))
	}
}
