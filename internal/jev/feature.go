package jev

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// FeatureSpec describes one Jev-backed feature: the exact questions it asks
// and the exact state fields it sends. Consent binds to a fingerprint of this
// spec plus the destination and model, so changing any of it requires new
// consent.
type FeatureSpec struct {
	// Name is the stable lowercase identifier used in config, consent rows,
	// daily counters, and the CLI.
	Name string
	// Title and Purpose are shown in disclosures.
	Title   string
	Purpose string
	// Questions is the exact wording sent with every request.
	Questions []Question
	// StateFields lists every field path that leaves the machine.
	StateFields []string
	// BodyNotice, when set, states that message body text leaves the
	// machine and how much. Disclosures print it prominently instead of the
	// "no message bodies" assurance. The state fields already carry the
	// body field, so the notice is not part of the fingerprint.
	BodyNotice string
}

// Validate checks the spec is complete enough to fingerprint.
func (s FeatureSpec) Validate() error {
	if !ValidFeatureName(s.Name) {
		return fmt.Errorf("jev feature name %q is invalid", s.Name)
	}
	if strings.TrimSpace(s.Title) == "" || strings.TrimSpace(s.Purpose) == "" {
		return fmt.Errorf("jev feature %s needs a title and purpose", s.Name)
	}
	if len(s.Questions) == 0 {
		return fmt.Errorf("jev feature %s has no questions", s.Name)
	}
	seen := make(map[string]struct{}, len(s.Questions))
	for _, question := range s.Questions {
		if question.ID == "" || question.Instructions == nil {
			return fmt.Errorf("jev feature %s has an incomplete question", s.Name)
		}
		if _, duplicate := seen[question.ID]; duplicate {
			return fmt.Errorf("jev feature %s repeats question %q", s.Name, question.ID)
		}
		seen[question.ID] = struct{}{}
	}
	if len(s.StateFields) == 0 {
		return fmt.Errorf("jev feature %s discloses no state fields", s.Name)
	}
	return nil
}

// Policy is the exact outbound policy a consent grants: the feature, its
// wording and fields, and where they go.
type Policy struct {
	Feature     string     `json:"feature"`
	Title       string     `json:"title"`
	Purpose     string     `json:"purpose"`
	Fingerprint string     `json:"fingerprint"`
	Endpoint    string     `json:"endpoint"`
	Model       string     `json:"model"`
	Questions   []Question `json:"questions"`
	StateFields []string   `json:"state_fields"`
	BodyNotice  string     `json:"body_notice,omitzero"`
}

type policyQuestion struct {
	ID           string       `json:"id"`
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions"`
	Criteria     any          `json:"criteria,omitzero"`
}

type policyDocument struct {
	Feature     string           `json:"feature"`
	Endpoint    string           `json:"endpoint"`
	Model       string           `json:"model"`
	Questions   []policyQuestion `json:"questions"`
	StateFields []string         `json:"state_fields"`
}

// Policy fingerprints the spec against a configuration's endpoint and model.
func (s FeatureSpec) Policy(cfg Config) (Policy, error) {
	if err := s.Validate(); err != nil {
		return Policy{}, err
	}
	if err := ValidateEndpoint(cfg.Endpoint); err != nil {
		return Policy{}, err
	}
	if cfg.Model == "" {
		return Policy{}, errors.New("jev model is required")
	}
	questions := make([]policyQuestion, len(s.Questions))
	for i, question := range s.Questions {
		questions[i] = policyQuestion(question)
	}
	fields := slices.Clone(s.StateFields)
	slices.Sort(fields)
	encoded, err := json.Marshal(policyDocument{
		Feature: s.Name, Endpoint: cfg.Endpoint, Model: cfg.Model,
		Questions: questions, StateFields: fields,
	}, json.Deterministic(true))
	if err != nil {
		return Policy{}, fmt.Errorf("encode jev policy: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return Policy{
		Feature: s.Name, Title: s.Title, Purpose: s.Purpose,
		Fingerprint: hex.EncodeToString(digest[:]),
		Endpoint:    cfg.Endpoint, Model: cfg.Model,
		Questions: slices.Clone(s.Questions), StateFields: fields, BodyNotice: s.BodyNotice,
	}, nil
}

// QuestionText renders a question's instructions for a disclosure. A string
// is shown as is; structured instructions are shown as compact JSON.
func QuestionText(instructions any) string {
	if text, ok := instructions.(string); ok {
		return text
	}
	encoded, err := json.Marshal(instructions, json.Deterministic(true))
	if err != nil {
		return "(unrenderable instructions)"
	}
	return string(encoded)
}
