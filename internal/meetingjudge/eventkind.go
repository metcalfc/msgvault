// Package meetingjudge asks Jev narrow questions about meetings: what kind
// of calendar event a series is, and which attendee owns an action item the
// meeting tool left unassigned. Answers are stored as inputs to
// deterministic code (meeting weights, the action item assignee filter);
// nothing here fails a user-facing operation when Jev is off or refuses.
package meetingjudge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/meetingweight"
	"go.kenn.io/msgvault/internal/store"
)

// Judge asks a subset of a feature's consented questions. *jev.Service
// implements it; every gate is rechecked on each call.
type Judge interface {
	JudgeQuestions(
		ctx context.Context, spec jev.FeatureSpec, automatic bool, state any, questionIDs []string, deadline time.Time,
	) (jev.Response, error)
}

// EventBatchSize is how many calendar series one event kind request carries.
const EventBatchSize = 10

// maxTitleRunes caps an event title in the state.
const maxTitleRunes = 160

// EventKindQuestionID is the question asking about events[i].
func EventKindQuestionID(i int) string {
	return "event_kind_" + strconv.Itoa(i)
}

var eventKindCriteria = map[string]string{
	string(meetingweight.KindOneOnOne): "Two people meeting: a one-on-one, a check-in, or an interview " +
		"with one other person.",
	string(meetingweight.KindSmallWorkingMeeting): "A few people working together: a team sync, a " +
		"planning or design session, a customer or partner call.",
	string(meetingweight.KindLargeGroupOrAllHands): "A large group: an all-hands, a town hall, a " +
		"department meeting, or a broadcast where most attendees listen.",
	string(meetingweight.KindExternalWebinar): "A webinar, a marketing event, a product demo for many " +
		"registrants, or a conference session run by an outside organizer.",
	string(meetingweight.KindPersonalHoldLogistics): "Not a meeting with others: a personal hold, a " +
		"reminder, travel, a commute, a meal block, or other logistics.",
	string(meetingweight.KindSocial): "A social gathering: a team lunch, a party, drinks, or a " +
		"celebration.",
}

// EventKindFeature is the exact policy the meeting event kind feature
// consents to: one Choice per calendar series, ten series per request, over
// the state fields listed. No attendee names or addresses, no descriptions,
// and nothing that identifies the owner leave the machine.
func EventKindFeature() jev.FeatureSpec {
	questions := make([]jev.Question, EventBatchSize)
	for i := range questions {
		questions[i] = jev.Question{
			ID: EventKindQuestionID(i), Type: jev.QuestionChoice,
			Instructions: fmt.Sprintf("What kind of calendar event is `events[%d]`? Judge from its title, "+
				"length, recurrence, and attendee counts.", i),
			Criteria: eventKindCriteria,
		}
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureMeetingEventKind,
		Title: "Meeting event kind",
		Purpose: "Decide whether a calendar series is a one-on-one, a small working meeting, a large " +
			"group or all-hands, an outside webinar, a personal hold, or a social event, so relationship " +
			"rankings count a real meeting with someone more than an all-hands or a webinar they also attended.",
		Questions: questions,
		StateFields: []string{
			"events[].title",
			"events[].all_day",
			"events[].duration_minutes",
			"events[].recurring",
			"events[].occurrences",
			"events[].attendee_count",
			"events[].external_attendee_count",
			"events[].organized_by_owner",
		},
	}
}

// EventState is the state sent about one calendar series. Its fields are
// exactly EventKindFeature's StateFields.
type EventState struct {
	Title                 string `json:"title"`
	AllDay                bool   `json:"all_day"`
	DurationMinutes       int    `json:"duration_minutes"`
	Recurring             bool   `json:"recurring"`
	Occurrences           int    `json:"occurrences"`
	AttendeeCount         int    `json:"attendee_count"`
	ExternalAttendeeCount int    `json:"external_attendee_count"`
	OrganizedByOwner      bool   `json:"organized_by_owner"`
}

// EventKindState is one event kind request's state.
type EventKindState struct {
	Events []EventState `json:"events"`
}

// EventKindStore is the archive authority an event kind run needs.
// *store.Store implements it.
type EventKindStore interface {
	CalendarEventKindCandidatesContext(ctx context.Context, limit int) ([]store.CalendarEventKindCandidate, error)
	WriteCalendarEventKindsContext(ctx context.Context, kinds []store.CalendarEventKind) (int, error)
}

// EventKindOptions configure one event kind run.
type EventKindOptions struct {
	// Limit caps how many series one run visits; zero means no cap.
	Limit int
	// Judge is nil when Jev is off; the run then only records series that
	// are not meetings.
	Judge Judge
	// Automatic marks unattended callers such as the cache build.
	Automatic bool
	Logger    *slog.Logger
}

// EventKindReport summarizes an event kind run. It never contains state.
type EventKindReport struct {
	Candidates  int                        `json:"candidates"`
	NotMeetings int                        `json:"not_meetings"`
	Requests    int                        `json:"requests"`
	Judged      int                        `json:"judged"`
	Kinds       map[meetingweight.Kind]int `json:"kinds"`
	// Confident counts judgments at or above meetingweight.KindThreshold,
	// the ones that set a meeting weight.
	Confident int `json:"confident"`
	// Skipped is the safe-failure category that stopped Jev for this run,
	// empty when every batch was asked.
	Skipped string `json:"skipped,omitempty"`
}

// storeError marks a failure writing results, which fails the run.
type storeError struct{ err error }

func (e storeError) Error() string { return e.err.Error() }
func (e storeError) Unwrap() error { return e.err }

func isStoreError(err error) bool {
	var target storeError
	return errors.As(err, &target)
}

// RunEventKinds classifies calendar series that have no kind yet. A series
// with no event that is a meeting is recorded by rule and never sent. The
// rest are asked of Jev ten per request when a Judge is present; any gate,
// budget, or provider failure stops Jev for the rest of the run and leaves
// those series for a later run. Only a store failure fails the run.
func RunEventKinds(ctx context.Context, st EventKindStore, options EventKindOptions) (EventKindReport, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	report := EventKindReport{Kinds: map[meetingweight.Kind]int{}}
	candidates, err := st.CalendarEventKindCandidatesContext(ctx, options.Limit)
	if err != nil {
		return report, fmt.Errorf("list calendar event kind candidates: %w", err)
	}
	report.Candidates = len(candidates)
	var rules []store.CalendarEventKind
	var meetings []store.CalendarEventKindCandidate
	for _, candidate := range candidates {
		if candidate.NotAMeeting {
			rules = append(rules, store.CalendarEventKind{
				ConversationID: candidate.ConversationID, Kind: store.CalendarEventKindNotAMeeting,
				Source: store.CalendarEventKindSourceRule, Confidence: 1,
			})
			continue
		}
		meetings = append(meetings, candidate)
	}
	written, err := st.WriteCalendarEventKindsContext(ctx, rules)
	if err != nil {
		return report, fmt.Errorf("write rule calendar event kinds: %w", err)
	}
	report.NotMeetings = written
	if options.Judge == nil {
		return report, nil
	}
	for start := 0; start < len(meetings); start += EventBatchSize {
		batch := meetings[start:min(start+EventBatchSize, len(meetings))]
		if err := judgeEventBatch(ctx, st, options, batch, &report); err != nil {
			if isStoreError(err) {
				return report, err
			}
			report.Skipped = jev.Skipped(err)
			options.Logger.Info("meeting event kind: jev skipped",
				"feature", jev.FeatureMeetingEventKind, "category", report.Skipped)
			break
		}
	}
	return report, nil
}

// judgeEventBatch asks about one batch and stores the answers. A batch the
// client rejects as too large is halved until it fits.
func judgeEventBatch(
	ctx context.Context, st EventKindStore, options EventKindOptions,
	batch []store.CalendarEventKindCandidate, report *EventKindReport,
) error {
	state := EventKindState{Events: make([]EventState, 0, len(batch))}
	ids := make([]string, 0, len(batch))
	for i, candidate := range batch {
		state.Events = append(state.Events, eventStateFor(candidate))
		ids = append(ids, EventKindQuestionID(i))
	}
	response, err := options.Judge.JudgeQuestions(ctx, EventKindFeature(), options.Automatic, state, ids, time.Time{})
	if errors.Is(err, jev.ErrRequestBounds) && len(batch) > 1 {
		half := len(batch) / 2
		if err := judgeEventBatch(ctx, st, options, batch[:half], report); err != nil {
			return err
		}
		return judgeEventBatch(ctx, st, options, batch[half:], report)
	}
	report.Requests++
	if err != nil {
		return err
	}
	kinds := make([]store.CalendarEventKind, 0, len(batch))
	for i, candidate := range batch {
		answer, ok := response.Answers[EventKindQuestionID(i)]
		if !ok {
			return fmt.Errorf("%w: answer %d missing", jev.ErrInvalidResponse, i)
		}
		kind := meetingweight.Kind(answer.Choice)
		if _, known := meetingweight.KindWeight(kind); !known {
			return fmt.Errorf("%w: unknown event kind option", jev.ErrInvalidResponse)
		}
		confidence := answer.Probabilities[answer.Choice]
		kinds = append(kinds, store.CalendarEventKind{
			ConversationID: candidate.ConversationID, Kind: string(kind),
			Source: store.CalendarEventKindSourceJev, Confidence: confidence,
			Probabilities: answer.Probabilities, Model: response.Model,
		})
	}
	written, err := st.WriteCalendarEventKindsContext(ctx, kinds)
	if err != nil {
		return storeError{fmt.Errorf("write jev calendar event kinds: %w", err)}
	}
	report.Judged += written
	for _, kind := range kinds {
		report.Kinds[meetingweight.Kind(kind.Kind)]++
		if kind.Confidence >= meetingweight.KindThreshold {
			report.Confident++
		}
	}
	return nil
}

func eventStateFor(candidate store.CalendarEventKindCandidate) EventState {
	return EventState{
		Title:                 truncateRunes(strings.Join(strings.Fields(candidate.Title), " "), maxTitleRunes),
		AllDay:                candidate.AllDay,
		DurationMinutes:       candidate.DurationMinutes,
		Recurring:             candidate.Recurring,
		Occurrences:           candidate.Occurrences,
		AttendeeCount:         candidate.AttendeeCount,
		ExternalAttendeeCount: candidate.ExternalAttendeeCount,
		OrganizedByOwner:      candidate.OrganizedByOwner,
	}
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
