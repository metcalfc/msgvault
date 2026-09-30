package api

import (
	"container/list"
	"context"
	"sync"
	"time"

	"go.kenn.io/msgvault/internal/remoteimage"
)

// Opening a newsletter makes the reader request each of its remote images
// through the proxy. Every request must check the image against the
// message's references; without a cache each one reloaded and reparsed the
// body. The cache keeps each message's normalized reference set, keyed on
// the message ID and its row version (which the database bumps on any
// message or body change), for a short time.
const (
	remoteImageRefCacheEntries = 32
	remoteImageRefCacheTTL     = time.Minute
)

type remoteImageRefKey struct {
	messageID int64
	version   string
}

type remoteImageRefEntry struct {
	key     remoteImageRefKey
	refs    remoteimage.References
	expires time.Time
}

// remoteImageReferenceCache is a small LRU with a TTL. Folder state (spam,
// trash) is never cached: the proxy reads it on every request.
type remoteImageReferenceCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     func() time.Time
	order   *list.List
	entries map[remoteImageRefKey]*list.Element
}

func newRemoteImageReferenceCache(maxEntries int, ttl time.Duration, now func() time.Time) *remoteImageReferenceCache {
	if now == nil {
		now = time.Now
	}
	return &remoteImageReferenceCache{
		max: maxEntries, ttl: ttl, now: now, order: list.New(),
		entries: make(map[remoteImageRefKey]*list.Element),
	}
}

// get returns the message's reference set, loading and parsing its bodies
// only on a miss.
func (c *remoteImageReferenceCache) get(
	ctx context.Context, bodies RemoteImagePolicyStore, messageID int64, version string,
) (remoteimage.References, error) {
	key := remoteImageRefKey{messageID: messageID, version: version}
	if c != nil {
		if refs, ok := c.lookup(key); ok {
			return refs, nil
		}
	}
	text, html, err := bodies.RemoteImageBodiesContext(ctx, messageID)
	if err != nil {
		return nil, err
	}
	refs := remoteimage.ReferencesOf(html, text)
	if c != nil {
		c.store(key, refs)
	}
	return refs, nil
}

func (c *remoteImageReferenceCache) lookup(key remoteImageRefKey) (remoteimage.References, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	entry, _ := element.Value.(*remoteImageRefEntry)
	if entry == nil || !c.now().Before(entry.expires) {
		c.order.Remove(element)
		delete(c.entries, key)
		return nil, false
	}
	c.order.MoveToFront(element)
	return entry.refs, true
}

func (c *remoteImageReferenceCache) store(key remoteImageRefKey, refs remoteimage.References) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.order.Remove(element)
		delete(c.entries, key)
	}
	c.entries[key] = c.order.PushFront(&remoteImageRefEntry{key: key, refs: refs, expires: c.now().Add(c.ttl)})
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if entry, _ := oldest.Value.(*remoteImageRefEntry); entry != nil {
			delete(c.entries, entry.key)
		}
		c.order.Remove(oldest)
	}
}
