import { describe, expect, it } from 'vitest';

import { resolveRange } from '@kenn-io/kit-ui';

import {
  activeDateRangePreset, dateInputBound, dateInputValue, dateRangeFilters, defaultEverythingFilters,
  endOfLocalDay, presetStart, startOfLocalDay, withDateBound, withDateRange, withPickedDays
} from './date-range';
import { withRangeSelection } from './date-range-selection';

// Local noon: an hour either way stays on the same local day in every zone.
const now = new Date(2026, 8, 29, 12, 0, 0);

describe('withPickedDays', () => {
  // Non-midnight instants, as a drilled row or a shared link would carry.
  const after = new Date(2026, 8, 20, 10, 30).toISOString();
  const before = new Date(2026, 8, 27, 15, 45).toISOString();
  const filters = [
    { dimension: 'source' as const, values: ['2'] },
    { dimension: 'after' as const, values: [after] },
    { dimension: 'before' as const, values: [before] }
  ];

  it('rewrites only the bound whose day changed and keeps the other instant exact', () => {
    const picked = withPickedDays(filters, { from: dateInputValue(after), to: '2026-09-29' });
    expect(picked.find((filter) => filter.dimension === 'after')?.values).toEqual([after]);
    expect(picked.find((filter) => filter.dimension === 'before')?.values).toEqual([dateInputBound('2026-09-29', 'before')]);
    expect(picked.find((filter) => filter.dimension === 'source')?.values).toEqual(['2']);
  });

  it('leaves both bounds untouched when the picker shows their own days', () => {
    expect(withPickedDays(filters, { from: dateInputValue(after), to: dateInputValue(before) })).toEqual(filters);
  });

  it('removes a bound the picker cleared', () => {
    const picked = withPickedDays(filters, { from: '', to: dateInputValue(before) });
    expect(picked.map((filter) => filter.dimension)).toEqual(['source', 'before']);
  });
});

describe('withRangeSelection', () => {
  it('turns a relative preset into whole days even when a bound sits inside its first day', () => {
    // An "after 10:30" bound on the day "Last 7 days" starts: the preset
    // means the whole day, so the partial-day instant must not survive and
    // the chip reads the preset rather than Custom.
    const range = resolveRange({ mode: 'relative', days: 7 });
    const [year, month, day] = range.from.split('-').map(Number) as [number, number, number];
    const partial = new Date(year, month - 1, day, 10, 30).toISOString();
    const filters = [
      { dimension: 'source' as const, values: ['2'] },
      { dimension: 'after' as const, values: [partial] }
    ];
    expect(activeDateRangePreset(filters)).toBe('custom');
    const bounded = withRangeSelection(filters, { mode: 'relative', days: 7 });
    expect(bounded.find((filter) => filter.dimension === 'after')?.values).toEqual([dateInputBound(range.from, 'after')]);
    expect(bounded.find((filter) => filter.dimension === 'before')?.values).toEqual([dateInputBound(range.to, 'before')]);
    expect(bounded.find((filter) => filter.dimension === 'after')?.values).not.toEqual([partial]);
    expect(activeDateRangePreset(bounded)).toBe('week');
  });

  it('keeps unchanged instants only for a custom selection, and clears bounds for no days', () => {
    const after = new Date(2026, 8, 20, 10, 30).toISOString();
    const filters = [{ dimension: 'after' as const, values: [after] }];
    const custom = withRangeSelection(filters, { mode: 'custom', from: dateInputValue(after), to: '2026-09-29' });
    expect(custom.find((filter) => filter.dimension === 'after')?.values).toEqual([after]);
    expect(withRangeSelection(filters, { mode: 'relative', days: 0 })).toEqual([]);
  });
});

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
