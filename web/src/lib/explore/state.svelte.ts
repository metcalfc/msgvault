import { SvelteSet } from 'svelte/reactivity';
import type {
  AllMatchingExploreSelection,
  ExploreFilter,
  ExploreGroupDimension,
  ExplorePredicate,
  ExploreSearchMode,
  ExploreSelection,
  ExploreURLState,
  ExploreWorkspace
} from './models';
import { isGroupingDimension } from '../grouping/catalog';
import { hasValidSearchAuthority, predicateFingerprint } from './selection';
import {
  ARCHIVE_MEETING_HISTORY_KEY,
  parseArchiveMeetingHistory,
  type ArchiveMeetingHistory
} from '../meetings/archive-selection';
import {
  availableSearchModeStorage,
  explicitSearchModeFromURL,
  rememberSearchMode,
  resolveInitialSearchMode,
  type SearchModeStorage
} from '../search/modes';
import { effectiveSearchMode } from '../search/query';
import { defaultEverythingFilters, isDateDimension } from './date-range';

import {
  freshExploreURLState as freshDefaults,
  normalizeExploreURLState as normalize,
  normalizeExploreFilters as filters,
  normalizeSelectedRow as selectedRow,
  normalizeScrollAnchor as scrollAnchor,
  normalizeOperationRunID as operationRunID,
  isStateRecord as isRecord,
  parseExploreURLState,
  serializeExploreURLState,
  isDefaultLanding
} from './url-codec';
// Keep existing consumers stable while pure bookmark codecs live independently
// of Svelte state, browser preferences, and history ownership.
export { defaultExploreURLState, parseExploreURLState, serializeExploreURLState, isDefaultLanding } from './url-codec';

const TRANSIENT_HISTORY_FIELDS = [
  'columns',
  'columnWidths',
  'activeRow',
  'scrollAnchor'
] as const satisfies ReadonlyArray<keyof ExploreURLState>;
const RESTORATION_INVALIDATING_FIELDS = new Set<keyof ExploreURLState>([
  'workspace',
  'directoryQuery',
  'directoryContactState',
  'directoryCategory',
  'directoryOrganization',
  'directoryPrimaryChannel',
  'directoryLastContactAfter',
  'directoryLastContactBefore',
  'directorySort',
  'directoryPersonID',
  'directoryHasName',
  'peopleSaved',
  'personTab',
  'reviewKind',
  'identityState',
  'identityOrigin',
  'relationshipReviewState',
  'query',
  'searchMode',
  'filters',
  'groupingChain',
  'presentation',
  'sort',
  'fileSort',
  'fileFilenameQuery',
  'fileMIMEFamilies',
  'personFilePresentation',
  'personFileDirections',
  'identityQuery',
  'identitySort',
  'analysisTarget',
  'selectedIdentifier',
  'relationshipFacet',
  'relationshipTarget',
  'operationLane',
  'operationKind',
  'operationState',
  'operationStartedFrom',
  'operationStartedBefore',
  'operationStatus',
  'settingsAuthority',
  'settingsSection',
  'messageID',
  'meetingID',
  'meetingPerson',
  'meetingSource',
  'meetingSince'
]);
const NORMALIZED_VIEW_FIELDS = [
  'workspace',
  'directoryPersonID',
  'peopleSaved',
  'personTab'
] as const satisfies ReadonlyArray<keyof ExploreURLState>;
const OPERATION_FILTER_FIELDS = [
  'operationLane',
  'operationKind',
  'operationState',
  'operationStartedFrom',
  'operationStartedBefore'
] as const satisfies ReadonlyArray<keyof ExploreURLState>;

interface ExploreWindow {
  location: Pick<Location, 'href' | 'pathname' | 'search' | 'hash'>;
  history: Pick<History, 'state' | 'pushState' | 'replaceState'>;
  addEventListener(type: 'popstate', listener: () => void): void;
  removeEventListener(type: 'popstate', listener: () => void): void;
}

/** The date bounds among a filter list, as a comparable key. */
function dateBoundsKey(filters: readonly ExploreFilter[]): string {
  return JSON.stringify(filters.filter((filter) => isDateDimension(filter.dimension)));
}

const HISTORY_DEPTH_KEY = 'exploreDepth';
/** Marks a history entry the app wrote while canonicalizing the address on
 * load, which records the address rather than a view the user chose. */
const HISTORY_CANONICAL_KEY = 'exploreCanonical';

function historyEntry(
  url: string,
  state: ExploreURLState,
  depth: number
): { exploreSearch: string; exploreState: unknown; [HISTORY_DEPTH_KEY]: number } {
  // History entries must be structured-cloneable, so strip reactive proxies.
  return { exploreSearch: url, exploreState: JSON.parse(JSON.stringify(state)), [HISTORY_DEPTH_KEY]: depth };
}

export class ExploreState {
  current = $state<ExploreURLState>(freshDefaults());
  restorationEpoch = $state(1);
  /** The page opened on an address that names no view (a bare `/`), so the
   * shell may step down from an unavailable default surface. */
  readonly arrivedAtDefault: boolean;
  private readonly browser: ExploreWindow;
  private readonly preferenceStorage: SearchModeStorage | null;
  private configuredDefaultSearchMode: ExploreSearchMode | undefined;
  // Keep user authority separate from modes added by URL canonicalization.
  private explicitSearchMode: ExploreSearchMode | undefined;
  private committed: ExploreURLState;
  private pendingRestorationEpoch = $state<number | undefined>(1);
  private pendingSearchPriorFocus?: Pick<ExploreURLState, 'activeRow' | 'scrollAnchor'>;
  // Everything opens on the last seven days once per session: the first
  // entry without an explicit date bound gets the default; "All time" and
  // hand-set bounds are then the user's and are never overwritten.
  private everythingDefaultApplied = false;
  private readonly handlePopState = (): void => {
    this.current = this.readURLState();
    this.explicitSearchMode = this.current.searchMode;
    this.committed = normalize(this.current);
    this.pendingSearchPriorFocus = undefined;
    this.restorationEpoch += 1;
    this.pendingRestorationEpoch = this.restorationEpoch;
  };

  constructor(
    browser: ExploreWindow = window,
    preferenceStorage: SearchModeStorage | null = browser === globalThis.window ? availableSearchModeStorage() : null
  ) {
    this.browser = browser;
    this.preferenceStorage = preferenceStorage;
    this.explicitSearchMode = explicitSearchModeFromURL(browser.location.search);
    this.arrivedAtDefault = isDefaultLanding(browser.location.pathname, browser.location.search);
    this.current = this.readURLState();
    // A shared or restored URL is the user's view, bounds and all; the
    // dateBoundsChosen marker says so even for an "All time" bookmark whose
    // empty filters list the serializer omits.
    this.everythingDefaultApplied = this.hasExplicitState();
    if (this.current.workspace === 'everything') {
      this.current = { ...this.current, ...this.everythingBoundsPatch(this.current.filters) };
    }
    this.committed = normalize(this.current);
    this.canonicalizeAddress();
    browser.addEventListener('popstate', this.handlePopState);
  }

  /** Rewrites a legacy (`/?workspace=…&explore=…`) or partial address to
   * the readable path for the same view, in place: the old link keeps
   * working and Back does not return to the legacy form. */
  private canonicalizeAddress(): void {
    const location = this.browser.location;
    const url = serializeExploreURLState(this.current, location.search);
    if (url === `${location.pathname}${location.search}`) return;
    const state = isRecord(this.browser.history.state) ? this.browser.history.state : {};
    this.browser.history.replaceState(
      { ...state, ...historyEntry(url, this.current, this.historyDepth()), [HISTORY_CANONICAL_KEY]: true },
      '',
      `${url}${location.hash}`
    );
  }

  /** How many in-app entries precede the current one: 0 on a page opened
   * from a link or bookmark, so Back buttons know whether Back stays in
   * the app. */
  private historyDepth(): number {
    const state = this.browser.history.state;
    const depth = isRecord(state) ? state[HISTORY_DEPTH_KEY] : undefined;
    return typeof depth === 'number' && Number.isSafeInteger(depth) && depth > 0 ? depth : 0;
  }

  /** True when browser Back returns to an earlier view of this app. */
  canGoBack(): boolean {
    return this.historyDepth() > 0;
  }

  /** The user's own Everything view, to which no default bounds are added:
   * a restored history entry the user produced (not one the app wrote while
   * canonicalizing the address), date bounds in the filters, or the
   * dateBoundsChosen marker (which every app-generated Everything link
   * without bounds carries, see serializeExploreURLState). A Directory or
   * Operations deep link is not an Everything view, and the app's
   * always-emitted ?workspace= and ?mode= shorthand on its own is not
   * explicit either, so the seven-day default still applies on the first
   * entry into Everything from those. */
  private hasExplicitState(): boolean {
    const history = this.browser.history.state;
    if (isRecord(history) && isRecord(history.exploreState) && history[HISTORY_CANONICAL_KEY] !== true) return true;
    return this.current.dateBoundsChosen || this.current.filters.some((filter) => isDateDimension(filter.dimension));
  }

  /** The seven-day default, applied once per session to an Everything view
   * without date bounds, and the marker that records the bounds as the
   * user's from then on. */
  private everythingBoundsPatch(filters: ExploreFilter[]): Partial<ExploreURLState> {
    if (this.everythingDefaultApplied) return {};
    this.everythingDefaultApplied = true;
    const bounded = filters.some((filter) => isDateDimension(filter.dimension))
      ? filters
      : [...filters, ...defaultEverythingFilters()];
    return { filters: bounded, dateBoundsChosen: true };
  }

  // Configuration arrives after URL canonicalization, which may already
  // have written a provisional mode. Only an incoming URL, a restored view,
  // or a user mode choice overrides the configured default.
  setConfiguredDefaultSearchMode(mode: ExploreSearchMode | undefined): void {
    this.configuredDefaultSearchMode = mode;
    const resolved = resolveInitialSearchMode(this.explicitSearchMode, this.preferenceStorage, mode);
    if (resolved === this.current.searchMode) return;
    this.current.searchMode = resolved;
    this.committed = normalize({ ...this.committed, searchMode: resolved });
    const location = this.browser.location;
    const address = serializeExploreURLState(this.current, location.search);
    const history = isRecord(this.browser.history.state) ? this.browser.history.state : {};
    this.browser.history.replaceState(
      { ...history, ...historyEntry(address, this.current, this.historyDepth()) },
      '',
      `${address}${location.hash}`
    );
  }

  replaceTransient(patch: Partial<ExploreURLState>): void {
    this.navigate(patch, 'replace');
  }

  replaceCommittedNavigation(patch: Partial<ExploreURLState>): void {
    this.navigate(patch, 'replace');
    this.committed = normalize(this.current);
    this.pendingSearchPriorFocus = undefined;
  }

  replaceCommittedDraft(patch: Partial<ExploreURLState>): void {
    const priorCommitted = this.committed;
    this.navigate(patch, 'replace');
    this.committed = normalize({ ...priorCommitted, ...patch });
  }

  peekRestorationEpoch(): number | undefined {
    return this.pendingRestorationEpoch;
  }

  acknowledgeRestoration(epoch: number): void {
    if (this.pendingRestorationEpoch === epoch) this.pendingRestorationEpoch = undefined;
  }

  replaceSearchDraft(query: string, searchMode: ExploreSearchMode): void {
    rememberSearchMode(searchMode, this.preferenceStorage);
    this.pendingSearchPriorFocus ??= {
      activeRow: this.current.activeRow,
      scrollAnchor: this.current.scrollAnchor ? { ...this.current.scrollAnchor } : null
    };
    this.navigate({ query, searchMode, activeRow: null, scrollAnchor: null }, 'replace');
  }

  /** Commits a search. `filters`, when given, replaces the filters in the
   * same history entry (operators moved out of the query into chips). */
  commitSearch(query: string, searchMode: ExploreSearchMode, filters?: ExploreFilter[]): void {
    rememberSearchMode(searchMode, this.preferenceStorage);
    this.navigate(
      {
        query,
        searchMode,
        ...(filters ? { filters } : {}),
        selectedRow: null,
        conversationAnchor: null,
        activeRow: null,
        scrollAnchor: null
      },
      'push'
    );
  }

  /** Opens a workspace. `patch` applies in the same history entry, after
   * the workspace resets (Search opens the Inbox view with its query). */
  commitWorkspace(workspace: ExploreWorkspace, patch: Partial<ExploreURLState> = {}): void {
    this.navigate(
      {
        workspace,
        ...(workspace === 'everything' ? this.everythingBoundsPatch(this.current.filters) : {}),
        // The Relationships ranking and cluster-timeline endpoints accept no
        // text query (ranking is over reciprocity signals, not lexical), so a
        // carried search query could only ever half-apply (domains and files
        // yes, ranked people and cluster timeline no). Entering the hub drops
        // the carried query — visibly, in the URL state — so every surface
        // consistently reflects no text filter.
        ...(workspace === 'relationships' ? { query: '' } : {}),
        analysisTarget: null,
        selectedIdentifier: null,
        activeRow: null,
        selectedRow: null,
        conversationAnchor: null,
        scrollAnchor: null,
        operationStatus: '',
        settingsAuthority: '',
        ...patch
      },
      'push'
    );
  }

  commitNavigation(patch: Partial<ExploreURLState>): void {
    const selectionChanged = 'selectedRow' in patch && patch.selectedRow !== this.current.selectedRow;
    this.navigate(
      selectionChanged && !('conversationAnchor' in patch) ? { ...patch, conversationAnchor: null } : patch,
      'push'
    );
  }

  commitRestorableNavigation(patch: Partial<ExploreURLState>): void {
    this.navigate(patch, 'push');
    this.restorationEpoch += 1;
    this.pendingRestorationEpoch = this.restorationEpoch;
  }

  replaceCommittedRestorableNavigation(patch: Partial<ExploreURLState>): void {
    this.navigate(patch, 'replace');
    this.committed = normalize(this.current);
    this.pendingSearchPriorFocus = undefined;
    this.restorationEpoch += 1;
    this.pendingRestorationEpoch = this.restorationEpoch;
  }

  commitGrouping(dimension: ExploreGroupDimension): void {
    if (!isGroupingDimension(dimension) || this.current.groupingChain.includes(dimension)) return;
    this.navigate(
      {
        groupingChain: [...this.current.groupingChain, dimension],
        activeRow: null,
        scrollAnchor: null
      },
      'push'
    );
  }

  commitUngroup(): void {
    if (this.current.groupingChain.length === 0) return;
    this.navigate(
      {
        groupingChain: this.current.groupingChain.slice(0, -1),
        activeRow: null,
        scrollAnchor: null
      },
      'push'
    );
  }

  predicate(): ExplorePredicate {
    const query = this.current.query.trim();
    return {
      // A filter-only query cannot be embedded, so it runs as full text
      // while the chosen mode stays selected for the next query.
      ...(query ? { query, search_mode: effectiveSearchMode(query, this.current.searchMode) } : {}),
      filters: this.current.filters,
      grouping: this.current.groupingChain,
      presentation: this.current.presentation,
      sort: this.current.sort,
      limit: 500
    };
  }

  destroy(): void {
    this.browser.removeEventListener('popstate', this.handlePopState);
  }

  private readURLState(): ExploreURLState {
    const history = this.browser.history.state;
    const { pathname, search } = this.browser.location;
    // A history entry this state wrote is the exact view, mode included.
    if (isRecord(history) && history.exploreSearch === `${pathname}${search}` && isRecord(history.exploreState)) {
      return normalize(history.exploreState);
    }
    const parsed = parseExploreURLState(search, pathname);
    parsed.searchMode = resolveInitialSearchMode(
      explicitSearchModeFromURL(this.browser.location.search),
      this.preferenceStorage,
      this.configuredDefaultSearchMode
    );
    return parsed;
  }

  private navigate(patch: Partial<ExploreURLState>, mode: 'push' | 'replace'): void {
    if (patch.searchMode !== undefined) this.explicitSearchMode = patch.searchMode;
    let effectivePatch = patch;
    // A change to Everything's date bounds (after/before added, removed, or
    // changed — including "All time") is the user's choice, so it is never
    // overwritten by the default and survives a bookmark. Filter edits in
    // other workspaces, and non-date filters, leave the default in place.
    if (
      patch.filters &&
      (patch.workspace ?? this.current.workspace) === 'everything' &&
      dateBoundsKey(normalize({ ...this.current, filters: patch.filters }).filters) !==
        dateBoundsKey(this.current.filters)
    ) {
      effectivePatch = { ...effectivePatch, dateBoundsChosen: true };
      this.everythingDefaultApplied = true;
    }
    // Overrides compose: a patch that both chooses bounds and changes an
    // operation filter keeps the marker and resets the run.
    if (
      mode === 'push' &&
      OPERATION_FILTER_FIELDS.some(
        (key) => key in patch && normalize({ ...this.current, ...patch })[key] !== this.current[key]
      )
    ) {
      effectivePatch = { ...effectivePatch, operationRunID: null };
    }
    if (
      mode === 'push' ||
      Object.keys(effectivePatch).some((key) => RESTORATION_INVALIDATING_FIELDS.has(key as keyof ExploreURLState))
    ) {
      this.pendingRestorationEpoch = undefined;
    }
    const baseSearch = this.browser.location.search;
    if (mode === 'push') {
      const priorFocus = this.pendingSearchPriorFocus;
      const transient = Object.fromEntries(
        TRANSIENT_HISTORY_FIELDS.filter((key) => !priorFocus || (key !== 'activeRow' && key !== 'scrollAnchor')).map(
          (key) => [key, this.current[key]]
        )
      ) as Partial<ExploreURLState>;
      const priorEntry = normalize({ ...this.committed, ...transient, ...priorFocus });
      const priorURL = serializeExploreURLState(priorEntry, baseSearch);
      const committedURL = `${priorURL}${this.browser.location.hash}`;
      this.browser.history.replaceState(
        {
          ...historyEntry(priorURL, priorEntry, this.historyDepth()),
          ...this.archiveHistoryState(priorEntry.selectedRow)
        },
        '',
        committedURL
      );
    }
    const next = normalize({ ...this.current, ...effectivePatch });
    // Preserve per-field reactivity: transient scroll/column changes must not
    // invalidate consumers that only read the canonical server predicate.
    const patchKeys = Object.keys(effectivePatch);
    // Normalizing can move a view (a contact list with no contact is the
    // People list), so those fields apply even when the patch omits them.
    const normalizedKeys = NORMALIZED_VIEW_FIELDS.filter((key) => next[key] !== this.current[key]);
    const keysToApply = OPERATION_FILTER_FIELDS.some((key) => key in effectivePatch)
      ? [...new Set([...patchKeys, ...normalizedKeys, ...OPERATION_FILTER_FIELDS, 'operationRunID'])]
      : [...new Set([...patchKeys, ...normalizedKeys])];
    for (const key of keysToApply) {
      if (key in next) this.current[key] = next[key];
    }
    const address = serializeExploreURLState(this.current, baseSearch);
    const url = `${address}${this.browser.location.hash}`;
    const depth = this.historyDepth();
    if (mode === 'push') {
      this.browser.history.pushState(historyEntry(address, this.current, depth + 1), '', url);
      this.committed = normalize(this.current);
      this.pendingSearchPriorFocus = undefined;
    } else
      this.browser.history.replaceState(
        { ...historyEntry(address, this.current, depth), ...this.archiveHistoryState(this.current.selectedRow) },
        '',
        url
      );
  }

  private archiveHistoryState(
    selectedRow: string | null
  ): { [ARCHIVE_MEETING_HISTORY_KEY]: ArchiveMeetingHistory } | null {
    const marker = parseArchiveMeetingHistory(this.browser.history.state, selectedRow);
    return marker ? { [ARCHIVE_MEETING_HISTORY_KEY]: marker } : null;
  }
}

export class ExploreSelectionState {
  mode = $state<'explicit' | 'all_matching'>('explicit');
  readonly explicitKeys = new SvelteSet<string>();
  readonly exclusions = new SvelteSet<string>();
  private allMatching?: Omit<AllMatchingExploreSelection, 'exclusions'>;
  private rangeAnchorKey: string | undefined;

  get count(): number {
    return this.mode === 'explicit' ? this.explicitKeys.size : 0;
  }

  isSelected(key: string): boolean {
    return this.mode === 'explicit' ? this.explicitKeys.has(key) : !this.exclusions.has(key);
  }

  selectedKeys(keys: string[]): string[] {
    return keys.filter((key) => this.isSelected(key));
  }

  toggle(key: string, index: number, orderedKeys: string[], range = false): void {
    if (this.mode === 'all_matching') {
      if (this.exclusions.has(key)) this.exclusions.delete(key);
      else this.exclusions.add(key);
      return;
    }
    const rangeAnchor = this.rangeAnchorKey ? orderedKeys.indexOf(this.rangeAnchorKey) : -1;
    if (range && rangeAnchor >= 0) {
      const start = Math.min(rangeAnchor, index);
      const end = Math.max(rangeAnchor, index);
      for (let cursor = start; cursor <= end; cursor += 1) {
        const next = orderedKeys[cursor];
        if (next !== undefined) this.explicitKeys.add(next);
      }
      return;
    }
    if (this.explicitKeys.has(key)) this.explicitKeys.delete(key);
    else this.explicitKeys.add(key);
    this.rangeAnchorKey = key;
  }

  selectVisible(keys: string[]): void {
    if (this.mode === 'all_matching') {
      for (const key of keys) this.exclusions.delete(key);
      return;
    }
    for (const key of keys) this.explicitKeys.add(key);
    if (keys.length > 0) this.rangeAnchorKey = keys[0];
  }

  selectAllMatching(selection: AllMatchingExploreSelection): void {
    if (selection.predicateFingerprint !== predicateFingerprint(selection.predicate)) {
      throw new Error('All-matching selection predicate fingerprint does not match');
    }
    if (selection.resultGeneration < 1) {
      throw new Error('All-matching selection requires a result generation');
    }
    if (
      (selection.predicate.search_mode === 'semantic' || selection.predicate.search_mode === 'hybrid') &&
      !selection.candidateSnapshotId
    ) {
      throw new Error('Semantic all-matching selection requires a server candidate snapshot');
    }
    if (!hasValidSearchAuthority(selection.predicate, selection.searchProvenance, selection.candidateSnapshotId)) {
      throw new Error('All-matching selection search provenance does not match its mode');
    }
    this.clear();
    this.mode = 'all_matching';
    const { exclusions, ...pinned } = selection;
    this.allMatching = pinned;
    for (const key of exclusions) this.exclusions.add(key);
  }

  clear(): void {
    this.mode = 'explicit';
    this.explicitKeys.clear();
    this.exclusions.clear();
    this.allMatching = undefined;
    this.rangeAnchorKey = undefined;
  }

  snapshot(): ExploreSelection {
    if (this.mode === 'all_matching' && this.allMatching) {
      return { ...this.allMatching, exclusions: [...this.exclusions] };
    }
    return { mode: 'explicit', rowKeys: [...this.explicitKeys] };
  }
}
