/**
 * Readable paths for the primary surfaces.
 *
 * The path names the surface and the thing on it (a person, a message, a
 * settings section); a few readable parameters carry what people type or
 * share (`q`, `mode`, `since`, `after`, `before`). Everything else — grouping
 * chains, column layout, per-surface filters — stays in the `explore` JSON
 * parameter, which the explore state owns.
 *
 * Links from before paths existed (`/?workspace=…&explore=…`) still parse:
 * the root path defers to the legacy `workspace` parameter and the JSON
 * payload, and the explore state rewrites the address to its path form.
 */
import type { ExploreFilter, ExploreURLState, ExploreWorkspace } from '../explore/models';
import {
  activeDateRangePreset,
  dateRangeFilters,
  isDateDimension,
  withoutDateRange,
} from '../explore/date-range';

export type PersonTab = 'overview' | 'timeline' | 'files' | 'meetings' | 'profile' | 'maintenance';
export const PERSON_TABS: readonly PersonTab[] = ['overview', 'timeline', 'files', 'meetings', 'profile', 'maintenance'];

export type ActivitySection = 'sources' | 'operations' | 'deletions';

/** State fields the path and readable parameters carry for a workspace.
 * The explore JSON leaves them out so a link names each fact once. */
const ROUTED_BY_WORKSPACE: Partial<Record<ExploreWorkspace, ReadonlyArray<keyof ExploreURLState>>> = {
  everything: ['query', 'dateBoundsChosen'],
  directory: ['directoryPersonID', 'personTab'],
  relationships: ['relationshipFacet', 'relationshipTarget', 'personTab'],
  settings: ['settingsSection'],
  message: ['messageID'],
};

export function isRoutedField(workspace: ExploreWorkspace, field: keyof ExploreURLState): boolean {
  // The workspace is the path; the search mode is the `mode` parameter.
  if (field === 'workspace' || field === 'searchMode') return true;
  return ROUTED_BY_WORKSPACE[workspace]?.includes(field) ?? false;
}

/** Query parameters the router owns. Anything else on the address (feature
 * flags, OAuth callbacks) is left alone. */
export const ROUTE_PARAMETERS: readonly string[] = ['workspace', 'explore', 'mode', 'q', 'since', 'after', 'before', 'domain'];

/** The workspace a bare `/` opens when no legacy parameter names one. */
export const DEFAULT_WORKSPACE: ExploreWorkspace = 'directory';

export interface RouteLocation {
  pathname: string;
  /** Readable parameters, in display order. */
  parameters: Array<[string, string]>;
  /** True when the date bounds travel as readable parameters, so the
   * explore JSON must carry only the non-date filters. */
  routesDateBounds: boolean;
}

function positiveInteger(value: string | undefined): number | undefined {
  if (value === undefined || !/^[1-9]\d*$/.test(value)) return undefined;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : undefined;
}

function isPersonTab(value: string | undefined): value is PersonTab {
  return PERSON_TABS.includes(value as PersonTab);
}

function personTabSuffix(tab: unknown): string {
  return typeof tab === 'string' && isPersonTab(tab) && tab !== 'overview' ? `/${tab}` : '';
}

/** `since=7d`/`30d` for the rolling presets, `since=all` for no bounds,
 * else the exact instants. Rolling presets stay rolling when bookmarked. */
function dateParameters(filters: readonly ExploreFilter[], now: Date): Array<[string, string]> {
  const preset = activeDateRangePreset(filters, now);
  if (preset === 'week') return [['since', '7d']];
  if (preset === 'month') return [['since', '30d']];
  if (preset === 'all') return [['since', 'all']];
  const parameters: Array<[string, string]> = [];
  for (const filter of filters) {
    if (isDateDimension(filter.dimension) && filter.values[0]) parameters.push([filter.dimension, filter.values[0]]);
  }
  return parameters;
}

/** The path and readable parameters for a state. */
export function routeForState(state: ExploreURLState, now: Date = new Date()): RouteLocation {
  const plain = (pathname: string): RouteLocation => ({ pathname, parameters: [], routesDateBounds: false });
  switch (state.workspace) {
    case 'everything': {
      const query = state.query.trim();
      // Search always names its mode so a shared link does not depend on the
      // reader's preference; the Inbox names it only when it is not the
      // default, so the mode chosen there survives a reload.
      const parameters: Array<[string, string]> = query
        ? [['q', state.query], ['mode', state.searchMode]]
        : state.searchMode !== 'full_text' ? [['mode', state.searchMode]] : [];
      parameters.push(...dateParameters(state.filters, now));
      return { pathname: query ? '/search' : '/inbox', parameters, routesDateBounds: true };
    }
    case 'directory':
      return plain(state.directoryPersonID !== null
        ? `/people/${state.directoryPersonID}${personTabSuffix(state.personTab)}`
        : '/people');
    case 'relationships': {
      const target = state.relationshipTarget;
      const cluster = target?.startsWith('cluster:') ? target.slice('cluster:'.length) : undefined;
      if (cluster) return plain(`/people/contact-${cluster}${personTabSuffix(state.personTab)}`);
      const domain = target?.startsWith('domain:') ? target.slice('domain:'.length) : undefined;
      // A domain rides as a parameter: a dotted final path segment reads as
      // a file extension to the daemon, which would not serve the app for it.
      if (domain) return { pathname: '/people/domains', parameters: [['domain', domain]], routesDateBounds: false };
      return plain('/people/domains');
    }
    case 'directory_review':
      return plain('/reviews');
    case 'files':
      return plain('/files');
    case 'saved_views':
      return plain('/saved-views');
    case 'sources':
    case 'operations':
    case 'deletions':
      return plain(`/activity/${state.workspace}`);
    case 'settings': {
      const section = typeof state.settingsSection === 'string' ? state.settingsSection : '';
      return plain(section ? `/settings/${encodeURIComponent(section)}` : '/settings');
    }
    case 'message':
      return plain(typeof state.messageID === 'number' ? `/messages/${state.messageID}` : '/inbox');
    default:
      return plain('/');
  }
}

/**
 * The state fields a path and its readable parameters name, as raw values
 * for the explore normalizer, or undefined for the root path (and any path
 * the app does not own), which defers to legacy parameters.
 */
export function stateFromRoute(pathname: string, parameters: URLSearchParams, now: Date = new Date()):
  Record<string, unknown> | undefined {
  const segments = pathname.split('/').filter(Boolean).map((segment) => {
    try {
      return decodeURIComponent(segment);
    } catch {
      return segment;
    }
  });
  const [first, second, third] = segments;
  switch (first) {
    case 'inbox':
    case 'search':
      return {
        workspace: 'everything',
        query: first === 'search' ? parameters.get('q') ?? '' : '',
        ...routedDateBounds(parameters, now),
      };
    case 'people': {
      if (second === undefined) return { workspace: 'directory', directoryPersonID: null };
      // The old observed-contacts list is the People list's Not saved filter.
      if (second === 'contacts') return { workspace: 'directory', directoryPersonID: null, peopleSaved: 'unsaved' };
      if (second === 'domains') {
        const domain = parameters.get('domain');
        return {
          workspace: 'relationships', relationshipFacet: 'domains',
          relationshipTarget: domain ? `domain:${domain}` : null,
        };
      }
      const tab = isPersonTab(third) ? third : 'overview';
      const contact = /^contact-([1-9]\d*)$/.exec(second);
      if (contact) {
        return { workspace: 'relationships', relationshipFacet: 'people', relationshipTarget: `cluster:${contact[1]}`, personTab: tab };
      }
      const personID = positiveInteger(second);
      return personID === undefined
        ? { workspace: 'directory', directoryPersonID: null }
        : { workspace: 'directory', directoryPersonID: personID, personTab: tab };
    }
    case 'reviews':
      return { workspace: 'directory_review' };
    case 'files':
      return { workspace: 'files' };
    case 'saved-views':
      return { workspace: 'saved_views' };
    case 'activity':
      return { workspace: second === 'operations' || second === 'deletions' ? second : 'sources' };
    case 'settings':
      return { workspace: 'settings', settingsSection: second ?? '' };
    case 'messages': {
      const messageID = positiveInteger(second);
      return messageID === undefined ? { workspace: 'everything' } : { workspace: 'message', messageID };
    }
    default:
      return undefined;
  }
}

/** Date bounds named by readable parameters, or nothing when none are set
 * (the explore state then applies its seven-day default once). */
function routedDateBounds(parameters: URLSearchParams, now: Date): { dateFilters?: ExploreFilter[]; dateBoundsChosen?: true } {
  const since = parameters.get('since');
  if (since === '7d') return { dateFilters: dateRangeFilters('week', now), dateBoundsChosen: true };
  if (since === '30d') return { dateFilters: dateRangeFilters('month', now), dateBoundsChosen: true };
  if (since === 'all') return { dateFilters: [], dateBoundsChosen: true };
  const bounds: ExploreFilter[] = [];
  for (const dimension of ['after', 'before'] as const) {
    const value = parameters.get(dimension);
    if (value && !Number.isNaN(Date.parse(value))) bounds.push({ dimension, values: [value] });
  }
  return bounds.length > 0 ? { dateFilters: bounds, dateBoundsChosen: true } : {};
}

/** Replaces the date bounds among filters with routed ones. */
export function withRoutedDateBounds(filters: readonly ExploreFilter[], dateFilters: readonly ExploreFilter[]): ExploreFilter[] {
  return [...withoutDateRange(filters), ...dateFilters.map((filter) => ({ ...filter, values: [...filter.values] }))];
}

/** A short human title for the surface a state shows, for document.title. */
export function routeTitle(state: ExploreURLState): string {
  switch (state.workspace) {
    case 'everything':
      return state.query.trim() ? `Search: ${state.query.trim()}` : 'Inbox';
    case 'directory':
    case 'relationships':
      return 'People';
    case 'directory_review':
      return 'Reviews';
    case 'files':
      return 'Files';
    case 'saved_views':
      return 'Saved Views';
    case 'sources':
      return 'Sources · Activity';
    case 'operations':
      return 'Operations · Activity';
    case 'deletions':
      return 'Deletions · Activity';
    case 'settings':
      return 'Settings';
    case 'message':
      return 'Message';
    default:
      return 'msgvault';
  }
}
