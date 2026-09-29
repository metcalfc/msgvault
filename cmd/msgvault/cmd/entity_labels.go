package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/textutil"
)

// cliEntityLabeler resolves entity labels; *daemonclient.Client and a direct
// store (through contextEntityLabeler) both satisfy it.
type cliEntityLabeler interface {
	EntityLabels(ctx context.Context, request store.EntityLabelRequest) (store.EntityLabels, error)
}

// entityLabelsContextStore is the store-side lookup that commands running
// inside the daemon subprocess call directly.
type entityLabelsContextStore interface {
	EntityLabelsContext(ctx context.Context, request store.EntityLabelRequest) (store.EntityLabels, error)
}

// contextEntityLabeler adapts a direct store handle to cliEntityLabeler.
type contextEntityLabeler struct{ st entityLabelsContextStore }

func (s contextEntityLabeler) EntityLabels(
	ctx context.Context, request store.EntityLabelRequest,
) (store.EntityLabels, error) {
	return s.st.EntityLabelsContext(ctx, request)
}

// cliEntityLabels holds terminal-safe labels for the entities one command
// prints. The zero value names nothing, so every accessor falls back to an
// "Unknown ..." label that still carries the ID for follow-up commands.
type cliEntityLabels struct {
	people        map[int64]string
	participants  map[int64]string
	organizations map[int64]string
}

// resolveCLIEntityLabels names the requested entities in one lookup. Human
// output must never fail over a label: an older daemon without the endpoint,
// a transport error, or a nil labeler all yield whatever resolved (possibly
// nothing), and the command prints unnamed fallbacks.
func resolveCLIEntityLabels(
	ctx context.Context, labeler cliEntityLabeler, request store.EntityLabelRequest,
) cliEntityLabels {
	if labeler == nil ||
		len(request.PersonIDs)+len(request.ParticipantIDs)+len(request.OrganizationIDs) == 0 {
		return cliEntityLabels{}
	}
	labels, err := labeler.EntityLabels(ctx, request)
	if err != nil {
		slog.Debug("entity labels unavailable; printing IDs without names", "error", err)
	}
	return cliEntityLabels{
		people:        sanitizedCLILabels(labels.People),
		participants:  sanitizedCLILabels(labels.Participants),
		organizations: sanitizedCLILabels(labels.Organizations),
	}
}

func sanitizedCLILabels(labels map[int64]string) map[int64]string {
	out := make(map[int64]string, len(labels))
	for id, label := range labels {
		if safe := strings.TrimSpace(textutil.SanitizeTerminal(label)); safe != "" {
			out[id] = safe
		}
	}
	return out
}

// person renders "Name (ID)", or "Unknown person (ID)" when nothing names it.
func (l cliEntityLabels) person(id int64) string {
	return cliNamedID(l.people[id], "Unknown person", id)
}

// participant renders "Name (ID)", or "Unknown participant (ID)".
func (l cliEntityLabels) participant(id int64) string {
	return cliNamedID(l.participants[id], "Unknown participant", id)
}

// organization renders "Name (ID)", or "Unknown organization (ID)".
func (l cliEntityLabels) organization(id int64) string {
	return cliNamedID(l.organizations[id], "Unknown organization", id)
}

// participantList renders participant IDs as a comma-separated label list,
// or "-" when there are none.
func (l cliEntityLabels) participantList(ids []int64) string {
	if len(ids) == 0 {
		return "-"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = l.participant(id)
	}
	return strings.Join(parts, ", ")
}

// cliNamedID formats a label with its ID, matching the "Name (123)" style of
// the CLI's tables, so the ID stays available for follow-up commands.
func cliNamedID(label, unknown string, id int64) string {
	if label == "" {
		label = unknown
	}
	return fmt.Sprintf("%s (%d)", label, id)
}

// cliOptionalName returns a terminal-safe, trimmed name, or "" when blank.
func cliOptionalName(name *string) string {
	if name == nil {
		return ""
	}
	return strings.TrimSpace(textutil.SanitizeTerminal(*name))
}
