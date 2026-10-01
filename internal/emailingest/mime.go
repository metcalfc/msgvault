// Package emailingest prepares shared MIME data for local imports and live sync.
package emailingest

import (
	"strings"

	"go.kenn.io/msgvault/internal/export"
	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/textutil"
)

// Recipients retains one envelope snapshot per participant and normalized address.
func Recipients(recipientType string, addresses []mime.Address, participantMap map[string]int64) store.RecipientSet {
	rs := store.RecipientSet{Type: recipientType}
	if len(addresses) == 0 {
		return rs
	}

	// One row per (participant, normalized envelope address): two aliases
	// that resolve to the same already-merged participant each keep their
	// own envelope snapshot instead of collapsing onto the first-seen
	// address, so identity discovery still observes both. Display names
	// stay per participant, preferring non-empty names — a duplicate whose
	// first occurrence has an empty name picks up a later, better one, and
	// every row of that participant carries the same name.
	type rowKey struct {
		participantID int64
		email         string
	}
	idToName := make(map[int64]string)
	seen := make(map[rowKey]struct{})
	var orderedIDs []int64
	var orderedEmails []string

	for _, addr := range addresses {
		id, ok := participantMap[addr.Email]
		if !ok {
			continue
		}
		name := textutil.EnsureUTF8(addr.Name)
		if existing, tracked := idToName[id]; !tracked || (existing == "" && name != "") {
			idToName[id] = name
		}
		key := rowKey{participantID: id, email: strings.ToLower(addr.Email)}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		orderedIDs = append(orderedIDs, id)
		orderedEmails = append(orderedEmails, addr.Email)
	}

	rs.ParticipantIDs = orderedIDs
	rs.DisplayNames = make([]string, len(orderedIDs))
	rs.EmailAddresses = orderedEmails
	for i, id := range orderedIDs {
		rs.DisplayNames[i] = idToName[id]
	}
	return rs
}

// PrepareAttachment publishes MIME content and returns its archive metadata.
func PrepareAttachment(attachmentsDir string, att *mime.Attachment) (store.AttachmentWrite, error) {
	storagePath, err := export.StoreAttachmentFile(attachmentsDir, att)
	if err != nil || storagePath == "" {
		return store.AttachmentWrite{}, err
	}

	role, roleSource := store.AttachmentRoleFromMIME(
		att.Disposition, att.IsInline, att.ContentID)
	return store.AttachmentWrite{
		Filename:      att.Filename,
		MIMEType:      att.ContentType,
		StoragePath:   storagePath,
		ContentHash:   att.ContentHash,
		Size:          int64(len(att.Content)),
		Role:          role,
		RoleSource:    roleSource,
		SourcePartKey: att.PartKey,
		ContentID:     att.ContentID,
	}, nil
}
