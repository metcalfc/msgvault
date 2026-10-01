package cmd

import (
	"context"
	"encoding/csv"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/query"
)

type queryFlags struct {
	format        string
	fresh, stream bool
}

func newQueryCommand() *cobra.Command {
	var options queryFlags
	command := &cobra.Command{
		Use:   "query [sql]",
		Short: "Run a SQL query against the analytics cache",
		Long: `Run arbitrary SQL against the Parquet analytics cache.

The following views are available:
  messages, participants, message_recipients, labels,
  message_labels, attachments, conversations, sources

Convenience views:
  v_messages   - messages with resolved sender and labels
  v_senders    - per-sender aggregates
  v_domains    - per-domain aggregates
  v_labels     - label name with message count and size
  v_threads    - per-conversation aggregates

Output formats:
  json   - JSON object with columns, rows, row_count (default)
  csv    - CSV with header row
  table  - Aligned text table

Interactive results are limited to 10,000 rows and 16 MiB of encoded data.
Use --stream for larger JSON or CSV exports; discard partial output on any error.

Examples:
  msgvault query "SELECT from_email, COUNT(*) AS n FROM v_messages GROUP BY 1 ORDER BY 2 DESC LIMIT 10"
	msgvault query --stream --format csv "SELECT * FROM v_senders ORDER BY message_count DESC"
	msgvault query --format table "SELECT name, message_count FROM v_labels"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHTTPQuery(cmd, args[0], options)
		},
	}
	command.Flags().BoolVar(&options.stream, "stream", false, "Stream a complete JSON or CSV export without interactive result limits; discard partial output on errors")
	command.Flags().BoolVar(&options.fresh, "fresh", false, "Wait for analytics to include writes committed before this request, then return rows")
	command.Flags().StringVar(&options.format, "format", outputFormatJSON, "Output format: json, csv, or table")
	return command
}

func runHTTPQuery(cmd *cobra.Command, sqlStr string, options queryFlags) error {
	format := strings.ToLower(strings.TrimSpace(options.format))
	if options.stream && format != outputFormatJSON && format != "csv" {
		return errors.New("--stream requires --format json or csv")
	}
	st, _, err := OpenHTTPStore(cmd.Context())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if options.stream {
		fresh := options.fresh
		for {
			accepted, err := streamSQLQueryAs(cmd.Context(), st, sqlStr, fresh, format, cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("query: %w", err)
			}
			if accepted == nil {
				return nil
			}
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Analytics cache build %s: %s; waiting for completion\n", accepted.Status, accepted.JobID); err != nil {
				return fmt.Errorf("write cache build status: %w", err)
			}
			if err := st.WaitForCacheBuild(cmd.Context(), accepted.JobID); err != nil {
				return fmt.Errorf("query: %w", err)
			}
			fresh = false
		}
	}
	result, accepted, err := st.RunSQLQueryWithFresh(cmd.Context(), sqlStr, options.fresh)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	for accepted != nil {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Analytics cache build %s: %s; waiting for completion\n",
			accepted.Status, accepted.JobID)
		if err != nil {
			return fmt.Errorf("write cache build status: %w", err)
		}
		if err := st.WaitForCacheBuild(cmd.Context(), accepted.JobID); err != nil {
			return fmt.Errorf("query: %w", err)
		}
		result, accepted, err = st.RunSQLQueryWithFresh(cmd.Context(), sqlStr, false)
		if err != nil {
			return fmt.Errorf("query: %w", err)
		}
	}
	if strings.ToLower(strings.TrimSpace(options.format)) != outputFormatJSON && result.Cache != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Analytics cache published %s; generation %s",
			result.Cache.PublishedAt.Format(time.RFC3339), result.Cache.Generation)
		if result.Cache.StaleReason != "" {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "; stale: %s", result.Cache.StaleReason)
		}
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
	}
	return writeQueryResult(cmd.OutOrStdout(), result, options.format)
}

// streamSQLQueryAs writes a streamed export in the requested format. CSV is
// converted row by row from the validated JSON stream, so it is as unbounded
// as the JSON export and carries the same discard-on-error contract.
func streamSQLQueryAs(
	ctx context.Context, st *daemonclient.Client, sqlStr string, fresh bool, format string, out io.Writer,
) (*daemonclient.CacheBuildAccepted, error) {
	if format != "csv" {
		return st.StreamSQLQuery(ctx, sqlStr, fresh, out)
	}
	reader, writer := io.Pipe()
	converted := make(chan error, 1)
	go func() {
		err := writeCSVFromSQLStream(out, reader)
		_ = reader.CloseWithError(err)
		converted <- err
	}()
	accepted, err := st.StreamSQLQuery(ctx, sqlStr, fresh, writer)
	_ = writer.CloseWithError(err)
	convertErr := <-converted
	if err != nil {
		return nil, err
	}
	if accepted != nil {
		return accepted, nil
	}
	if convertErr != nil {
		return nil, fmt.Errorf("SQL export incomplete; discard partial output: %w", convertErr)
	}
	return nil, nil //nolint:nilnil // No accepted job means the export completed successfully.
}

// writeCSVFromSQLStream reads one SQL result object and writes its columns and
// rows as CSV without retaining the row array. An empty stream (a cache build
// was accepted instead) writes nothing.
func writeCSVFromSQLStream(w io.Writer, r io.Reader) error {
	dec := jsontext.NewDecoder(r)
	if _, err := dec.ReadToken(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	cw := csv.NewWriter(w)
	haveColumns := false
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return err
		}
		switch name.String() {
		case "columns":
			var columns []string
			if err := json.UnmarshalDecode(dec, &columns); err != nil {
				return fmt.Errorf("decode columns: %w", err)
			}
			if err := cw.Write(columns); err != nil {
				return fmt.Errorf("write csv header: %w", err)
			}
			haveColumns = true
		case "rows":
			if !haveColumns {
				return errors.New("SQL result rows arrived before columns")
			}
			if dec.PeekKind() == 'n' {
				if _, err := dec.ReadToken(); err != nil {
					return err
				}
				continue
			}
			if _, err := dec.ReadToken(); err != nil {
				return err
			}
			for dec.PeekKind() != ']' {
				var row []any
				if err := json.UnmarshalDecode(dec, &row); err != nil {
					return fmt.Errorf("decode row: %w", err)
				}
				record := make([]string, len(row))
				for i, v := range row {
					record[i] = displayVal(v)
				}
				if err := cw.Write(record); err != nil {
					return fmt.Errorf("write csv row: %w", err)
				}
			}
			if _, err := dec.ReadToken(); err != nil {
				return err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return err
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

func writeQueryResult(w io.Writer, result *query.QueryResult, format string) error {
	if result == nil {
		return errors.New("nil query result")
	}
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case outputFormatJSON:
		return writeJSON(w, result)
	case "csv":
		return writeCSV(w, result.Columns, result.Rows)
	case "table":
		return writeTable(w, result.Columns, result.Rows)
	default:
		return fmt.Errorf("unknown format %q (use json, csv, or table)", format)
	}
}

func writeJSON(w io.Writer, result *query.QueryResult) error {
	enc := jsontext.NewEncoder(w, jsontext.WithIndentPrefix(""), jsontext.WithIndent("  "))

	return json.MarshalEncode(enc, result, json.Deterministic(true))
}

// displayVal formats a value for CSV/table output. SQL NULLs become
// empty strings; floats use plain decimal notation (fmt's %v switches
// to scientific notation at ~1e6); other values use fmt.Sprintf.
func displayVal(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func writeCSV(
	w io.Writer, cols []string, rows [][]any,
) error {
	cw := csv.NewWriter(w)

	if err := cw.Write(cols); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}

	for _, row := range rows {
		record := make([]string, len(row))
		for i, v := range row {
			record[i] = displayVal(v)
		}
		if err := cw.Write(record); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}

	cw.Flush()
	return cw.Error()
}

// nil error return mirrors writeJSON/writeCSV so the format switch can
// `return writeTable(...)` uniformly; text printing never fails.
func writeTable(
	w io.Writer, cols []string, rows [][]any,
) error {
	// Convert all values to strings for width calculation
	strRows := make([][]string, len(rows))
	for i, row := range rows {
		strRows[i] = make([]string, len(row))
		for j, v := range row {
			strRows[i][j] = displayVal(v)
		}
	}

	// Calculate column widths (min = header length)
	widths := make([]int, len(cols))
	for i, col := range cols {
		widths[i] = len(col)
	}
	for _, row := range strRows {
		for i, val := range row {
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	// Print header
	for i, col := range cols {
		if i > 0 {
			_, _ = fmt.Fprint(w, "  ")
		}
		_, _ = fmt.Fprintf(w, "%-*s", widths[i], col)
	}
	_, _ = fmt.Fprintln(w)

	// Print separator
	for i, width := range widths {
		if i > 0 {
			_, _ = fmt.Fprint(w, "  ")
		}
		_, _ = fmt.Fprint(w, strings.Repeat("-", width))
	}
	_, _ = fmt.Fprintln(w)

	// Print rows
	for _, row := range strRows {
		for i, val := range row {
			if i > 0 {
				_, _ = fmt.Fprint(w, "  ")
			}
			_, _ = fmt.Fprintf(w, "%-*s", widths[i], val)
		}
		_, _ = fmt.Fprintln(w)
	}

	// Print row count
	_, _ = fmt.Fprintf(w, "(%d rows)\n", len(rows))
	return nil
}

func init() { registerCommandFactory(newQueryCommand) }
