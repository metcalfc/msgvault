package queryunderstand

import (
	"strings"
	"unicode"
)

// token is one word of the query and where it sits. Operator tokens
// (from:x) and quoted phrases are marked blocked: they are never part of a
// candidate span.
type token struct {
	lower   string
	start   int
	end     int
	blocked bool
}

// tokenize splits a query into words at whitespace, trimming surrounding
// punctuation from each word so a span never swallows a comma. A trailing
// possessive ("Ana's") is dropped from the word but kept in its span.
func tokenize(query string) []token {
	var tokens []token
	inQuote := false
	for start := 0; start < len(query); {
		for start < len(query) && isSpace(query[start]) {
			start++
		}
		if start >= len(query) {
			break
		}
		end := start
		for end < len(query) && !isSpace(query[end]) {
			end++
		}
		field := query[start:end]
		quotes := strings.Count(field, `"`)
		blocked := inQuote || quotes > 0 || strings.Contains(field, ":") || strings.Contains(field, "=")
		if quotes%2 == 1 {
			inQuote = !inQuote
		}
		wordStart, wordEnd := trimWord(query, start, end)
		if wordStart < wordEnd {
			word := strings.ToLower(query[wordStart:wordEnd])
			word = strings.TrimSuffix(strings.TrimSuffix(word, "'s"), "’s")
			tokens = append(tokens, token{lower: word, start: wordStart, end: wordEnd, blocked: blocked})
		}
		start = end
	}
	return tokens
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// trimWord narrows [start, end) to the part between its first and last
// letter or digit.
func trimWord(query string, start, end int) (int, int) {
	runes := []rune(query[start:end])
	first, last := -1, -1
	for i, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return start, start
	}
	// Keep a possessive suffix attached to the word.
	if last+2 < len(runes) && (runes[last+1] == '\'' || runes[last+1] == '’') && (runes[last+2] == 's' || runes[last+2] == 'S') {
		last += 2
	}
	return start + len(string(runes[:first])), start + len(string(runes[:last+1]))
}

// isNameWord reports whether a word could be part of a person's name.
func isNameWord(word string) bool {
	letters := 0
	for _, r := range word {
		switch {
		case unicode.IsLetter(r):
			letters++
		case r == '-' || r == '\'' || r == '’' || r == '.':
		default:
			return false
		}
	}
	return letters >= 2
}

// span is a run of tokens [first, last].
type span struct{ first, last int }

func (s span) text(query string, tokens []token) string {
	return query[tokens[s.first].start:tokens[s.last].end]
}

// SpanPos is where a span sits in the (trimmed) query, as byte offsets.
type SpanPos struct {
	Start int
	End   int
}

func (s span) pos(tokens []token) SpanPos {
	return SpanPos{Start: tokens[s.first].start, End: tokens[s.last].end}
}

// extendBack widens a span over up to limit preceding words from words, so
// "from Ana" and "in March" are removed whole.
func extendBack(tokens []token, used []bool, s span, words map[string]bool, limit int) span {
	for range limit {
		previous := s.first - 1
		if previous < 0 || used[previous] || tokens[previous].blocked || !words[tokens[previous].lower] {
			break
		}
		s.first = previous
	}
	return s
}

func extendForward(tokens []token, used []bool, s span, words map[string]bool) span {
	next := s.last + 1
	if next < len(tokens) && !used[next] && !tokens[next].blocked && words[tokens[next].lower] {
		s.last = next
	}
	return s
}

func wordSet(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[word] = true
	}
	return set
}
