/** Date-range presets for the Everything view, expressed as `after`/`before`
 * filter dimensions (RFC3339 instants, which is what the explore API
 * requires) rather than `after:`/`before:` text operators in the query. */
import type { ExploreFilter } from './models';

export type DateRangePreset = 'week' | 'month' | 'all';
export type DateDimension = 'after' | 'before';

export const DATE_RANGE_PRESETS: ReadonlyArray<{ value: DateRangePreset; label: string }> = [
  { value: 'week', label: 'This week' },
  { value: 'month', label: 'This month' },
  { value: 'all', label: 'All time' }
];

const DAY_MS = 86_400_000;
const PRESET_DAYS: Record<Exclude<DateRangePreset, 'all'>, number> = { week: 7, month: 30 };

export function isDateDimension(dimension: string): dimension is DateDimension {
  return dimension === 'after' || dimension === 'before';
}

/** The last instant of the local calendar day containing `now`. A range
 * bounded here keeps the rest of today but drops next year's calendar
 * events, which otherwise sit on top of a newest-first list. */
export function endOfLocalDay(now: Date): Date {
  const end = new Date(now);
  end.setHours(23, 59, 59, 999);
  return end;
}

export function startOfLocalDay(now: Date): Date {
  const start = new Date(now);
  start.setHours(0, 0, 0, 0);
  return start;
}

export function dateBound(filters: readonly ExploreFilter[], dimension: DateDimension): string | undefined {
  return filters.find((filter) => filter.dimension === dimension)?.values[0];
}

/** Replaces one bound; an empty value removes it. Other filters keep their order. */
export function withDateBound(filters: readonly ExploreFilter[], dimension: DateDimension, value: string | undefined): ExploreFilter[] {
  const rest = filters.filter((filter) => filter.dimension !== dimension);
  return value ? [...rest, { dimension, values: [value] }] : rest;
}

export function withoutDateRange(filters: readonly ExploreFilter[]): ExploreFilter[] {
  return filters.filter((filter) => !isDateDimension(filter.dimension));
}

export function dateRangeFilters(preset: DateRangePreset, now: Date = new Date()): ExploreFilter[] {
  if (preset === 'all') return [];
  return [
    { dimension: 'after', values: [new Date(now.getTime() - PRESET_DAYS[preset] * DAY_MS).toISOString()] },
    { dimension: 'before', values: [endOfLocalDay(now).toISOString()] }
  ];
}

export function withDateRange(filters: readonly ExploreFilter[], preset: DateRangePreset, now: Date = new Date()): ExploreFilter[] {
  return [...withoutDateRange(filters), ...dateRangeFilters(preset, now)];
}

/** Which preset the current bounds amount to, or 'custom' when the bounds
 * were set by hand (or a preset has aged out of tolerance). */
export function activeDateRangePreset(filters: readonly ExploreFilter[], now: Date = new Date()): DateRangePreset | 'custom' {
  const after = dateBound(filters, 'after');
  const before = dateBound(filters, 'before');
  if (!after && !before) return 'all';
  if (!after) return 'custom';
  const afterAt = new Date(after).getTime();
  const beforeAt = before ? new Date(before).getTime() : undefined;
  if (Number.isNaN(afterAt) || (beforeAt !== undefined && Number.isNaN(beforeAt))) return 'custom';
  // A preset's `before` bound is the end of the day it was set on: accept
  // anything from a day ago to two days out so a bound set earlier in the
  // session still reads as the preset.
  if (beforeAt !== undefined && (beforeAt < now.getTime() - DAY_MS || beforeAt > now.getTime() + 2 * DAY_MS)) return 'custom';
  const ageDays = (now.getTime() - afterAt) / DAY_MS;
  for (const preset of ['week', 'month'] as const) {
    if (Math.abs(ageDays - PRESET_DAYS[preset]) <= 0.5) return preset;
  }
  return 'custom';
}

/** The Everything view opens on the last seven days. */
export function defaultEverythingFilters(now: Date = new Date()): ExploreFilter[] {
  return dateRangeFilters('week', now);
}

/** `YYYY-MM-DD` in local time for a native date input, or '' when unset/invalid. */
export function dateInputValue(iso: string | undefined): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.valueOf())) return '';
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  return `${date.getFullYear()}-${month}-${day}`;
}

/** Converts a native date input's `YYYY-MM-DD` to the instant that bounds the
 * range: the start of that local day for `after`, its end for `before`. */
export function dateInputBound(value: string, dimension: DateDimension): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return undefined;
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  if (Number.isNaN(date.valueOf())) return undefined;
  return (dimension === 'after' ? startOfLocalDay(date) : endOfLocalDay(date)).toISOString();
}
