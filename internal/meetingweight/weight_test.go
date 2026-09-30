package meetingweight

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExclusionRules(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		want     Exclusion
	}{
		{"ordinary meeting", `{"status":"confirmed","event_type":"default"}`, ""},
		{"missing metadata", ``, ""},
		{"invalid metadata", `{`, ""},
		{"cancelled", `{"status":"cancelled"}`, ExcludedCancelled},
		{"owner declined", `{"owner_response_status":"declined"}`, ExcludedOwnerDeclined},
		{"owner accepted", `{"owner_response_status":"accepted"}`, ""},
		{"out of office", `{"event_type":"outOfOffice"}`, ExcludedOutOfOffice},
		{"focus time", `{"event_type":"focusTime"}`, ExcludedFocusTime},
		{"working location", `{"event_type":"workingLocation"}`, ExcludedWorkingLocation},
		{"transparent", `{"transparency":"transparent"}`, ExcludedTransparent},
		{"opaque", `{"transparency":"opaque"}`, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ParseMetadata(test.metadata).Exclusion())
		})
	}
}

func TestCountWeightCapsAttendees(t *testing.T) {
	assert := assert.New(t)
	assert.InDelta(1.0, CountWeight(0), 1e-9)
	assert.InDelta(1.0, CountWeight(2), 1e-9)
	assert.InDelta(1.0, CountWeight(AttendeeCap), 1e-9)
	assert.InDelta(0.5, CountWeight(2*AttendeeCap), 1e-9)
	assert.InDelta(0.05, CountWeight(200), 1e-9)
}

func TestWeightPrefersExclusionThenConfidentKind(t *testing.T) {
	assert := assert.New(t)
	ordinary := Metadata{Status: "confirmed"}
	assert.InDelta(0.0, Weight(Metadata{EventType: "focusTime"}, 2,
		&Judgment{Kind: KindOneOnOne, Confidence: 0.99}), 1e-9, "a plain exclusion wins over any judgment")
	assert.InDelta(0.25, Weight(ordinary, 3, &Judgment{Kind: KindLargeGroupOrAllHands, Confidence: 0.60}), 1e-9)
	assert.InDelta(0.0, Weight(ordinary, 3, &Judgment{Kind: KindExternalWebinar, Confidence: 0.9}), 1e-9)
	assert.InDelta(1.0, Weight(ordinary, 3, &Judgment{Kind: KindExternalWebinar, Confidence: 0.59}), 1e-9,
		"below the threshold the attendee count decides")
	assert.InDelta(0.5, Weight(ordinary, 20, nil), 1e-9)
	assert.InDelta(0.5, Weight(ordinary, 20, &Judgment{Kind: "unknown", Confidence: 0.9}), 1e-9)
	for _, kind := range Kinds() {
		_, ok := KindWeight(kind)
		assert.True(ok, "every kind has a weight: %s", kind)
	}
}

func TestSocialWithOnePersonCountsAsAOneOnOne(t *testing.T) {
	ordinary := Metadata{Status: "confirmed"}
	tests := []struct {
		name      string
		attendees int
		want      float64
	}{
		{"drinks with one person", OneOnOneAttendees, 1},
		{"no invite list", 0, 1},
		{"three people", 3, 0.5},
		{"party", 30, 0.5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.InDelta(t, test.want,
				Weight(ordinary, test.attendees, &Judgment{Kind: KindSocial, Confidence: 0.95}), 1e-9)
		})
	}
	assert.InDelta(t, 0.0, Weight(Metadata{Status: "cancelled"}, OneOnOneAttendees,
		&Judgment{Kind: KindSocial, Confidence: 0.95}), 1e-9, "exclusions still win")
}
