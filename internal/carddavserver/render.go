package carddavserver

import (
	"context"
	"fmt"
	"sync"

	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/vcard"
	"go.kenn.io/msgvault/internal/vcardmap"
)

// sourceRef marks envelopes rendered for the served book. It never reaches a
// device; it only labels the in-memory envelope.
const sourceRef = "carddav-served"

type renderedCard struct {
	uid  string
	body []byte
	// etag is always derived from the vCard 3.0 body, whichever version was
	// requested, so a listing and a later fetch agree on the same tag.
	etag string
}

type cacheKey struct {
	personID int64
	version  vcard.Version
}

type cacheEntry struct {
	fingerprint string
	card        renderedCard
}

// renderer turns a person into a served card and remembers the result until
// the person's projection fingerprint moves. The cache is bounded by the
// number of persons times the two supported versions.
type renderer struct {
	store *store.Store
	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
}

func newRenderer(st *store.Store) *renderer {
	return &renderer{store: st, cache: map[cacheKey]cacheEntry{}}
}

// render loads the snapshot, removes inferred facts, and renders the card at
// the requested version. The ETag comes from the 3.0 rendering.
func (r *renderer) render(ctx context.Context, person store.Person, version vcard.Version) (renderedCard, error) {
	if version != vcard.Version30 && version != vcard.Version40 {
		return renderedCard{}, fmt.Errorf("unsupported vCard version %q", version)
	}
	snapshot, err := r.store.LoadPersonVCardSnapshotContext(ctx, person.ID)
	if err != nil {
		return renderedCard{}, err
	}
	if cached, ok := r.lookup(person.ID, version, snapshot.Fingerprint); ok {
		return cached, nil
	}
	curated := curatedOnly(*snapshot)
	body30, err := renderBody(curated, person, vcard.Version30)
	if err != nil {
		return renderedCard{}, err
	}
	card := renderedCard{uid: person.VCardUID, body: body30, etag: vcard.ETagForBody(body30)}
	if version == vcard.Version40 {
		body40, err := renderBody(curated, person, vcard.Version40)
		if err != nil {
			return renderedCard{}, err
		}
		card.body = body40
	}
	r.remember(person.ID, version, snapshot.Fingerprint, card)
	return card, nil
}

// renderBody renders one version, dropping media when the card would exceed
// the advertised maximum resource size.
func renderBody(snapshot store.PersonVCardSnapshot, person store.Person, version vcard.Version) ([]byte, error) {
	body, err := renderOnce(snapshot, person, version)
	if err != nil {
		return nil, err
	}
	if len(body) <= maxResourceSize {
		return body, nil
	}
	withoutMedia := snapshot
	withoutMedia.MediaData = nil
	withoutMedia.Profile.Media = nil
	body, err = renderOnce(withoutMedia, person, version)
	if err != nil {
		return nil, err
	}
	if len(body) > maxResourceSize {
		return nil, fmt.Errorf("person %d renders to %d bytes, above the %d byte limit", person.ID, len(body), maxResourceSize)
	}
	return body, nil
}

func renderOnce(snapshot store.PersonVCardSnapshot, person store.Person, version vcard.Version) ([]byte, error) {
	displayName := ""
	if person.DisplayName != nil {
		displayName = *person.DisplayName
	}
	seed, err := vcardmap.SeedEnvelope(person.VCardUID, displayName, sourceRef, person.VCardUID+".vcf")
	if err != nil {
		return nil, err
	}
	envelope, err := vcardmap.RenderPersonCard(snapshot, seed, version)
	if err != nil {
		return nil, err
	}
	return envelope.StoredBody, nil
}

// curatedOnly removes facts msgvault inferred from messages or enrichment.
// Declared, imported, and observed facts stay. The publication flow requires
// an explicit review before exporting inferred facts; the served book simply
// leaves them out.
func curatedOnly(snapshot store.PersonVCardSnapshot) store.PersonVCardSnapshot {
	employments := make([]store.PersonVCardEmployment, 0, len(snapshot.Employments))
	for _, item := range snapshot.Employments {
		if !inferred(item.Employment.Source) {
			employments = append(employments, item)
		}
	}
	snapshot.Employments = employments

	attributes := make([]store.PersonVCardAttribute, 0, len(snapshot.Attributes))
	for _, attribute := range snapshot.Attributes {
		values := make([]store.PersonAttributeValue, 0, len(attribute.Values))
		for _, value := range attribute.Values {
			if !inferred(value.Source) {
				values = append(values, value)
			}
		}
		attribute.Values = values
		attributes = append(attributes, attribute)
	}
	snapshot.Attributes = attributes
	return snapshot
}

func inferred(source store.Provenance) bool {
	return source == store.ProvenanceExtraction || source == store.ProvenanceEnrichment
}

func (r *renderer) lookup(personID int64, version vcard.Version, fingerprint string) (renderedCard, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[cacheKey{personID: personID, version: version}]
	if !ok || entry.fingerprint != fingerprint {
		return renderedCard{}, false
	}
	return entry.card, true
}

func (r *renderer) remember(personID int64, version vcard.Version, fingerprint string, card renderedCard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[cacheKey{personID: personID, version: version}] = cacheEntry{fingerprint: fingerprint, card: card}
}
