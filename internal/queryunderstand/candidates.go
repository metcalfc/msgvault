package queryunderstand

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/vector"
)

// ErrQueryTooLong rejects a query longer than MaxQueryRunes.
var ErrQueryTooLong = errors.New("query is too long to judge")

// TypeCandidate is a word naming a message type option.
type TypeCandidate struct {
	Option string
	Span   string
	At     SpanPos
}

// PersonCandidate is a person the people index matched to a query word.
type PersonCandidate struct {
	ParticipantID int64
	// Label is sent and shown; it never holds an address or phone number.
	Label string
	Span  string
	At    SpanPos
}

// AccountCandidate is one of the owner's accounts a query word names.
type AccountCandidate struct {
	SourceID int64
	Label    string
	Span     string
	At       SpanPos
}

// Candidates are the options code found in one query.
type Candidates struct {
	Query    string
	Windows  []Window
	Types    []TypeCandidate
	People   []PersonCandidate
	Accounts []AccountCandidate
	// Words is how many words the query has, operators included.
	Words int
}

// AccountInput is one archived account.
type AccountInput struct {
	SourceID    int64
	SourceType  string
	Identifier  string
	DisplayName string
}

// PersonMatch is one person the people index matched to a phrase.
type PersonMatch struct {
	ParticipantID int64
	DisplayLabel  string
}

// PeopleLookup finds people whose name matches a phrase. The daemon
// answers it from its people completion index.
type PeopleLookup func(ctx context.Context, phrase string) ([]PersonMatch, error)

// Input is everything option generation reads.
type Input struct {
	Query string
	// Now is the current time in the person's time zone; date phrases are
	// read in it.
	Now      time.Time
	Accounts []AccountInput
	People   PeopleLookup
}

// maxPersonLookups bounds how many phrases one query looks up.
const maxPersonLookups = 6

var typeWords = map[string][]string{
	"email": {TypeEmail}, "emails": {TypeEmail}, "e-mail": {TypeEmail}, "e-mails": {TypeEmail},
	"mail": {TypeEmail}, "mails": {TypeEmail},
	"text": {TypeTextMessage}, "texts": {TypeTextMessage}, "texted": {TypeTextMessage},
	"sms": {TypeTextMessage}, "mms": {TypeTextMessage}, "imessage": {TypeTextMessage}, "imessages": {TypeTextMessage},
	"whatsapp": {TypeWhatsApp}, "slack": {TypeSlack}, "discord": {TypeDiscord}, "teams": {TypeTeams},
	"gchat": {TypeGoogleChat}, "hangouts": {TypeGoogleChat}, "messenger": {TypeFacebookMessenger},
	"event": {TypeCalendarEvent}, "events": {TypeCalendarEvent}, "calendar": {TypeCalendarEvent},
	"invite": {TypeCalendarEvent}, "invites": {TypeCalendarEvent},
	"invitation": {TypeCalendarEvent}, "invitations": {TypeCalendarEvent},
	"meeting": {TypeCalendarEvent, TypeMeetingTranscript}, "meetings": {TypeCalendarEvent, TypeMeetingTranscript},
	"transcript": {TypeMeetingTranscript}, "transcripts": {TypeMeetingTranscript},
}

// typePhrases are two-word names; they win over their words alone.
var typePhrases = map[[2]string][]string{
	{"text", "message"}: {TypeTextMessage}, {"text", "messages"}: {TypeTextMessage},
	{"google", "chat"}: {TypeGoogleChat}, {"facebook", "messenger"}: {TypeFacebookMessenger},
	{"meeting", "notes"}:   {TypeMeetingTranscript},
	{"calendar", "invite"}: {TypeCalendarEvent}, {"calendar", "invites"}: {TypeCalendarEvent},
}

var (
	typeLead    = wordSet("on", "in", "via", "over", "by", "through")
	personLead  = wordSet("from", "to", "with", "by", "cc")
	accountLead = wordSet("my", "in", "from", "on")
	accountTail = wordSet("account", "inbox", "mailbox")
)

// Generate finds the candidates in a query. It reads nothing but its input
// and the people lookup, which it calls at most maxPersonLookups times.
func Generate(ctx context.Context, input Input) (Candidates, error) {
	query := strings.TrimSpace(input.Query)
	if utf8.RuneCountInString(query) > MaxQueryRunes {
		return Candidates{}, ErrQueryTooLong
	}
	tokens := tokenize(query)
	used := make([]bool, len(tokens))
	candidates := Candidates{Query: query, Words: len(strings.Fields(query))}
	candidates.Windows = findWindows(query, tokens, used, input.Now)
	candidates.Types = findTypes(query, tokens, used)
	candidates.Accounts = findAccounts(query, tokens, used, input.Accounts)
	if input.People != nil {
		people, err := findPeople(ctx, query, tokens, used, input.People)
		if err != nil {
			return candidates, err
		}
		candidates.People = people
	}
	return candidates, nil
}

func findTypes(query string, tokens []token, used []bool) []TypeCandidate {
	var found []TypeCandidate
	seen := map[string]bool{}
	add := func(options []string, s span) {
		s = extendBack(tokens, used, s, typeLead, 1)
		for j := s.first; j <= s.last; j++ {
			used[j] = true
		}
		for _, option := range options {
			if !seen[option] {
				seen[option] = true
				found = append(found, TypeCandidate{Option: option, Span: s.text(query, tokens), At: s.pos(tokens)})
			}
		}
	}
	for i := 0; i < len(tokens); i++ {
		if used[i] || tokens[i].blocked {
			continue
		}
		if i+1 < len(tokens) && !used[i+1] && !tokens[i+1].blocked {
			if options, ok := typePhrases[[2]string{tokens[i].lower, tokens[i+1].lower}]; ok {
				add(options, span{first: i, last: i + 1})
				i++
				continue
			}
		}
		if options, ok := typeWords[tokens[i].lower]; ok {
			add(options, span{first: i, last: i})
		}
	}
	return found
}

// commonDomainLabels never identify one account on their own.
var commonDomainLabels = wordSet("com", "org", "net", "edu", "gov", "io", "co", "uk", "us", "de", "mail", "email", "me")

func accountKeywords(account AccountInput) map[string]bool {
	keywords := map[string]bool{}
	if sourceType := strings.ToLower(strings.TrimSpace(account.SourceType)); sourceType != "" {
		keywords[sourceType] = true
	}
	if name := accountDisplayName(account); name != "" {
		for word := range strings.FieldsSeq(strings.ToLower(name)) {
			word = strings.Trim(word, `"'.,;:()`)
			if utf8.RuneCountInString(word) >= 3 && !vector.IsStopword(word) {
				keywords[word] = true
			}
		}
	}
	if domain := accountDomain(account); domain != "" {
		for label := range strings.SplitSeq(domain, ".") {
			if utf8.RuneCountInString(label) >= 3 && !commonDomainLabels[label] {
				keywords[label] = true
			}
		}
	}
	return keywords
}

// accountDisplayName is the account's display name when it is a name, not
// an address or number.
func accountDisplayName(account AccountInput) string {
	name := strings.TrimSpace(account.DisplayName)
	if name == "" || strings.Contains(name, "@") {
		return ""
	}
	return meetingjudge.IdentifierFreeLabel(name)
}

func accountDomain(account AccountInput) string {
	_, domain, found := strings.Cut(strings.TrimSpace(account.Identifier), "@")
	if !found {
		return ""
	}
	return strings.ToLower(domain)
}

// accountLabel describes an account without its address: its type, its
// display name when that is a name, and the domain it is at.
func accountLabel(account AccountInput) string {
	sourceType := strings.TrimSpace(account.SourceType)
	if sourceType == "" {
		sourceType = "mail"
	}
	label := sourceType + " account"
	if name := accountDisplayName(account); name != "" && !strings.EqualFold(name, sourceType) {
		label += " named " + name
	}
	if domain := accountDomain(account); domain != "" {
		label += " at " + domain
	}
	return truncateRunes(label, maxLabelRunes)
}

func findAccounts(query string, tokens []token, used []bool, accounts []AccountInput) []AccountCandidate {
	// With one account there is nothing to choose between.
	if len(accounts) < 2 {
		return nil
	}
	keywords := make([]map[string]bool, len(accounts))
	for i, account := range accounts {
		keywords[i] = accountKeywords(account)
	}
	var found []AccountCandidate
	claimed := map[int64]bool{}
	for i := range tokens {
		if used[i] || tokens[i].blocked {
			continue
		}
		s := extendForward(tokens, used, extendBack(tokens, used, span{first: i, last: i}, accountLead, 2), accountTail)
		matched := false
		for a, account := range accounts {
			if claimed[account.SourceID] || !keywords[a][tokens[i].lower] || len(found) >= MaxAccounts {
				continue
			}
			claimed[account.SourceID] = true
			matched = true
			found = append(found, AccountCandidate{
				SourceID: account.SourceID, Label: accountLabel(account), Span: s.text(query, tokens), At: s.pos(tokens),
			})
		}
		if matched {
			for j := s.first; j <= s.last; j++ {
				used[j] = true
			}
		}
	}
	return found
}

// personPhrases lists the phrases to look up: pairs of adjacent name words
// first (full names), then single words, at most maxPersonLookups.
func personPhrases(tokens []token, used []bool) []span {
	eligible := func(i int) bool {
		return i < len(tokens) && !used[i] && !tokens[i].blocked && isNameWord(tokens[i].lower) &&
			!vector.IsStopword(tokens[i].lower)
	}
	var spans []span
	for i := range tokens {
		if eligible(i) && eligible(i+1) {
			spans = append(spans, span{first: i, last: i + 1})
		}
	}
	for i := range tokens {
		if eligible(i) {
			spans = append(spans, span{first: i, last: i})
		}
	}
	if len(spans) > maxPersonLookups {
		spans = spans[:maxPersonLookups]
	}
	return spans
}

func findPeople(ctx context.Context, query string, tokens []token, used []bool, lookup PeopleLookup) ([]PersonCandidate, error) {
	var found []PersonCandidate
	seen := map[int64]bool{}
	for _, s := range personPhrases(tokens, used) {
		if len(found) >= MaxPeople {
			break
		}
		phrase := make([]string, 0, 2)
		for j := s.first; j <= s.last; j++ {
			phrase = append(phrase, tokens[j].lower)
		}
		matches, err := lookup(ctx, strings.Join(phrase, " "))
		if err != nil {
			return found, err
		}
		removal := extendBack(tokens, used, s, personLead, 1)
		for _, match := range matches {
			if len(found) >= MaxPeople || seen[match.ParticipantID] || match.ParticipantID <= 0 {
				continue
			}
			label := truncateRunes(meetingjudge.IdentifierFreeLabel(match.DisplayLabel), maxLabelRunes)
			// The people index matches substrings ("art" finds "Martha");
			// only a name whose words include every query word is offered.
			if label == "" || !nameHasWords(label, phrase) {
				continue
			}
			seen[match.ParticipantID] = true
			found = append(found, PersonCandidate{
				ParticipantID: match.ParticipantID, Label: label, Span: removal.text(query, tokens), At: removal.pos(tokens),
			})
		}
	}
	return found, nil
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return strings.TrimSpace(string([]rune(value)[:limit]))
}

// nameHasWords reports whether every word of phrase is a whole word of
// name, ignoring case and accents: "jane" and "jane doe" match "Jane Doe",
// "art" does not match "Martha".
func nameHasWords(name string, phrase []string) bool {
	words := strings.FieldsFunc(foldName(name), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, want := range phrase {
		want = foldName(want)
		if utf8.RuneCountInString(want) < 2 || !slices.Contains(words, want) {
			return false
		}
	}
	return len(phrase) > 0
}

// foldName lowercases and strips accents.
func foldName(value string) string {
	var builder strings.Builder
	for _, r := range norm.NFD.String(value) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		builder.WriteRune(unicode.ToLower(r))
	}
	return builder.String()
}
