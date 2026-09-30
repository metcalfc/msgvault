package daemonclient

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"

	"go.kenn.io/msgvault/pkg/client/generated"
)

// StreamSQLQuery exports JSON without buffering all rows. A cache build returns
// acceptance before writing output. Partial JSON or a failed writer is an error;
// callers must discard any already-written output when this method fails.
func (c *Client) StreamSQLQuery(ctx context.Context, sql string, fresh bool, output io.Writer) (*CacheBuildAccepted, error) {
	stream := true
	resp, err := c.DoGeneratedStreamingRequestWithContext(ctx, http.MethodPost, "/api/v1/query", &generated.RunQueryRequestOptions{
		Body: &generated.RunQueryBody{SQL: sql, Fresh: &fresh, Stream: &stream},
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusAccepted {
		var accepted CacheBuildAccepted
		if err := json.UnmarshalRead(resp.Body, &accepted); err != nil {
			return nil, fmt.Errorf("decode cache build acceptance: %w", err)
		}
		return &accepted, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, HandleCLIErrorResponse(resp)
	}
	if err := copySQLResultJSON(output, resp.Body); err != nil {
		return nil, fmt.Errorf("SQL export incomplete; discard partial output: %w", err)
	}
	return nil, nil //nolint:nilnil // No accepted job means the export completed successfully.
}

// Token copying keeps numbers exact and validates completion without retaining
// arrays. A syntactically valid but missing/mismatched row_count is not success.
func copySQLResultJSON(output io.Writer, input io.Reader) error {
	dec := jsontext.NewDecoder(input)
	enc := jsontext.NewEncoder(output)
	started, complete, nameNext := false, false, true
	columns, rows, countSeen := false, false, false
	var name string
	var actualCount, declaredCount int64
	rowArray := false
	for {
		depth := dec.StackDepth()
		token, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			if !complete {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if err != nil {
			return err
		}
		kind := token.Kind()
		if depth == 0 {
			if started || kind != '{' {
				return errors.New("expected one SQL result object")
			}
			started = true
		} else if depth == 1 {
			if kind == '}' {
				if !columns || !rows || !countSeen || actualCount != declaredCount {
					return errors.New("SQL result missing completion metadata or row count mismatch")
				}
				complete = true
			} else if nameNext {
				name = token.String()
				nameNext = false
			} else {
				switch name {
				case "columns":
					columns = kind == '['
				case "rows":
					rows = kind == '[' || kind == 'n'
					rowArray = kind == '['
				case "row_count":
					if kind != '0' {
						return errors.New("invalid SQL row count")
					}
					declaredCount, err = token.Int()
					if err != nil || declaredCount < 0 {
						return errors.New("invalid SQL row count")
					}
					countSeen = true
				}
				nameNext = true
			}
		} else if depth == 2 && rowArray && name == "rows" {
			if kind == '[' {
				actualCount++
			} else if kind != ']' {
				return errors.New("invalid SQL result row")
			}
		}
		if err := enc.WriteToken(token); err != nil {
			return err
		}
	}
}
