package api

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"go.kenn.io/msgvault/internal/query"
)

// A stream retains the ordinary JSON result shape. Only a successful query
// writes the closing rows array and row_count, so a partial export is invalid
// JSON and cannot be mistaken for a complete result by a validating client.
func (s *Server) handleSQLQueryStream(w http.ResponseWriter, r *http.Request, sql string, fresh, archiveOnly bool) {
	started := false
	count := 0
	consume := func(columns []string, row []any) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if row == nil {
			w.Header().Set("Content-Type", "application/json")
			started = true
			if _, err := fmt.Fprint(w, `{"columns":`); err != nil {
				return fmt.Errorf("write SQL export header: %w", err)
			}
			if err := json.MarshalWrite(w, columns); err != nil {
				return err
			}
			_, err := fmt.Fprint(w, `,"rows":[`)
			if err != nil {
				return fmt.Errorf("write SQL export rows header: %w", err)
			}
			return nil
		}
		if count > 0 {
			if _, err := fmt.Fprint(w, ","); err != nil {
				return fmt.Errorf("write SQL export row separator: %w", err)
			}
		}
		if err := json.MarshalWrite(w, row); err != nil {
			return err
		}
		count++
		return nil
	}
	var result *query.QueryResult
	var accepted *CacheBuildAccepted
	var err error
	if s.sqlQueryStreamRunner != nil {
		result, accepted, err = s.sqlQueryStreamRunner(r.Context(), sql, fresh, archiveOnly, consume)
	} else if streamer, ok := s.queryEngineForContext(r.Context()).(query.SQLStreamer); ok && !archiveOnly && !fresh {
		result, err = streamer.StreamSQL(r.Context(), sql, consume)
	} else {
		err = ErrSQLQueryEngineUnavailable
	}
	if err != nil {
		if started {
			s.logger.Warn("SQL export ended before completion", "error", err)
			return
		}
		if errors.Is(err, ErrSQLQueryEngineUnavailable) || errors.Is(err, ErrCacheBuildUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "engine_unavailable", err.Error())
		} else if !s.writeIfContextError(w, err) {
			writeError(w, http.StatusBadRequest, "query_error", err.Error())
		}
		return
	}
	if accepted != nil {
		writeJSON(w, http.StatusAccepted, accepted)
		return
	}
	if result == nil || !started { // An invalid runner response is never a successful export.
		if !started {
			writeError(w, http.StatusInternalServerError, "query_error", "SQL export returned no result")
		}
		return
	}
	if _, err := fmt.Fprintf(w, `],"row_count":%d`, count); err != nil {
		return
	}
	if result.Cache != nil {
		if _, err := fmt.Fprint(w, `,"cache":`); err != nil {
			return
		}
		if err := json.MarshalWrite(w, result.Cache); err != nil {
			return
		}
	}
	_, _ = fmt.Fprintln(w, "}")
}
