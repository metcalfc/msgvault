// Package attachmentstore adapts Kit's reusable packed-CAS reader to
// msgvault's established attachment-opening interfaces.
package attachmentstore

import (
	"context"
	"fmt"
	"io"
	"strings"

	"go.kenn.io/kit/packstore"

	"go.kenn.io/msgvault/internal/export"
)

// Store is the daemon-owned mixed loose and packed attachment reader.
type Store struct {
	store *packstore.Store
}

// Wrap adapts an existing physical store without changing its ownership.
// The caller remains responsible for closing the underlying store exactly once.
func Wrap(physical *packstore.Store) *Store {
	return &Store{store: physical}
}

// New constructs a store rooted at attachmentsDir using resolver for
// msgvault's attachment-membership and pack-location authority.
func New(resolver packstore.Resolver, attachmentsDir string) (*Store, error) {
	layout, err := packstore.NewLayout(attachmentsDir, packstore.LayoutOptions{
		Staging: packstore.StagingSameDirectory,
	})
	if err != nil {
		return nil, fmt.Errorf("create attachment pack layout: %w", err)
	}
	physical, err := packstore.NewStore(resolver, layout, packstore.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("create attachment pack store: %w", err)
	}
	return Wrap(physical), nil
}

// OpenStream returns a verified sequential reader without buffering the whole
// attachment. Callers must consume it through EOF (or use the Kit-specific
// verification methods) and must observe the Close error.
func (s *Store) OpenStream(ctx context.Context, hash string) (io.ReadCloser, int64, error) {
	parsed, err := parseHash(hash)
	if err != nil {
		return nil, 0, fmt.Errorf("parse attachment stream hash: %w", err)
	}
	reader, size, err := s.store.OpenStream(ctx, parsed)
	if err != nil {
		return nil, 0, fmt.Errorf("open attachment stream %s: %w", hash, err)
	}
	return reader, size, nil
}

func parseHash(hash string) (packstore.Hash, error) {
	if err := export.ValidateContentHash(hash); err != nil {
		return "", err
	}
	parsed, err := packstore.ParseHash(strings.ToLower(hash))
	if err != nil {
		return "", fmt.Errorf("parse canonical attachment hash: %w", err)
	}
	return parsed, nil
}

// Close releases cached pack descriptors.
func (s *Store) Close() error {
	if err := s.store.Close(); err != nil {
		return fmt.Errorf("close attachment store: %w", err)
	}
	return nil
}
