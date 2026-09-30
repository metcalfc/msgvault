package queryunderstand

import (
	"fmt"
	"strconv"
	"time"
)

// Window is a time period a date phrase may mean. After is the first
// instant and Before the last instant (the end of its last local day),
// matching how the Web UI bounds a picked day.
type Window struct {
	Label  string
	Span   string
	After  time.Time
	Before time.Time
}

// dateLead are words that introduce a date phrase and go with it when the
// phrase is removed from the query.
var dateLead = wordSet("in", "during", "over", "within", "from", "on", "for", "the", "of")

var monthNames = map[string]time.Month{
	"january": time.January, "jan": time.January,
	"february": time.February, "feb": time.February,
	"march": time.March, "mar": time.March,
	"april": time.April, "apr": time.April,
	"may":  time.May,
	"june": time.June, "jun": time.June,
	"july": time.July, "jul": time.July,
	"august": time.August, "aug": time.August,
	"september": time.September, "sep": time.September, "sept": time.September,
	"october": time.October, "oct": time.October,
	"november": time.November, "nov": time.November,
	"december": time.December, "dec": time.December,
}

var numberWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	"a": 1, "couple": 2, "few": 3,
}

// dayRange is an inclusive range of local calendar days.
type dayRange struct {
	first time.Time
	last  time.Time
	desc  string
}

func startOfDay(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

func (r dayRange) window(query string, tokens []token, s span) Window {
	after := startOfDay(r.first)
	before := startOfDay(r.last).AddDate(0, 0, 1).Add(-time.Millisecond)
	return Window{
		Label:  r.desc + " (" + formatDays(r.first, r.last) + ")",
		Span:   s.text(query, tokens),
		After:  after,
		Before: before,
	}
}

func formatDays(first, last time.Time) string {
	if first.Equal(last) {
		return first.Format("Jan 2, 2006")
	}
	if first.Year() == last.Year() {
		return first.Format("Jan 2") + " to " + last.Format("Jan 2, 2006")
	}
	return first.Format("Jan 2, 2006") + " to " + last.Format("Jan 2, 2006")
}

// clip ends a range at today so a period that has not finished never
// reaches into the future.
func clip(r dayRange, today time.Time) (dayRange, bool) {
	if r.first.After(today) {
		return r, false
	}
	if r.last.After(today) {
		r.last = today
	}
	return r, true
}

func monthRange(year int, month time.Month, loc *time.Location) dayRange {
	first := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	return dayRange{first: first, last: first.AddDate(0, 1, -1), desc: first.Format("January 2006")}
}

func yearRange(year int, loc *time.Location) dayRange {
	first := time.Date(year, time.January, 1, 0, 0, 0, 0, loc)
	return dayRange{first: first, last: first.AddDate(1, 0, -1), desc: strconv.Itoa(year)}
}

func quarterRange(year, quarter int, loc *time.Location) dayRange {
	first := time.Date(year, time.Month(3*(quarter-1)+1), 1, 0, 0, 0, 0, loc)
	return dayRange{first: first, last: first.AddDate(0, 3, -1), desc: fmt.Sprintf("Q%d %d", quarter, year)}
}

func rolling(today time.Time, days int, desc string) dayRange {
	return dayRange{first: today.AddDate(0, 0, -(days - 1)), last: today, desc: desc}
}

// parseYear accepts a four-digit year from 1970 through this year.
func parseYear(word string, today time.Time) (int, bool) {
	if len(word) != 4 {
		return 0, false
	}
	year, err := strconv.Atoi(word)
	if err != nil || year < 1970 || year > today.Year() {
		return 0, false
	}
	return year, true
}

func parseQuarter(word string) (int, bool) {
	if len(word) == 2 && word[0] == 'q' && word[1] >= '1' && word[1] <= '4' {
		return int(word[1] - '0'), true
	}
	return 0, false
}

// dateMatch is one date phrase: the tokens it covers and what it may mean.
type dateMatch struct {
	span   span
	ranges []dayRange
}

// matchDate recognizes a date phrase starting at token i.
func matchDate(tokens []token, i int, today time.Time) (dateMatch, bool) {
	loc := today.Location()
	word := func(j int) string {
		if j < len(tokens) && !tokens[j].blocked {
			return tokens[j].lower
		}
		return ""
	}
	one := func(n int, ranges ...dayRange) (dateMatch, bool) {
		return dateMatch{span: span{first: i, last: i + n - 1}, ranges: ranges}, true
	}
	weekday := (int(today.Weekday()) + 6) % 7 // Monday is 0
	monday := today.AddDate(0, 0, -weekday)
	switch w := word(i); w {
	case "today":
		return one(1, dayRange{first: today, last: today, desc: "Today"})
	case "yesterday":
		yesterday := today.AddDate(0, 0, -1)
		return one(1, dayRange{first: yesterday, last: yesterday, desc: "Yesterday"})
	case "recently", "recent", "lately":
		return one(1, rolling(today, 30, "Past 30 days"))
	case "since", "after":
		if match, ok := matchDate(tokens, i+1, today); ok {
			ranges := make([]dayRange, 0, len(match.ranges))
			for _, r := range match.ranges {
				ranges = append(ranges, dayRange{first: r.first, last: today, desc: "Since " + r.desc})
			}
			return dateMatch{span: span{first: i, last: match.span.last}, ranges: ranges}, true
		}
	case "this", "last", "past":
		next := word(i + 1)
		previousMonth := time.Date(today.Year(), today.Month()-1, 1, 0, 0, 0, 0, loc)
		switch {
		case w == "this" && next == "week":
			return one(2, dayRange{first: monday, last: today, desc: "This week"})
		case w == "this" && next == "weekend":
			saturday := monday.AddDate(0, 0, 5)
			if saturday.After(today) {
				saturday = saturday.AddDate(0, 0, -7)
			}
			return one(2, dayRange{first: saturday, last: saturday.AddDate(0, 0, 1), desc: "This weekend"})
		case w == "last" && next == "weekend":
			saturday := monday.AddDate(0, 0, -2)
			return one(2, dayRange{first: saturday, last: saturday.AddDate(0, 0, 1), desc: "Last weekend"})
		case w == "this" && next == "month":
			return one(2, dayRange{first: time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc), last: today, desc: "This month"})
		case w == "this" && next == "year":
			return one(2, dayRange{first: time.Date(today.Year(), time.January, 1, 0, 0, 0, 0, loc), last: today, desc: "This year"})
		case w == "last" && next == "week":
			previous := monday.AddDate(0, 0, -7)
			return one(2, dayRange{first: previous, last: previous.AddDate(0, 0, 6), desc: "Previous calendar week"},
				rolling(today, 7, "Past 7 days"))
		case w == "past" && next == "week":
			return one(2, rolling(today, 7, "Past 7 days"))
		case w == "last" && next == "month":
			previous := monthRange(previousMonth.Year(), previousMonth.Month(), loc)
			previous.desc = "Previous calendar month, " + previous.desc
			return one(2, previous, rolling(today, 30, "Past 30 days"))
		case w == "past" && next == "month":
			return one(2, rolling(today, 30, "Past 30 days"))
		case w == "last" && next == "year":
			previous := yearRange(today.Year()-1, loc)
			previous.desc = "Previous calendar year, " + previous.desc
			return one(2, previous, rolling(today, 365, "Past 365 days"))
		case w == "past" && next == "year":
			return one(2, rolling(today, 365, "Past 365 days"))
		case w != "this":
			if count, ok := parseCount(next); ok {
				if r, ok := rollingUnits(today, count, word(i+2)); ok {
					return one(3, r)
				}
			}
		}
	}
	if month, ok := monthNames[word(i)]; ok {
		if year, ok := parseYear(word(i+1), today); ok {
			if r, ok := clip(monthRange(year, month, loc), today); ok {
				return one(2, r)
			}
			return dateMatch{}, false
		}
		year := today.Year()
		if month > today.Month() {
			year--
		}
		latest, _ := clip(monthRange(year, month, loc), today)
		return one(1, latest, monthRange(year-1, month, loc))
	}
	if quarter, ok := parseQuarter(word(i)); ok {
		if year, ok := parseYear(word(i+1), today); ok {
			if r, ok := clip(quarterRange(year, quarter, loc), today); ok {
				return one(2, r)
			}
			return dateMatch{}, false
		}
		year := today.Year()
		if time.Month(3*(quarter-1)+1) > today.Month() {
			year--
		}
		latest, _ := clip(quarterRange(year, quarter, loc), today)
		return one(1, latest)
	}
	if year, ok := parseYear(word(i), today); ok {
		r, _ := clip(yearRange(year, loc), today)
		return one(1, r)
	}
	return dateMatch{}, false
}

func parseCount(word string) (int, bool) {
	if count, ok := numberWords[word]; ok {
		return count, true
	}
	count, err := strconv.Atoi(word)
	if err != nil || count < 1 || count > 999 {
		return 0, false
	}
	return count, true
}

func rollingUnits(today time.Time, count int, unit string) (dayRange, bool) {
	plural := func(name string) string {
		if count == 1 {
			return "Past " + name
		}
		return fmt.Sprintf("Past %d %ss", count, name)
	}
	switch unit {
	case "day", "days":
		return rolling(today, count, plural("day")), true
	case "week", "weeks":
		return rolling(today, 7*count, plural("week")), true
	case "month", "months":
		first := today.AddDate(0, -count, 1)
		return dayRange{first: first, last: today, desc: plural("month")}, true
	case "year", "years":
		first := today.AddDate(-count, 0, 1)
		return dayRange{first: first, last: today, desc: plural("year")}, true
	}
	return dayRange{}, false
}

// findWindows finds every date phrase and returns up to MaxWindows windows,
// marking the tokens it used.
func findWindows(query string, tokens []token, used []bool, now time.Time) []Window {
	today := startOfDay(now)
	var windows []Window
	for i := 0; i < len(tokens); i++ {
		if used[i] || tokens[i].blocked {
			continue
		}
		match, ok := matchDate(tokens, i, today)
		if !ok {
			continue
		}
		s := extendBack(tokens, used, match.span, dateLead, 2)
		for j := s.first; j <= s.last; j++ {
			used[j] = true
		}
		for _, r := range match.ranges {
			if len(windows) < MaxWindows {
				windows = append(windows, r.window(query, tokens, s))
			}
		}
		i = s.last
	}
	return windows
}
