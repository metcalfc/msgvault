package vcardmap

import (
	"errors"
	"fmt"
	"strings"

	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/vcard"
)

// ServerOwnedProperties are the vCard properties a CardDAV server
// conventionally rewrites. They are stripped before a card leaves msgvault so
// their churn cannot masquerade as a user edit, and they are ignored when two
// cards are compared semantically.
var ServerOwnedProperties = map[string]bool{
	"PRODID": true, "REV": true, "SOURCE": true,
	"CREATED": true, "LAST-MODIFIED": true,
}

// SeedEnvelope builds the minimal card a projection starts from when no
// existing resource is being updated: a VERSION, the person's UID, and an FN.
// sourceRef and href identify where the rendered resource will live.
func SeedEnvelope(uid, displayName, sourceRef, href string) (vcard.ResourceEnvelope, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return vcard.ResourceEnvelope{}, errors.New("seed vCard envelope: person UID is empty")
	}
	fullName := strings.TrimSpace(displayName)
	if fullName == "" {
		fullName = uid
	}
	raw := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:" + vcard.EscapeText(uid) +
		"\r\nFN:" + vcard.EscapeText(fullName) + "\r\nEND:VCARD\r\n")
	envelope, err := vcard.ParseResourceEnvelope(raw)
	if err != nil {
		return vcard.ResourceEnvelope{}, err
	}
	envelope.SourceRef = sourceRef
	envelope.SourceResourceUID = href
	envelope.Href = href
	envelope.CanonicalPersonUID = uid
	return envelope, nil
}

// RenderPersonCard projects a person snapshot onto an envelope, removes the
// server-owned properties, and renders the wire body at the requested version.
// The CardDAV client uses it to publish a person to a remote book and the
// served address book uses it to answer a device; both produce the same card
// for the same snapshot.
func RenderPersonCard(
	snapshot store.PersonVCardSnapshot, envelope vcard.ResourceEnvelope, version vcard.Version,
) (vcard.ResourceEnvelope, error) {
	prepared, err := ProjectPersonEnvelope(snapshot, envelope)
	if err != nil {
		return vcard.ResourceEnvelope{}, fmt.Errorf("project person for CardDAV: %w", err)
	}
	prepared, err = StripServerOwnedProperties(prepared)
	if err != nil {
		return vcard.ResourceEnvelope{}, err
	}
	return prepared.PrepareWireRender(version)
}

// StripServerOwnedProperties deletes every ServerOwnedProperties occurrence
// from the envelope's property tree.
func StripServerOwnedProperties(envelope vcard.ResourceEnvelope) (vcard.ResourceEnvelope, error) {
	edits := make([]vcard.PropertyEdit, 0)
	for _, occurrence := range envelope.PropertyTree {
		if ServerOwnedProperties[strings.ToUpper(occurrence.Property.Name)] {
			edits = append(edits, vcard.PropertyEdit{Identity: occurrence.Identity, Delete: true})
		}
	}
	if len(edits) == 0 {
		return envelope, nil
	}
	return envelope.MergeProperties(edits)
}
