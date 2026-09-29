import type { SavedViewStateEnvelope } from '../api/generated/models';
import type { ExploreURLState } from '../explore/models';

export const SAVED_VIEW_SCHEMA_VERSION = 1;

/** The exact state a Saved View stores for the current Everything view:
 * query and mode, filters, grouping, presentation, sort, and columns. */
export function canonicalSavedViewState(state: ExploreURLState): SavedViewStateEnvelope {
  const query = state.query.trim();
  return {
    ...(query ? { query, search_mode: state.searchMode } : {}),
    filters: state.filters.map((filter) => ({
      field: filter.dimension,
      operator: 'in',
      values: [...filter.values],
    })),
    grouping: [...state.groupingChain],
    presentation: state.presentation,
    sort: state.sort.map((sort) => ({ field: sort.field, direction: sort.direction })),
    columns: [...state.columns],
  };
}
