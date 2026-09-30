import type {
  ExploreColumn,
  ExploreFilter,
  ExploreGroupDimension,
  ExploreScrollAnchor,
  ExploreSort,
  FileSearchSort,
  FileMIMEFamily,
  PersonFileDirection,
  ExploreURLState,
  ExploreWorkspace,
  DirectoryReviewKind,
  IdentityReviewOrigin,
  IdentityReviewState,
  RelationshipReviewState,
  RelationshipFacet
} from './models';
import { DEFAULT_EXPLORE_COLUMNS, isValidSourceID } from './models';
import { validateGroupingChain } from '../grouping/catalog';
import { parseAttachmentSelection } from './attachment-authority';
import { normalizeSettingsNavigationAuthority } from '../carddav/navigation';
import { normalizeOperationURLState, normalizeOperationRunID as operationRunID } from '../operations/url-state';
import { withoutDateRange } from './date-range';
import {
  DEFAULT_WORKSPACE,
  PERSON_TABS,
  isRoutedField,
  ROUTE_PARAMETERS,
  routeForState,
  stateFromRoute,
  withRoutedDateBounds,
  type PersonTab
} from '../routing/routes';

const STATE_PARAMETER = 'explore';
const FILTER_DIMENSIONS = new Set([
  'source',
  'identity',
  'participant',
  'domain',
  'mailing_list',
  'message_type',
  'after',
  'before',
  'deletion'
]);
const COLUMNS = new Set(['kind', 'people', 'title', 'excerpt', 'time', 'attachments', 'size']);
const FILE_MIME_FAMILIES = new Set<FileMIMEFamily>([
  'image',
  'pdf',
  'audio',
  'video',
  'text',
  'document',
  'archive',
  'other'
]);
const PERSON_FILE_DIRECTIONS = new Set<PersonFileDirection>(['from_person', 'to_person', 'group']);
export const defaultExploreURLState: ExploreURLState = {
  schemaVersion: 2,
  workspace: 'directory',
  directoryQuery: '',
  directoryContactState: '',
  directoryCategory: '',
  directoryOrganization: '',
  directoryPrimaryChannel: '',
  directoryLastContactAfter: '',
  directoryLastContactBefore: '',
  directorySort: 'last_contact_desc',
  directoryPersonID: null,
  directoryHasName: false,
  peopleSaved: '',
  personTab: 'overview',
  reviewKind: 'identity',
  identityState: 'candidate',
  identityOrigin: 'all',
  relationshipReviewState: 'pending',
  query: '',
  searchMode: 'full_text',
  filters: [],
  dateBoundsChosen: false,
  groupingChain: [],
  presentation: 'table',
  sort: [{ field: 'occurred_at', direction: 'desc' }],
  fileSort: { field: 'occurred_at', direction: 'desc' },
  fileFilenameQuery: '',
  fileMIMEFamilies: [],
  personFilePresentation: 'files',
  personFileDirections: ['from_person'],
  identityQuery: '',
  identitySort: { field: 'activity_count', direction: 'desc' },
  analysisTarget: null,
  selectedIdentifier: null,
  relationshipFacet: 'people',
  relationshipTarget: null,
  relationshipShowAll: false,
  relationshipFiles: false,
  operationLane: '',
  operationKind: '',
  operationState: '',
  operationStartedFrom: '',
  operationStartedBefore: '',
  operationRunID: null,
  operationStatus: '',
  settingsAuthority: '',
  settingsSection: '',
  messageID: null,
  meetingID: null,
  meetingPerson: '',
  meetingSource: '',
  meetingSince: '30d',
  columns: [...DEFAULT_EXPLORE_COLUMNS],
  columnWidths: {},
  activeRow: null,
  selectedRow: null,
  inspectorPinned: true,
  conversationAnchor: null,
  scrollAnchor: null
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function freshDefaults(): ExploreURLState {
  return {
    ...defaultExploreURLState,
    filters: defaultExploreURLState.filters.map((filter) => ({
      ...filter,
      values: [...filter.values]
    })),
    groupingChain: [...defaultExploreURLState.groupingChain],
    sort: defaultExploreURLState.sort.map((sort) => ({ ...sort })),
    fileSort: defaultExploreURLState.fileSort ? { ...defaultExploreURLState.fileSort } : undefined,
    fileMIMEFamilies: [...defaultExploreURLState.fileMIMEFamilies],
    personFileDirections: [...defaultExploreURLState.personFileDirections],
    columns: [...defaultExploreURLState.columns],
    columnWidths: { ...defaultExploreURLState.columnWidths }
  };
}

function isFilter(value: unknown): value is ExploreFilter {
  return (
    isRecord(value) &&
    typeof value.dimension === 'string' &&
    FILTER_DIMENSIONS.has(value.dimension) &&
    Array.isArray(value.values) &&
    value.values.every((item) => typeof item === 'string')
  );
}

function filters(value: unknown): ExploreFilter[] {
  if (!Array.isArray(value) || !value.every(isFilter)) return [];
  const copied = value.map((filter) => ({ ...filter, values: [...filter.values] }));
  const sourceFilters = copied.filter((filter) => filter.dimension === 'source');
  const sourceValue =
    sourceFilters.length === 1 && sourceFilters[0]?.values.length === 1 ? sourceFilters[0].values[0] : undefined;
  const sourceID = isValidSourceID(sourceValue) ? sourceValue : undefined;
  const identityFilters = copied.filter((filter) => filter.dimension === 'identity');
  const identity = identityFilters.length === 1 ? identityFilters[0] : undefined;
  const validIdentity =
    identity !== undefined &&
    sourceID !== undefined &&
    identity.values.length === 3 &&
    identity.values[0] === sourceID &&
    identity.values[1] !== '' &&
    (identity.values[2] === 'any' || identity.values[2] === 'sender' || identity.values[2] === 'recipient');
  return copied.filter((filter) => filter.dimension !== 'identity' || validIdentity);
}

function groups(value: unknown): ExploreGroupDimension[] {
  return validateGroupingChain(value);
}

function columns(value: unknown): ExploreColumn[] {
  return Array.isArray(value) && value.every((item) => COLUMNS.has(String(item)))
    ? ([...value] as ExploreColumn[])
    : [...DEFAULT_EXPLORE_COLUMNS];
}

function sorts(value: unknown): ExploreSort[] {
  return Array.isArray(value) &&
    value.every((item) => isRecord(item) && item.field === 'occurred_at' && item.direction === 'desc')
    ? (value.map((item) => ({ ...item })) as ExploreSort[])
    : defaultExploreURLState.sort.map((sort) => ({ ...sort }));
}

function fileSort(value: unknown): FileSearchSort {
  return isRecord(value) &&
    (value.field === 'occurred_at' || value.field === 'filename' || value.field === 'size') &&
    (value.direction === 'asc' || value.direction === 'desc')
    ? { field: value.field, direction: value.direction }
    : { field: 'occurred_at', direction: 'desc' };
}

function fileMIMEFamilies(value: unknown): FileMIMEFamily[] {
  return Array.isArray(value) &&
    value.every((item) => typeof item === 'string' && FILE_MIME_FAMILIES.has(item as FileMIMEFamily))
    ? ([...new Set(value)] as FileMIMEFamily[])
    : [];
}

function personFileDirections(value: unknown): PersonFileDirection[] {
  if (
    !Array.isArray(value) ||
    value.length === 0 ||
    !value.every((item) => typeof item === 'string' && PERSON_FILE_DIRECTIONS.has(item as PersonFileDirection))
  )
    return ['from_person'];
  const selected = new Set(value as PersonFileDirection[]);
  return (['from_person', 'to_person', 'group'] as PersonFileDirection[]).filter((direction) =>
    selected.has(direction)
  );
}

function widths(value: unknown): Partial<Record<ExploreColumn, number>> {
  if (!isRecord(value)) return {};
  const result: Partial<Record<ExploreColumn, number>> = {};
  for (const [key, width] of Object.entries(value)) {
    if (COLUMNS.has(key) && typeof width === 'number' && Number.isFinite(width) && width > 0) {
      result[key as ExploreColumn] = width;
    }
  }
  return result;
}

function scrollAnchor(value: unknown): ExploreScrollAnchor | null {
  if (value === null) return null;
  return isRecord(value) && typeof value.key === 'string' && typeof value.offset === 'number'
    ? { key: value.key, offset: value.offset }
    : null;
}

function selectedRow(value: unknown): string | null {
  if (value === null) return null;
  if (typeof value !== 'string') return null;
  if (!value.startsWith('attachment:')) return value;
  return parseAttachmentSelection(value) === undefined ? null : value;
}

function relationshipTargetValue(value: unknown): string | null {
  return typeof value === 'string' && (/^cluster:\d+$/.test(value) || /^domain:\S+$/.test(value)) ? value : null;
}

function directoryPersonID(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : null;
}

function personTab(value: unknown): PersonTab {
  return PERSON_TABS.includes(value as PersonTab) ? (value as PersonTab) : 'overview';
}

function settingsSection(value: unknown): string {
  return typeof value === 'string' && /^[a-z0-9_-]{1,64}$/.test(value) ? value : '';
}

function legacyRelationshipTarget(analysisTarget: string | null, facet: RelationshipFacet): string | null {
  if (analysisTarget === null) return null;
  if (facet === 'people' && analysisTarget.startsWith('person:')) {
    return relationshipTargetValue(`cluster:${analysisTarget.slice('person:'.length)}`);
  }
  if (facet === 'domains' && analysisTarget.startsWith('domain:')) {
    return relationshipTargetValue(analysisTarget);
  }
  return null;
}

function normalize(value: unknown): ExploreURLState {
  if (!isRecord(value)) return freshDefaults();
  const {
    selection: _selection,
    bulkSelection: _bulkSelection,
    operationCursor: _operationCursor,
    ...knownAndFuture
  } = value;
  const searchMode =
    value.searchMode === 'full_text' || value.searchMode === 'semantic' || value.searchMode === 'hybrid'
      ? value.searchMode
      : defaultExploreURLState.searchMode;
  const presentation =
    value.presentation === 'table' || value.presentation === 'timeline' || value.presentation === 'files'
      ? value.presentation
      : defaultExploreURLState.presentation;
  const legacyFacet: RelationshipFacet | undefined =
    value.workspace === 'people' ? 'people' : value.workspace === 'domains' ? 'domains' : undefined;
  // Legacy People and Domains workspaces were Relationships facets.
  const workspace = legacyFacet
    ? 'relationships'
    : value.workspace === 'everything' ||
        value.workspace === 'directory' ||
        value.workspace === 'directory_review' ||
        value.workspace === 'settings' ||
        value.workspace === 'files' ||
        value.workspace === 'relationships' ||
        value.workspace === 'saved_views' ||
        value.workspace === 'sources' ||
        value.workspace === 'deletions' ||
        value.workspace === 'operations' ||
        value.workspace === 'meetings' ||
        (value.workspace === 'message' && directoryPersonID(value.messageID) !== null)
      ? value.workspace
      : DEFAULT_WORKSPACE;
  const analysisTarget =
    typeof value.analysisTarget === 'string' &&
    (/^person:[1-9][0-9]*$/.test(value.analysisTarget) || /^domain:[a-z0-9.-]+$/.test(value.analysisTarget))
      ? value.analysisTarget
      : null;
  const relationshipFacet: RelationshipFacet =
    legacyFacet ??
    (value.relationshipFacet === 'people' || value.relationshipFacet === 'domains'
      ? value.relationshipFacet
      : 'people');
  const relationshipTarget = legacyFacet
    ? legacyRelationshipTarget(analysisTarget, legacyFacet)
    : relationshipTargetValue(value.relationshipTarget);
  const reviewKind: DirectoryReviewKind =
    value.reviewKind === 'fact' ||
    value.reviewKind === 'relationship' ||
    value.reviewKind === 'enrichment' ||
    value.reviewKind === 'organization' ||
    value.reviewKind === 'correspondent'
      ? value.reviewKind
      : 'identity';
  const identityState: IdentityReviewState =
    value.identityState === 'conflict' || value.identityState === 'accepted' || value.identityState === 'rejected'
      ? value.identityState
      : 'candidate';
  const identityOrigin: IdentityReviewOrigin =
    value.identityOrigin === 'contact_match' || value.identityOrigin === 'person_duplicate'
      ? value.identityOrigin
      : 'all';
  const relationshipReviewState: RelationshipReviewState =
    value.relationshipReviewState === 'accepted' || value.relationshipReviewState === 'rejected'
      ? value.relationshipReviewState
      : 'pending';
  // The ranked contacts list became the People list's Not saved filter;
  // only domains and single contacts still open the relationships views.
  const peopleList = workspace === 'relationships' && relationshipFacet === 'people' && relationshipTarget === null;
  return {
    ...knownAndFuture,
    schemaVersion:
      value.schemaVersion === 1
        ? defaultExploreURLState.schemaVersion
        : typeof value.schemaVersion === 'number' && Number.isSafeInteger(value.schemaVersion)
          ? value.schemaVersion
          : defaultExploreURLState.schemaVersion,
    workspace: peopleList ? 'directory' : workspace,
    directoryQuery: typeof value.directoryQuery === 'string' ? value.directoryQuery : '',
    directoryContactState: typeof value.directoryContactState === 'string' ? value.directoryContactState : '',
    directoryCategory: typeof value.directoryCategory === 'string' ? value.directoryCategory : '',
    directoryOrganization: typeof value.directoryOrganization === 'string' ? value.directoryOrganization : '',
    directoryPrimaryChannel: typeof value.directoryPrimaryChannel === 'string' ? value.directoryPrimaryChannel : '',
    directoryLastContactAfter:
      typeof value.directoryLastContactAfter === 'string' ? value.directoryLastContactAfter : '',
    directoryLastContactBefore:
      typeof value.directoryLastContactBefore === 'string' ? value.directoryLastContactBefore : '',
    directorySort:
      value.directorySort === 'name' || value.directorySort === 'last_contact_asc'
        ? value.directorySort
        : 'last_contact_desc',
    directoryPersonID: peopleList ? null : directoryPersonID(value.directoryPersonID),
    directoryHasName: value.directoryHasName === true,
    peopleSaved: peopleList
      ? 'unsaved'
      : value.peopleSaved === 'saved' || value.peopleSaved === 'unsaved' || value.peopleSaved === 'not_people'
        ? value.peopleSaved
        : '',
    // A contact opened with its files pane (before person tabs) opens on Files.
    personTab:
      personTab(value.personTab) === 'overview' &&
      value.relationshipFiles === true &&
      relationshipTarget?.startsWith('cluster:')
        ? 'files'
        : personTab(value.personTab),
    reviewKind,
    identityState,
    identityOrigin,
    relationshipReviewState,
    query: typeof value.query === 'string' ? value.query : '',
    searchMode,
    filters: filters(value.filters),
    dateBoundsChosen: value.dateBoundsChosen === true,
    groupingChain: groups(value.groupingChain),
    presentation,
    sort: sorts(value.sort),
    fileSort: fileSort(value.fileSort),
    fileFilenameQuery:
      value.schemaVersion === 2 && typeof value.fileFilenameQuery === 'string' ? value.fileFilenameQuery : '',
    fileMIMEFamilies: value.schemaVersion === 2 ? fileMIMEFamilies(value.fileMIMEFamilies) : [],
    personFilePresentation: value.personFilePresentation === 'media' ? 'media' : 'files',
    personFileDirections: personFileDirections(value.personFileDirections),
    identityQuery: typeof value.identityQuery === 'string' ? value.identityQuery : '',
    identitySort:
      isRecord(value.identitySort) &&
      (value.identitySort.field === 'activity_count' ||
        value.identitySort.field === 'latest_at' ||
        value.identitySort.field === 'display_label') &&
      (value.identitySort.direction === 'asc' || value.identitySort.direction === 'desc')
        ? { field: value.identitySort.field, direction: value.identitySort.direction }
        : { field: 'activity_count', direction: 'desc' },
    analysisTarget,
    selectedIdentifier: typeof value.selectedIdentifier === 'string' ? value.selectedIdentifier : null,
    relationshipFacet,
    relationshipTarget,
    relationshipShowAll: value.relationshipShowAll === true,
    relationshipFiles: value.relationshipFiles === true,
    ...normalizeOperationURLState(value),
    settingsAuthority: normalizeSettingsNavigationAuthority(value.settingsAuthority),
    settingsSection: settingsSection(value.settingsSection),
    messageID: directoryPersonID(value.messageID),
    meetingID: directoryPersonID(value.meetingID),
    meetingPerson:
      typeof value.meetingPerson === 'string' && /^[1-9]\d*$/.test(value.meetingPerson) ? value.meetingPerson : '',
    meetingSource:
      typeof value.meetingSource === 'string' && /^[1-9]\d*$/.test(value.meetingSource) ? value.meetingSource : '',
    meetingSince: value.meetingSince === '90d' || value.meetingSince === 'all' ? value.meetingSince : '30d',
    columns: columns(value.columns),
    columnWidths: widths(value.columnWidths),
    activeRow: typeof value.activeRow === 'string' || value.activeRow === null ? value.activeRow : null,
    selectedRow: selectedRow(value.selectedRow),
    inspectorPinned: true,
    conversationAnchor:
      typeof value.conversationAnchor === 'string' || value.conversationAnchor === null
        ? value.conversationAnchor
        : null,
    scrollAnchor: scrollAnchor(value.scrollAnchor)
  } as ExploreURLState;
}

// Fields that only describe one workspace stay out of the link when another
// workspace is shared; browser history still carries them for Back/Forward.
const WORKSPACE_FIELDS: Partial<Record<keyof ExploreURLState, ReadonlyArray<ExploreWorkspace>>> = {
  directoryQuery: ['directory'],
  directoryContactState: ['directory'],
  directoryCategory: ['directory'],
  directoryOrganization: ['directory'],
  directoryPrimaryChannel: ['directory'],
  directoryLastContactAfter: ['directory'],
  directoryLastContactBefore: ['directory'],
  directorySort: ['directory'],
  directoryPersonID: ['directory', 'directory_review'],
  directoryHasName: ['directory'],
  peopleSaved: ['directory'],
  reviewKind: ['directory_review'],
  identityState: ['directory_review'],
  identityOrigin: ['directory_review'],
  relationshipReviewState: ['directory_review'],
  fileSort: ['files'],
  fileFilenameQuery: ['files'],
  fileMIMEFamilies: ['files'],
  personFilePresentation: ['relationships'],
  personFileDirections: ['relationships'],
  identityQuery: ['relationships'],
  identitySort: ['relationships'],
  analysisTarget: ['relationships'],
  selectedIdentifier: ['relationships'],
  relationshipFacet: ['relationships'],
  relationshipTarget: ['relationships'],
  relationshipShowAll: ['relationships'],
  relationshipFiles: ['relationships'],
  operationLane: ['operations'],
  operationKind: ['operations'],
  operationState: ['operations'],
  operationStartedFrom: ['operations'],
  operationStartedBefore: ['operations'],
  operationRunID: ['operations'],
  operationStatus: ['operations'],
  settingsAuthority: ['settings'],
  settingsSection: ['settings'],
  personTab: ['directory', 'relationships'],
  messageID: ['message'],
  meetingID: ['meetings'],
  meetingPerson: ['meetings'],
  meetingSource: ['meetings'],
  meetingSince: ['meetings'],
  dateBoundsChosen: ['everything']
};
const ARCHIVE_PREDICATE_FIELDS = new Set<keyof ExploreURLState>(['filters', 'groupingChain', 'presentation', 'sort']);
const FILTERLESS_WORKSPACES = new Set<ExploreWorkspace>([
  'directory',
  'directory_review',
  'settings',
  'message',
  'saved_views',
  'meetings'
]);
// Keyboard focus and scroll position live only in browser history.
const SESSION_ONLY_FIELDS = new Set<keyof ExploreURLState>(['activeRow', 'scrollAnchor']);

function sharedDetails(state: ExploreURLState, routesDateBounds: boolean): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(state).flatMap(([key, value]) => {
      const field = key as keyof ExploreURLState;
      if (field === 'schemaVersion') return [];
      if (isRoutedField(state.workspace, field)) return [];
      // Archive filters shape Inbox, Files, and contact timelines; a person,
      // review, settings, or message link does not carry them.
      if (ARCHIVE_PREDICATE_FIELDS.has(field) && FILTERLESS_WORKSPACES.has(state.workspace)) return [];
      if (SESSION_ONLY_FIELDS.has(field)) return [];
      const owners = WORKSPACE_FIELDS[field];
      if (owners && !owners.includes(state.workspace)) return [];
      // Date bounds the path already names as readable parameters.
      const shared = field === 'filters' && routesDateBounds ? withoutDateRange(state.filters) : value;
      return JSON.stringify(shared) === JSON.stringify(defaultExploreURLState[field]) ? [] : [[key, shared]];
    })
  );
}

/**
 * The address for a state: a readable path (`/inbox`, `/people/42`,
 * `/activity/operations`), readable parameters where people type or share
 * (`q`, `mode`, `since`), and the `explore` JSON for the rest. Parameters
 * the router does not own (feature flags) are kept from `baseSearch`.
 */
export function serializeExploreURLState(state: ExploreURLState, baseSearch = ''): string {
  const parameters = new URLSearchParams(baseSearch.startsWith('?') ? baseSearch.slice(1) : baseSearch);
  for (const name of ROUTE_PARAMETERS) parameters.delete(name);
  const normalized = normalize(state);
  const route = routeForState(normalized);
  const routed = new URLSearchParams(route.parameters);
  // An explicit mode keeps a shared link independent of browser preferences.
  if (normalized.workspace !== 'everything' && normalized.query.trim()) routed.set('mode', normalized.searchMode);
  for (const [name, value] of parameters) routed.append(name, value);
  const details = sharedDetails(normalized, route.routesDateBounds);
  if (Object.keys(details).length > 0) {
    routed.set(STATE_PARAMETER, JSON.stringify({ schemaVersion: normalized.schemaVersion, ...details }));
  }
  const search = routed.toString();
  return `${route.pathname}${search ? `?${search}` : ''}`;
}

/**
 * Reads an address. A path the router owns names the surface; the root
 * path (and any path the app does not own) falls back to the legacy
 * `?workspace=` parameter and the workspace inside the JSON payload, so
 * bookmarks from before readable paths keep opening the same view.
 */
export function parseExploreURLState(address: string, pathname = '/'): ExploreURLState {
  // Accepts a search string with its pathname, or a whole address such as
  // serializeExploreURLState returns.
  let search = address;
  if (address.startsWith('/')) {
    const url = new URL(address, 'http://msgvault.invalid');
    pathname = url.pathname;
    search = url.search;
  }
  const parameters = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  const encoded = parameters.get(STATE_PARAMETER);
  let details: unknown = {};
  try {
    if (encoded !== null) details = JSON.parse(encoded);
  } catch {
    // A malformed detail payload must not discard the selected workspace.
  }
  const payload = isRecord(details) ? details : {};
  const route = stateFromRoute(pathname, parameters);
  if (!route) {
    return normalize({
      ...payload,
      ...(parameters.has('workspace') ? { workspace: parameters.get('workspace') } : {}),
      ...(parameters.has('mode') ? { searchMode: parameters.get('mode') } : {})
    });
  }
  const { dateFilters, ...routeFields } = route as Record<string, unknown> & { dateFilters?: ExploreFilter[] };
  const payloadFilters = Array.isArray(payload.filters) ? (payload.filters as ExploreFilter[]) : [];
  return normalize({
    ...payload,
    ...(parameters.has('mode') ? { searchMode: parameters.get('mode') } : {}),
    ...routeFields,
    ...(dateFilters ? { filters: withRoutedDateBounds(payloadFilters, dateFilters) } : {})
  });
}

/** True for an address that names no view: the bare root with no legacy
 * workspace or payload, or a path the app does not own. */
export function isDefaultLanding(pathname: string, search: string): boolean {
  const parameters = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  return (
    stateFromRoute(pathname, parameters) === undefined &&
    !parameters.has('workspace') &&
    !parameters.has(STATE_PARAMETER)
  );
}
export {
  isRecord as isStateRecord,
  freshDefaults as freshExploreURLState,
  normalize as normalizeExploreURLState,
  filters as normalizeExploreFilters,
  selectedRow as normalizeSelectedRow,
  scrollAnchor as normalizeScrollAnchor,
  operationRunID as normalizeOperationRunID
};
