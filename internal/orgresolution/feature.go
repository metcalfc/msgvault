// Package orgresolution resolves an organization name that missed the exact
// lookup. When the name matches the only organization on the name's
// registrable domain, code decides and Jev is not asked. Otherwise it asks
// Jev whether the name is one of a few existing organizations, and in both
// cases whether two job titles at one organization name the same role. It
// stores each decision and confident answer as an alias the deterministic
// lookup and employment projection read, so replays never depend on a live
// call.
package orgresolution

import (
	"fmt"

	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
)

// Thresholds applied to Jev's probabilities.
const (
	// AliasThreshold is the candidate probability at or above which the name
	// becomes a durable alias of that organization.
	AliasThreshold = 0.85
	// ReviewThreshold is the candidate probability at or above which, below
	// AliasThreshold, the pair goes to the user as an organization match
	// review. Below it the organization is created as before.
	ReviewThreshold = 0.50
	// TitleThreshold is the title_same_role probability at or above which
	// the two titles become one role at the organization.
	TitleThreshold = 0.85
	// MaxTitlePairs is how many title pairs one request may ask about.
	MaxTitlePairs = 4
)

// Question IDs and the org_ref option that means "none of them".
const (
	QuestionOrgRef        = "org_ref"
	OptionNewOrganization = "new_organization"
)

// CandidateKey is the org_ref option and state key of the i-th (zero-based)
// shortlisted organization.
func CandidateKey(i int) string { return fmt.Sprintf("candidate_%d", i+1) }

// PairKey is the state key of the i-th (zero-based) title pair.
func PairKey(i int) string { return fmt.Sprintf("pair_%d", i+1) }

// TitleQuestion is the question ID asking about the i-th title pair.
func TitleQuestion(i int) string { return fmt.Sprintf("title_same_role_%d", i+1) }

// Feature is the exact policy the organization resolution feature consents
// to. Every request carries a subset of these questions, worded exactly as
// here; changing any wording or field changes the fingerprint and requires
// new consent.
func Feature() jev.FeatureSpec {
	criteria := make(map[string]string, store.MaxOrganizationShortlist+1)
	for i := range store.MaxOrganizationShortlist {
		key := CandidateKey(i)
		criteria[key] = fmt.Sprintf("`candidates.%s` is the same organization as `reference`.", key)
	}
	criteria[OptionNewOrganization] = "No organization in `candidates` is `reference`: it is a different " +
		"organization, a competitor, or only has a similar name."
	questions := []jev.Question{{
		ID: QuestionOrgRef, Type: jev.QuestionChoice,
		Instructions: "Which organization in `candidates` is the same real-world organization as " +
			"`reference`? Allow a legal suffix, an accelerator batch tag, a former name, a regional " +
			"office, or a shared domain. An option whose key is absent from `candidates` never applies.",
		Criteria: criteria,
	}}
	for i := range MaxTitlePairs {
		pair := "title_pairs." + PairKey(i)
		questions = append(questions, jev.Question{
			ID: TitleQuestion(i), Type: jev.QuestionNoul,
			Instructions: fmt.Sprintf("Do `%s.title` and `%s.other_title` name the same role at `%s.organization`?",
				pair, pair, pair),
			Criteria: jev.NoulCriteria{
				True: "The same role for one person: a synonym, an abbreviation, a longer or shorter form, " +
					"or a formal and an informal name for it.",
				False: "Different roles: a different function, a clearly different seniority, or unrelated positions.",
			},
		})
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureOrganizationResolution,
		Title: "Organization resolution",
		Purpose: "When an employment claim names an organization that matches no existing organization " +
			"exactly, decide whether it is one of up to eight similar existing organizations, and whether " +
			"two job titles a person holds at one organization name the same role.",
		Questions: questions,
		StateFields: []string{
			"reference.name", "reference.domain",
			"candidates.candidate_N.name", "candidates.candidate_N.domains[]",
			"candidates.candidate_N.other_names[]",
			"title_pairs.pair_N.organization", "title_pairs.pair_N.title", "title_pairs.pair_N.other_title",
		},
	}
}

// State is everything one request sends. Only these fields leave the
// machine: organization names, domains, and job titles.
type State struct {
	Reference  *ReferenceState           `json:"reference,omitzero"`
	Candidates map[string]CandidateState `json:"candidates,omitzero"`
	TitlePairs map[string]TitlePairState `json:"title_pairs,omitzero"`
}

// ReferenceState is the organization name that missed the exact lookup.
type ReferenceState struct {
	Name   string `json:"name"`
	Domain string `json:"domain,omitzero"`
}

// CandidateState is one shortlisted existing organization.
type CandidateState struct {
	Name       string   `json:"name"`
	Domains    []string `json:"domains,omitzero"`
	OtherNames []string `json:"other_names,omitzero"`
}

// TitlePairState asks whether two titles are one role at an organization.
type TitlePairState struct {
	Organization string `json:"organization"`
	Title        string `json:"title"`
	OtherTitle   string `json:"other_title"`
}
