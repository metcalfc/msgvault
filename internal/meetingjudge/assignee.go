package meetingjudge

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
)

// Assignee judgment limits and thresholds.
const (
	// MaxAssigneeAttendees is how many attendees one request can offer. A
	// meeting with more is not sent.
	MaxAssigneeAttendees = 12
	// AssigneeItemsPerRequest is how many action items one request asks
	// about; a meeting with more is asked in several requests.
	AssigneeItemsPerRequest = 8
	// AssigneeThreshold is the probability an attendee or the owner needs
	// before it is stored as the item's assignee.
	AssigneeThreshold = 0.80
)

// Assignee options besides the attendee slots.
const (
	OptionOwner         = "owner"
	OptionNoneOrUnclear = "none_or_unclear"
)

// Title and description caps for the state.
const (
	maxActionTitleRunes       = 200
	maxActionDescriptionRunes = 500
	maxAttendeeLabelRunes     = 120
)

// tooManyAttendeesModel marks the stored "none" of a meeting that was not
// sent because it has more attendees than one request can offer.
const tooManyAttendeesModel = "rule:too_many_attendees"

// AttendeeKey is the option and state key of the i-th (zero-based) attendee.
func AttendeeKey(i int) string { return "attendee_" + strconv.Itoa(i+1) }

// ItemKey is the state key of the i-th (zero-based) action item.
func ItemKey(i int) string { return "item_" + strconv.Itoa(i+1) }

// AssigneeQuestionID is the question asking about the i-th item.
func AssigneeQuestionID(i int) string { return "assignee_" + strconv.Itoa(i+1) }

// AssigneeFeature is the exact policy the meeting action assignee feature
// consents to. Every request carries the questions for the items it sends,
// worded exactly as here.
func AssigneeFeature() jev.FeatureSpec {
	criteria := make(map[string]string, MaxAssigneeAttendees+2)
	for i := range MaxAssigneeAttendees {
		key := AttendeeKey(i)
		criteria[key] = fmt.Sprintf("`attendees.%s` is the one person responsible for the item.", key)
	}
	criteria[OptionOwner] = "The person whose meeting notes these are, who is not in `attendees`: " +
		"the notes' \"I\", \"me\", or \"my\", or an item addressed to the reader."
	criteria[OptionNoneOrUnclear] = "No single person: the whole group, someone not listed, or the text " +
		"does not say who."
	questions := make([]jev.Question, AssigneeItemsPerRequest)
	for i := range questions {
		item := "action_items." + ItemKey(i)
		questions[i] = jev.Question{
			ID: AssigneeQuestionID(i), Type: jev.QuestionChoice,
			Instructions: fmt.Sprintf("Who is responsible for `%s` from the meeting `meeting.title`? Match "+
				"names in its title and description to attendee labels. An option whose key is absent "+
				"from `attendees` never applies.", item),
			Criteria: criteria,
		}
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureMeetingActionAssignee,
		Title: "Meeting action assignee",
		Purpose: "When a meeting tool records an action item without saying who owns it, decide which " +
			"attendee (or you) is responsible, so action items can be listed by person. Email addresses " +
			"and phone numbers are removed from the title, labels, and item text before sending; an " +
			"attendee with no name is sent as \"attendee N\".",
		Questions: questions,
		StateFields: []string{
			"meeting.title",
			"attendees.attendee_N.label",
			"action_items.item_N.title",
			"action_items.item_N.description",
		},
	}
}

// AssigneeState is one assignee request's state. Its fields are exactly
// AssigneeFeature's StateFields.
type AssigneeState struct {
	Meeting     MeetingState               `json:"meeting"`
	Attendees   map[string]AttendeeState   `json:"attendees"`
	ActionItems map[string]ActionItemState `json:"action_items"`
}

// MeetingState is the meeting an assignee request is about.
type MeetingState struct {
	Title string `json:"title"`
}

// AttendeeState is one attendee offered as an option.
type AttendeeState struct {
	Label string `json:"label"`
}

// ActionItemState is one action item asked about.
type ActionItemState struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// AssigneeStore is the archive authority an assignee run needs.
// *store.Store implements it.
type AssigneeStore interface {
	MeetingActionAssigneeCandidatesContext(ctx context.Context, limit int) ([]store.MeetingAssigneeCandidate, error)
	WriteInferredMeetingActionAssigneesContext(ctx context.Context, assignees []store.MeetingActionAssignee) (int, error)
}

// AssigneeOptions configure one assignee run.
type AssigneeOptions struct {
	// Limit caps how many meetings one run visits; zero means no cap.
	Limit int
	// Judge is nil when Jev is off; the run then does nothing.
	Judge     Judge
	Automatic bool
	Logger    *slog.Logger
}

// AssigneeReport summarizes an assignee run. It never contains state.
type AssigneeReport struct {
	Meetings int `json:"meetings"`
	Items    int `json:"items"`
	Requests int `json:"requests"`
	// Attendees and Owner count items assigned at AssigneeThreshold or
	// above; Unclear counts items judged but left without an assignee.
	Attendees int `json:"attendees"`
	Owner     int `json:"owner"`
	Unclear   int `json:"unclear"`
	// TooManyAttendees counts meetings not sent because they have more than
	// MaxAssigneeAttendees attendees.
	TooManyAttendees int    `json:"too_many_attendees"`
	Skipped          string `json:"skipped,omitempty"`
}

// RunAssignees infers owners for meeting action items the meeting tool left
// unassigned, one meeting at a time. Without a Judge it does nothing. Any
// gate, budget, or provider failure stops Jev for the rest of the run and
// leaves the remaining items for a later run. Only a store failure fails
// the run.
func RunAssignees(ctx context.Context, st AssigneeStore, options AssigneeOptions) (AssigneeReport, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	var report AssigneeReport
	if options.Judge == nil {
		return report, nil
	}
	candidates, err := st.MeetingActionAssigneeCandidatesContext(ctx, options.Limit)
	if err != nil {
		return report, fmt.Errorf("list meeting assignee candidates: %w", err)
	}
	report.Meetings = len(candidates)
	for _, candidate := range candidates {
		report.Items += len(candidate.Actions)
		if len(candidate.Attendees) > MaxAssigneeAttendees {
			if err := recordTooManyAttendees(ctx, st, candidate); err != nil {
				return report, err
			}
			report.TooManyAttendees++
			continue
		}
		if err := judgeMeetingAssignees(ctx, st, options, candidate, &report); err != nil {
			if isStoreError(err) {
				return report, err
			}
			report.Skipped = jev.Skipped(err)
			options.Logger.Info("meeting action assignee: jev skipped",
				"feature", jev.FeatureMeetingActionAssignee, "category", report.Skipped)
			break
		}
	}
	return report, nil
}

func recordTooManyAttendees(ctx context.Context, st AssigneeStore, candidate store.MeetingAssigneeCandidate) error {
	rows := make([]store.MeetingActionAssignee, 0, len(candidate.Actions))
	for _, action := range candidate.Actions {
		rows = append(rows, store.MeetingActionAssignee{
			MessageID: candidate.MessageID, Ordinal: action.Ordinal, ActionTitle: action.Title,
			Fingerprint: action.Fingerprint,
			Choice:      store.MeetingAssigneeChoiceNone, Model: tooManyAttendeesModel,
		})
	}
	if _, err := st.WriteInferredMeetingActionAssigneesContext(ctx, rows); err != nil {
		return storeError{fmt.Errorf("record meeting with too many attendees: %w", err)}
	}
	return nil
}

func judgeMeetingAssignees(
	ctx context.Context, st AssigneeStore, options AssigneeOptions,
	candidate store.MeetingAssigneeCandidate, report *AssigneeReport,
) error {
	attendees := make(map[string]AttendeeState, len(candidate.Attendees))
	for i, attendee := range candidate.Attendees {
		attendees[AttendeeKey(i)] = AttendeeState{Label: truncateRunes(AttendeeLabel(attendee.Label, i), maxAttendeeLabelRunes)}
	}
	meeting := MeetingState{Title: truncateRunes(RedactText(candidate.Title), maxTitleRunes)}
	for start := 0; start < len(candidate.Actions); start += AssigneeItemsPerRequest {
		chunk := candidate.Actions[start:min(start+AssigneeItemsPerRequest, len(candidate.Actions))]
		state := AssigneeState{Meeting: meeting, Attendees: attendees, ActionItems: map[string]ActionItemState{}}
		ids := make([]string, 0, len(chunk))
		for i, action := range chunk {
			state.ActionItems[ItemKey(i)] = ActionItemState{
				Title:       truncateRunes(RedactText(action.Title), maxActionTitleRunes),
				Description: truncateRunes(RedactText(action.Description), maxActionDescriptionRunes),
			}
			ids = append(ids, AssigneeQuestionID(i))
		}
		response, err := options.Judge.JudgeQuestions(ctx, AssigneeFeature(), options.Automatic, state, ids, time.Time{})
		report.Requests++
		if err != nil {
			return err
		}
		rows := make([]store.MeetingActionAssignee, 0, len(chunk))
		for i, action := range chunk {
			answer, ok := response.Answers[AssigneeQuestionID(i)]
			if !ok {
				return fmt.Errorf("%w: answer %d missing", jev.ErrInvalidResponse, i)
			}
			row := assigneeFor(candidate, answer)
			row.MessageID, row.Ordinal, row.ActionTitle = candidate.MessageID, action.Ordinal, action.Title
			row.Fingerprint = action.Fingerprint
			row.Probabilities, row.Model = answer.Probabilities, response.Model
			rows = append(rows, row)
		}
		if _, err := st.WriteInferredMeetingActionAssigneesContext(ctx, rows); err != nil {
			return storeError{fmt.Errorf("write inferred meeting assignees: %w", err)}
		}
		for _, row := range rows {
			switch row.Choice {
			case store.MeetingAssigneeChoiceAttendee:
				report.Attendees++
			case store.MeetingAssigneeChoiceOwner:
				report.Owner++
			default:
				report.Unclear++
			}
		}
	}
	return nil
}

// assigneeFor maps one answer: an attendee or the owner at or above
// AssigneeThreshold is the assignee; anything else, including an attendee
// slot the request did not offer, is none_or_unclear.
func assigneeFor(candidate store.MeetingAssigneeCandidate, answer jev.Answer) store.MeetingActionAssignee {
	confidence := answer.Probabilities[answer.Choice]
	none := store.MeetingActionAssignee{Choice: store.MeetingAssigneeChoiceNone, Confidence: clampProbability(confidence)}
	if confidence < AssigneeThreshold {
		return none
	}
	if answer.Choice == OptionOwner {
		return store.MeetingActionAssignee{
			Choice: store.MeetingAssigneeChoiceOwner, ParticipantID: candidate.OwnerParticipantID, Confidence: confidence,
		}
	}
	for i, attendee := range candidate.Attendees {
		if answer.Choice == AttendeeKey(i) {
			return store.MeetingActionAssignee{
				Choice: store.MeetingAssigneeChoiceAttendee, ParticipantID: attendee.ParticipantID, Confidence: confidence,
			}
		}
	}
	return none
}

func clampProbability(value float64) float64 {
	return max(0, min(1, value))
}
