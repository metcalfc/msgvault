package queryunderstand

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// now is Wednesday 2026-09-30 at noon in UTC.
var now = time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)

func day(month time.Month, d, year int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func endOf(t time.Time) time.Time { return t.AddDate(0, 0, 1).Add(-time.Millisecond) }

func TestFindWindows(t *testing.T) {
	tests := []struct {
		query string
		want  []Window
	}{
		{"budget last week", []Window{
			{Label: "Previous calendar week (Sep 21 to Sep 27, 2026)", Span: "last week",
				After: day(time.September, 21, 2026), Before: endOf(day(time.September, 27, 2026))},
			{Label: "Past 7 days (Sep 24 to Sep 30, 2026)", Span: "last week",
				After: day(time.September, 24, 2026), Before: endOf(day(time.September, 30, 2026))},
		}},
		{"offsite notes in 2025", []Window{
			{Label: "2025 (Jan 1 to Dec 31, 2025)", Span: "in 2025",
				After: day(time.January, 1, 2025), Before: endOf(day(time.December, 31, 2025))},
		}},
		{"invoices since March 2026", []Window{
			{Label: "Since March 2026 (Mar 1 to Sep 30, 2026)", Span: "since March 2026",
				After: day(time.March, 1, 2026), Before: endOf(day(time.September, 30, 2026))},
		}},
		{"trip in december", []Window{
			{Label: "December 2025 (Dec 1 to Dec 31, 2025)", Span: "in december",
				After: day(time.December, 1, 2025), Before: endOf(day(time.December, 31, 2025))},
			{Label: "December 2024 (Dec 1 to Dec 31, 2024)", Span: "in december",
				After: day(time.December, 1, 2024), Before: endOf(day(time.December, 31, 2024))},
		}},
		{"photos from the past 3 days", []Window{
			{Label: "Past 3 days (Sep 28 to Sep 30, 2026)", Span: "from the past 3 days",
				After: day(time.September, 28, 2026), Before: endOf(day(time.September, 30, 2026))},
		}},
		{"Q3 plan yesterday", []Window{
			{Label: "Q3 2026 (Jul 1 to Sep 30, 2026)", Span: "Q3",
				After: day(time.July, 1, 2026), Before: endOf(day(time.September, 30, 2026))},
			{Label: "Yesterday (Sep 29, 2026)", Span: "yesterday",
				After: day(time.September, 29, 2026), Before: endOf(day(time.September, 29, 2026))},
		}},
		{"release 2031 and after:2025-01-01", []Window{}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			assert := assert.New(t)
			tokens := tokenize(tt.query)
			assert.Equal(tt.want, unplaced(findWindows(tt.query, tokens, make([]bool, len(tokens)), now)))
		})
	}
}

func TestGenerateFindsTypesAccountsAndPeople(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var lookups []string
	people := func(_ context.Context, phrase string) ([]PersonMatch, error) {
		lookups = append(lookups, phrase)
		switch phrase {
		case "jane doe":
			return []PersonMatch{{ParticipantID: 7, DisplayLabel: "Jane Doe <jane.doe@example.com>"}}, nil
		case "jane":
			return []PersonMatch{
				{ParticipantID: 7, DisplayLabel: "Jane Doe"},
				{ParticipantID: 9, DisplayLabel: "+1 555 010 0199"},
				{ParticipantID: 8, DisplayLabel: "jane.roe@example.org"},
			}, nil
		}
		return nil, nil
	}
	candidates, err := Generate(t.Context(), Input{
		Query: `budget emails from Jane Doe in my work account last week from:boss@example.com`,
		Now:   now,
		Accounts: []AccountInput{
			{SourceID: 1, SourceType: "gmail", Identifier: "owner@example.com", DisplayName: "Work"},
			{SourceID: 2, SourceType: "imap", Identifier: "owner@example.net", DisplayName: "owner@example.net"},
		},
		People: people,
	})
	require.NoError(err)
	assert.Equal([]TypeCandidate{{Option: TypeEmail, Span: "emails"}}, unplaced(candidates.Types))
	assert.Equal([]AccountCandidate{
		{SourceID: 1, Label: "gmail account named Work at example.com", Span: "in my work account"},
	}, unplaced(candidates.Accounts))
	assert.Equal([]PersonCandidate{
		{ParticipantID: 7, Label: "Jane Doe", Span: "from Jane Doe"},
		{ParticipantID: 8, Label: "jane.roe", Span: "from Jane"},
	}, unplaced(candidates.People), "addresses and phone-number labels never become labels")
	require.Len(candidates.Windows, 2)
	assert.Equal("last week", candidates.Windows[0].Span)
	assert.Equal([]string{"jane doe", "budget", "jane", "doe"}, lookups,
		"full names first; operator tokens, stopwords, and words claimed by other kinds are never looked up")
}

func TestGenerateNeedsTwoAccountsAndBoundsQueries(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	candidates, err := Generate(t.Context(), Input{
		Query: "gmail receipts", Now: now,
		Accounts: []AccountInput{{SourceID: 1, SourceType: "gmail", Identifier: "owner@example.com"}},
	})
	require.NoError(err)
	assert.Empty(candidates.Accounts, "one account leaves nothing to choose")

	_, err = Generate(t.Context(), Input{Query: strings.Repeat("a", MaxQueryRunes+1), Now: now})
	require.ErrorIs(err, ErrQueryTooLong)

	failure := errors.New("people index unavailable")
	_, err = Generate(t.Context(), Input{
		Query: "notes from Ana", Now: now,
		People: func(context.Context, string) ([]PersonMatch, error) { return nil, failure },
	})
	require.ErrorIs(err, failure)
}

func TestTypePhrasesAndMeetingAmbiguity(t *testing.T) {
	assert := assert.New(t)
	tokens := tokenize("text messages and meetings on slack")
	used := make([]bool, len(tokens))
	assert.Equal([]TypeCandidate{
		{Option: TypeTextMessage, Span: "text messages"},
		{Option: TypeCalendarEvent, Span: "meetings"},
		{Option: TypeMeetingTranscript, Span: "meetings"},
		{Option: TypeSlack, Span: "on slack"},
	}, unplaced(findTypes("text messages and meetings on slack", tokens, used)))
}

func TestPeopleMustMatchWholeNameWords(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	index := func(_ context.Context, phrase string) ([]PersonMatch, error) {
		// The people index matches substrings, as the daemon's does.
		all := []PersonMatch{
			{ParticipantID: 1, DisplayLabel: "Martha Example"},
			{ParticipantID: 2, DisplayLabel: "Art Sample"},
			{ParticipantID: 3, DisplayLabel: "Zoë Müller"},
		}
		var found []PersonMatch
		for _, person := range all {
			if strings.Contains(strings.ToLower(person.DisplayLabel), phrase) {
				found = append(found, person)
			}
		}
		return found, nil
	}
	labels := func(query string) []string {
		candidates, err := Generate(t.Context(), Input{Query: query, Now: now, People: index})
		require.NoError(err)
		out := []string{}
		for _, person := range candidates.People {
			out = append(out, person.Label)
		}
		return out
	}
	assert.Equal([]string{"Art Sample"}, labels("art"), "a substring of Martha is not her name")
	assert.Equal([]string{"Martha Example"}, labels("martha"))
	assert.Equal([]string{"Martha Example"}, labels("MARTHA example"))
	assert.Equal([]string{}, labels("zoe"), "the index itself must find the name")
	assert.True(nameHasWords("Zoë Müller", []string{"zoe", "muller"}), "accents are ignored")
	assert.False(nameHasWords("Martha Example", []string{"art"}))
}

func TestPeopleWithPunctuatedNames(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	index := func(context.Context, string) ([]PersonMatch, error) {
		all := []PersonMatch{
			{ParticipantID: 1, DisplayLabel: "Anne-Marie Example"},
			{ParticipantID: 2, DisplayLabel: "Pat O’Neil"},
			{ParticipantID: 3, DisplayLabel: "Anne Sample"},
		}
		// The worst case: an index that returns everyone for any phrase.
		return all, nil
	}
	labels := func(query string) []string {
		candidates, err := Generate(t.Context(), Input{Query: query, Now: now, People: index})
		require.NoError(err)
		out := []string{}
		for _, person := range candidates.People {
			out = append(out, person.Label+"|"+person.Span)
		}
		return out
	}
	assert.Equal([]string{"Anne-Marie Example|from Anne-Marie"}, labels("notes from Anne-Marie"))
	assert.Equal([]string{"Pat O’Neil|from O'Neil"}, labels("notes from O'Neil"))
	assert.Equal([]string{"Anne-Marie Example|from Anne", "Anne Sample|from Anne"}, labels("notes from Anne"))
}

// placed is any candidate or suggestion with a query position.
type placed interface {
	Window | TypeCandidate | PersonCandidate | AccountCandidate | Suggestion
}

// unplaced clears positions so expectations can list spans by text;
// TestSpanPositions checks the positions themselves.
func unplaced[T placed](values []T) []T {
	out := make([]T, len(values))
	for i, value := range values {
		switch v := any(&value).(type) {
		case *Window:
			v.At = SpanPos{}
		case *TypeCandidate:
			v.At = SpanPos{}
		case *PersonCandidate:
			v.At = SpanPos{}
		case *AccountCandidate:
			v.At = SpanPos{}
		case *Suggestion:
			v.At = SpanPos{}
		}
		out[i] = value
	}
	return out
}

func TestSpanPositionsPointAtTheCandidateNotAnEarlierCopy(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	query := `"last week" notes last week`
	candidates, err := Generate(t.Context(), Input{Query: query, Now: now})
	require.NoError(err)
	require.Len(candidates.Windows, 2)
	for _, window := range candidates.Windows {
		assert.Equal("last week", window.Span)
		assert.Equal(SpanPos{Start: 18, End: 27}, window.At, "the quoted phrase is not a candidate")
		assert.Equal(window.Span, query[window.At.Start:window.At.End])
	}
}
