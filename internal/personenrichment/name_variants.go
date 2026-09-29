package personenrichment

import (
	"slices"
	"strings"
)

// nameSuffixes are generational and credential suffixes that providers often
// omit. Compared after composite normalization (folded, single-spaced).
var nameSuffixes = map[string]struct{}{
	"jr": {}, "jr.": {}, "sr": {}, "sr.": {}, "ii": {}, "iii": {}, "iv": {},
	"phd": {}, "ph.d.": {}, "ph.d": {}, "md": {}, "m.d.": {}, "esq": {}, "esq.": {},
	"mba": {}, "cpa": {}, "dds": {}, "jd": {}, "j.d.": {},
}

// NameVariants returns deterministic rewrites of a person name that a public
// source may use instead: "Last, First" collapsed to "First Last", trailing
// suffixes such as Jr. dropped, and middle names or initials dropped. Each
// variant is composite-normalized, unique, and differs from the normalized
// input; the order is the retry order. No network and no model is involved.
func NameVariants(name string) []string {
	original := normalizeCompositePart(name)
	if original == "" {
		return nil
	}
	seen := map[string]struct{}{original: {}}
	variants := make([]string, 0, 4)
	add := func(tokens []string) {
		candidate := strings.Join(tokens, " ")
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		variants = append(variants, candidate)
	}

	// Comma-free tokens drive suffix and middle handling; the comma itself
	// only signals "Last, First" order.
	tokens := strings.Fields(strings.ReplaceAll(original, ",", " "))
	if last, rest, found := strings.Cut(original, ","); found {
		restTokens := dropSuffixTokens(strings.Fields(strings.ReplaceAll(rest, ",", " ")), 0)
		lastTokens := dropSuffixTokens(strings.Fields(last), 0)
		if len(restTokens) > 0 && len(lastTokens) > 0 {
			tokens = append(slices.Clone(restTokens), lastTokens...)
		} else {
			tokens = dropSuffixTokens(tokens, 1)
		}
	} else {
		tokens = dropSuffixTokens(tokens, 1)
	}
	add(tokens)
	if len(tokens) >= 3 {
		add([]string{tokens[0], tokens[len(tokens)-1]})
	}
	if len(variants) == 0 {
		return nil
	}
	return variants
}

// dropSuffixTokens removes trailing suffix tokens, keeping at least keep
// tokens so a name never collapses to nothing.
func dropSuffixTokens(tokens []string, keep int) []string {
	trimmed := slices.Clone(tokens)
	for len(trimmed) > keep {
		if _, suffix := nameSuffixes[trimmed[len(trimmed)-1]]; !suffix {
			break
		}
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

// nameIdentifierMatch reports whether a returned name is the requested name
// or one of its deterministic variants after normalization. It is the only
// widening of the exact rule and never runs in the other direction: a
// returned name that is shorter than every variant does not match.
func nameIdentifierMatch(requestName, returnedName string) bool {
	if requestName == "" || returnedName == "" {
		return false
	}
	want := normalizeCompositePart(requestName)
	got := normalizeCompositePart(returnedName)
	if want == "" || got == "" {
		return false
	}
	if want == got {
		return true
	}
	return slices.Contains(NameVariants(requestName), got)
}
