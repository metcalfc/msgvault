// Package queryunderstand turns a typed Explore query into suggested
// filters. Code finds the candidates: date phrases become time windows,
// words that name a message type or one of the owner's accounts become
// those, and names found in the people index become person candidates.
// Jev then decides which candidates the query actually asks for, and
// whether the query reads as natural language. Only the query text and the
// candidates' labels leave the machine, with email addresses and phone
// numbers removed. A suggestion is only offered; the person applies it.
package queryunderstand

import (
	"fmt"
	"strconv"
	"time"

	"go.kenn.io/msgvault/internal/jev"
)

// Limits and thresholds.
const (
	// Budget bounds option generation and the judgment together. A
	// judgment that is not back in time is dropped; the search never
	// waits for it.
	Budget = 800 * time.Millisecond
	// SuggestionThreshold is the probability a chosen option needs before
	// it is offered as a chip.
	SuggestionThreshold = 0.80
	// HybridThreshold is the natural-language probability at or above
	// which an empty full-text search offers hybrid search.
	HybridThreshold = 0.70
	// MaxQueryRunes is the longest query that is judged.
	MaxQueryRunes = 300
	// MaxWindows, MaxPeople, and MaxAccounts are how many candidates of
	// each kind one request can offer.
	MaxWindows  = 4
	MaxPeople   = 4
	MaxAccounts = 6
	// maxLabelRunes caps each label sent.
	maxLabelRunes = 120
)

// Question IDs.
const (
	QuestionMessageType     = "message_type"
	QuestionTimeWindow      = "time_window"
	QuestionPerson          = "person"
	QuestionPersonRole      = "person_role"
	QuestionAccount         = "account"
	QuestionNaturalLanguage = "natural_language"
)

// OptionNone is every Choice's "the query does not ask for this" option.
const OptionNone = "none"

// Person role options.
const (
	RoleSender    = "sender"
	RoleRecipient = "recipient"
	RoleEither    = "either"
)

// Message type options. Each maps to the archive message types a filter
// on it selects.
const (
	TypeEmail             = "email"
	TypeTextMessage       = "text_message"
	TypeWhatsApp          = "whatsapp"
	TypeSlack             = "slack"
	TypeDiscord           = "discord"
	TypeTeams             = "teams"
	TypeGoogleChat        = "google_chat"
	TypeFacebookMessenger = "facebook_messenger"
	TypeCalendarEvent     = "calendar_event"
	TypeMeetingTranscript = "meeting_transcript"
)

// messageTypeOptions describes each message type option, in offer order.
var messageTypeOptions = []struct {
	option   string
	label    string
	types    []string
	criteria string
}{
	{TypeEmail, "Email", []string{"email"}, "Email messages."},
	{TypeTextMessage, "Texts", []string{"sms", "mms", "imessage", "rcs", "google_voice_text"},
		"Text messages: SMS, MMS, iMessage, or RCS."},
	{TypeWhatsApp, "WhatsApp", []string{"whatsapp"}, "WhatsApp messages."},
	{TypeSlack, "Slack", []string{"slack"}, "Slack messages."},
	{TypeDiscord, "Discord", []string{"discord"}, "Discord messages."},
	{TypeTeams, "Teams", []string{"teams"}, "Microsoft Teams chat messages."},
	{TypeGoogleChat, "Google Chat", []string{"google_chat"}, "Google Chat messages."},
	{TypeFacebookMessenger, "Messenger", []string{"fbmessenger"}, "Facebook Messenger messages."},
	{TypeCalendarEvent, "Events", []string{"calendar_event"}, "Calendar events and meeting invitations."},
	{TypeMeetingTranscript, "Meeting notes", []string{"meeting_transcript"},
		"Recorded meeting notes or transcripts."},
}

// WindowKey, PersonKey, and AccountKey are the option and state keys of
// the i-th (zero-based) candidate of each kind.
func WindowKey(i int) string  { return "window_" + strconv.Itoa(i+1) }
func PersonKey(i int) string  { return "person_" + strconv.Itoa(i+1) }
func AccountKey(i int) string { return "account_" + strconv.Itoa(i+1) }

// StateFields are exactly the fields a request carries.
var StateFields = []string{
	"query.text",
	"time_windows.window_N.label",
	"people.person_N.label",
	"accounts.account_N.label",
}

// JevFeature is the exact policy the Explore query understanding feature
// consents to. A request asks only the questions its candidates need,
// worded exactly as here.
func JevFeature() jev.FeatureSpec {
	typeCriteria := make(map[string]string, len(messageTypeOptions)+1)
	for _, option := range messageTypeOptions {
		typeCriteria[option.option] = option.criteria
	}
	typeCriteria[OptionNone] = "The query does not limit the kind of message, or names a kind only as its topic."
	return jev.FeatureSpec{
		Name:  jev.FeatureQueryUnderstanding,
		Title: "Explore query understanding",
		Purpose: "When you type an Explore search, suggest filters for what it asks: a message type, a " +
			"time period, a person, or one of your accounts, and whether it reads as a question that " +
			"hybrid search would answer better. Sends the query text and the labels of the candidates " +
			"code found (time periods, people's names from your directory, account names). Email " +
			"addresses and phone numbers are removed first. Suggestions are only offered; nothing " +
			"changes until you apply one.",
		Questions: []jev.Question{
			{
				ID: QuestionMessageType, Type: jev.QuestionChoice,
				Instructions: "Does `query.text` ask only for one kind of message? Choose the kind it asks for.",
				Criteria:     typeCriteria,
			},
			{
				ID: QuestionTimeWindow, Type: jev.QuestionChoice,
				Instructions: "Does `query.text` limit results to a time period? Choose the entry of " +
					"`time_windows` that means what the query means. An option whose key is absent from " +
					"`time_windows` never applies.",
				Criteria: slotCriteria(MaxWindows, WindowKey, "`time_windows.%s` is the period the query asks for.",
					"The query sets no time limit, or its date words are part of the topic (for example a "+
						"newsletter or report title)."),
			},
			{
				ID: QuestionPerson, Type: jev.QuestionChoice,
				Instructions: "Does `query.text` ask for messages with a specific person listed in `people`? " +
					"Choose that person. An option whose key is absent from `people` never applies.",
				Criteria: slotCriteria(MaxPeople, PersonKey, "`people.%s` is the person the query is about.",
					"No listed person: the query names nobody, names someone not listed, or the name is "+
						"part of the topic (for example a company or a place)."),
			},
			{
				ID: QuestionPersonRole, Type: jev.QuestionChoice,
				Instructions: "If `query.text` asks for messages with a person, did that person send them, " +
					"receive them, or either?",
				Criteria: map[string]string{
					RoleSender:    "Messages the person sent (for example \"from Ana\" or \"what Ana said\").",
					RoleRecipient: "Messages sent to the person (for example \"to Ana\" or \"what I sent Ana\").",
					RoleEither:    "Any messages with the person, or the query does not say.",
				},
			},
			{
				ID: QuestionAccount, Type: jev.QuestionChoice,
				Instructions: "Does `query.text` ask for messages in one of the user's own accounts listed in " +
					"`accounts`? Choose that account. An option whose key is absent from `accounts` never applies.",
				Criteria: slotCriteria(MaxAccounts, AccountKey, "`accounts.%s` is the account the query asks for.",
					"The query does not name one of the user's accounts."),
			},
			{
				ID: QuestionNaturalLanguage, Type: jev.QuestionNoul,
				Instructions: "Is `query.text` a natural-language question or description rather than keywords " +
					"to match exactly?",
				Criteria: jev.NoulCriteria{
					True: "A sentence, question, or description of what the user wants (for example " +
						"\"emails where the landlord asked about the deposit\").",
					False: "Keywords, a name, an exact phrase, or search operators.",
				},
			},
		},
		StateFields: StateFields,
	}
}

func slotCriteria(slots int, key func(int) string, format, none string) map[string]string {
	criteria := make(map[string]string, slots+1)
	for i := range slots {
		criteria[key(i)] = fmt.Sprintf(format, key(i))
	}
	criteria[OptionNone] = none
	return criteria
}

// State is one request's state. Its fields are exactly StateFields.
type State struct {
	Query       QueryState            `json:"query"`
	TimeWindows map[string]LabelState `json:"time_windows,omitempty"`
	People      map[string]LabelState `json:"people,omitempty"`
	Accounts    map[string]LabelState `json:"accounts,omitempty"`
}

// QueryState is the typed query.
type QueryState struct {
	Text string `json:"text"`
}

// LabelState is one candidate offered as an option.
type LabelState struct {
	Label string `json:"label"`
}
