package jev

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Token budgets. TypeSafe documents a 32,000-token budget for the state plus
// the longest question and 64,000 tokens for a whole request, and answers a
// request over either with HTTP 400 and error_type max_tokens_exceeded.
// msgvault cannot run the provider's tokenizer, so it estimates (see
// EstimateTokens) and packs to budgets a little below the documented ones.
const (
	// MaxStateTokens is the estimated-token budget for one request's state
	// plus its longest question. Every request is checked against it before
	// it is sent, and features pack their items to fit it.
	MaxStateTokens = 30_000
	// MaxRequestTokens is the estimated-token budget for a whole encoded
	// request: state, model, and every question.
	MaxRequestTokens = 60_000
)

// ErrStateTooLarge reports a request whose state is over the token budget:
// either msgvault's estimate refused it before sending (the error also
// matches ErrRequestBounds), or the provider answered max_tokens_exceeded.
// Features that pack several items into one state split and retry on it.
var ErrStateTooLarge = errors.New("request state exceeds the Jev token budget")

// Oversize reports whether err means the request was too big to send or too
// big for the provider, so a smaller request may succeed.
func Oversize(err error) bool {
	return errors.Is(err, ErrStateTooLarge) || errors.Is(err, ErrRequestBounds)
}

// EstimateTokens is a deliberately pessimistic token count for text as it
// appears on the wire (callers pass encoded JSON). The assumption, calibrated
// against the dense order and receipt mail that overran the provider budget:
//
//   - a run of ASCII letters costs one token per four letters, rounded up,
//     which is close to real tokenizers for prose;
//   - every ASCII digit costs one token, since numbers, SKUs, and tracking
//     ids are where tokenizers are least efficient;
//   - every other ASCII symbol (punctuation, URL separators, JSON escapes)
//     costs one token, and a newline or a run of two or more spaces costs one;
//   - a non-ASCII rune costs one token per UTF-8 continuation byte, at least one.
//
// The estimate is never below one token per four bytes. Prose estimates
// near a typical tokenizer; dense text estimates well above it, which is
// the direction that keeps requests under the provider's budget.
func EstimateTokens(text string) int {
	tokens := 0
	letters := 0
	spaces := 0
	flushLetters := func() {
		if letters > 0 {
			tokens += (letters + 3) / 4
			letters = 0
		}
	}
	flushSpaces := func() {
		if spaces > 1 {
			tokens++
		}
		spaces = 0
	}
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			flushSpaces()
			letters++
			continue
		case r == ' ':
			flushLetters()
			spaces++
			continue
		}
		flushLetters()
		flushSpaces()
		switch {
		case r >= utf8.RuneSelf:
			tokens += max(1, utf8.RuneLen(r)-1)
		default:
			// Digits, punctuation, newlines, tabs, and control characters.
			tokens++
		}
	}
	flushLetters()
	flushSpaces()
	return max(tokens, (len(text)+3)/4)
}

// EstimateStateTokens estimates the provider's state budget for one request:
// the encoded state plus the longest encoded question.
func EstimateStateTokens(state any, questions []Question) (int, error) {
	encoded, err := json.Marshal(state, json.Deterministic(true))
	if err != nil {
		return 0, errors.New("encode Jev state")
	}
	return EstimateTokens(string(encoded)) + longestQuestionTokens(questions), nil
}

// FitsStateBudget reports whether a state and questions fit within budget
// estimated tokens.
func FitsStateBudget(state any, questions []Question, budget int) bool {
	tokens, err := EstimateStateTokens(state, questions)
	return err == nil && tokens <= budget
}

func stateTooLarge(tokens, budget int) error {
	return fmt.Errorf("%w: %w: estimated %d tokens exceed %d", ErrRequestBounds, ErrStateTooLarge, tokens, budget)
}

// Span is a contiguous run of items, [Start, End).
type Span struct {
	Start, End int
}

// Len is the number of items in the span.
func (s Span) Len() int { return s.End - s.Start }

// PackSpans splits n items into contiguous spans of at most maxItems whose
// state, rendered by build(start, end), fits within budget estimated tokens
// together with the longest of questions. Pass the feature's full question
// list: the longest question bounds every subset. An item that does not fit
// even alone gets a span of its own; the client refuses it before sending.
func PackSpans(n, maxItems, budget int, questions []Question, build func(start, end int) any) []Span {
	if n <= 0 {
		return nil
	}
	maxItems = max(1, maxItems)
	longest := longestQuestionTokens(questions)
	spans := make([]Span, 0, 1)
	for start := 0; start < n; {
		end := start + 1
		for end < n && end-start < maxItems && fitsState(build(start, end+1), longest, budget) {
			end++
		}
		spans = append(spans, Span{Start: start, End: end})
		start = end
	}
	return spans
}

// JudgeSpans packs n items with PackSpans at MaxStateTokens and calls judge
// for each span in order. When judge reports that a span was too large for
// the provider (ErrStateTooLarge) and the span holds more than one item, the
// span is repacked once at half its own estimated size and each piece is
// judged; a second oversize answer is returned. Any other error stops at
// once and is returned.
func JudgeSpans(n, maxItems int, questions []Question, build func(start, end int) any, judge func(span Span) error) error {
	longest := longestQuestionTokens(questions)
	for _, span := range PackSpans(n, maxItems, MaxStateTokens, questions, build) {
		err := judge(span)
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrStateTooLarge) || span.Len() < 2 {
			return err
		}
		estimate := stateTokens(build(span.Start, span.End), longest)
		half := min(MaxStateTokens, estimate) / 2
		pieces := PackSpans(span.Len(), maxItems, half, questions, func(start, end int) any {
			return build(span.Start+start, span.Start+end)
		})
		if len(pieces) < 2 {
			pieces = []Span{{0, span.Len() / 2}, {span.Len() / 2, span.Len()}}
		}
		for _, piece := range pieces {
			if err := judge(Span{Start: span.Start + piece.Start, End: span.Start + piece.End}); err != nil {
				return err
			}
		}
	}
	return nil
}

// longestQuestionTokens estimates the longest question as encoded on the
// wire. A question that cannot be encoded is refused by Encode anyway.
func longestQuestionTokens(questions []Question) int {
	longest := 0
	for _, question := range questions {
		wire, err := json.Marshal(wireQuestion{
			Type: question.Type, Instructions: question.Instructions, Criteria: question.Criteria,
		}, json.Deterministic(true))
		if err != nil {
			continue
		}
		longest = max(longest, EstimateTokens(string(wire)))
	}
	return longest
}

func stateTokens(state any, longestQuestion int) int {
	encoded, err := json.Marshal(state, json.Deterministic(true))
	if err != nil {
		return MaxRequestTokens
	}
	return EstimateTokens(string(encoded)) + longestQuestion
}

func fitsState(state any, longestQuestion, budget int) bool {
	return stateTokens(state, longestQuestion) <= budget
}
