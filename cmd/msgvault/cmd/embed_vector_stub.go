//go:build !sqlite_vec

package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

// runEmbed is a stub for builds without sqlite_vec. Embedding generation
// requires the SQLite vectors.db backend. Binaries produced by `make build`
// (which sets `-tags "fts5 sqlite_vec"`) use embed_vector.go instead.
func runEmbed(_ *cobra.Command) error {
	return errors.New("msgvault embeddings build requires a vector backend; rebuild with `go build -tags sqlite_vec`")
}
