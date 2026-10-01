package query

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSQLiteBuildFTSTerm asserts the SQLite dialect renders a
// dialect-neutral term slice into an FTS5 MATCH argument: each term is
// double-quote-wrapped with a trailing "*" for prefix matching, embedded
// double-quotes are doubled to neutralize FTS5 operator injection, and
// stray "*" inside a term is stripped. This exercises the escaping used
// by the query engine and hybrid search.
func TestSQLiteBuildFTSTerm(t *testing.T) {
	d := SQLiteQueryDialect{}

	cases := []struct {
		name    string
		terms   []string
		wantArg string
	}{
		{
			name:    "plain terms quote-wrapped and prefix-matched",
			terms:   []string{"security", "alert"},
			wantArg: `"security"* "alert"*`,
		},
		{
			name:    "embedded double-quote doubled",
			terms:   []string{`a"b`},
			wantArg: `"a""b"*`,
		},
		{
			name:    "stray star stripped",
			terms:   []string{"a*b"},
			wantArg: `"ab"*`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			expr, arg := d.BuildFTSTerm(tc.terms)
			assert.Equal("messages_fts MATCH ?", expr)
			assert.Equal(tc.wantArg, arg)
		})
	}
}

func TestBuildFTSBodyTermScopesExactBodyField(t *testing.T) {
	t.Run("SQLite body column", func(t *testing.T) {
		expr, arg := (SQLiteQueryDialect{}).BuildFTSBodyTerm([]string{"foo", "bar"})
		assert.Equal(t, "messages_fts MATCH ?", expr)
		assert.Equal(t, `body : ("foo"* "bar"*)`, arg)
	})
}

func TestBuildFTSAnyTermOrsTerms(t *testing.T) {
	t.Run("SQLite", func(t *testing.T) {
		expr, arg := (SQLiteQueryDialect{}).BuildFTSAnyTerm([]string{"budget", `q"3`})
		assert.Equal(t, "messages_fts MATCH ?", expr)
		assert.Equal(t, `"budget"* OR "q""3"*`, arg)
	})
}
