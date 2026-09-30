import type { ExploreFilter } from '../explore/models';
import { withPersonFilter } from '../explore/group-context';

/** A filter the daemon suggests for a typed query (see
 * POST /api/v1/explore/query-understanding). */
export interface QuerySuggestion {
  kind: 'time_window' | 'person' | 'message_type' | 'account';
  label: string;
  /** Exact text of the query the suggestion replaces; empty removes nothing. */
  span?: string;
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

/** Removes the first exact occurrence of `span` from `query` and tidies
 * the spaces and punctuation it leaves behind. A span that is no longer in
 * the query (the person edited it, or another suggestion took it) removes
 * nothing. */
export function removeSpan(query: string, span: string | undefined): string {
  if (!span) return query;
  const index = query.indexOf(span);
  if (index < 0) return query;
  const joined = `${query.slice(0, index)} ${query.slice(index + span.length)}`;
  return joined
    .replace(/\s+([,.;:!?])/g, '$1')
    .replace(/([,;])[,;]+/g, '$1')
    .replace(/\s{2,}/g, ' ')
    .replace(/^[\s,;:]+|[\s,;:]+$/g, '')
    .trim();
}

/** Dimensions a query may carry once: a suggestion replaces them. */
const SINGLE_DIMENSIONS = new Set(['after', 'before', 'message_type', 'source']);

/** Applies a suggestion: its text leaves the query, its operators join the
 * query, and its filters join the current filters. A person narrows the
 * existing people (AND); a date bound, message type, or account replaces
 * the current one. */
export function applySuggestion(
  query: string,
  filters: readonly ExploreFilter[],
  suggestion: QuerySuggestion,
): { query: string; filters: ExploreFilter[] } {
  let nextQuery = removeSpan(query, suggestion.span);
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
  return { query: nextQuery, filters: nextFilters };
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
