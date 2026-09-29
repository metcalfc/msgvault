package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/msgvault/internal/jev"
)

// ReserveJevDayRequest counts one Jev request against a feature's UTC day.
// The increment is a single conditional UPDATE so two workers cannot both
// pass a limit from the same snapshot. It reports which limit stopped the
// request through jev's daily categories.
func (s *Store) ReserveJevDayRequest(ctx context.Context, reservation jev.DayReservation) error {
	if err := validateJevDay(reservation.Feature, reservation.UTCDay); err != nil {
		return err
	}
	if reservation.Limits.MaxRequests < 0 || reservation.Limits.MaxCostUSDMicros < 0 {
		return errors.New("jev day limits must be non-negative")
	}
	return s.withTxContext(ctx, func(tx *loggedTx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO jev_day_counters (feature, utc_day)
			VALUES (?, ?) ON CONFLICT (feature, utc_day) DO NOTHING`,
			reservation.Feature, reservation.UTCDay); err != nil {
			return fmt.Errorf("ensure jev day counter: %w", err)
		}
		updated, err := tx.ExecContext(ctx, `UPDATE jev_day_counters
			SET requests = requests + 1
			WHERE feature = ? AND utc_day = ?
			  AND (? = 0 OR requests < ?)
			  AND (? = 0 OR cost_usd_micros < ?)`,
			reservation.Feature, reservation.UTCDay,
			reservation.Limits.MaxRequests, reservation.Limits.MaxRequests,
			reservation.Limits.MaxCostUSDMicros, reservation.Limits.MaxCostUSDMicros)
		if err != nil {
			return fmt.Errorf("reserve jev day request: %w", err)
		}
		rows, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read jev day reservation result: %w", err)
		}
		if rows == 1 {
			return nil
		}
		counters, err := scanJevDayCounters(tx.QueryRowContext(ctx,
			jevDayCountersSelect+` WHERE feature = ? AND utc_day = ?`,
			reservation.Feature, reservation.UTCDay))
		if err != nil {
			return fmt.Errorf("read jev day counter after refusal: %w", err)
		}
		if reservation.Limits.MaxCostUSDMicros > 0 && counters.CostUSDMicros >= reservation.Limits.MaxCostUSDMicros {
			return jev.ErrDayCostStop
		}
		return jev.ErrDayRequestLimit
	})
}

// ReleaseJevDayRequest returns one reservation for a request that never left
// the process. It never takes the day below zero.
func (s *Store) ReleaseJevDayRequest(ctx context.Context, reservation jev.DayReservation) error {
	if err := validateJevDay(reservation.Feature, reservation.UTCDay); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jev_day_counters
		SET requests = requests - 1
		WHERE feature = ? AND utc_day = ? AND requests > 0`,
		reservation.Feature, reservation.UTCDay); err != nil {
		return fmt.Errorf("release jev day request: %w", err)
	}
	return nil
}

// RecordJevDayUsage adds a completed request's measured tokens and cost to
// its day. Requests are counted at reservation, so only usage moves here.
func (s *Store) RecordJevDayUsage(ctx context.Context, usage jev.DayUsage) error {
	if err := validateJevDay(usage.Feature, usage.UTCDay); err != nil {
		return err
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CostUSDMicros < 0 {
		return errors.New("jev day usage must be non-negative")
	}
	if !usage.UsageKnown {
		return nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE jev_day_counters
		SET input_tokens = input_tokens + ?, output_tokens = output_tokens + ?,
		    cost_usd_micros = cost_usd_micros + ?
		WHERE feature = ? AND utc_day = ?`,
		usage.InputTokens, usage.OutputTokens, usage.CostUSDMicros, usage.Feature, usage.UTCDay)
	if err != nil {
		return fmt.Errorf("record jev day usage: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read jev day usage result: %w", err)
	}
	if rows != 1 {
		return errors.New("jev day usage recorded before its reservation")
	}
	return nil
}

// JevDayCounters returns one feature's counters for a day; a day with no
// requests reads as zeros.
func (s *Store) JevDayCounters(ctx context.Context, feature, utcDay string) (jev.DayCounters, error) {
	if err := validateJevDay(feature, utcDay); err != nil {
		return jev.DayCounters{}, err
	}
	counters, err := scanJevDayCounters(s.db.QueryRowContext(ctx,
		jevDayCountersSelect+` WHERE feature = ? AND utc_day = ?`, feature, utcDay))
	if errors.Is(err, sql.ErrNoRows) {
		return jev.DayCounters{Feature: feature, UTCDay: utcDay}, nil
	}
	if err != nil {
		return jev.DayCounters{}, fmt.Errorf("read jev day counter: %w", err)
	}
	return counters, nil
}

const jevDayCountersSelect = `SELECT feature, utc_day, requests, input_tokens, output_tokens, cost_usd_micros
	FROM jev_day_counters`

func scanJevDayCounters(row scanner) (jev.DayCounters, error) {
	var counters jev.DayCounters
	err := row.Scan(&counters.Feature, &counters.UTCDay, &counters.Requests,
		&counters.InputTokens, &counters.OutputTokens, &counters.CostUSDMicros)
	return counters, err
}

func validateJevDay(feature, utcDay string) error {
	if !jev.ValidFeatureName(feature) {
		return errors.New("jev feature name is invalid")
	}
	if _, err := time.Parse(time.DateOnly, utcDay); err != nil {
		return errors.New("jev utc day must be YYYY-MM-DD")
	}
	return nil
}
