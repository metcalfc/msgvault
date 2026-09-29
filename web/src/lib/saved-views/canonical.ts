import type { SavedView, SavedViewStateEnvelope } from '../api/generated/models';
import { DEFAULT_EXPLORE_COLUMNS, type ExploreURLState } from '../explore/models';

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

/** Why a Saved View cannot open here, or '' when it can. */
export function savedViewIncompatibility(view: SavedView): string {
  if (view.schema_version !== SAVED_VIEW_SCHEMA_VERSION) {
    return `This view uses schema version ${view.schema_version}. Automatic migration is not supported; remove it and save the current view again.`;
  }
  return view.incompatibility_reason ?? '';
}

/** The Inbox or Search state a Saved View opens. */
export function exploreStateFromSavedView(view: SavedView): Partial<ExploreURLState> {
  const saved = view.canonical_state as SavedViewStateEnvelope;
  const aliases: Record<string, ExploreURLState['filters'][number]['dimension']> = {
    source_id: 'source',
    participant_id: 'participant',
  };
  const filters = (saved.filters ?? []).map((filter) => ({
    dimension: aliases[filter.field] ?? (filter.field as ExploreURLState['filters'][number]['dimension']),
    values: [...filter.values],
  }));
  return {
    workspace: 'everything',
    query: saved.query ?? '',
    searchMode: saved.search_mode === 'semantic' || saved.search_mode === 'hybrid' ? saved.search_mode : 'full_text',
    filters,
    groupingChain: [...(saved.grouping ?? [])] as ExploreURLState['groupingChain'],
    presentation: saved.presentation ?? 'table',
    sort: (saved.sort ?? [{ field: 'occurred_at', direction: 'desc' }]) as ExploreURLState['sort'],
    columns: (saved.columns ?? DEFAULT_EXPLORE_COLUMNS) as ExploreURLState['columns'],
    activeRow: null,
    selectedRow: null,
    conversationAnchor: null,
    scrollAnchor: null,
  };
}
