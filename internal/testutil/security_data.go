package testutil

import (
	"path/filepath"
)

// PathTraversalCase describes a single path traversal test vector.
type PathTraversalCase struct{ Name, Path string }

// PathTraversalCases returns a fresh slice of path traversal attack vectors for
// testing path sanitization logic on supported hosts.
func PathTraversalCases() []PathTraversalCase {
	return []PathTraversalCase{
		{"rooted path", string(filepath.Separator) + "rooted" + string(filepath.Separator) + "path.txt"},
		{"escape dot dot", "../escape.txt"},
		{"escape dot dot nested", "subdir/../../escape.txt"},
		{"escape just dot dot", ".."},
		{"absolute path", "/abs/path"},
	}
}
