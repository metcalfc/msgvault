package vector

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// stopwords are English function words too common to say anything about a
// message. They never earn a subject boost and are left out of the lexical
// any-term fallback, where they would match nearly every message.
var stopwords = func() map[string]struct{} {
	words := strings.Fields(`a about above after again all also am an and any are as at be been before
		being below between both but by can could did do does doing down during each few for from
		further had has have having he her here hers him his how i if in into is it its just me more
		most my no nor not now of off on once only or other our ours out over own same she should so
		some such than that the their theirs them then there these they this those through to too
		under until up us very was we were what when where which while who whom why will with would
		you your yours`)
	set := make(map[string]struct{}, len(words))
	for _, word := range words {
		set[word] = struct{}{}
	}
	return set
}()

// IsStopword reports whether word (any case) is an English function word.
func IsStopword(word string) bool {
	_, ok := stopwords[strings.ToLower(strings.TrimSpace(word))]
	return ok
}

// SubjectBoostTerms turns a query's free-text terms into subject boost
// terms: lowercased, stopwords and punctuation-only terms dropped, and each
// kept once. A quoted phrase stays one term unless every word in it is a
// stopword.
func SubjectBoostTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" || !hasContentWord(term) {
			continue
		}
		if _, duplicate := seen[term]; duplicate {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

// ContentTerms keeps the terms that are not stopwords and contain a letter
// or digit, in order, preserving their spelling.
func ContentTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if hasContentWord(term) {
			out = append(out, term)
		}
	}
	return out
}

// hasContentWord reports whether text has a word that is not a stopword.
func hasContentWord(text string) bool {
	for _, word := range strings.FieldsFunc(text, isWordSeparator) {
		if !IsStopword(word) {
			return true
		}
	}
	return false
}

func isWordSeparator(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// SubjectHasTerm reports whether a lowercased subject contains term as whole
// words: the match must start at a word boundary and end at one, allowing a
// plural "s" or "es". "plan" matches "Q3 plan" and "plans" but not
// "explanation".
func SubjectHasTerm(lowerSubject, term string) bool {
	if term == "" {
		return false
	}
	for offset := 0; offset <= len(lowerSubject)-len(term); {
		index := strings.Index(lowerSubject[offset:], term)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(term)
		if wordBoundaryBefore(lowerSubject, start) && wordEnd(lowerSubject, end) {
			return true
		}
		_, size := utf8.DecodeRuneInString(lowerSubject[start:])
		offset = start + size
	}
	return false
}

func wordBoundaryBefore(text string, index int) bool {
	if index == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index])
	return isWordSeparator(r)
}

func wordEnd(text string, index int) bool {
	rest := text[index:]
	for _, suffix := range []string{"", "s", "es"} {
		if !strings.HasPrefix(rest, suffix) {
			continue
		}
		after := rest[len(suffix):]
		if after == "" {
			return true
		}
		r, _ := utf8.DecodeRuneInString(after)
		if isWordSeparator(r) {
			return true
		}
	}
	return false
}
