package cleanupsuggest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
)

// Store is the archive authority a run needs. *store.Store implements it.
type Store interface {
	CleanupCandidatesContext(ctx context.Context, query store.CleanupCandidateQuery) ([]store.CleanupCandidate, error)
	SenderKindsContext(ctx context.Context, senderIDs []int64) (map[int64]string, error)
	CleanupEvidenceContext(ctx context.Context, messageID int64) (store.CleanupEvidence, error)
	WriteCleanupSuggestionsContext(ctx context.Context, suggestions []store.CleanupSuggestion) (int, error)
}

// Judge asks a subset of a feature's consented questions. *jev.Service
// implements it; every gate is rechecked on each call.
type Judge interface {
	JudgeQuestions(
		ctx context.Context, spec jev.FeatureSpec, automatic bool, state any, questionIDs []string, deadline time.Time,
	) (jev.Response, error)
}

// DefaultLimit is how many pool messages one run judges.
const DefaultLimit = 50

// pageSize is how many pool rows one store read returns.
const pageSize = 100

// maxScanFactor bounds how many pool rows a run reads per message it
// judges, so a pool full of link-less mail cannot turn one run into a scan
// of the whole archive.
const maxScanFactor = 20

// Options configure one run.
type Options struct {
	// Limit caps how many messages are judged; zero takes DefaultLimit.
	Limit int
	// Rejudge includes messages that already have a suggestion.
	Rejudge bool
	// Judge is nil when Jev is off; the run then only reports the pool.
	Judge Judge
	// TrustedAuthservIDs are the configured authserv-ids trusted for every
	// source, beyond Gmail's own for Gmail sources.
	TrustedAuthservIDs []string
	Logger             *slog.Logger
}

// Report summarizes a run. It never contains message content.
type Report struct {
	// Visited counts pool messages read.
	Visited int `json:"visited"`
	// PersonSenders counts pool messages left out because their sender is
	// classified as a person.
	PersonSenders int `json:"person_senders"`
	// NoLinks counts pool messages left out because they carry no links.
	NoLinks int `json:"no_links"`
	// Eligible counts messages ready to judge.
	Eligible int `json:"eligible"`
	Requests int `json:"requests"`
	Judged   int `json:"judged"`
	// Suspected counts judged messages at or above SuspectThreshold.
	Suspected int `json:"suspected"`
	// Keep counts judged messages at or above KeepThreshold.
	Keep int `json:"keep"`
	// Skipped is the safe-failure category that stopped Jev (for example
	// consent_required), empty when every batch was asked.
	Skipped string `json:"skipped,omitempty"`
}

type pending struct {
	messageID int64
	state     Message
}

// Run finds pool messages and, when a Judge is present, judges them and
// stores the suggestions. Any gate, budget, or provider failure stops Jev
// for the rest of the run; only a store failure fails the run. Nothing is
// staged or deleted.
func Run(ctx context.Context, st Store, options Options) (Report, error) {
	if options.Limit <= 0 {
		options.Limit = DefaultLimit
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	report := Report{}
	eligible, err := collect(ctx, st, options, &report)
	if err != nil {
		return report, err
	}
	report.Eligible = len(eligible)
	if options.Judge == nil {
		return report, nil
	}
	for start := 0; start < len(eligible); start += BatchSize {
		batch := eligible[start:min(start+BatchSize, len(eligible))]
		if err := judgeBatch(ctx, st, options, batch, &report); err != nil {
			if isStoreError(err) {
				return report, err
			}
			report.Skipped = jev.Skipped(err)
			options.Logger.Info("cleanup suggestions: jev skipped",
				"feature", jev.FeatureCleanupSuggestions, "category", report.Skipped)
			break
		}
	}
	return report, nil
}

// collect pages through the pool and builds the state of each message that
// is eligible: its sender is not classified as a person and it has links.
func collect(ctx context.Context, st Store, options Options, report *Report) ([]pending, error) {
	eligible := []pending{}
	maxScan := options.Limit * maxScanFactor
	beforeID := int64(0)
	for len(eligible) < options.Limit && report.Visited < maxScan {
		page, err := st.CleanupCandidatesContext(ctx, store.CleanupCandidateQuery{
			BeforeID: beforeID, Limit: pageSize, Rejudge: options.Rejudge,
		})
		if err != nil {
			return nil, fmt.Errorf("list cleanup pool: %w", err)
		}
		if len(page) == 0 {
			break
		}
		senders := make([]int64, 0, len(page))
		for _, candidate := range page {
			if candidate.SenderID > 0 {
				senders = append(senders, candidate.SenderID)
			}
		}
		kinds, err := st.SenderKindsContext(ctx, senders)
		if err != nil {
			return nil, fmt.Errorf("resolve cleanup sender kinds: %w", err)
		}
		for _, candidate := range page {
			beforeID = candidate.MessageID
			if len(eligible) == options.Limit || report.Visited == maxScan {
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			report.Visited++
			kind := kinds[candidate.SenderID]
			if kind == string(correspondentkind.Person) {
				report.PersonSenders++
				continue
			}
			evidence, err := st.CleanupEvidenceContext(ctx, candidate.MessageID)
			if err != nil {
				return nil, fmt.Errorf("gather cleanup evidence: %w", err)
			}
			state := MessageState(evidence, kind, TrustedAuthservIDs(evidence.SourceType, options.TrustedAuthservIDs))
			if len(state.LinkHosts) == 0 {
				report.NoLinks++
				continue
			}
			eligible = append(eligible, pending{messageID: candidate.MessageID, state: state})
		}
		if len(page) < pageSize {
			break
		}
	}
	return eligible, nil
}

// storeError marks a failure writing results, which fails the run.
type storeError struct{ err error }

func (e storeError) Error() string { return e.err.Error() }
func (e storeError) Unwrap() error { return e.err }

func isStoreError(err error) bool {
	var target storeError
	return errors.As(err, &target)
}

// judgeBatch asks about one batch and stores the suggestions. A batch the
// client rejects as too large is halved until it fits.
func judgeBatch(ctx context.Context, st Store, options Options, batch []pending, report *Report) error {
	state := State{Messages: make([]Message, len(batch))}
	ids := make([]string, 0, len(batch)*3)
	for i, item := range batch {
		state.Messages[i] = item.state
		ids = append(ids, ImpersonationID(i), PressureID(i), CategoryID(i))
	}
	response, err := options.Judge.JudgeQuestions(ctx, JevFeature(), false, state, ids, time.Time{})
	if errors.Is(err, jev.ErrRequestBounds) && len(batch) > 1 {
		half := len(batch) / 2
		if err := judgeBatch(ctx, st, options, batch[:half], report); err != nil {
			return err
		}
		return judgeBatch(ctx, st, options, batch[half:], report)
	}
	report.Requests++
	if err != nil {
		return err
	}
	suggestions := make([]store.CleanupSuggestion, 0, len(batch))
	for i, item := range batch {
		judgment, category, err := judgmentFor(response, i)
		if err != nil {
			return err
		}
		score, signals := Score(item.state, judgment)
		keep := KeepProbability(judgment)
		suggestions = append(suggestions, store.CleanupSuggestion{
			MessageID: item.messageID, Score: score, Impersonation: judgment.Impersonation,
			Pressure: judgment.Pressure, Category: category, CategoryProbabilities: judgment.Categories,
			KeepProbability: keep, Signals: signals, Model: response.Model,
		})
		if score >= SuspectThreshold {
			report.Suspected++
		}
		if keep >= KeepThreshold {
			report.Keep++
		}
	}
	if _, err := st.WriteCleanupSuggestionsContext(ctx, suggestions); err != nil {
		return storeError{fmt.Errorf("write cleanup suggestions: %w", err)}
	}
	report.Judged += len(suggestions)
	return nil
}

func judgmentFor(response jev.Response, i int) (Judgment, string, error) {
	impersonation, ok := response.Answers[ImpersonationID(i)]
	if !ok || impersonation.Type != jev.QuestionNoul {
		return Judgment{}, "", fmt.Errorf("%w: impersonation answer %d missing", jev.ErrInvalidResponse, i)
	}
	pressure, ok := response.Answers[PressureID(i)]
	if !ok || pressure.Type != jev.QuestionNoul {
		return Judgment{}, "", fmt.Errorf("%w: pressure answer %d missing", jev.ErrInvalidResponse, i)
	}
	category, ok := response.Answers[CategoryID(i)]
	if !ok || category.Type != jev.QuestionChoice {
		return Judgment{}, "", fmt.Errorf("%w: category answer %d missing", jev.ErrInvalidResponse, i)
	}
	if _, known := categoryCriteria[category.Choice]; !known {
		return Judgment{}, "", fmt.Errorf("%w: category answer %d is not an offered option", jev.ErrInvalidResponse, i)
	}
	probabilities := make(map[string]float64, len(categoryCriteria))
	for option := range categoryCriteria {
		probabilities[option] = category.Probabilities[option]
	}
	return Judgment{
		Impersonation: impersonation.Noul, Pressure: pressure.Noul, Categories: probabilities,
	}, category.Choice, nil
}
