// Package profilejudge asks Jev (feature person_profile_choices) the small
// profile questions code cannot settle by rule: which of several system-set
// current roles is a person's primary one, which of several names a newly
// promoted person goes by, and whether two conflicting values left by a
// person merge state the same fact. Code applies each answer only above a
// fixed threshold and never over a user's value or pin. Options and values
// equal after normalization are settled in code and never sent; see
// classifyConflict and foldText.
package profilejudge

import (
	"context"
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
			"the same thing. Roles, names, and values that are equal after normalization are settled " +
			"without asking. Nothing you set or pinned is ever changed.",
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
	SettleEqualMergeConflictContext(ctx context.Context, settlement store.MergeConflictSettlement) (bool, error)
}

// Options configure one run.
type Options struct {
	// Limit caps how many people and how many conflicts one run judges;
	// zero means no cap.
	Limit int
	// Judge is nil when Jev is off; the run then only settles in code what
	// normalization decides.
	Judge     Judge
	Automatic bool
	Logger    *slog.Logger
}

// Report summarizes a run. It never contains names, titles, or values.
type Report struct {
	Requests         int `json:"requests"`
	PrimaryRoles     int `json:"primary_roles"`
	PrimaryRolesSet  int `json:"primary_roles_set"`
	DisplayNames     int `json:"display_names"`
	DisplayNamesSet  int `json:"display_names_set"`
	MergeConflicts   int `json:"merge_conflicts"`
	ConflictsSettled int `json:"conflicts_settled"`
	// SettledInCode counts roles, names, and merge conflicts decided without
	// Jev because their options or values are equal after normalization.
	SettledInCode int    `json:"settled_in_code"`
	Skipped       string `json:"skipped,omitempty"`
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

// Run settles in code what normalization decides, then asks Jev the rest.
// A primary-role or display-name question whose options are all equal
// after normalization needs no judgment: the rule's choice stays. A merge
// conflict whose values are equal after normalization is closed in code,
// keeping the user-declared value, else the survivor's. This code part runs
// even when Jev is off. Any gate, budget, or provider failure stops Jev for
// the rest of the run, leaving the remaining questions for a later run;
// only a store failure fails the run.
func Run(ctx context.Context, st Store, options Options) (Report, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	var report Report
	r := &runner{st: st, options: options, report: &report}
	for _, step := range []func(context.Context) error{r.primaryRoles, r.displayNames, r.mergeConflicts} {
		if err := step(ctx); err != nil {
			return report, err
		}
	}
	return report, nil
}

type runner struct {
	st      Store
	options Options
	report  *Report
}

// ask sends one request. On any Jev failure it records the skip category,
// turns Jev off for the rest of the run, and reports false.
func (r *runner) ask(ctx context.Context, state any, ids []string) (jev.Response, bool) {
	response, err := r.options.Judge.JudgeQuestions(ctx, Feature(), r.options.Automatic, state, ids, time.Time{})
	r.report.Requests++
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
		r.report.Skipped = jev.Skipped(err)
		r.options.Logger.Info("person profile choices: jev skipped",
			"feature", jev.FeaturePersonProfileChoices, "category", r.report.Skipped)
		r.options.Judge = nil
		return jev.Response{}, false
	}
	return response, true
}

// group is one set of options equal after normalization, represented by
// one of them.
type group struct {
	key            string
	representative int
}

// groupOptions groups option indexes by key in first-appearance order. The
// representative is the first preferred member, else the first member: the
// rule's current primary role, or the rule's own spelling of a name, so
// choosing that group changes nothing.
func groupOptions(count int, key func(int) string, preferred func(int) bool) []group {
	groups := []group{}
	index := map[string]int{}
	for i := range count {
		k := key(i)
		at, ok := index[k]
		if !ok {
			index[k] = len(groups)
			groups = append(groups, group{key: k, representative: i})
			continue
		}
		if preferred(i) && !preferred(groups[at].representative) {
			groups[at].representative = i
		}
	}
	return groups
}

func (r *runner) primaryRoles(ctx context.Context) error {
	candidates, err := r.st.PrimaryRoleCandidatesContext(ctx, r.options.Limit)
	if err != nil {
		return fmt.Errorf("list primary role candidates: %w", err)
	}
	for _, candidate := range candidates {
		roles := candidate.Roles
		groups := groupOptions(len(roles),
			func(i int) string { return roleKey(roles[i]) },
			func(i int) bool { return roles[i].IsPrimary })
		judgment := store.PrimaryRoleJudgment{PersonID: candidate.PersonID, Fingerprint: candidate.Fingerprint}
		if len(groups) == 1 {
			// Every role is the same organization and title: the rule's
			// primary role stays and nothing is asked.
			judgment.Confidence, judgment.Model = 1, normalizedModel
			if _, err := r.st.ApplyPrimaryRoleJudgmentContext(ctx, judgment); err != nil {
				return fmt.Errorf("record equal primary roles: %w", err)
			}
			r.report.SettledInCode++
			continue
		}
		if r.options.Judge == nil {
			continue
		}
		state := roleRequest{Roles: make(map[string]RoleState, len(groups))}
		for i, entry := range groups {
			role := roles[entry.representative]
			state.Roles[RoleKey(i)] = RoleState{
				Organization: truncateRunes(meetingjudge.RedactText(role.Organization), maxLabelRunes),
				Title:        truncateRunes(meetingjudge.RedactText(role.Title), maxLabelRunes), Start: role.Start,
			}
		}
		response, ok := r.ask(ctx, state, []string{PrimaryRoleQuestionID})
		if !ok {
			return nil
		}
		answer := response.Answers[PrimaryRoleQuestionID]
		judgment.Confidence = clamp(answer.Probabilities[answer.Choice])
		judgment.Probabilities, judgment.Model = answer.Probabilities, response.Model
		if judgment.Confidence >= ChoiceThreshold {
			for i, entry := range groups {
				if answer.Choice == RoleKey(i) {
					id := roles[entry.representative].EmploymentID
					judgment.EmploymentID = &id
				}
			}
		}
		changed, err := r.st.ApplyPrimaryRoleJudgmentContext(ctx, judgment)
		if err != nil {
			return fmt.Errorf("apply primary role judgment: %w", err)
		}
		r.report.PrimaryRoles++
		if changed {
			r.report.PrimaryRolesSet++
		}
	}
	return nil
}

func (r *runner) displayNames(ctx context.Context) error {
	candidates, err := r.st.DisplayNameCandidatesContext(ctx, r.options.Limit)
	if err != nil {
		return fmt.Errorf("list display name candidates: %w", err)
	}
	for _, candidate := range candidates {
		judgment := store.DisplayNameJudgment{PersonID: candidate.PersonID, Fingerprint: candidate.Fingerprint}
		all := groupOptions(len(candidate.Names),
			func(i int) string { return foldText(candidate.Names[i]) },
			func(int) bool { return false })
		if len(all) == 1 {
			// Every name is the same after normalization: the rule's name
			// stays and nothing is asked.
			judgment.Confidence, judgment.Model = 1, normalizedModel
			if _, err := r.st.ApplyDisplayNameJudgmentContext(ctx, judgment); err != nil {
				return fmt.Errorf("record equal display names: %w", err)
			}
			r.report.SettledInCode++
			continue
		}
		if r.options.Judge == nil {
			continue
		}
		names := []string{}
		for _, name := range candidate.Names {
			if meetingjudge.RedactText(name) == strings.TrimSpace(name) {
				names = append(names, name)
			}
		}
		groups := groupOptions(len(names),
			func(i int) string { return foldText(names[i]) },
			func(i int) bool { return names[i] == candidate.Current })
		if len(groups) < 2 {
			continue
		}
		state := nameRequest{Names: make(map[string]string, len(groups))}
		keys := make(map[string]string, len(groups))
		for i, entry := range groups {
			name := names[entry.representative]
			keys[NameKey(i)] = name
			state.Names[NameKey(i)] = truncateRunes(name, maxLabelRunes)
		}
		response, ok := r.ask(ctx, state, []string{DisplayNameQuestionID})
		if !ok {
			return nil
		}
		answer := response.Answers[DisplayNameQuestionID]
		judgment.Confidence = clamp(answer.Probabilities[answer.Choice])
		judgment.Probabilities, judgment.Model = answer.Probabilities, response.Model
		if name, ok := keys[answer.Choice]; ok && judgment.Confidence >= ChoiceThreshold {
			judgment.Name = &name
		}
		changed, err := r.st.ApplyDisplayNameJudgmentContext(ctx, judgment)
		if err != nil {
			return fmt.Errorf("apply display name judgment: %w", err)
		}
		r.report.DisplayNames++
		if changed {
			r.report.DisplayNamesSet++
		}
	}
	return nil
}

func (r *runner) mergeConflicts(ctx context.Context) error {
	candidates, err := r.st.MergeConflictCandidatesContext(ctx, r.options.Limit)
	if err != nil {
		return fmt.Errorf("list merge conflict candidates: %w", err)
	}
	sendable := []store.MergeConflictCandidate{}
	for _, candidate := range candidates {
		switch classifyConflict(candidate) {
		case conflictEqual:
			settled, err := r.st.SettleEqualMergeConflictContext(ctx, store.MergeConflictSettlement{
				CandidateID: candidate.CandidateID, PersonID: candidate.PersonID,
				KeepAbsorbed: keepAbsorbed(candidate),
			})
			if err != nil {
				return fmt.Errorf("settle equal merge conflict: %w", err)
			}
			if settled {
				r.report.SettledInCode++
			}
			continue
		case conflictAsk:
			if !candidate.AbsorbedSource.IsDeclared() && conflictSendable(candidate) {
				if r.options.Judge != nil {
					sendable = append(sendable, candidate)
				}
				continue
			}
		case conflictDifferent:
		}
		// Never sent: two different typed values, a user-declared absorbed
		// value, or a value that cannot be sent whole. Recorded so it is not
		// listed again, and left pending for the user.
		if _, err := r.st.ApplyMergeConflictJudgmentContext(ctx, store.MergeConflictJudgment{
			CandidateID: candidate.CandidateID, PersonID: candidate.PersonID, Model: notSentModel,
		}); err != nil {
			return fmt.Errorf("record unsent merge conflict: %w", err)
		}
	}
	for start := 0; start < len(sendable) && r.options.Judge != nil; start += ConflictsPerRequest {
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
		response, ok := r.ask(ctx, state, ids)
		if !ok {
			return nil
		}
		for i, candidate := range chunk {
			probability := clamp(response.Answers[SameValueQuestionID(i)].Noul)
			resolved, err := r.st.ApplyMergeConflictJudgmentContext(ctx, store.MergeConflictJudgment{
				CandidateID: candidate.CandidateID, PersonID: candidate.PersonID,
				Probability: probability, Model: response.Model,
				Resolve: probability >= SameValueThreshold,
			})
			if err != nil {
				return fmt.Errorf("apply merge conflict judgment: %w", err)
			}
			r.report.MergeConflicts++
			if resolved {
				r.report.ConflictsSettled++
			}
		}
	}
	return nil
}

// normalizedModel marks a profile choice recorded without asking Jev
// because its options are equal after normalization.
const normalizedModel = store.PersonMergeConflictNormalizedActor

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
