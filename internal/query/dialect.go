// Package query keeps query SQL and FTS behavior behind a small SQLite contract.
// The store package owns a separate contract for archive writes and migrations.

package query

import (
	"fmt"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/sqliteutil"
)

type messageBodyContextBackend uint8

const queryTimestampLayout = "2006-01-02 15:04:05.999999999"

func queryTimeUTC(value time.Time) time.Time {
	return value.UTC()
}

const (
	messageBodyContextSQLite messageBodyContextBackend = iota
)

// Dialect groups SQL generation and search behavior for the query engine.
type Dialect interface {
	// Rebind converts ? placeholders to the driver's native form.
	// SQLite keeps the placeholders unchanged.
	Rebind(query string) string

	// TimeTruncExpression returns SQL to truncate a timestamp column to a
	// given granularity ("year", "month", "day"). Used in GROUP BY for
	// the Time aggregate view.
	TimeTruncExpression(column string, granularity string) string

	// FTSSearchExpression returns the SQL boolean expression (with a ?
	// placeholder for the search term) to use in a WHERE clause for
	// full-text search through messages_fts MATCH.
	FTSSearchExpression() string

	// HasFTSTableSQL returns SQL to probe whether the FTS index exists.
	// Returns a single-row, single-column integer: 1 if present, 0 if absent.
	HasFTSTableSQL() string

	// FTSLivenessSQL returns a runtime liveness probe to run AFTER
	// HasFTSTableSQL confirms the FTS relation exists, or "" when the
	// existence probe is already authoritative.
	//
	// SQLite needs this: HasFTSTableSQL only checks sqlite_master for the
	// messages_fts virtual table, which does NOT prove the fts5 module is
	// loadable. A DB created by an fts5-enabled binary but opened by a
	// binary built without fts5 still has the row in sqlite_master, yet any
	// query against it fails with "no such module: fts5". The liveness probe
	// (`SELECT 1 FROM messages_fts LIMIT 1`) surfaces that so search falls
	// back to LIKE instead of erroring. This mirrors the store dialect's
	// FTSAvailable contract (internal/store/dialect_sqlite.go).

	FTSLivenessSQL() string

	// FTSJoin returns a JOIN clause that must be added to the FROM clause
	// when using FTSSearchExpression.
	FTSJoin() string

	// BuildFTSTerm converts a slice of user-supplied search terms into a SQL
	// expression and a single FTS5 argument string with prefix matching.
	BuildFTSTerm(terms []string) (expr string, arg string)

	// BuildFTSBodyTerm is the exact-body counterpart to BuildFTSTerm.
	// SQLite scopes the FTS5 query to its body column.
	BuildFTSBodyTerm(terms []string) (expr string, arg string)

	// FTSBodySearchReadinessSQL returns a query that yields true when the
	// backend's body-search layout is ready, or "" when no version probe is
	// needed. SQLite's FTS5 table has a durable body column and needs no
	// separate version watermark.
	FTSBodySearchReadinessSQL() string

	// SanitizeFTSQuery converts a raw user search string to a form safe to
	// pass to FTSSearchExpression. Returns "" if the result is empty after
	// sanitization (caller should treat as no-match).
	SanitizeFTSQuery(query string) string

	// BoolTrueExpr returns a SQL boolean expression that is true when col
	// holds a "true" value. SQLite stores booleans as 0/1 INTEGER so we
	// must emit "col = 1".
	BoolTrueExpr(col string) string

	// UnicodeLowerExpression returns a Unicode-aware lowercasing SQL expression.
	// SQLite delegates to a deterministic Go scalar registered on every
	// connection.
	UnicodeLowerExpression(expr string) string

	// DateParam normalizes an instant for the backend timestamp representation.
	DateParam(value time.Time) any

	// DateComparison returns a backend-native comparison against one bound
	// placeholder. SQLite parses both operands as instants because archives can
	// contain mixed textual offsets.
	DateComparison(column, operator string) string

	// messageBodyContextBackend selects the backend-native highlighter used to
	// extract exact context for body-index hits.
	messageBodyContextBackend() messageBodyContextBackend
}

// SQLiteQueryDialect implements Dialect for SQLite.
type SQLiteQueryDialect struct{}

func (SQLiteQueryDialect) Rebind(query string) string { return query }

func (SQLiteQueryDialect) BoolTrueExpr(col string) string { return col + " = 1" }

func (SQLiteQueryDialect) UnicodeLowerExpression(expr string) string {
	return sqliteutil.UnicodeLowerFunction + "(" + expr + ")"
}

func (SQLiteQueryDialect) DateParam(value time.Time) any {
	return queryTimeUTC(value).Format(queryTimestampLayout)
}

func (SQLiteQueryDialect) DateComparison(column, operator string) string {
	return fmt.Sprintf("julianday(%s) %s julianday(?)", column, operator)
}

func (SQLiteQueryDialect) messageBodyContextBackend() messageBodyContextBackend {
	return messageBodyContextSQLite
}

func (SQLiteQueryDialect) TimeTruncExpression(column string, granularity string) string {
	switch granularity {
	case "year":
		return fmt.Sprintf("strftime('%%Y', %s)", column)
	case timeGranularityMonth:
		return fmt.Sprintf("strftime('%%Y-%%m', %s)", column)
	case "day":
		return fmt.Sprintf("strftime('%%Y-%%m-%%d', %s)", column)
	default:
		return fmt.Sprintf("strftime('%%Y-%%m', %s)", column)
	}
}

func (SQLiteQueryDialect) FTSSearchExpression() string {
	return "messages_fts MATCH ?"
}

func (SQLiteQueryDialect) HasFTSTableSQL() string {
	return `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='messages_fts'`
}

// FTSLivenessSQL probes that the fts5 module is actually loadable, not just
// that the messages_fts row exists in sqlite_master. See the interface doc.
func (SQLiteQueryDialect) FTSLivenessSQL() string {
	return `SELECT 1 FROM messages_fts LIMIT 1`
}

func (SQLiteQueryDialect) FTSJoin() string {
	return "JOIN messages_fts fts ON fts.rowid = m.id"
}

// BuildFTSTerm for SQLite FTS5: quote each term and add "*" for prefix match,
// AND them together. Escaping double-quotes prevents injection of FTS5 operators.
func (SQLiteQueryDialect) BuildFTSTerm(terms []string) (expr string, arg string) {
	ftsTerms := make([]string, len(terms))
	for i, term := range terms {
		term = strings.ReplaceAll(term, "\"", "\"\"")
		term = strings.ReplaceAll(term, "*", "")
		ftsTerms[i] = fmt.Sprintf("\"%s\"*", term)
	}
	return "messages_fts MATCH ?", strings.Join(ftsTerms, " ")
}

// BuildFTSAnyTerm is BuildFTSTerm with the terms OR-ed: a row matches
// when any one term prefix-matches.
func (SQLiteQueryDialect) BuildFTSAnyTerm(terms []string) (expr string, arg string) {
	ftsTerms := make([]string, len(terms))
	for i, term := range terms {
		term = strings.ReplaceAll(term, "\"", "\"\"")
		term = strings.ReplaceAll(term, "*", "")
		ftsTerms[i] = fmt.Sprintf("\"%s\"*", term)
	}
	return "messages_fts MATCH ?", strings.Join(ftsTerms, " OR ")
}

func (d SQLiteQueryDialect) BuildFTSBodyTerm(terms []string) (expr string, arg string) {
	expr, arg = d.BuildFTSTerm(terms)
	if arg == "" {
		return expr, arg
	}
	return expr, "body : (" + arg + ")"
}

func (SQLiteQueryDialect) FTSBodySearchReadinessSQL() string { return "" }

// SanitizeFTSQuery strips FTS5 metacharacters from a single query string
// and wraps it in quotes for literal phrase interpretation with prefix match.
func (SQLiteQueryDialect) SanitizeFTSQuery(query string) string {
	var b strings.Builder
	for _, r := range query {
		switch r {
		case '"', '*', ':', '-', '(', ')', '.':
			continue
		default:
			b.WriteRune(r)
		}
	}
	clean := strings.TrimSpace(b.String())
	if clean == "" {
		return ""
	}
	return `"` + clean + `"*`
}
