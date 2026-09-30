// Package meetingweight decides how much a calendar event counts as a
// meeting with its attendees. Relationship rankings multiply meeting
// activity by this weight, and the activity spine leaves out an event whose
// weight is zero.
//
// Plain rules come first: a cancelled event, an event the owner declined,
// an out-of-office, focus-time, or working-location block, and an event
// marked free (transparent) are not meetings and weigh nothing. Otherwise a
// confident event kind judgment sets the weight, and without one the
// attendee count does: a meeting counts in full up to AttendeeCap attendees
// and proportionally less above it, so an all-hands does not outweigh a
// one-on-one.
package meetingweight

import (
	"encoding/json/v2"
	"strings"
)

// Exclusion names why an event is not a meeting. Empty means it is one.
type Exclusion string

// Exclusions, checked in this order.
const (
	ExcludedCancelled       Exclusion = "cancelled"
	ExcludedOwnerDeclined   Exclusion = "owner_declined"
	ExcludedOutOfOffice     Exclusion = "out_of_office"
	ExcludedFocusTime       Exclusion = "focus_time"
	ExcludedWorkingLocation Exclusion = "working_location"
	ExcludedTransparent     Exclusion = "transparent"
)

// AttendeeCap is the attendee count up to which a meeting counts in full.
// A larger meeting weighs AttendeeCap divided by its attendee count.
const AttendeeCap = 10

// KindThreshold is the probability an event kind needs before it sets the
// weight. Below it the attendee-count weight applies.
const KindThreshold = 0.60

// Metadata is the part of a calendar event's messages.metadata this package
// reads. Calendar sync writes these keys.
type Metadata struct {
	Status              string `json:"status"`
	EventType           string `json:"event_type"`
	Transparency        string `json:"transparency"`
	OwnerResponseStatus string `json:"owner_response_status"`
	// AttendeeCount is the invited people count calendar sync records;
	// zero for events synced before it did.
	AttendeeCount int `json:"attendee_count"`
}

// ParseMetadata reads the keys it needs from a metadata document. Invalid
// or missing metadata reads as an ordinary event.
func ParseMetadata(raw string) Metadata {
	var metadata Metadata
	if strings.TrimSpace(raw) == "" {
		return metadata
	}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return Metadata{}
	}
	return metadata
}

// Exclusion reports why the event is not a meeting, or "" when it is one.
func (m Metadata) Exclusion() Exclusion {
	switch {
	case strings.EqualFold(strings.TrimSpace(m.Status), "cancelled"):
		return ExcludedCancelled
	case strings.EqualFold(strings.TrimSpace(m.OwnerResponseStatus), "declined"):
		return ExcludedOwnerDeclined
	}
	switch strings.TrimSpace(m.EventType) {
	case "outOfOffice":
		return ExcludedOutOfOffice
	case "focusTime":
		return ExcludedFocusTime
	case "workingLocation":
		return ExcludedWorkingLocation
	}
	if strings.EqualFold(strings.TrimSpace(m.Transparency), "transparent") {
		return ExcludedTransparent
	}
	return ""
}

// CountWeight is the attendee-count weight: 1 up to AttendeeCap attendees,
// then AttendeeCap divided by the count.
func CountWeight(attendees int) float64 {
	if attendees <= AttendeeCap {
		return 1
	}
	return float64(AttendeeCap) / float64(attendees)
}

// Kind is a judged event kind.
type Kind string

// Event kinds, in the order the judgment offers them.
const (
	KindOneOnOne              Kind = "one_on_one"
	KindSmallWorkingMeeting   Kind = "small_working_meeting"
	KindLargeGroupOrAllHands  Kind = "large_group_or_all_hands"
	KindExternalWebinar       Kind = "external_webinar_or_marketing"
	KindPersonalHoldLogistics Kind = "personal_hold_or_logistics"
	KindSocial                Kind = "social"
)

// Kinds lists every event kind in offer order.
func Kinds() []Kind {
	return []Kind{
		KindOneOnOne, KindSmallWorkingMeeting, KindLargeGroupOrAllHands,
		KindExternalWebinar, KindPersonalHoldLogistics, KindSocial,
	}
}

var kindWeights = map[Kind]float64{
	KindOneOnOne:              1,
	KindSmallWorkingMeeting:   1,
	KindLargeGroupOrAllHands:  0.25,
	KindExternalWebinar:       0,
	KindPersonalHoldLogistics: 0,
	KindSocial:                0.5,
}

// KindWeight is the weight a confident judgment of kind gives an event.
func KindWeight(kind Kind) (float64, bool) {
	weight, ok := kindWeights[kind]
	return weight, ok
}

// Judgment is a stored event kind judgment. Confidence is the probability
// of Kind.
type Judgment struct {
	Kind       Kind
	Confidence float64
}

// Weight combines the rules: an excluded event weighs 0; a judgment at or
// above KindThreshold sets the weight; otherwise the attendee count does.
func Weight(metadata Metadata, attendees int, judgment *Judgment) float64 {
	if metadata.Exclusion() != "" {
		return 0
	}
	if judgment != nil && judgment.Confidence >= KindThreshold {
		if weight, ok := KindWeight(judgment.Kind); ok {
			return weight
		}
	}
	return CountWeight(attendees)
}
