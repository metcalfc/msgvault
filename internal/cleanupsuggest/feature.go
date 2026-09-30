// Package cleanupsuggest finds junk and phishing worth deleting. Code picks
// a pool (spam or Promotions, never replied to, sender not a person, has
// links); Jev judges impersonation, pressure, and a category for each
// message; code combines those with hard signals such as failed DMARC into
// one score. Suggestions are only listed: staging still takes a user
// action, and nothing is ever deleted automatically.
package cleanupsuggest

import (
	"fmt"
	"strconv"

	"go.kenn.io/msgvault/internal/jev"
)

// BatchSize is how many messages one Jev request carries.
const BatchSize = 4

// BodyChars is how much of a message's opening text leaves the machine.
const BodyChars = 500

// Thresholds applied to the composite score and the category.
const (
	// SuspectThreshold is the score at or above which a message is listed as
	// suspected phishing.
	SuspectThreshold = 0.80
	// KeepThreshold is the personal-plus-work probability at or above which
	// deletion review lists a staged message as possibly worth keeping.
	KeepThreshold = 0.50
)

// Category options, in the order they are offered.
const (
	CategoryPersonal      = "personal"
	CategoryWork          = "work"
	CategoryTransactional = "transactional_or_account"
	CategoryMarketing     = "marketing_or_newsletter"
	CategoryPhishing      = "phishing_or_scam"
	CategoryOtherJunk     = "other_junk"
)

// ImpersonationID asks whether messages[i] impersonates a sender.
func ImpersonationID(i int) string { return "impersonation_" + strconv.Itoa(i) }

// PressureID asks whether messages[i] pressures the reader.
func PressureID(i int) string { return "pressure_" + strconv.Itoa(i) }

// CategoryID asks what kind of mail messages[i] is.
func CategoryID(i int) string { return "category_" + strconv.Itoa(i) }

var categoryCriteria = map[string]string{
	CategoryPersonal:      "Written by a person to the reader about their personal life, family, or friends.",
	CategoryWork:          "About the reader's job, colleagues, customers, or professional dealings.",
	CategoryTransactional: "A receipt, order, shipping, billing, account, or security notice from a service the reader uses.",
	CategoryMarketing:     "Advertising, promotions, or a newsletter sent in bulk.",
	CategoryPhishing:      "An attempt to steal credentials, money, or personal data, or to install malware.",
	CategoryOtherJunk:     "Other unwanted bulk mail that fits none of the other options.",
}

// StateFields are exactly the fields each request carries about a message.
var StateFields = []string{
	"messages[].from_name",
	"messages[].from_domain",
	"messages[].reply_to_domain",
	"messages[].link_hosts[]",
	"messages[].authentication.spf",
	"messages[].authentication.dkim",
	"messages[].authentication.dmarc",
	"messages[].addressed_as",
	"messages[].labels[]",
	"messages[].thread_replied",
	"messages[].sender_kind",
	"messages[].subject",
	"messages[].body_start",
}

// JevFeature is the exact policy the cleanup suggestion feature consents to:
// for each of up to BatchSize messages, two Nouls and one Choice over the
// fields listed. body_start is at most the first 500 characters of the
// message's text. Changing any wording or field changes the fingerprint and
// requires new consent.
func JevFeature() jev.FeatureSpec {
	questions := make([]jev.Question, 0, BatchSize*3)
	for i := range BatchSize {
		message := fmt.Sprintf("messages[%d]", i)
		questions = append(questions,
			jev.Question{
				ID: ImpersonationID(i), Type: jev.QuestionNoul,
				Instructions: fmt.Sprintf("Does `%s` pretend to come from a brand, organization, or person "+
					"that `from_domain`, `reply_to_domain`, and `link_hosts` show it is not from?", message),
				Criteria: jev.NoulCriteria{
					True: "The name, subject, or text claims a sender whose domains do not match it, " +
						"or links lead somewhere the claimed sender would not use.",
					False: "The claimed sender, its domains, and its links are consistent, or it claims no one in particular.",
				},
			},
			jev.Question{
				ID: PressureID(i), Type: jev.QuestionNoul,
				Instructions: fmt.Sprintf("Does `%s` pressure the reader to act at once: urgency, threats, "+
					"account suspension, prizes, or requests for credentials or payment?", message),
				Criteria: jev.NoulCriteria{
					True:  "It uses urgency, fear, or reward to push the reader to click, pay, reply, or sign in.",
					False: "It informs or advertises without pushing the reader to act under pressure.",
				},
			},
			jev.Question{
				ID: CategoryID(i), Type: jev.QuestionChoice,
				Instructions: fmt.Sprintf("What kind of mail is `%s`? Judge from its sender, subject, "+
					"opening text, labels, and authentication results.", message),
				Criteria: categoryCriteria,
			},
		)
	}
	return jev.FeatureSpec{
		Name:  jev.FeatureCleanupSuggestions,
		Title: "Cleanup suggestions",
		Purpose: "Judge spam and promotional mail you never replied to: whether it impersonates a sender, " +
			"pressures you to act, or is actually personal or work mail. Code combines the answers with " +
			"authentication results into a phishing score. Suggestions are only listed; nothing is staged " +
			"or deleted. Each message sends at most the first 500 characters of its text.",
		Questions:   questions,
		StateFields: StateFields,
	}
}
