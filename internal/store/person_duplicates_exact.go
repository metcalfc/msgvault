package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"go.kenn.io/msgvault/internal/emailaddr"
	"go.kenn.io/msgvault/internal/textimport"
)

// Exact duplicate signals are decided in code: two clusters that share a
// mailbox, a phone number, or a provider account are one identity by
// construction, so they become review candidates without asking Jev.
const (
	// PersonDuplicateSameMailbox: both clusters have an address that delivers
	// to the same mailbox under the emailaddr rule (case, plus tags, Gmail
	// dots and googlemail.com). Relay and robot mailboxes are left out.
	PersonDuplicateSameMailbox PersonDuplicateSignal = "same_mailbox"
	// PersonDuplicateSamePhone: both clusters carry the same phone number
	// after E.164 normalization.
	PersonDuplicateSamePhone PersonDuplicateSignal = "same_phone"
	// PersonDuplicateSameProviderID: both clusters were observed with the
	// same provider user ID on the same service and scope.
	PersonDuplicateSameProviderID PersonDuplicateSignal = "same_provider_id"
)

// PersonDuplicateRuleModel is the model recorded for a pair decided in code.
const PersonDuplicateRuleModel = "rule"

// exactDuplicateSignals lists the exact signals in the order a proposal
// reports them; the first one present is the candidate's basis.
var exactDuplicateSignals = []PersonDuplicateSignal{
	PersonDuplicateSameMailbox, PersonDuplicateSamePhone, PersonDuplicateSameProviderID,
}

// proposalSignalOrder is the order a proposal lists its signals in.
var proposalSignalOrder = append(slices.Clone(exactDuplicateSignals),
	PersonDuplicateSameName, PersonDuplicateSameLocalPart)

// Exact reports whether the signal is an exact identifier match.
func (s PersonDuplicateSignal) Exact() bool {
	return slices.Contains(exactDuplicateSignals, s)
}

// ExactSignal returns the proposal's first exact signal, if it has one. A
// proposal with an exact signal is decided in code and never sent to Jev.
func (p PersonDuplicateProposal) ExactSignal() (PersonDuplicateSignal, bool) {
	for _, signal := range p.Signals {
		if signal.Exact() {
			return signal, true
		}
	}
	return "", false
}

// identityMatchBasisForExactSignal names the identity match basis of a
// candidate decided by an exact signal.
func identityMatchBasisForExactSignal(signal PersonDuplicateSignal) IdentityMatchBasis {
	switch signal {
	case PersonDuplicateSamePhone:
		return IdentityMatchPhone
	case PersonDuplicateSameProviderID:
		return IdentityMatchStableProviderID
	default:
		return IdentityMatchEmail
	}
}

// duplicateExactKeys are one cluster's exact identity keys, each set by
// signal.
type duplicateExactKeys map[PersonDuplicateSignal]map[string]struct{}

func (k duplicateExactKeys) add(signal PersonDuplicateSignal, value string) {
	if value == "" {
		return
	}
	if k[signal] == nil {
		k[signal] = map[string]struct{}{}
	}
	k[signal][value] = struct{}{}
}

func (k duplicateExactKeys) has(signal PersonDuplicateSignal, value string) bool {
	_, ok := k[signal][value]
	return ok
}

// duplicateMailboxKey returns the mailbox an address delivers to, or false
// for an invalid address or a relay or robot mailbox.
func duplicateMailboxKey(address string) (string, bool) {
	if emailaddr.IsAutomatedMailbox(address) {
		return "", false
	}
	return emailaddr.Mailbox(address)
}

// duplicatePhoneKey returns a phone number in E.164 form, or false when the
// value is not shaped like a phone number.
func duplicatePhoneKey(raw string) (string, bool) {
	if !phoneShaped(raw) {
		return "", false
	}
	normalized, err := textimport.NormalizePhone(raw)
	return normalized, err == nil
}

// loadDuplicateExactKeysTx fills each cluster's exact keys from all of its
// members: mailboxes from participant addresses and email identifiers,
// phone numbers from participant phones and phone-shaped identifiers, and
// provider user IDs from current contact observations.
func loadDuplicateExactKeysTx(
	ctx context.Context, tx *loggedTx, clusters map[int64]*duplicateCluster,
) error {
	rootOf := map[int64]int64{}
	members := []int64{}
	for root, cluster := range clusters {
		cluster.exact = duplicateExactKeys{}
		for _, member := range cluster.members {
			rootOf[member] = root
			members = append(members, member)
		}
	}
	slices.Sort(members)
	keys := func(id int64) duplicateExactKeys { return clusters[rootOf[id]].exact }

	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT id, COALESCE(email_address, ''), COALESCE(phone_number, '')
		FROM participants WHERE id IN (%s)`, func(rows *loggedRows) error {
		var id int64
		var email, phone string
		if err := rows.Scan(&id, &email, &phone); err != nil {
			return fmt.Errorf("scan duplicate-person contact: %w", err)
		}
		if mailbox, ok := duplicateMailboxKey(email); ok {
			keys(id).add(PersonDuplicateSameMailbox, mailbox)
		}
		if normalized, ok := duplicatePhoneKey(phone); ok {
			keys(id).add(PersonDuplicateSamePhone, normalized)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("load duplicate-person contacts: %w", err)
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT participant_id, identifier_type, identifier_value
		FROM participant_identifiers WHERE participant_id IN (%s)`, func(rows *loggedRows) error {
		var id int64
		var kind, value string
		if err := rows.Scan(&id, &kind, &value); err != nil {
			return fmt.Errorf("scan duplicate-person identifier: %w", err)
		}
		if kind == "email" {
			if mailbox, ok := duplicateMailboxKey(value); ok {
				keys(id).add(PersonDuplicateSameMailbox, mailbox)
			}
			return nil
		}
		if normalized, ok := duplicatePhoneKey(value); ok {
			keys(id).add(PersonDuplicateSamePhone, normalized)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("load duplicate-person identifiers: %w", err)
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT o.participant_id, COALESCE(cs.slug, ''), COALESCE(o.scope_kind, ''),
			COALESCE(o.scope_value, ''), o.provider_user_id
		FROM participant_contact_observations o
		LEFT JOIN communication_services cs ON cs.id = o.service_id
		WHERE o.participant_id IN (%s)
		  AND o.provider_user_id IS NOT NULL AND TRIM(o.provider_user_id) <> ''
		  AND o.active_until IS NULL AND o.superseded_at IS NULL`, func(rows *loggedRows) error {
		var id int64
		var service, scopeKind, scopeValue string
		var provider sql.NullString
		if err := rows.Scan(&id, &service, &scopeKind, &scopeValue, &provider); err != nil {
			return fmt.Errorf("scan duplicate-person provider identity: %w", err)
		}
		keys(id).add(PersonDuplicateSameProviderID,
			duplicateProviderKey(service, scopeKind, scopeValue, provider.String))
		return nil
	}); err != nil {
		return fmt.Errorf("load duplicate-person provider identities: %w", err)
	}
	return nil
}

// duplicateProviderKey scopes a provider user ID by its service and scope,
// since the same ID on two services or workspaces is not one account.
func duplicateProviderKey(service, scopeKind, scopeValue, providerUserID string) string {
	providerUserID = strings.TrimSpace(providerUserID)
	if providerUserID == "" {
		return ""
	}
	return service + "|" + scopeKind + "|" + scopeValue + "|" + providerUserID
}
