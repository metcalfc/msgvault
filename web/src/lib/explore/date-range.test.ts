import { describe, expect, it } from 'vitest';

import {
  activeDateRangePreset, dateInputBound, dateInputValue, dateRangeFilters, defaultEverythingFilters,
  withDateBound, withDateRange
} from './date-range';

const now = new Date('2026-09-29T15:00:00Z');

describe('date range presets', () => {
  it('writes after/before filter dimensions as RFC3339 instants', () => {
    const filters = dateRangeFilters('week', now);
    expect(filters.map((filter) => filter.dimension)).toEqual(['after', 'before']);
    expect(filters[0]?.values).toEqual(['2026-09-22T15:00:00.000Z']);
    expect(new Date(filters[1]!.values[0]!).getTime()).toBeGreaterThan(now.getTime());
    expect(new Date(filters[1]!.values[0]!).getTime()).toBeLessThan(now.getTime() + 86_400_000);
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
    expect(filters[1]?.values).toEqual(['2026-08-30T15:00:00.000Z']);
    expect(withDateRange(filters, 'all', now)).toEqual([{ dimension: 'source', values: ['2'] }]);
  });

  it('recognizes which preset the current bounds amount to', () => {
    expect(activeDateRangePreset([], now)).toBe('all');
    expect(activeDateRangePreset(dateRangeFilters('week', now), now)).toBe('week');
    expect(activeDateRangePreset(dateRangeFilters('month', now), now)).toBe('month');
    // Set earlier in the session, the preset still reads as itself.
    expect(activeDateRangePreset(dateRangeFilters('week', new Date(now.getTime() - 3_600_000)), now)).toBe('week');
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
