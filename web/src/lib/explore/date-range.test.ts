import { describe, expect, it } from 'vitest';

import {
  activeDateRangePreset, dateInputBound, dateInputValue, dateRangeFilters, defaultEverythingFilters,
  endOfLocalDay, presetStart, startOfLocalDay, withDateBound, withDateRange
} from './date-range';

// Local noon: an hour either way stays on the same local day in every zone.
const now = new Date(2026, 8, 29, 12, 0, 0);

describe('date range presets', () => {
  it('writes start-of-local-day windows as RFC3339 after/before filter dimensions', () => {
    const filters = dateRangeFilters('week', now);
    expect(filters.map((filter) => filter.dimension)).toEqual(['after', 'before']);
    // "Last 7 days" counts today: the window starts six local days ago at midnight.
    const expectedStart = startOfLocalDay(new Date(2026, 8, 23, 12));
    expect(filters[0]?.values).toEqual([expectedStart.toISOString()]);
    expect(presetStart('week', now).getTime()).toBe(expectedStart.getTime());
    expect(presetStart('month', now).getTime()).toBe(startOfLocalDay(new Date(2026, 7, 31, 12)).getTime());
    expect(filters[1]?.values).toEqual([endOfLocalDay(now).toISOString()]);
    expect(filters[0]!.values[0]).toMatch(/^\d{4}-\d{2}-\d{2}T/);
    expect(dateRangeFilters('all', now)).toEqual([]);
    expect(defaultEverythingFilters(now)).toEqual(dateRangeFilters('week', now));
  });

  it('replaces only the date bounds and keeps other filters in place', () => {
    const filters = withDateRange(
      [{ dimension: 'source', values: ['2'] }, { dimension: 'after', values: ['2020-01-01T00:00:00Z'] }],
      'month', now
    );
    expect(filters[0]).toEqual({ dimension: 'source', values: ['2'] });
    expect(filters.map((filter) => filter.dimension)).toEqual(['source', 'after', 'before']);
    expect(filters[1]?.values).toEqual([presetStart('month', now).toISOString()]);
    expect(withDateRange(filters, 'all', now)).toEqual([{ dimension: 'source', values: ['2'] }]);
  });

  it('recognizes a preset only by an exact match of both bounds for today', () => {
    expect(activeDateRangePreset([], now)).toBe('all');
    expect(activeDateRangePreset(dateRangeFilters('week', now), now)).toBe('week');
    expect(activeDateRangePreset(dateRangeFilters('month', now), now)).toBe('month');
    // The same local day reads the same at any hour; an earlier day is custom.
    expect(activeDateRangePreset(dateRangeFilters('week', new Date(now.getTime() - 3_600_000)), now)).toBe('week');
    expect(activeDateRangePreset(dateRangeFilters('week', new Date(now.getTime() - 86_400_000)), now)).toBe('custom');
    expect(activeDateRangePreset(dateRangeFilters('week', now).slice(0, 1), now)).toBe('custom');
    expect(activeDateRangePreset([{ dimension: 'after', values: ['2020-01-01T00:00:00Z'] }], now)).toBe('custom');
    expect(activeDateRangePreset([{ dimension: 'before', values: ['2020-01-01T00:00:00Z'] }], now)).toBe('custom');
    expect(activeDateRangePreset([{ dimension: 'after', values: ['not a date'] }], now)).toBe('custom');
  });

  it('sets and clears a single bound', () => {
    const withAfter = withDateBound([{ dimension: 'source', values: ['2'] }], 'after', '2026-01-01T00:00:00Z');
    expect(withAfter).toEqual([{ dimension: 'source', values: ['2'] }, { dimension: 'after', values: ['2026-01-01T00:00:00Z'] }]);
    expect(withDateBound(withAfter, 'after', undefined)).toEqual([{ dimension: 'source', values: ['2'] }]);
  });

  it('round-trips native date input values through local day bounds', () => {
    const after = dateInputBound('2026-03-05', 'after')!;
    const before = dateInputBound('2026-03-05', 'before')!;
    expect(dateInputValue(after)).toBe('2026-03-05');
    expect(dateInputValue(before)).toBe('2026-03-05');
    expect(new Date(before).getTime() - new Date(after).getTime()).toBe(86_399_999);
    expect(dateInputBound('nope', 'after')).toBeUndefined();
    expect(dateInputValue(undefined)).toBe('');
    expect(dateInputValue('nope')).toBe('');
  });
});
