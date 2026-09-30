import type { ExploreFilter } from '../explore/models';
import { withPersonFilter } from '../explore/group-context';

/** A filter the daemon suggests for a typed query (see
 * POST /api/v1/explore/query-understanding). */
export interface QuerySuggestion {
  kind: 'time_window' | 'person' | 'message_type' | 'account';
  label: string;
  /** Exact text of the query the suggestion replaces; empty removes nothing. */
  span?: string;
  /** Where `span` sits in the query, as string indexes; the daemon sends
   * them so the right occurrence is removed. */
  spanStart?: number;
  spanEnd?: number;
  probability: number;
  filters: ExploreFilter[];
  queryOperators: string[];
}

export interface QueryUnderstanding {
  status: 'judged' | 'skipped' | 'late';
  reason?: string;
  suggestions: QuerySuggestion[];
  /** An empty full-text search should offer hybrid for this query. */
  offerHybrid: boolean;
}

/** A query edit and where each old index lands in the new query
 * (undefined for an index inside the removed text). */
export interface SpanRemoval {
  query: string;
  map: (index: number) => number | undefined;
}

/** True when the suggestion's positions still hold its span in `query`. */
export function spanHolds(query: string, suggestion: QuerySuggestion): boolean {
  const { span, spanStart, spanEnd } = suggestion;
  return Boolean(span) && spanStart !== undefined && spanEnd !== undefined && spanStart < spanEnd &&
    query.slice(spanStart, spanEnd) === span;
}

/** Removes query[start, end) and tidies the spaces and punctuation it
 * leaves behind, reporting where every other index moved. */
export function removeSpanAt(query: string, start: number, end: number): SpanRemoval {
  let left = query.slice(0, start).replace(/\s+$/, '');
  const right = query.slice(end);
  let rest = right.replace(/^\s+/, '');
  if (!left) rest = rest.replace(/^[\s,;:]+/, '');
  else if (/[,;]$/.test(left) && /^[,;]/.test(rest)) rest = rest.slice(1).replace(/^\s+/, '');
  if (!rest) left = left.replace(/[\s,;:]+$/, '');
  const dropped = right.length - rest.length;
  const separator = left && rest && !/^[,.;:!?]/.test(rest) ? ' ' : '';
  const shift = left.length + separator.length;
  return {
    query: left + separator + rest,
    map: (index) => {
      if (index <= start) return Math.min(index, left.length);
      if (index < end) return undefined;
      return Math.max(shift, shift + index - end - dropped);
    },
  };
}

/** Moves a suggestion's span through a removal: a span inside the removed
 * text is dropped, any other span follows its words. */
export function rebaseSuggestion(suggestion: QuerySuggestion, removal: SpanRemoval): QuerySuggestion {
  if (suggestion.spanStart === undefined || suggestion.spanEnd === undefined) return suggestion;
  const start = removal.map(suggestion.spanStart);
  const end = removal.map(suggestion.spanEnd);
  if (start === undefined || end === undefined || end - start !== suggestion.spanEnd - suggestion.spanStart) {
    return { ...suggestion, span: undefined, spanStart: undefined, spanEnd: undefined };
  }
  return { ...suggestion, spanStart: start, spanEnd: end };
}

/** Dimensions a query may carry once: a suggestion replaces them. */
const SINGLE_DIMENSIONS = new Set(['after', 'before', 'message_type', 'source']);

/** Applies a suggestion: its text leaves the query at its own position (an
 * earlier copy of the same words stays), its operators join the query, and
 * its filters join the current filters. A person narrows the existing
 * people (AND); a date bound, message type, or account replaces the
 * current one. `rebase` moves the other suggestions' spans to the new
 * query. */
export function applySuggestion(
  query: string,
  filters: readonly ExploreFilter[],
  suggestion: QuerySuggestion,
): { query: string; filters: ExploreFilter[]; rebase: (other: QuerySuggestion) => QuerySuggestion } {
  const removal: SpanRemoval = spanHolds(query, suggestion)
    ? removeSpanAt(query, suggestion.spanStart!, suggestion.spanEnd!)
    : { query, map: (index) => index };
  let nextQuery = removal.query;
  const operators = suggestion.queryOperators.filter((operator) => !nextQuery.split(/\s+/).includes(operator));
  if (operators.length) nextQuery = [nextQuery, ...operators].filter(Boolean).join(' ');
  let nextFilters = [...filters];
  for (const filter of suggestion.filters) {
    if (filter.dimension === 'participant') {
      for (const value of filter.values) nextFilters = withPersonFilter(nextFilters, value);
      continue;
    }
    if (SINGLE_DIMENSIONS.has(filter.dimension)) {
      nextFilters = nextFilters.filter((existing) => existing.dimension !== filter.dimension);
    }
    nextFilters.push({ dimension: filter.dimension, values: [...filter.values] });
  }
  return { query: nextQuery, filters: nextFilters, rebase: (other) => rebaseSuggestion(other, removal) };
}

/** What a chip says to a screen reader. */
export function suggestionAccessibleName(suggestion: QuerySuggestion): string {
  const kind: Record<QuerySuggestion['kind'], string> = {
    time_window: 'time period',
    person: 'person',
    message_type: 'message type',
    account: 'account',
  };
  const removes = suggestion.span ? `, replacing “${suggestion.span}” in the search` : '';
  return `Apply suggested ${kind[suggestion.kind]} filter: ${suggestion.label}${removes}`;
}
