// Package kindclassify decides which archive identity clusters are people.
// Deterministic rules (correspondentkind.Classify) decide what they can;
// the remainder is asked of Jev, batched ten identities per request, when
// the correspondent_kind feature is enabled and consented. Results are
// stored as rule or jev rows in correspondent_kinds, below any user
// decision.
package kindclassify

import (
	"fmt"
	"strconv"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/jev"
)

// BatchSize is how many identities one Jev request carries.
const BatchSize = 10

// Jev options, in the order they are offered.
const (
	OptionIndividualPerson = "individual_person"
	OptionSharedMailbox    = "shared_role_or_team_mailbox"
	OptionMailingList      = "mailing_list_or_group"
	OptionAutomated        = "automated_notification_or_transactional"
	OptionMarketing        = "marketing_or_newsletter"
	OptionUnclear          = "unclear"
)

// Threshold is the probability a Jev option needs before it decides a
// cluster. Below it the cluster is stored as unclear, which keeps it out of
// relationship rankings and puts it in review.
const Threshold = 0.60

// State limits: what one identity may carry.
const (
	maxAddresses         = 5
	maxLabelRunes        = 120
	maxSubjectsFromThem  = 5
	maxSubjectsFromOwner = 3
)

// QuestionID is the question asking about identities[i].
func QuestionID(i int) string {
	return "kind_" + strconv.Itoa(i)
}

var optionCriteria = map[string]string{
	OptionIndividualPerson: "One human writing as themselves, including from a work address.",
	OptionSharedMailbox:    "A role or team address that several people read or write from, such as a support desk or a team alias people reply to.",
	OptionMailingList:      "A list or group address that relays messages from many members.",
	OptionAutomated:        "Machine-generated notifications, receipts, alerts, security or account messages.",
	OptionMarketing:        "Marketing, promotions, or a newsletter sent in bulk.",
	OptionUnclear:          "The evidence does not support any other option.",
}

// JevFeature is the exact policy the correspondent kind feature consents
// to: one Choice per identity, ten identities per request, over the state
// fields listed. No message bodies and no identifiers leave the machine.
// Changing any wording or field changes the fingerprint and requires new
// consent.
func JevFeature() jev.FeatureSpec {
	questions := make([]jev.Question, BatchSize)
	for i := range questions {
		questions[i] = jev.Question{
			ID: QuestionID(i), Type: jev.QuestionChoice,
			Instructions: fmt.Sprintf("What kind of correspondent is `identities[%d]`? Judge from its label, "+
				"address parts, message counts, list and category shares, header counts, and subjects.", i),
			Criteria: optionCriteria,
		}
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureCorrespondentKind,
		Title: "Correspondent kind",
		Purpose: "Decide whether an archive identity that the deterministic rules could not classify " +
			"is an individual person, a shared or team mailbox, a mailing list, an automated sender, " +
			"or a newsletter, so rankings and enrichment can leave out what is not a person.",
		Questions: questions,
		StateFields: []string{
			"identities[].label",
			"identities[].addresses[].local_part",
			"identities[].addresses[].domain",
			"identities[].counts.sent",
			"identities[].counts.received",
			"identities[].counts.meetings",
			"identities[].list_id_share",
			"identities[].category_shares",
			"identities[].header_counts.sampled",
			"identities[].header_counts.list_unsubscribe",
			"identities[].header_counts.auto_submitted",
			"identities[].header_counts.precedence_bulk",
			"identities[].header_counts.list_id",
			"identities[].subjects_from_them[]",
			"identities[].subjects_from_owner[]",
		},
	}
}

// KindForAnswer maps one Jev answer to a stored kind: individual_person at
// or above Threshold is a person; another decided option at or above
// Threshold maps to its kind; anything else is unclear.
func KindForAnswer(answer jev.Answer) correspondentkind.Kind {
	if answer.Probabilities[OptionIndividualPerson] >= Threshold {
		return correspondentkind.Person
	}
	if answer.Probabilities[answer.Choice] < Threshold {
		return correspondentkind.Unclear
	}
	switch answer.Choice {
	case OptionSharedMailbox:
		return correspondentkind.SharedMailbox
	case OptionMailingList:
		return correspondentkind.MailingList
	case OptionAutomated, OptionMarketing:
		return correspondentkind.Automated
	default:
		return correspondentkind.Unclear
	}
}
