/** The date range picker's side of the date bounds. Kept apart from
 * date-range.ts, which the URL-state serializer uses, so that module stays
 * free of UI toolkit imports and loads outside the browser build. */
import { resolveRange, type RangeSelection } from '@kenn-io/kit-ui';

import type { ExploreFilter } from './models';
import { dateInputBound, withDateBound, withoutDateRange, withPickedDays } from './date-range';

/** Applies a range picker selection. Only a custom selection preserves the
 * instants of bounds whose day did not change; a relative or calendar
 * preset means whole days, so both bounds become full-day edges even when
 * a bound already sat somewhere inside the preset's first or last day. A
 * relative selection of no days is "All time". */
export function withRangeSelection(filters: readonly ExploreFilter[], selection: RangeSelection): ExploreFilter[] {
  if (selection.mode === 'relative' && selection.days <= 0) return withoutDateRange(filters);
  const range = resolveRange(selection);
  if (selection.mode === 'custom') return withPickedDays(filters, range);
  const bounded = withDateBound(filters, 'after', dateInputBound(range.from, 'after'));
  return withDateBound(bounded, 'before', dateInputBound(range.to, 'before'));
}
