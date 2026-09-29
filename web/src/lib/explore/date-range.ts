/** Date-range presets for the Everything view, expressed as `after`/`before`
 * filter dimensions (RFC3339 instants, which is what the explore API
 * requires) rather than `after:`/`before:` text operators in the query. */
import type { ExploreFilter } from './models';

export type DateRangePreset = 'week' | 'month' | 'all';
export type DateDimension = 'after' | 'before';

/** Rolling windows, so the chips say so. */
export const DATE_RANGE_PRESETS: ReadonlyArray<{ value: DateRangePreset; label: string }> = [
  { value: 'week', label: 'Last 7 days' },
  { value: 'month', label: 'Last 30 days' },
  { value: 'all', label: 'All time' }
];

const DAY_MS = 86_400_000;
/** "Last N days" counts today, like kit-ui's DateRangePicker: the window
 * starts at the start of the local day N−1 days ago. */
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

/** The first instant of a preset's window: the start of the local day
 * N−1 days before `now`. */
export function presetStart(preset: Exclude<DateRangePreset, 'all'>, now: Date = new Date()): Date {
  const start = startOfLocalDay(now);
  start.setDate(start.getDate() - (PRESET_DAYS[preset] - 1));
  return start;
}

export function dateRangeFilters(preset: DateRangePreset, now: Date = new Date()): ExploreFilter[] {
  if (preset === 'all') return [];
  return [
    { dimension: 'after', values: [presetStart(preset, now).toISOString()] },
    { dimension: 'before', values: [endOfLocalDay(now).toISOString()] }
  ];
}

export function withDateRange(filters: readonly ExploreFilter[], preset: DateRangePreset, now: Date = new Date()): ExploreFilter[] {
  return [...withoutDateRange(filters), ...dateRangeFilters(preset, now)];
}

/** Which preset the current bounds are, by exact match against the
 * preset's bounds for today's local day, or 'custom' for anything else
 * (hand-set bounds, or a preset set on an earlier day). */
export function activeDateRangePreset(filters: readonly ExploreFilter[], now: Date = new Date()): DateRangePreset | 'custom' {
  const after = dateBound(filters, 'after');
  const before = dateBound(filters, 'before');
  if (!after && !before) return 'all';
  for (const preset of ['week', 'month'] as const) {
    const [expectedAfter, expectedBefore] = dateRangeFilters(preset, now);
    if (after === expectedAfter!.values[0] && before === expectedBefore!.values[0]) return preset;
  }
  return 'custom';
}

/** A window of one day either side of an instant: enough to find one
 * message by its key on the first page without scanning the archive. */
export function dayWindowFilters(iso: string): ExploreFilter[] {
  const at = new Date(iso);
  if (Number.isNaN(at.valueOf())) return [];
  return [
    { dimension: 'after', values: [new Date(at.getTime() - DAY_MS).toISOString()] },
    { dimension: 'before', values: [new Date(at.getTime() + DAY_MS).toISOString()] }
  ];
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

/** Applies the days a range picker shows for both bounds, rewriting only
 * the bound whose day changed. A bound the picker still shows on its own
 * day keeps its exact instant: re-deriving it from the local day would
 * shift a non-midnight bound (an "after 10:30" from a drilled row) to the
 * edge of the day. An empty day removes that bound. */
export function withPickedDays(filters: readonly ExploreFilter[], days: { from: string; to: string }): ExploreFilter[] {
  let next: ExploreFilter[] = [...filters];
  for (const [dimension, day] of [['after', days.from], ['before', days.to]] as const) {
    if (day === dateInputValue(dateBound(filters, dimension))) continue;
    next = withDateBound(next, dimension, dateInputBound(day, dimension));
  }
  return next;
}
