// Package profilejudge asks Jev (feature person_profile_choices) the small
// profile questions code cannot settle by rule: which of several system-set
// current roles is a person's primary one, which of several names a newly
// promoted person goes by, and whether two conflicting values left by a
// person merge state the same fact. Code applies each answer only above a
// fixed threshold and never over a user's value or pin.
package profilejudge

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
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/store"
)

// Thresholds code applies to the answers.
const (
	// ChoiceThreshold is the probability a role or name needs before it is
	// written.
	ChoiceThreshold = 0.80
	// SameValueThreshold is the probability two merge-conflict values need
	// to be treated as the same fact, keeping the survivor's value.
	SameValueThreshold = 0.95
	// ConflictsPerRequest is how many merge conflicts one request asks about.
	ConflictsPerRequest = 8
	// OptionUnclear is the Choice option for "none stands out".
	OptionUnclear   = "unclear"
	maxFieldRunes   = 120
	maxValueRunes   = 300
	maxLabelRunes   = 160
	primaryRoleID   = "primary_role"
	displayNameID   = "display_name"
	sameValuePrefix = "same_value_"
)

// RoleKey is the option and state key of the i-th (zero-based) role.
func RoleKey(i int) string { return "role_" + strconv.Itoa(i+1) }

// NameKey is the option and state key of the i-th (zero-based) name.
func NameKey(i int) string { return "name_" + strconv.Itoa(i+1) }

// ConflictKey is the state key of the i-th (zero-based) merge conflict.
func ConflictKey(i int) string { return "conflict_" + strconv.Itoa(i+1) }

// SameValueQuestionID asks about the i-th merge conflict.
func SameValueQuestionID(i int) string { return sameValuePrefix + strconv.Itoa(i+1) }

// Question IDs of the two Choices.
const (
	PrimaryRoleQuestionID = primaryRoleID
	DisplayNameQuestionID = displayNameID
)

// Feature is the exact policy the person_profile_choices feature consents
// to. A request asks only the questions its state needs, worded exactly as
// here.
func Feature() jev.FeatureSpec {
	roleCriteria := make(map[string]string, store.MaxPrimaryRoleOptions+1)
	for i := range store.MaxPrimaryRoleOptions {
		key := RoleKey(i)
		roleCriteria[key] = fmt.Sprintf("`roles.%s` is the person's main current job.", key)
	}
	roleCriteria[OptionUnclear] = "No single current role stands out as the main one."
	nameCriteria := make(map[string]string, store.MaxDisplayNameOptions+1)
	for i := range store.MaxDisplayNameOptions {
		key := NameKey(i)
		nameCriteria[key] = fmt.Sprintf("`names.%s` is the person's own full name, written the way they use it.", key)
	}
	nameCriteria[OptionUnclear] = "None of the names is clearly the person's own name."
	questions := []jev.Question{
		{
			ID: PrimaryRoleQuestionID, Type: jev.QuestionChoice,
			Instructions: "Which of `roles` is this person's primary current role? An option whose key is " +
				"absent from `roles` never applies.",
			Criteria: roleCriteria,
		},
		{
			ID: DisplayNameQuestionID, Type: jev.QuestionChoice,
			Instructions: "Which of `names` should a contact list show for this person? An option whose key " +
				"is absent from `names` never applies.",
			Criteria: nameCriteria,
		},
	}
	for i := range ConflictsPerRequest {
		conflict := "conflicts." + ConflictKey(i)
		questions = append(questions, jev.Question{
			ID: SameValueQuestionID(i), Type: jev.QuestionNoul,
			Instructions: fmt.Sprintf("Do `%s.first` and `%s.second` state the same fact for `%s.field`?",
				conflict, conflict, conflict),
			Criteria: jev.NoulCriteria{
				True: "The same fact written differently: formatting, abbreviation, spelling variant, or more " +
					"or less detail that does not disagree.",
				False: "Different facts, or one contradicts the other.",
			},
		})
	}
	return jev.FeatureSpec{
		Name:  jev.FeaturePersonProfileChoices,
		Title: "Person profile choices",
		Purpose: "Settle three small profile questions: which of a person's current roles is primary when " +
			"several were found automatically, which name a newly saved person should show when their " +
			"addresses use different names, and whether two values left in conflict by a person merge say " +
			"the same thing. Nothing you set or pinned is ever changed.",
		Questions: questions,
		StateFields: []string{
			"roles.role_N.organization, .title, and .start: each current role's organization name, job " +
				"title (email addresses and phone numbers replaced by placeholders), and start year and month " +
				"(primary role only)",
			"names.name_N: each display name the person's identities use, cut to 160 characters; a name " +
				"containing an email address or phone number is never sent (display name only)",
			"conflicts.conflict_N.field, .first, and .second: the attribute's label and the two conflicting " +
				"values, sent whole; a conflict with a value over 300 characters or with an email address or " +
				"phone number is never sent (merge conflicts only)",
		},
	}
}

// Judge is the shared Jev door. *jev.Service implements it.
type Judge interface {
	JudgeQuestions(
		ctx context.Context, spec jev.FeatureSpec, automatic bool, state any, questionIDs []string, deadline time.Time,
	) (jev.Response, error)
}

// Store is the archive authority a run needs. *store.Store implements it.
type Store interface {
	PrimaryRoleCandidatesContext(ctx context.Context, limit int) ([]store.PrimaryRoleCandidate, error)
	ApplyPrimaryRoleJudgmentContext(ctx context.Context, judgment store.PrimaryRoleJudgment) (bool, error)
	DisplayNameCandidatesContext(ctx context.Context, limit int) ([]store.DisplayNameCandidate, error)
	ApplyDisplayNameJudgmentContext(ctx context.Context, judgment store.DisplayNameJudgment) (bool, error)
	MergeConflictCandidatesContext(ctx context.Context, limit int) ([]store.MergeConflictCandidate, error)
	ApplyMergeConflictJudgmentContext(ctx context.Context, judgment store.MergeConflictJudgment) (bool, error)
}

// Options configure one run.
type Options struct {
	// Limit caps how many people and how many conflicts one run judges;
	// zero means no cap.
	Limit int
	// Judge is nil when Jev is off; the run then does nothing.
	Judge     Judge
	Automatic bool
	Logger    *slog.Logger
}

// Report summarizes a run. It never contains names, titles, or values.
type Report struct {
	Requests         int    `json:"requests"`
	PrimaryRoles     int    `json:"primary_roles"`
	PrimaryRolesSet  int    `json:"primary_roles_set"`
	DisplayNames     int    `json:"display_names"`
	DisplayNamesSet  int    `json:"display_names_set"`
	MergeConflicts   int    `json:"merge_conflicts"`
	ConflictsSettled int    `json:"conflicts_settled"`
	Skipped          string `json:"skipped,omitempty"`
}

// RoleState is one role as sent.
type RoleState struct {
	Organization string `json:"organization"`
	Title        string `json:"title"`
	Start        string `json:"start"`
}

// ConflictState is one merge conflict as sent.
type ConflictState struct {
	Field  string `json:"field"`
	First  string `json:"first"`
	Second string `json:"second"`
}

type roleRequest struct {
	Roles map[string]RoleState `json:"roles"`
}

type nameRequest struct {
	Names map[string]string `json:"names"`
}

type conflictRequest struct {
	Conflicts map[string]ConflictState `json:"conflicts"`
}

// stopError marks a Jev failure that stops the run's remaining requests.
type stopError struct{ err error }

func (e stopError) Error() string { return e.err.Error() }
func (e stopError) Unwrap() error { return e.err }

// Run asks every question that has candidates. Any gate, budget, or
// provider failure stops Jev for the rest of the run and leaves the rest
// for a later run; only a store failure fails the run.
func Run(ctx context.Context, st Store, options Options) (Report, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	var report Report
	if options.Judge == nil {
		return report, nil
	}
	for _, step := range []func(context.Context, Store, Options, *Report) error{
		runPrimaryRoles, runDisplayNames, runMergeConflicts,
	} {
		err := step(ctx, st, options, &report)
		if stop, ok := errors.AsType[stopError](err); ok {
			report.Skipped = jev.Skipped(stop.err)
			options.Logger.Info("person profile choices: jev skipped",
				"feature", jev.FeaturePersonProfileChoices, "category", report.Skipped)
			return report, nil
		}
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

func ask(
	ctx context.Context, options Options, report *Report, state any, ids []string,
) (jev.Response, error) {
	response, err := options.Judge.JudgeQuestions(ctx, Feature(), options.Automatic, state, ids, time.Time{})
	report.Requests++
	if err == nil && strings.TrimSpace(response.Model) == "" {
		err = fmt.Errorf("%w: no model", jev.ErrInvalidResponse)
	}
	for _, id := range ids {
		if err != nil {
			break
		}
		if _, ok := response.Answers[id]; !ok {
			err = fmt.Errorf("%w: answer %s missing", jev.ErrInvalidResponse, id)
		}
	}
	if err != nil {
		return jev.Response{}, stopError{err}
	}
	return response, nil
}

func runPrimaryRoles(ctx context.Context, st Store, options Options, report *Report) error {
	candidates, err := st.PrimaryRoleCandidatesContext(ctx, options.Limit)
	if err != nil {
		return fmt.Errorf("list primary role candidates: %w", err)
	}
	for _, candidate := range candidates {
		state := roleRequest{Roles: make(map[string]RoleState, len(candidate.Roles))}
		for i, role := range candidate.Roles {
			state.Roles[RoleKey(i)] = RoleState{
				Organization: truncateRunes(meetingjudge.RedactText(role.Organization), maxLabelRunes),
				Title:        truncateRunes(meetingjudge.RedactText(role.Title), maxLabelRunes), Start: role.Start,
			}
		}
		response, err := ask(ctx, options, report, state, []string{PrimaryRoleQuestionID})
		if err != nil {
			return err
		}
		answer := response.Answers[PrimaryRoleQuestionID]
		confidence := clamp(answer.Probabilities[answer.Choice])
		judgment := store.PrimaryRoleJudgment{
			PersonID: candidate.PersonID, Fingerprint: candidate.Fingerprint,
			Confidence: confidence, Probabilities: answer.Probabilities, Model: response.Model,
		}
		if confidence >= ChoiceThreshold {
			for i, role := range candidate.Roles {
				if answer.Choice == RoleKey(i) {
					id := role.EmploymentID
					judgment.EmploymentID = &id
				}
			}
		}
		changed, err := st.ApplyPrimaryRoleJudgmentContext(ctx, judgment)
		if err != nil {
			return fmt.Errorf("apply primary role judgment: %w", err)
		}
		report.PrimaryRoles++
		if changed {
			report.PrimaryRolesSet++
		}
	}
	return nil
}

func runDisplayNames(ctx context.Context, st Store, options Options, report *Report) error {
	candidates, err := st.DisplayNameCandidatesContext(ctx, options.Limit)
	if err != nil {
		return fmt.Errorf("list display name candidates: %w", err)
	}
	for _, candidate := range candidates {
		state := nameRequest{Names: map[string]string{}}
		keys := map[string]string{}
		for i, name := range candidate.Names {
			if meetingjudge.RedactText(name) != strings.TrimSpace(name) {
				continue
			}
			key := NameKey(len(keys))
			keys[key] = candidate.Names[i]
			state.Names[key] = truncateRunes(name, maxLabelRunes)
		}
		if len(keys) < 2 {
			continue
		}
		response, err := ask(ctx, options, report, state, []string{DisplayNameQuestionID})
		if err != nil {
			return err
		}
		answer := response.Answers[DisplayNameQuestionID]
		confidence := clamp(answer.Probabilities[answer.Choice])
		judgment := store.DisplayNameJudgment{
			PersonID: candidate.PersonID, Fingerprint: candidate.Fingerprint,
			Confidence: confidence, Probabilities: answer.Probabilities, Model: response.Model,
		}
		if name, ok := keys[answer.Choice]; ok && confidence >= ChoiceThreshold {
			judgment.Name = &name
		}
		changed, err := st.ApplyDisplayNameJudgmentContext(ctx, judgment)
		if err != nil {
			return fmt.Errorf("apply display name judgment: %w", err)
		}
		report.DisplayNames++
		if changed {
			report.DisplayNamesSet++
		}
	}
	return nil
}

func runMergeConflicts(ctx context.Context, st Store, options Options, report *Report) error {
	candidates, err := st.MergeConflictCandidatesContext(ctx, options.Limit)
	if err != nil {
		return fmt.Errorf("list merge conflict candidates: %w", err)
	}
	sendable := candidates[:0]
	for _, candidate := range candidates {
		if conflictSendable(candidate) {
			sendable = append(sendable, candidate)
			continue
		}
		// Never sent: recorded so it is not listed again, and left pending
		// for the user.
		if _, err := st.ApplyMergeConflictJudgmentContext(ctx, store.MergeConflictJudgment{
			CandidateID: candidate.CandidateID, PersonID: candidate.PersonID, Model: notSentModel,
		}); err != nil {
			return fmt.Errorf("record unsent merge conflict: %w", err)
		}
	}
	for start := 0; start < len(sendable); start += ConflictsPerRequest {
		chunk := sendable[start:min(start+ConflictsPerRequest, len(sendable))]
		state := conflictRequest{Conflicts: make(map[string]ConflictState, len(chunk))}
		ids := make([]string, len(chunk))
		for i, candidate := range chunk {
			state.Conflicts[ConflictKey(i)] = ConflictState{
				Field:  candidate.Field,
				First:  candidate.Survivor,
				Second: candidate.Absorbed,
			}
			ids[i] = SameValueQuestionID(i)
		}
		response, err := ask(ctx, options, report, state, ids)
		if err != nil {
			return err
		}
		for i, candidate := range chunk {
			probability := clamp(response.Answers[SameValueQuestionID(i)].Noul)
			resolved, err := st.ApplyMergeConflictJudgmentContext(ctx, store.MergeConflictJudgment{
				CandidateID: candidate.CandidateID, PersonID: candidate.PersonID,
				Probability: probability, Model: response.Model,
				Resolve: probability >= SameValueThreshold,
			})
			if err != nil {
				return fmt.Errorf("apply merge conflict judgment: %w", err)
			}
			report.MergeConflicts++
			if resolved {
				report.ConflictsSettled++
			}
		}
	}
	return nil
}

// notSentModel marks a merge conflict recorded without asking Jev.
const notSentModel = "rule:not_sent"

// conflictSendable reports whether both values can be sent whole: no email
// address or phone number, and short enough that nothing is cut. A value
// that would be truncated could hide the difference that makes two values
// disagree, so such a conflict is never judged and stays with the user.
func conflictSendable(candidate store.MergeConflictCandidate) bool {
	for _, value := range []string{candidate.Survivor, candidate.Absorbed} {
		if meetingjudge.RedactText(value) != strings.TrimSpace(value) ||
			utf8.RuneCountInString(value) > maxValueRunes {
			return false
		}
	}
	return utf8.RuneCountInString(candidate.Field) <= maxFieldRunes
}

func clamp(value float64) float64 { return min(1, max(0, value)) }

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
