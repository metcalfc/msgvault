package queryunderstand

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/explorecatalog"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/meetingjudge"
)

// Judge is the Jev door. *jev.Service implements it.
type Judge interface {
	JudgeQuestions(
		ctx context.Context, spec jev.FeatureSpec, automatic bool, state any, questionIDs []string, deadline time.Time,
	) (jev.Response, error)
}

// Suggestion kinds.
const (
	KindTimeWindow  = "time_window"
	KindPerson      = "person"
	KindMessageType = "message_type"
	KindAccount     = "account"
)

// Filter is one Explore filter a suggestion adds.
type Filter struct {
	Dimension string
	Values    []string
}

// Suggestion is one filter the query seems to ask for. Applying it adds
// Filters and QueryOperators and removes Span from the query.
type Suggestion struct {
	Kind  string
	Label string
	// Span is the exact text of the query the suggestion replaces; empty
	// when nothing should be removed.
	Span string
	// At is where Span sits in the query, as byte offsets.
	At             SpanPos
	Probability    float64
	Filters        []Filter
	QueryOperators []string
}

// Outcome is one judged query.
type Outcome struct {
	Model       string
	Suggestions []Suggestion
	// NaturalLanguage is the probability the query is a natural-language
	// question; nil when it was not asked.
	NaturalLanguage *float64
}

// OfferHybrid reports whether an empty full-text search should offer
// hybrid search for this query.
func (o Outcome) OfferHybrid() bool {
	return o.NaturalLanguage != nil && *o.NaturalLanguage >= HybridThreshold
}

// PersonAddresses returns the email addresses of a participant's identity
// cluster, used to turn a sender or recipient judgment into from: or to:
// operators. They stay on this machine.
type PersonAddresses func(ctx context.Context, participantID int64) ([]string, error)

// minNaturalLanguageWords is the fewest words for which the query is asked
// whether it is natural language; shorter queries are keywords.
const minNaturalLanguageWords = 3

// Plan is the state and questions one judgment sends.
type Plan struct {
	State     State
	Questions []string
}

// BuildPlan builds the request for candidates. ok is false when nothing is
// worth asking.
func BuildPlan(candidates Candidates) (Plan, bool) {
	state := State{Query: QueryState{Text: meetingjudge.RedactText(candidates.Query)}}
	var questions []string
	if len(candidates.Types) > 0 {
		questions = append(questions, QuestionMessageType)
	}
	if len(candidates.Windows) > 0 {
		state.TimeWindows = make(map[string]LabelState, len(candidates.Windows))
		for i, window := range candidates.Windows {
			state.TimeWindows[WindowKey(i)] = LabelState{Label: window.Label}
		}
		questions = append(questions, QuestionTimeWindow)
	}
	if len(candidates.People) > 0 {
		state.People = make(map[string]LabelState, len(candidates.People))
		for i, person := range candidates.People {
			state.People[PersonKey(i)] = LabelState{Label: person.Label}
		}
		questions = append(questions, QuestionPerson, QuestionPersonRole)
	}
	if len(candidates.Accounts) > 0 {
		state.Accounts = make(map[string]LabelState, len(candidates.Accounts))
		for i, account := range candidates.Accounts {
			state.Accounts[AccountKey(i)] = LabelState{Label: account.Label}
		}
		questions = append(questions, QuestionAccount)
	}
	if candidates.Words >= minNaturalLanguageWords {
		questions = append(questions, QuestionNaturalLanguage)
	}
	if len(questions) == 0 {
		return Plan{}, false
	}
	return Plan{State: state, Questions: questions}, true
}

// Understand asks Jev about the candidates and turns confident answers into
// suggestions. It returns ok=false without sending anything when no
// question is worth asking. A Jev error is returned as is so the caller can
// report its category; a failed address lookup only drops the operator.
func Understand(
	ctx context.Context, judge Judge, candidates Candidates, addresses PersonAddresses, deadline time.Time,
) (Outcome, bool, error) {
	plan, ok := BuildPlan(candidates)
	if !ok {
		return Outcome{}, false, nil
	}
	response, err := judge.JudgeQuestions(ctx, JevFeature(), false, plan.State, plan.Questions, deadline)
	if err != nil {
		return Outcome{}, true, err
	}
	outcome := Outcome{Model: response.Model}
	if answer, ok := response.Answers[QuestionNaturalLanguage]; ok && answer.Type == jev.QuestionNoul {
		probability := answer.Noul
		outcome.NaturalLanguage = &probability
	}
	if option, p, ok := chosen(response.Answers, QuestionTimeWindow); ok {
		if i, ok := slotIndex(option, WindowKey, len(candidates.Windows)); ok {
			window := candidates.Windows[i]
			outcome.Suggestions = append(outcome.Suggestions, Suggestion{
				Kind: KindTimeWindow, Label: window.Label, Span: window.Span, At: window.At, Probability: p,
				Filters: []Filter{
					{Dimension: explorecatalog.FilterAfter, Values: []string{window.After.Format(time.RFC3339Nano)}},
					{Dimension: explorecatalog.FilterBefore, Values: []string{window.Before.Format(time.RFC3339Nano)}},
				},
			})
		}
	}
	if option, p, ok := chosen(response.Answers, QuestionPerson); ok {
		if i, ok := slotIndex(option, PersonKey, len(candidates.People)); ok {
			outcome.Suggestions = append(outcome.Suggestions,
				personSuggestion(ctx, candidates.People[i], p, response.Answers, addresses))
		}
	}
	if option, p, ok := chosen(response.Answers, QuestionMessageType); ok {
		if suggestion, ok := typeSuggestion(candidates.Types, option, p); ok {
			outcome.Suggestions = append(outcome.Suggestions, suggestion)
		}
	}
	if option, p, ok := chosen(response.Answers, QuestionAccount); ok {
		if i, ok := slotIndex(option, AccountKey, len(candidates.Accounts)); ok {
			account := candidates.Accounts[i]
			outcome.Suggestions = append(outcome.Suggestions, Suggestion{
				Kind: KindAccount, Label: account.Label, Span: account.Span, At: account.At, Probability: p,
				Filters: []Filter{{Dimension: explorecatalog.FilterSource, Values: []string{strconv.FormatInt(account.SourceID, 10)}}},
			})
		}
	}
	return outcome, true, nil
}

// chosen returns a Choice answer's option when it is not "none" and its
// probability reaches SuggestionThreshold.
func chosen(answers map[string]jev.Answer, questionID string) (string, float64, bool) {
	answer, ok := answers[questionID]
	if !ok || answer.Type != jev.QuestionChoice || answer.Choice == OptionNone {
		return "", 0, false
	}
	probability, ok := answer.Probabilities[answer.Choice]
	if !ok {
		probability = answer.Confidence
	}
	if probability < SuggestionThreshold {
		return "", 0, false
	}
	return answer.Choice, probability, true
}

func slotIndex(option string, key func(int) string, count int) (int, bool) {
	for i := range count {
		if key(i) == option {
			return i, true
		}
	}
	return 0, false
}

func typeSuggestion(types []TypeCandidate, option string, probability float64) (Suggestion, bool) {
	// Only a type some query word named is offered: that word is the
	// evidence, and it is what the chip removes.
	index := slices.IndexFunc(types, func(candidate TypeCandidate) bool { return candidate.Option == option })
	if index < 0 {
		return Suggestion{}, false
	}
	for _, described := range messageTypeOptions {
		if described.option == option {
			return Suggestion{
				Kind: KindMessageType, Label: described.label, Span: types[index].Span, At: types[index].At, Probability: probability,
				Filters: []Filter{{Dimension: explorecatalog.FilterMessageType, Values: slices.Clone(described.types)}},
			}, true
		}
	}
	return Suggestion{}, false
}

// personSuggestion filters by the person. When Jev is confident the person
// sent (or received) the messages and the person has exactly one email
// address, the suggestion uses one from: (or to:) operator on it instead,
// which keeps the direction. Repeated from:/to: operators are AND-ed by the
// search engines, so a person with several addresses keeps the participant
// filter, which matches any of the person's identities in any role.
func personSuggestion(
	ctx context.Context, person PersonCandidate, probability float64, answers map[string]jev.Answer,
	addresses PersonAddresses,
) Suggestion {
	suggestion := Suggestion{
		Kind: KindPerson, Label: "With " + person.Label, Span: person.Span, At: person.At, Probability: probability,
		Filters: []Filter{{Dimension: explorecatalog.FilterParticipant, Values: []string{strconv.FormatInt(person.ParticipantID, 10)}}},
	}
	role, _, ok := chosen(answers, QuestionPersonRole)
	if !ok || role == RoleEither || addresses == nil {
		return suggestion
	}
	found, err := addresses(ctx, person.ParticipantID)
	if err != nil {
		return suggestion
	}
	unique := make([]string, 0, len(found))
	for _, address := range found {
		address = strings.ToLower(strings.TrimSpace(address))
		if address == "" || strings.ContainsAny(address, " \t\"") || slices.Contains(unique, address) {
			continue
		}
		unique = append(unique, address)
	}
	if len(unique) != 1 {
		return suggestion
	}
	operator, label := "from:", "From "
	if role == RoleRecipient {
		operator, label = "to:", "To "
	}
	suggestion.Label = label + person.Label
	suggestion.Filters = nil
	suggestion.QueryOperators = []string{operator + unique[0]}
	return suggestion
}
