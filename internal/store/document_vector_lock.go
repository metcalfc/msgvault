package store

import (
	"context"
	"errors"
)

// WithDocumentVectorOperationLock serializes document-vector writers within a
// Store. The daemon operation gate separately serializes routed CLI children.
func (s *Store) WithDocumentVectorOperationLock(ctx context.Context, operation func() error) (retErr error) {
	if operation == nil {
		return errors.New("document vector operation is required")
	}
	{
		s.documentVectorOperationMu.Lock()
		defer s.documentVectorOperationMu.Unlock()
		return operation()
	}
}
