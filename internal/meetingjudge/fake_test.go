package meetingjudge_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/calsync"
	"go.kenn.io/msgvault/internal/gcal"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

const ownerAddress = "owner@example.com"

// fakeJev is a local System One endpoint. answer maps one question's state
// item (and its question ID) to a choice and probabilities; every request
// body is recorded so tests can assert exactly what left the machine.
type fakeJev struct {
	mu     sync.Mutex
	bodies []map[string]any
	raw    []string
	answer func(questionID string, state map[string]any) (string, map[string]float64)
}

func (j *fakeJev) server(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		j.mu.Lock()
		j.bodies = append(j.bodies, body)
		j.raw = append(j.raw, string(raw))
		j.mu.Unlock()
		state := body["state"].(map[string]any)
		answers := map[string]any{}
		for id := range body["questions"].(map[string]any) {
			choice, probabilities := j.answer(id, state)
			answers[id] = map[string]any{
				"type": "choice", "choice": choice, "probabilities": probabilities, "confidence": probabilities[choice],
			}
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"model": jev.DefaultModel, "answers": answers,
			"usage": map[string]any{"input_tokens": 700, "output_tokens": 30},
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

func (j *fakeJev) rawRequests() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.raw...)
}

// jevService is a real service against the fake endpoint with every
// meeting feature enabled.
func jevService(t *testing.T, endpoint string, st *store.Store) (*jev.Service, jev.Config) {
	t.Helper()
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	cfg.MeetingEventKind = jev.FeatureConfig{Enabled: true}
	service, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg, nil },
		Consents:   st,
		Ledger:     st,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
		Logger:     slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	return service, cfg
}

func grantConsent(t *testing.T, st *store.Store, cfg jev.Config, spec jev.FeatureSpec) {
	t.Helper()
	policy, err := spec.Policy(cfg)
	require.NoError(t, err)
	_, _, err = st.GrantJevFeatureConsent(t.Context(), spec.Name, policy.Fingerprint, "test")
	require.NoError(t, err)
}

// syncCalendar stores events through the real calendar sync path.
func syncCalendar(t *testing.T, st *store.Store, events ...gcal.Event) {
	t.Helper()
	mock := gcal.NewMockAPI()
	mock.Calendars = []gcal.Calendar{{ID: "primary", Summary: "Work", AccessRole: "owner", Primary: true, TimeZone: "UTC"}}
	mock.FullEvents["primary"] = [][]gcal.Event{events}
	syncer := calsync.New(mock, st, calsync.Options{AccountEmail: ownerAddress}).WithLogger(slog.New(slog.DiscardHandler))
	_, err := syncer.Full(t.Context())
	require.NoError(t, err)
}

var eventStart = time.Date(2026, 5, 4, 15, 0, 0, 0, time.UTC)

func calendarEvent(id, summary string, minutes int, attendees ...string) gcal.Event {
	event := gcal.Event{
		ID: id, Status: gcal.StatusConfirmed, Summary: summary,
		Description: "Agenda: private notes that must never leave the machine.",
		Organizer:   gcal.Person{Email: ownerAddress, DisplayName: "Owner Example", Self: true},
		Start:       gcal.EventDateTime{DateTime: eventStart, TimeZone: "UTC"},
		End:         gcal.EventDateTime{DateTime: eventStart.Add(time.Duration(minutes) * time.Minute)},
		Attendees:   []gcal.Attendee{{Email: ownerAddress, DisplayName: "Owner Example", Self: true, ResponseStatus: "accepted"}},
	}
	for _, address := range attendees {
		event.Attendees = append(event.Attendees, gcal.Attendee{Email: address, ResponseStatus: "accepted"})
	}
	return event
}

func manyAttendees(count int, domain string) []string {
	addresses := make([]string, count)
	for i := range addresses {
		addresses[i] = fmt.Sprintf("person%02d@%s", i, domain)
	}
	return addresses
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	return testutil.NewTestStore(t)
}
