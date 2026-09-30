<script lang="ts">
  import { getCLIMessageRaw as generatedGetCLIMessageRaw } from '../../api/generated/api/api';
  import { preflightExploreSelection as generatedPreflightExploreSelection } from '../../api/generated/exploration/exploration';
  import {
    Button,
    CommandPalette,
    getThemeMode,
    Menu,
    MenuContent,
    MenuItem,
    MenuTrigger,
    SelectDropdown,
    setThemeMode,
    StatusDot,
    ThemeToggle,
    TopBar,
    appShortcuts,
    initShortcuts,
    type PaletteCommand,
  } from '@kenn-io/kit-ui';
  import { onDestroy, onMount, setContext, tick, type Snippet, untrack } from 'svelte';
  import type { APIClient } from '../../api/client';
  import type {
    MeetingRef,
    ExplorePreflightResponse as GeneratedExplorePreflightResponse,
    ExploreSelection as GeneratedExploreSelection,
  } from '../../api/generated/models';
  import type {
    EntryRow,
    ExploreColumn,
    ExploreFilter,
    ExploreGroupDimension,
    ExploreGroupRow,
    ExploreFileFact,
    ExploreSearchMode,
    OperationStatusAuthority,
    ExploreURLState,
    ExploreWorkspace,
    PersonSummary,
    FileViewerTarget,
    FileSearchSort,
  } from '../../explore/models';
  import { attachmentSelection, parseAttachmentSelection } from '../../explore/attachment-authority';
  import { filtersForGroup, parseGroupSelection } from '../../explore/group-context';
  import { ExploreLoader } from '../../explore/loader.svelte';
  import { GROUPING_CATALOG, groupingByDimension } from '../../grouping/catalog';
  import { canonicalFingerprint, predicateFingerprint } from '../../explore/selection';
  import { ExploreSelectionState, ExploreState } from '../../explore/state.svelte';
  import { RelationshipsController } from '../../relationships/controller.svelte';
  import { DirectoryController } from '../../directory/controller.svelte';
  import type { DirectoryPromotionResult } from '../../directory/models';
  import { DirectoryReviewController } from '../../directory/review-controller.svelte';
  import { RelationshipReviewController } from '../../directory/relationship-review-controller.svelte';
  import { FactLedgerController } from '../../directory/fact-ledger-controller.svelte';
  import { OperationsController } from '../../operations/controller.svelte';
  import type { OperationRunDetail, OperationsURLState } from '../../operations/models';
  import {
    settingsNavigationTarget as targetForSettingsAuthority,
    type CardDAVSettingsRequest,
    type SettingsNavigationAuthority,
    type SettingsNavigationTarget
  } from '../../carddav/navigation';
  import { createCommandRegistry, type AppCommand, type CommandHandlers } from '../../commands/registry';
  import {
    createAppearancePreferences,
    type AppearanceDefaults,
    type DensityPreference,
    type ThemePreference,
  } from '../../theme/preferences.svelte';
  import ContextBar from '../explore/ContextBar.svelte';
  import GroupTable from '../explore/GroupTable.svelte';
  import SavedViewsWorkspace from '../saved-views/SavedViewsWorkspace.svelte';
  import SourcesWorkspace from '../sources/SourcesWorkspace.svelte';
  import OperationsWorkspace from '../operations/OperationsWorkspace.svelte';
  import DeletionsWorkspace from '../deletions/DeletionsWorkspace.svelte';
  import FilesWorkspace from '../files/FilesWorkspace.svelte';
  import FileViewer from '../files/FileViewer.svelte';
  import RelationshipsWorkspace from '../relationships/RelationshipsWorkspace.svelte';
  import PeopleWorkspace from '../people/PeopleWorkspace.svelte';
  import MeetingsWorkspace from '../meetings/MeetingsWorkspace.svelte';
  import MeetingPage from '../meetings/MeetingPage.svelte';
  import SavedPersonPage from '../people/SavedPersonPage.svelte';
  import { PeopleHub, type PeopleFilters, type PeopleRow } from '../../people/hub.svelte';
  import type { PersonTab } from '../../routing/routes';
  import DirectoryReviewWorkspace from '../directory/DirectoryReviewWorkspace.svelte';
  import { PendingReviewsMonitor } from '../../directory/pending-reviews.svelte';
  import type { TopBarTab } from '@kenn-io/kit-ui';
  import KeyboardHelp from './KeyboardHelp.svelte';
  import MessagePage from '../reader/MessagePage.svelte';
  import HeaderSearch from './HeaderSearch.svelte';
  import SettingsIcon from '@lucide/svelte/icons/settings';
  import { routeTitle } from '../../routing/routes';
  import ArchivedMeetingReader from '../meetings/ArchivedMeetingReader.svelte';
  import { ArchiveMeetingNavigation, archiveMeetingSelection, parseArchiveMeetingSelection } from '../../meetings/archive-navigation.svelte';
  import { getMessage } from '../../api/generated/api/api';
  import type { MessageDetail } from '../../api/generated/models';
  import type { RelationshipSiblingCluster } from '../../relationships/models';
  import { messageEntryKey, messageRowFilters } from '../../explore/entry-key';
  import { ARCHIVE_MEETING_HISTORY_KEY, parseArchiveMeetingHistory } from '../../meetings/archive-selection';
  import EverythingWorkspace from './EverythingWorkspace.svelte';
  import { stepThread } from '../../reader/thread-stepper';
  import { EverythingSessionState } from './EverythingSessionState.svelte';
  import { bufferedCallback } from '../../util/buffered-callback';
  /** The settings category the address names, and how to change it. */
  interface SettingsSectionBinding {
    value: string;
    change: (section: string) => void;
    /** This browser's theme and density, shown in the Appearance category. */
    browserControls: Snippet;
  }
  interface Props {
    client: APIClient;
    state?: ExploreState;
    enabled?: boolean;
    settings?: Snippet<[
      CardDAVSettingsRequest | undefined,
      (key: number) => void,
      SettingsNavigationTarget | undefined,
      SettingsSectionBinding
    ]>;
    appearanceDefaults?: AppearanceDefaults;
    searchModeDefault?: ExploreSearchMode;
    /** The embedding endpoint is local (loopback); see EverythingWorkspace. */
    embeddingsLocal?: boolean;
    archiveContextKey?: string;
  }
  let {
    client,
    state: providedState = undefined,
    enabled = true,
    settings = undefined,
    appearanceDefaults = { theme: 'system', density: 'compact' },
    searchModeDefault = undefined,
    embeddingsLocal = false,
    archiveContextKey = '',
  }: Props = $props();
  const ownsState = untrack(() => providedState === undefined);
  const exploreState = untrack(() => providedState ?? new ExploreState());
  const ATTACHMENT_HISTORY_MARKER = 'msgvaultAttachmentViewer';
  const archivedMeeting = new ArchiveMeetingNavigation(untrack(() => client));
  let archiveReturnFocus: HTMLElement | undefined;
  let archiveReturnSelection = $state<string | null>(null);
  let archiveWasOpen = false;
  const archiveMeetingID = $derived(parseArchiveMeetingSelection(exploreState.current.selectedRow));
  const archiveNavigationFingerprint = $derived(canonicalFingerprint(exploreState.current));

  $effect(() => {
    void archiveNavigationFingerprint;
    const id = archiveMeetingID;
    untrack(() => {
      archivedMeeting.cancel();
      if (id !== undefined) {
        const history = parseArchiveMeetingHistory(window.history.state, exploreState.current.selectedRow);
        archiveReturnSelection = history?.returnSelectedRow ?? null;
        if (archivedMeeting.detail?.id !== id) void archivedMeeting.load(id);
      } else {
        archivedMeeting.error = '';
        if (archiveWasOpen) void restoreArchiveFocus();
      }
      archiveWasOpen = id !== undefined;
    });
  });
  const DEFAULT_SORT_NOTICE = 'Newest first is the canonical Inbox order.';
  const SEARCH_TYPING_DEBOUNCE_MS = 250;
  const debouncedSearchPatch = bufferedCallback((patch: Partial<ExploreURLState>) => {
    // Typing a search query is itself a user-initiated interaction, even
    // though it only ever writes committed *draft* state (never a
    // commit* wrapper) — see the disarming note below. Firing on its own
    // 250ms after the last keystroke, with no navigation ever committing,
    // must disarm the one-shot landing fallback exactly like beforeCommit
    // does; only beforeCommit's own explicit flush of THIS callback (a
    // navigation committing while the patch is still pending) reached it
    // before.
    exploreState.replaceCommittedDraft(patch);
  }, SEARCH_TYPING_DEBOUNCE_MS);
  // A pending debounced search patch (People/Domains identity search, Files
  // filename search) applies to whatever state is current when it eventually
  // fires. If a navigation commits while the patch is still pending, flushing
  // it first applies the typed text immediately so it is not silently
  // dropped; the navigation patch is then committed on top and wins for the
  // fields it touches. Every write to ExploreState funnels through these
  // wrappers so the pending patch is flushed before any navigation commits.
  function beforeCommit(): void {
    debouncedSearchPatch.flush();
  }
  // Transient replaces (active row, scroll anchor) are user navigation too:
  // apply any pending typed search patch first so it cannot fire later and
  // clobber the newer transient state with the null active row / scroll
  // anchor it snapshotted at typing time. Unlike beforeCommit this does not
  // disarm the landing fallback, because transient replaces also fire
  // programmatically (e.g. the first loaded row auto-activating); a flush of
  // a pending patch still disarms it inside the debounced callback itself.
  function replaceTransient(patch: Partial<ExploreURLState>): void {
    debouncedSearchPatch.flush();
    exploreState.replaceTransient(patch);
  }
  function commitNavigation(patch: Partial<ExploreURLState>): void {
    beforeCommit();
    exploreState.commitNavigation(patch);
  }
  function replaceCommittedNavigation(patch: Partial<ExploreURLState>): void {
    beforeCommit();
    exploreState.replaceCommittedNavigation(patch);
  }
  // Directory text filters are committed drafts like the Everything typed
  // search: they rewrite the current history entry instead of pushing one.
  function replaceCommittedDraft(patch: Partial<ExploreURLState>): void {
    beforeCommit();
    exploreState.replaceCommittedDraft(patch);
  }
  function commitRestorableNavigation(patch: Partial<ExploreURLState>): void {
    beforeCommit();
    exploreState.commitRestorableNavigation(patch);
  }
  function replaceCommittedRestorableNavigation(patch: Partial<ExploreURLState>): void {
    beforeCommit();
    exploreState.replaceCommittedRestorableNavigation(patch);
  }
  function commitWorkspace(workspace: ExploreWorkspace): void {
    beforeCommit();
    exploreState.commitWorkspace(workspace);
  }
  /** Promotes a Relationships participant and, on success, opens the new
   * durable person in Directory. Failures stay with the Relationships header
   * so the guidance appears next to the person it is about. */
  async function promoteRelationshipParticipant(participantID: number): Promise<DirectoryPromotionResult> {
    const context = relationshipsController.personMergeContextSnapshot();
    const result = await directoryController.promote(participantID);
    if (result.ok) {
      // The hub skips reopening an unchanged target on re-entry, so refresh
      // its detail now or the header would still offer promotion for a person
      // who just gained a profile.
      void relationshipsController.reconcilePersonMerge(context);
      openDirectoryPerson(result.personID);
    }
    return result;
  }
  function openDirectoryPerson(personID: number): void {
    beforeCommit();
    // Opening the person already on screen (a merge or split handoff)
    // re-reads them: their bindings or history may have changed.
    if (exploreState.current.workspace === 'directory' && exploreState.current.directoryPersonID === personID) {
      if (exploreState.current.personTab !== 'overview') exploreState.commitNavigation({ personTab: 'overview' });
      void directoryController.reloadSelection();
      return;
    }
    exploreState.commitNavigation({ workspace: 'directory', directoryPersonID: personID, personTab: 'overview' });
  }
  /** Opens the People list, keeping its filters. */
  /** Opens the People list, keeping its filters. From a person page this
   * leaves the person (like Meetings leaves a meeting); on the list itself
   * it adds no history entry. */
  function openPeopleList(): void {
    beforeCommit();
    if (exploreState.current.workspace === 'directory' && exploreState.current.directoryPersonID === null) return;
    exploreState.commitWorkspace('directory', { directoryPersonID: null, relationshipTarget: null, personTab: 'overview' });
  }
  function openPeopleRow(row: PeopleRow): void {
    if (row.kind === 'saved') openDirectoryPerson(row.id);
    else openRelationship(row.id);
  }
  function changePersonTab(personTab: PersonTab): void {
    commitNavigation({ personTab });
  }
  const peopleFilters = $derived<PeopleFilters>({
    query: exploreState.current.directoryQuery,
    saved: exploreState.current.peopleSaved,
    hasName: exploreState.current.directoryHasName,
    category: exploreState.current.directoryCategory,
    organization: exploreState.current.directoryOrganization,
  });
  function changePeopleFilters(patch: Partial<PeopleFilters>, history: 'push' | 'replace'): void {
    const statePatch: Partial<ExploreURLState> = {
      ...('query' in patch ? { directoryQuery: patch.query } : {}),
      ...('saved' in patch ? { peopleSaved: patch.saved } : {}),
      ...('hasName' in patch ? { directoryHasName: patch.hasName } : {}),
      ...('category' in patch ? { directoryCategory: patch.category } : {}),
      ...('organization' in patch ? { directoryOrganization: patch.organization } : {}),
    };
    if (history === 'replace') replaceCommittedDraft(statePatch);
    else commitNavigation(statePatch);
  }
  function announceOperation(message: string): void {
    operationAnnouncement = { key: ++operationAnnouncementKey, message };
  }
  function openCardDAVConflict(conflictID: number): void {
    if (!Number.isSafeInteger(conflictID) || conflictID <= 0) return;
    cardDAVSettingsRequest = { conflictID, key: ++cardDAVSettingsRequestKey };
    announceOperation(`Opening CardDAV conflict ${conflictID} in Settings.`);
    commitWorkspace('settings');
  }
  function openCardDAVSettings(): void {
    cardDAVSettingsRequest = { key: ++cardDAVSettingsRequestKey };
    announceOperation('Opening CardDAV settings.');
    commitWorkspace('settings');
  }
  function openOperations(
    operationLane: OperationsURLState['operationLane'],
    operationKind: OperationsURLState['operationKind']
  ): void {
    announceOperation('Opening filtered operation history.');
    commitNavigation({
      workspace: 'operations',
      operationLane,
      operationKind,
      operationState: '',
      operationStartedFrom: '',
      operationStartedBefore: '',
      operationRunID: null,
      operationStatus: ''
    });
  }

  type RelatedOperationStatus = NonNullable<OperationRunDetail['related_status']>;

  function openOperationAuthority(target: RelatedOperationStatus): void {
    if (target === 'listSourceStatus') {
      announceOperation('Opening source status.');
      commitWorkspace('sources');
      return;
    }
    if (target === 'getCardDAVStatus') {
      openCardDAVSettings();
      return;
    }
    const statusTargets: Record<
      Exclude<RelatedOperationStatus, 'listSourceStatus' | 'getCardDAVStatus'>,
      string
    > = {
      getDocumentIndexStatus: 'Opening live document index status.',
      getDocumentVectorStatus: 'Opening live document vector status.',
      getVisualAttachmentStatus: 'Opening live visual attachment status.'
    };
    announceOperation(statusTargets[target]);
    commitNavigation({ workspace: 'operations', operationStatus: target });
  }

  function openOperationConfiguration(target: OperationStatusAuthority): void {
    const settingsTargets: Record<OperationStatusAuthority, SettingsNavigationAuthority> = {
      getDocumentIndexStatus: 'document_index',
      getDocumentVectorStatus: 'document_vector',
      getVisualAttachmentStatus: 'visual_attachments'
    };
    commitNavigation({ workspace: 'settings', settingsAuthority: settingsTargets[target] });
  }

  setContext('msgvault:open-carddav-operations', () => openOperations('contacts', 'carddav_sync'));
  function consumeCardDAVSettingsRequest(key: number): void {
    if (cardDAVSettingsRequest?.key === key) cardDAVSettingsRequest = undefined;
  }
  function openWorkspaceTab(workspace: ExploreWorkspace): void {
    if (workspace === 'settings') cardDAVSettingsRequest = undefined;
    commitWorkspace(workspace);
  }
  function commitGrouping(dimension: ExploreGroupDimension): void {
    beforeCommit();
    exploreState.commitGrouping(dimension);
  }
  function commitUngroup(): void {
    beforeCommit();
    exploreState.commitUngroup();
  }
  function commitSearch(query: string, mode: ExploreSearchMode, filters: ExploreFilter[] | undefined = undefined): void {
    beforeCommit();
    exploreState.commitSearch(query, mode, filters);
  }
  const selection = new ExploreSelectionState();
  const appearance = createAppearancePreferences(untrack(() => appearanceDefaults));
  const relationshipsController = new RelationshipsController(
    untrack(() => client),
    () => Intl.DateTimeFormat().resolvedOptions().timeZone,
  );
  // Directory owns ephemeral request/page state while ExploreState remains
  // the browser-restorable source for its query, filters, and selection.
  // Keeping the commit callback at the shell boundary gives it the same
  // history treatment as every other workspace navigation.
  const directoryController = new DirectoryController(
    untrack(() => client),
    (patch, history) => (history === 'replace' ? replaceCommittedDraft(patch) : commitNavigation(patch)),
  );
  const directoryReviewController = new DirectoryReviewController(
    untrack(() => client),
    (patch) => commitNavigation(patch),
  );
  const relationshipReviewController = new RelationshipReviewController(
    untrack(() => client),
    (patch) => commitNavigation(patch),
  );
  const factLedgerController = new FactLedgerController(untrack(() => client));
  const peopleHub = new PeopleHub(untrack(() => client), directoryController);
  const operationsController = new OperationsController(
    untrack(() => client),
    (patch) => commitNavigation(patch)
  );
  let cardDAVSettingsRequest = $state<CardDAVSettingsRequest>();
  let cardDAVSettingsRequestKey = 0;
  const settingsNavigationTarget = $derived(exploreState.current.workspace === 'settings'
    ? targetForSettingsAuthority(exploreState.current.settingsAuthority)
    : undefined);
  let operationAnnouncementKey = 0;
  let operationAnnouncement = $state({ key: 0, message: '' });
  type APIExploreSelection = GeneratedExploreSelection;
  type ExplorePreflight = GeneratedExplorePreflightResponse;
  // Primary navigation: the places people go. Settings and Saved Views
  // live in the gear menu; Search is the header field. Reviews shows a dot,
  // not a count, when anything waits: the daemon answers that with one
  // indexed lookup per queue.
  type NavigationID = 'people' | 'inbox' | 'files' | 'meetings' | 'reviews' | 'activity';
  const pendingReviews = new PendingReviewsMonitor(untrack(() => client));
  const tabs = $derived<TopBarTab[]>([
    { id: 'people', label: 'People' },
    { id: 'inbox', label: 'Inbox' },
    { id: 'files', label: 'Files' },
    { id: 'meetings', label: 'Meetings' },
    {
      id: 'reviews',
      label: 'Reviews',
      ...(pendingReviews.waiting ? { indicator: { tone: 'info' as const, title: 'Items waiting' } } : {}),
    },
    { id: 'activity', label: 'Activity' },
  ]);
  const activitySections = [
    { id: 'sources', label: 'Sources' },
    { id: 'operations', label: 'Operations' },
    { id: 'deletions', label: 'Deletions' },
  ] as const;
  function navigationFor(workspace: ExploreWorkspace): NavigationID | '' {
    switch (workspace) {
      case 'directory':
      case 'relationships':
        return 'people';
      case 'everything':
        return 'inbox';
      case 'files':
        return 'files';
      case 'meetings':
        return 'meetings';
      case 'directory_review':
        return 'reviews';
      case 'sources':
      case 'operations':
      case 'deletions':
        return 'activity';
      default:
        return '';
    }
  }
  const activeNavigation = $derived(navigationFor(exploreState.current.workspace));
  function openNavigation(id: NavigationID): void {
    if (id === 'people') openPeopleList();
    else if (id === 'inbox') openInbox();
    else if (id === 'files') openWorkspaceTab('files');
    else if (id === 'meetings') openMeetings();
    else if (id === 'reviews') openWorkspaceTab('directory_review');
    else openWorkspaceTab('sources');
  }
  /** The Meetings list, keeping its filters. */
  function openMeetings(): void {
    beforeCommit();
    exploreState.commitWorkspace('meetings', { meetingID: null });
  }
  /** One meeting's page: its event card or transcript and action items. */
  function openMeetingPage(meetingID: number): void {
    commitNavigation({ workspace: 'meetings', meetingID });
  }
  function leaveMeetingPage(): void {
    if (exploreState.canGoBack()) window.history.back();
    else commitNavigation({ workspace: 'meetings', meetingID: null });
  }
  /** The Inbox is the browse surface: the Everything view with no query. */
  function openInbox(): void {
    beforeCommit();
    exploreState.commitWorkspace('everything', { query: '' });
  }
  /** Search from anywhere: the header field and the palette land here. */
  function searchArchive(query: string): void {
    beforeCommit();
    exploreState.commitWorkspace('everything', { query });
    everythingSession.submitTypedQuery(query);
    void focusGridAfterUpdate();
  }
  const themeOptions: { value: ThemePreference; label: string }[] = [
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
    { value: 'system', label: 'System' },
  ];
  const densityOptions = [
    { value: 'daemon', label: 'Density: Auto' },
    { value: 'compact', label: 'Density: Compact' },
    { value: 'comfortable', label: 'Density: Comfortable' },
  ];
  $effect(() => {
    if (exploreState.current.workspace !== 'settings') cardDAVSettingsRequest = undefined;
  });

  $effect(() => {
    if (exploreState.current.workspace !== 'operations') return;
    const operationState: OperationsURLState = {
      operationLane: exploreState.current.operationLane,
      operationKind: exploreState.current.operationKind,
      operationState: exploreState.current.operationState,
      operationStartedFrom: exploreState.current.operationStartedFrom,
      operationStartedBefore: exploreState.current.operationStartedBefore,
      operationRunID: exploreState.current.operationRunID,
      operationStatus: exploreState.current.operationStatus
    };
    const context = archiveContextKey;
    untrack(() => { void operationsController.applyURLState(operationState, context); });
  });
  let contextualViewerFile = $state<FileViewerTarget>();
  let contextualViewerReturnFocus = $state<HTMLElement>();
  let previousAttachmentID: number | undefined;
  let selectionPreflight = $state<ExplorePreflight>();
  let selectionPreflightController: AbortController | undefined;
  let pendingDeletionReview = $state<'explicit' | 'all_matching'>();
  let searchInput = $state<HTMLInputElement>();
  let headerSearchInput = $state<HTMLInputElement>();
  // The loader also drives the Files-shell grouped view (AppShell gates its
  // internal load effect on `workspace === 'files' && groupingChain.length >
  // 0` in addition to `workspace === 'everything'`), so it is owned here
  // rather than by EverythingWorkspace alone. `sortNotice`, `selection`, and
  // grid focus restoration are shared shell-wide state the loader cannot own
  // itself; it reaches them only through these callbacks, called at the
  // exact points the original inline effect wrote to that shared state.
  const loader = new ExploreLoader(
    untrack(() => client),
    exploreState,
    {
      isEnabled: () => enabled,
      onPredicateChange: () => selection.clear(),
      onPagingNotice: (message) => {
        sortNotice = message ?? DEFAULT_SORT_NOTICE;
      },
      onRestorationFocus: () => focusGrid(),
    },
  );
  // Owned here (not by EverythingWorkspace) so coverage-poll backoff, the
  // exact lexical match-count cache, and loaded reading-pane group detail
  // survive a workspace round-trip: AppShell renders EverythingWorkspace
  // behind an {#if}, so it is destroyed and recreated on every switch away
  // from and back to 'everything'.
  const everythingSession = new EverythingSessionState();
  let paletteOpen = $state(false);
  let keyboardHelpOpen = $state(false);
  let keyboardHelpScopeCleanup: (() => void) | undefined;
  let sortNotice = $state(DEFAULT_SORT_NOTICE);
  let editableScopeCleanup: (() => void) | undefined;
  let previousSortNoticeWorkspace = untrack(() => exploreState.current.workspace);
  // An epoch is a monotonically increasing restoration event, not a boolean
  // mode. Capture the initial value so ordinary URL commits do not turn into
  // repeated page-one reloads merely because the initial epoch is positive.
  let appliedDirectoryRestorationEpoch = untrack(() => exploreState.restorationEpoch);
  let appliedDirectoryReviewRestorationEpoch = untrack(() => exploreState.restorationEpoch);
  let appliedFactLedgerRestorationEpoch = untrack(() => exploreState.restorationEpoch);
  $effect(() => {
    const defaults = appearanceDefaults;
    untrack(() => appearance.setDefaults(defaults));
  });
  // URL restoration is shell-owned, just like Relationships' target
  // hydration below. DirectoryWorkspace also supports isolated mounting for
  // component tests, but normal app navigation reaches the controller here.
  $effect(() => {
    const restorationEpoch = exploreState.restorationEpoch;
    const historyRestoration = restorationEpoch !== appliedDirectoryRestorationEpoch;
    appliedDirectoryRestorationEpoch = restorationEpoch;
    if (exploreState.current.workspace !== 'directory') return;
    const directoryState = {
      directoryQuery: exploreState.current.directoryQuery,
      directoryContactState: exploreState.current.directoryContactState,
      directoryCategory: exploreState.current.directoryCategory,
      directoryOrganization: exploreState.current.directoryOrganization,
      directoryPrimaryChannel: exploreState.current.directoryPrimaryChannel,
      directoryLastContactAfter: exploreState.current.directoryLastContactAfter,
      directoryLastContactBefore: exploreState.current.directoryLastContactBefore,
      directorySort: exploreState.current.directorySort,
      directoryHasName: exploreState.current.directoryHasName,
      directoryPersonID: exploreState.current.directoryPersonID,
    };
    untrack(() => directoryController.applyURLState(directoryState, historyRestoration));
  });
  // Saving, merging, or linking someone happens away from the list (on a
  // contact, person, or review page), so archive contacts reload each time
  // the list is shown again: a saved contact never shows twice.
  let wasOnPeopleList = untrack(() => exploreState.current.workspace === 'directory' && exploreState.current.directoryPersonID === null);
  $effect(() => {
    const onList = exploreState.current.workspace === 'directory' && exploreState.current.directoryPersonID === null;
    untrack(() => {
      if (onList && !wasOnPeopleList) peopleHub.refresh();
      wasOnPeopleList = onList;
    });
  });
  // Archive contacts follow the People list's filters.
  $effect(() => {
    if (exploreState.current.workspace !== 'directory' || exploreState.current.directoryPersonID !== null) return;
    const filters = peopleFilters;
    untrack(() => peopleHub.apply(filters));
  });
  // One human, one page: an archive contact that has been saved opens as
  // its saved person, on the same tab.
  $effect(() => {
    if (exploreState.current.workspace !== 'relationships') return;
    const target = exploreState.current.relationshipTarget;
    const detail = relationshipsController.detail;
    if (!target?.startsWith('cluster:') || relationshipsController.target !== target) return;
    const profileID = detail && 'identifiers' in detail ? (detail as PersonSummary).profile?.id : undefined;
    if (!profileID) return;
    const personTab = exploreState.current.personTab;
    untrack(() => replaceCommittedNavigation({
      workspace: 'directory', directoryPersonID: profileID, relationshipTarget: null, relationshipFiles: false,
      personTab: personTab === 'overview' || personTab === 'timeline' || personTab === 'files' || personTab === 'meetings'
        ? personTab : 'overview',
    }));
  });
  // Review offsets are ephemeral like Directory cursors. A Back/Forward
  // restoration always starts its restored queue at page zero, while an
  // ordinary workspace round trip keeps the AppShell-owned request/page.
  $effect(() => {
    const restorationEpoch = exploreState.restorationEpoch;
    const historyRestoration = restorationEpoch !== appliedDirectoryReviewRestorationEpoch;
    appliedDirectoryReviewRestorationEpoch = restorationEpoch;
    const relationshipState = exploreState.current.relationshipReviewState;
    const factActive =
      exploreState.current.workspace === 'directory_review' && exploreState.current.reviewKind === 'fact';
    const factHistoryRestoration = factActive && restorationEpoch !== appliedFactLedgerRestorationEpoch;
    if (factActive) appliedFactLedgerRestorationEpoch = restorationEpoch;
    untrack(() =>
      factLedgerController.applyContext(factActive, exploreState.current.directoryPersonID, factHistoryRestoration),
    );
    if (exploreState.current.workspace !== 'directory_review') {
      untrack(() => relationshipReviewController.applyContext(false, relationshipState, historyRestoration));
      return;
    }
    const reviewState = {
      reviewKind: exploreState.current.reviewKind,
      identityState: exploreState.current.identityState,
      identityOrigin: exploreState.current.identityOrigin,
    };
    untrack(() => directoryReviewController.applyURLState(reviewState, historyRestoration));
    untrack(() =>
      relationshipReviewController.applyContext(
        reviewState.reviewKind === 'relationship',
        relationshipState,
        historyRestoration,
      ),
    );
  });
  $effect(() => {
    const mode = getThemeMode();
    if (mode === appearance.current.theme) return;
    untrack(() => appearance.setTemporary({ theme: mode as ThemePreference }));
  });
  $effect(() => {
    const mode = searchModeDefault;
    untrack(() => exploreState.setConfiguredDefaultSearchMode(mode));
  });
  // sortNotice is shared across workspaces (Everything, Files, etc.). A
  // workspace-specific notice (e.g. the Files grouped End-cap pause message)
  // must not leak into another workspace after switching — for example via
  // the ContextBar "Show as" presentation control, which changes the
  // workspace through commitNavigation rather than commitWorkspace. Resetting
  // here, keyed off the actual workspace value rather than the commit path,
  // covers every route that changes it. Ordinary paging/sorting within the
  // same workspace must not clear the notice, so this only fires on a change.
  $effect(() => {
    const workspace = exploreState.current.workspace;
    untrack(() => {
      if (workspace !== previousSortNoticeWorkspace) {
        previousSortNoticeWorkspace = workspace;
        sortNotice = DEFAULT_SORT_NOTICE;
      }
    });
  });
  const selectedAttachmentID = $derived(parseAttachmentSelection(exploreState.current.selectedRow));
  const readingTargetKey = $derived(archiveMeetingID !== undefined ? archiveReturnSelection : selectedAttachmentID === undefined ? exploreState.current.selectedRow : null);
  const apiSelection = $derived.by((): APIExploreSelection | undefined => {
    const snapshot = selection.snapshot();
    const authority = loader.result;
    if (snapshot.mode === 'explicit') {
      if (snapshot.rowKeys.length === 0 || !authority) return undefined;
      return {
        mode: 'explicit',
        predicate: exploreState.predicate(),
        row_keys: snapshot.rowKeys,
        cache_revision: authority.cacheRevision,
        search_provenance: authority.searchProvenance,
        ...(authority.candidateSnapshotId ? { candidate_snapshot_id: authority.candidateSnapshotId } : {}),
      };
    }
    return {
      mode: 'all_matching',
      predicate: snapshot.predicate,
      exclusions: snapshot.exclusions,
      cache_revision: snapshot.cacheRevision,
      search_provenance: snapshot.searchProvenance,
      ...(snapshot.candidateSnapshotId ? { candidate_snapshot_id: snapshot.candidateSnapshotId } : {}),
    };
  });
  const apiSelectionFingerprint = $derived(canonicalFingerprint(apiSelection));
  $effect(() => {
    const workspace = exploreState.current.workspace;
    void apiSelectionFingerprint;
    const candidate = untrack(() => apiSelection);
    selectionPreflightController?.abort();
    selectionPreflightController = undefined;
    selectionPreflight = undefined;
    if (!candidate || workspace !== 'everything') return;
    const controller = new AbortController();
    selectionPreflightController = controller;
    void generatedPreflightExploreSelection(
      { selection: candidate },
      {
        ...client,
        signal: controller.signal,
      },
    )
      .then(({ data }) => {
        if (!controller.signal.aborted) selectionPreflight = data;
      })
      .catch(() => undefined);
  });
  async function exportSelection(): Promise<void> {
    const target = selectionPreflight?.action_targets.find((item) => item.action === 'export');
    if (!target || !apiSelection || selectionPreflight?.count !== 1) return;
    try {
      const { data, response } = await generatedGetCLIMessageRaw(
        { id: String(target.message_id) },
        client,
      );
      if (!response.ok || data === undefined)
        throw new Error('The authorized raw message export is no longer available.');
      const objectURL = URL.createObjectURL(data);
      const anchor = document.createElement('a');
      anchor.href = objectURL;
      anchor.download = target.filename;
      anchor.click();
      URL.revokeObjectURL(objectURL);
    } catch (cause) {
      loader.error = cause instanceof Error ? cause.message : 'Unable to export the selected message.';
    }
  }
  function openDeletionReview(mode: 'explicit' | 'all_matching'): void {
    const candidate = apiSelection;
    if (exploreState.current.workspace !== 'everything' || !candidate || candidate.mode !== mode) return;
    pendingDeletionReview = mode;
    commitWorkspace('deletions');
  }
  async function openSavedView(state: Partial<ExploreURLState>): Promise<void> {
    selection.clear();
    // From Saved Views the view replaces the list's entry (Back skips the
    // list); from anywhere else it is a new place, so Back returns there.
    if (exploreState.current.workspace === 'saved_views') replaceCommittedNavigation(state);
    else commitNavigation(state);
    await tick();
    const grid = currentGrid();
    if (grid) grid.focus();
    else searchInput?.focus();
  }
  function viewerTargetFromFact(file: ExploreFileFact): FileViewerTarget {
    return {
      id: file.id,
      key: file.key,
      entry_key: file.entry_key,
      message_id: file.message_id,
      conversation_id: file.conversation_id,
      filename: file.filename,
      mime_type: file.mime_type,
      size_bytes: file.size,
      title: file.title,
      occurred_at: file.occurred_at,
      source_identifier: file.source_identifier,
    };
  }
  $effect(() => {
    const attachmentID = selectedAttachmentID;
    const facts = loader.fileFacts;
    if (attachmentID === undefined) {
      contextualViewerFile = undefined;
      if (previousAttachmentID !== undefined) {
        void tick().then(() => (contextualViewerReturnFocus ?? currentGrid())?.focus());
      }
      previousAttachmentID = undefined;
      return;
    }
    previousAttachmentID = attachmentID;
    if (!untrack(() => contextualViewerReturnFocus)) {
      contextualViewerReturnFocus = currentGrid() ?? undefined;
    }
    const local = facts.find((file) => file.id === attachmentID);
    const existing = untrack(() => contextualViewerFile);
    if (local) {
      if (existing?.key !== local.key) contextualViewerFile = viewerTargetFromFact(local);
    } else if (existing?.id !== attachmentID) contextualViewerFile = { id: attachmentID };
  });
  const conversationAnchorId = $derived.by(() => {
    const anchor = exploreState.current.conversationAnchor;
    if (anchor === null) return undefined;
    const parsed = Number(anchor);
    return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : undefined;
  });
  function editableTarget(target: EventTarget | null): boolean {
    if (!(target instanceof Element)) return false;
    const element = target as HTMLElement;
    return Boolean(
      element?.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"]), iframe'),
    );
  }
  const DIRECTIONAL_CONTROLS = [
    'button', 'select', 'input', 'textarea', '[role="radio"]', '[role="option"]', '[role="separator"]',
    '[role="slider"]', '[role="spinbutton"]', '[role="tab"]', '[role="menuitem"]', '[role="treeitem"]',
    // Kit comboboxes are inputs or buttons, both covered above.
    '[role="button"]'
  ].join(', ');
  const NATIVE_SCROLL_KEYS = new Set(['PageUp', 'PageDown', 'Home', 'End', ' ', 'ArrowUp', 'ArrowDown']);
  function preserveNativeControlKey(event: KeyboardEvent): void {
    if (!(event.target instanceof Element)) return;
    const target = event.target;
    const activationControl = target.closest('button, a[href], summary, [role="button"], [role="option"]');
    // Arrow keys belong to the focused control: a split-pane separator or a
    // slider resizes, a button or tab moves within its own widget. Only the
    // grid and plain reading surfaces hand arrows to app shortcuts.
    const directionalControl = target.closest(DIRECTIONAL_CONTROLS);
    if (
      (activationControl && (event.key === 'Enter' || event.key === ' ')) ||
      (directionalControl && event.key.startsWith('Arrow'))
    ) {
      // Local control handlers and browser defaults run before this document
      // listener; stop only the app-wide shortcut listener on window.
      event.stopPropagation();
      return;
    }
    // The paging and row keys drive the results grid. Where a workspace has
    // no grid to relay them to, they belong to the browser, which scrolls the
    // page the reader is in.
    if (NATIVE_SCROLL_KEYS.has(event.key) && !currentGrid()) event.stopPropagation();
  }
  function syncEditableShortcutScope(target: EventTarget | null): void {
    const focused = editableTarget(target);
    if (focused && appShortcuts.activeScope() === 'everything-editable') return;
    if (!focused && !editableScopeCleanup) return;
    editableScopeCleanup?.();
    editableScopeCleanup = focused ? appShortcuts.pushScope('everything-editable') : undefined;
  }
  function focusGrid(): void {
    currentGrid()?.focus();
  }
  async function restoreHistoryFocus(): Promise<void> {
    await tick();
    if (exploreState.current.workspace === 'everything') {
      if (exploreState.current.selectedRow === null) focusGrid();
      return;
    }
    document.querySelector<HTMLButtonElement>('button[aria-current="page"]')?.focus();
  }
  async function focusGridAfterUpdate(): Promise<void> {
    await tick();
    focusGrid();
  }
  function currentGrid(): HTMLElement | null {
    return document.querySelector<HTMLElement>(
      '[role="grid"][aria-label="Message results"], [role="grid"][aria-label^="Messages grouped by"], [role="grid"][aria-label="Files in current context"]',
    );
  }
  function relayGridKey(event: KeyboardEvent, key: string): void {
    if (event.target instanceof Element && event.target.closest('button, a, summary, [role="button"]')) return;
    const grid = currentGrid();
    if (!grid || event.target === grid) return;
    grid.focus();
    grid.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: false, cancelable: true }));
  }
  async function closeReadingPane(): Promise<void> {
    commitNavigation({ selectedRow: null });
    await tick();
    focusGrid();
  }
  async function openArchivedMeeting(meeting: MeetingRef): Promise<void> {
    const origin = canonicalFingerprint(exploreState.current);
    const returnSelectedRow = archiveMeetingID !== undefined ? archiveReturnSelection : exploreState.current.selectedRow;
    if (archiveMeetingID === undefined) archiveReturnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    const message = await archivedMeeting.load(meeting.message_id, meeting.conversation_id);
    if (!message || origin !== canonicalFingerprint(exploreState.current)) return;
    archiveReturnSelection = returnSelectedRow;
    commitRestorableNavigation({ selectedRow: archiveMeetingSelection(message.id), conversationAnchor: null });
    window.history.replaceState({
      ...window.history.state,
      [ARCHIVE_MEETING_HISTORY_KEY]: { id: message.id, returnSelectedRow }
    }, '', window.location.href);
  }
  /** Opens a message known only by id (a Directory contact-state ref) in
   * the Everything reading pane: a fresh table view bounded to the day it
   * was sent, with the message's row selected and the thread anchored on
   * it. The explore loader restores a selected key across pages, so the
   * row is found without a dedicated message route. */
  async function openMessageByID(messageID: number): Promise<void> {
    const origin = canonicalFingerprint(exploreState.current);
    let data: MessageDetail | undefined;
    let status: number | undefined;
    try {
      ({ data, response: { status } } = await getMessage({ id: messageID }, { ...client }));
    } catch {
      data = undefined;
    }
    if (origin !== canonicalFingerprint(exploreState.current)) return;
    if (!data) {
      announceOperation(status === 404
        ? 'Couldn\'t open that message: it is no longer in the archive.'
        : 'Couldn\'t open that message: the archive did not respond.');
      return;
    }
    // The detail carries message_type and conversation_type, so the row key
    // is derived exactly (chat rows are keyed by conversation).
    const key = messageEntryKey(data);
    if (!key) {
      announceOperation('Couldn\'t open that message: the archive has no row for it.');
      return;
    }
    commitRestorableNavigation({
      workspace: 'everything',
      presentation: 'table',
      query: '',
      groupingChain: [],
      filters: messageRowFilters(data),
      selectedRow: key,
      conversationAnchor: String(data.id),
      analysisTarget: null,
      selectedIdentifier: null,
      activeRow: null,
      scrollAnchor: null,
    });
  }

  async function restoreArchiveFocus(): Promise<void> {
    await tick();
    // Kit releases its focus trap during teardown; focus the surviving source
    // link after that cleanup (or the current workspace's own control).
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    const target = archiveReturnFocus?.isConnected ? archiveReturnFocus : currentGrid() ?? document.querySelector<HTMLButtonElement>('button[aria-current="page"]');
    target?.focus();
  }

  function closeArchivedMeeting(): void {
    if (parseArchiveMeetingHistory(window.history.state, exploreState.current.selectedRow)) {
      window.history.back();
    } else {
      replaceCommittedNavigation({ selectedRow: null, conversationAnchor: null });
    }
  }

  function openFileItem(entryKey: string): void {
    if (selectedAttachmentID !== undefined) {
      replaceCommittedRestorableNavigation({
        workspace: 'everything',
        presentation: 'table',
        selectedRow: entryKey,
        conversationAnchor: null,
        activeRow: null,
        scrollAnchor: null,
      });
      return;
    }
    commitRestorableNavigation({
      workspace: 'everything',
      presentation: 'table',
      selectedRow: entryKey,
      conversationAnchor: null,
      activeRow: null,
      scrollAnchor: null,
    });
  }
  function openFileConversation(entryKey: string, messageID: number, _conversationID: number): void {
    if (selectedAttachmentID !== undefined) {
      replaceCommittedRestorableNavigation({
        workspace: 'everything',
        presentation: 'table',
        selectedRow: entryKey,
        conversationAnchor: String(messageID),
        activeRow: null,
        scrollAnchor: null,
      });
      return;
    }
    commitRestorableNavigation({
      workspace: 'everything',
      presentation: 'table',
      selectedRow: entryKey,
      conversationAnchor: String(messageID),
      activeRow: null,
      scrollAnchor: null,
    });
  }
  function openContextualFile(file: ExploreFileFact): void {
    openAttachmentTarget(viewerTargetFromFact(file));
  }
  function openAttachmentTarget(target: FileViewerTarget): void {
    const file = target;
    contextualViewerReturnFocus = currentGrid() ?? undefined;
    contextualViewerFile = target;
    commitNavigation({
      selectedRow: attachmentSelection(file.id),
      conversationAnchor: null,
    });
    window.history.replaceState(
      {
        ...(window.history.state && typeof window.history.state === 'object' ? window.history.state : {}),
        [ATTACHMENT_HISTORY_MARKER]: file.id,
      },
      '',
      window.location.href,
    );
  }
  async function closeContextualViewer(): Promise<void> {
    if (window.history.state?.[ATTACHMENT_HISTORY_MARKER] === selectedAttachmentID) {
      window.history.back();
      return;
    }
    replaceCommittedNavigation({ selectedRow: null });
    contextualViewerFile = undefined;
    await tick();
    (contextualViewerReturnFocus ?? currentGrid())?.focus();
  }
  function changeConversationAnchor(anchorId: number): void {
    replaceCommittedNavigation({ conversationAnchor: String(anchorId) });
  }
  // The Relationships hub owns its own Esc layering (reading pane → timeline
  // → list) and only lets an Esc it didn't consume bubble here once it has
  // nothing left to close. This function's branches read/write state that
  // belongs to Everything/Files (selectedRow, groupingChain, the conversation
  // and attachment viewers) — none of it relevant to the hub — so a bubbled
  // Esc must not fall through into clearing leftover state from a workspace
  // the user isn't even in (e.g. a groupingChain left behind by
  // commitWorkspace, which does not reset it).
  function handleEscape(event: KeyboardEvent): void {
    if (editableTarget(event.target)) return;
    if (exploreState.current.workspace === 'relationships') return;
    if (selectedAttachmentID !== undefined) {
      void closeContextualViewer();
    } else if (exploreState.current.selectedRow !== null) {
      // Closing the reading pane clears any in-thread anchor with it —
      // the thread is the pane's default content, not a separate layer.
      void closeReadingPane();
    } else if (exploreState.current.groupingChain.length > 0) {
      commitUngroup();
    }
    focusGrid();
  }
  function openContextControl(kind: 'filters' | 'grouping' | 'sort'): void {
    const selector =
      kind === 'filters'
        ? 'button[aria-label="Filters"]'
        : kind === 'grouping'
          ? '[data-group-picker] button'
          : 'button[aria-label^="Sort: "]';
    const control = document.querySelector<HTMLButtonElement>(selector);
    control?.focus();
    control?.click();
  }
  function fixedSortNotice(): void {
    const mode = exploreState.predicate().search_mode;
    sortNotice = mode === 'semantic' || mode === 'hybrid'
      ? 'Semantic and hybrid results are ranked by relevance; date order does not apply.'
      : 'The Inbox remains newest first; reverse order is not supported by the canonical entry API.';
    document.querySelector<HTMLButtonElement>('button[aria-label^="Sort: "]')?.focus();
  }
  function followRow(row: EntryRow): void {
    if (exploreState.current.selectedRow === row.key) return;
    // Keyboard moves with the pane open replace the entry rather than
    // pushing one history step per keypress.
    replaceCommittedNavigation({ selectedRow: row.key, conversationAnchor: null });
  }
  function navigateReader(delta: number, event: KeyboardEvent | undefined = undefined): void {
    // A focused control keeps its own arrow keys (a separator resizes, a
    // button or tab moves within its widget); h/l are free unless the
    // focus is in a text field.
    if (event?.key.startsWith('Arrow')) {
      if (event.target instanceof Element && event.target.closest(`${DIRECTIONAL_CONTROLS}, a, summary`)) return;
    } else if (event && editableTarget(event.target)) {
      return;
    }
    // h/l and ←/→ step within the open thread first.
    if (stepThread(delta)) return;
    if (!exploreState.current.selectedRow || loader.rows.length === 0) return;
    const index = loader.rows.findIndex((row) => row.key === exploreState.current.selectedRow);
    if (index < 0) return;
    const next = loader.rows[Math.max(0, Math.min(loader.rows.length - 1, index + delta))];
    if (next && next.key !== exploreState.current.selectedRow) openRow(next);
  }
  function relay(event: KeyboardEvent | undefined, key: string | undefined = undefined): void {
    const resolvedKey = key ?? event?.key;
    if (!resolvedKey) return;
    if (event) {
      relayGridKey(event, resolvedKey);
      return;
    }
    queueMicrotask(() => {
      const grid = currentGrid();
      if (!grid) return;
      grid.focus();
      grid.dispatchEvent(new KeyboardEvent('keydown', { key: resolvedKey, bubbles: false, cancelable: true }));
    });
  }
  const commandHandlers: CommandHandlers = {
    'move-next': (event) => relay(event, 'j'),
    'move-previous': (event) => relay(event, 'k'),
    'reader-previous': (event) => navigateReader(-1, event),
    'reader-next': (event) => navigateReader(1, event),
    'page-up': (event) => relay(event, 'PageUp'),
    'page-down': (event) => relay(event, 'PageDown'),
    'first-row': (event) => relay(event, 'Home'),
    'last-row': (event) => relay(event, 'End'),
    'open-row': (event) => relay(event, 'Enter'),
    'close-layer': (event) => handleEscape(event ?? new KeyboardEvent('keydown', { key: 'Escape' })),
    'focus-search': (event) => {
      if (event?.key === '/' && editableTarget(event.target)) return;
      event?.preventDefault();
      (headerSearchInput?.isConnected ? headerSearchInput : searchInput)?.focus();
    },
    'toggle-selection': (event) => relay(event, ' '),
    'select-visible': (event) => relay(event, 'A'),
    'clear-selection': (event) => {
      if (event) relay(event, 'x');
      else {
        selection.clear();
        queueMicrotask(focusGrid);
      }
    },
    'review-delete-selected': () => openDeletionReview('explicit'),
    'review-delete-matching': () => openDeletionReview('all_matching'),
    'open-filters': () => openContextControl('filters'),
    'open-grouping': () => openContextControl('grouping'),
    'change-sort': () => openContextControl('sort'),
    'reverse-sort': fixedSortNotice,
    'open-keyboard-help': () => {
      keyboardHelpOpen = true;
    },
    'open-command-palette': (event) => {
      if (!editableTarget(event?.target ?? null)) paletteOpen = true;
    },
  };
  const groupingCommands = $derived(
    GROUPING_CATALOG.flatMap((entry) => {
      if (!entry.requestable)
        return [
          {
            id: `unavailable:${entry.concept}`,
            label: `${entry.label} — unavailable: ${entry.unavailableReason}`,
            section: 'Group by',
            keywords: `${entry.keywords} ${entry.unavailableReason ?? ''}`,
            keys: [],
            combos: [],
            destructive: false,
            review: false,
            disabled: true,
            run: () => undefined,
          },
        ];
      return entry.requestDimensions.map((dimension) => ({
        id: `group:${dimension}`,
        label: `Group by ${dimension === 'year' ? 'Year' : dimension === 'month' ? 'Month' : entry.label}`,
        section: 'Group by',
        keywords: entry.keywords,
        keys: [],
        combos: [],
        destructive: false,
        review: false,
        disabled: exploreState.current.groupingChain.includes(dimension),
        run: () => {
          commitGrouping(dimension);
          void focusGridAfterUpdate();
        },
      }));
    }),
  );
  const reviewWorkspaceCommand: AppCommand = {
    id: 'workspace:directory-review',
    label: 'Open Reviews',
    section: 'Navigate',
    keywords: 'Navigate Reviews Directory identity matches fact review',
    keys: [],
    combos: [],
    destructive: false,
    review: false,
    run: () => commitWorkspace('directory_review'),
  };
  function navigationCommand(id: string, label: string, keywords: string, run: () => void): AppCommand {
    return { id, label, section: 'Go to', keywords: `Go to ${label} ${keywords}`, keys: [], combos: [], destructive: false, review: false, run };
  }
  const goToCommands: AppCommand[] = [
    navigationCommand('go:people', 'Go to People', 'contacts directory relationships', openPeopleList),
    navigationCommand('go:person', 'Go to person…', 'find person contact', () => void openPersonFinder()),
    navigationCommand('go:inbox', 'Go to Inbox', 'everything browse messages', openInbox),
    navigationCommand('go:files', 'Go to Files', 'attachments documents', () => openWorkspaceTab('files')),
    navigationCommand('go:meetings', 'Go to Meetings', 'calendar events transcripts', openMeetings),
    navigationCommand('go:activity', 'Go to Activity', 'sources operations deletions', () => openWorkspaceTab('sources')),
    navigationCommand('go:settings', 'Go to Settings', 'preferences appearance theme density', () => openWorkspaceTab('settings')),
    navigationCommand('go:saved-views', 'Go to Saved Views', 'bookmarks', () => openWorkspaceTab('saved_views')),
  ];
  function appearanceCommand(id: string, label: string, run: () => void): AppCommand {
    return { id, label, section: 'Appearance', keywords: `Appearance ${label}`, keys: [], combos: [], destructive: false, review: false, run };
  }
  const appearanceCommands: AppCommand[] = [
    ...themeOptions.map((option) => appearanceCommand(`theme:${option.value}`, `Theme: ${option.label}`, () => setThemeMode(option.value))),
    appearanceCommand('theme:daemon', 'Theme: Daemon default', () => appearance.clearTemporary('theme')),
    ...densityOptions.map((option) => appearanceCommand(`density:${option.value}`, option.label, () => applyTemporaryDensity(option.value))),
  ];
  /** People opens with its search focused, so a name is one keystroke away. */
  async function openPersonFinder(): Promise<void> {
    openPeopleList();
    await tick();
    document.querySelector<HTMLInputElement>('input[aria-label="Search people"]')?.focus();
  }
  const commandRegistry = $derived([
    ...createCommandRegistry(commandHandlers),
    ...goToCommands,
    reviewWorkspaceCommand,
    ...appearanceCommands,
    ...groupingCommands,
  ]);
  const paletteCommands = $derived(
    commandRegistry.map(
      (command): PaletteCommand => ({
        id: command.id,
        label: command.label,
        section: command.section,
        keywords: command.keywords,
        combo: command.combos[0],
        disabled: command.disabled,
      }),
    ),
  );
  function runPalette(command: PaletteCommand): void {
    commandRegistry.find(({ id }) => id === command.id)?.run();
  }
  function applyTemporaryDensity(value: string): void {
    if (value === 'daemon') appearance.clearTemporary('density');
    else appearance.setTemporary({ density: value as DensityPreference });
  }
  function openRow(row: EntryRow): void {
    // Single-click selects AND opens; re-opening the already-open row must
    // not push a duplicate history entry.
    if (exploreState.current.selectedRow === row.key) return;
    commitNavigation({ selectedRow: row.key });
  }
  function drillGroup(row: ExploreGroupRow): void {
    const [dimension, ...remaining] = exploreState.current.groupingChain;
    if (!dimension || !groupingByDimension(dimension).drillable) return;
    const filters = filtersForGroup(exploreState.current.filters, dimension, row.key);
    if (!filters) {
      commitNavigation({ selectedRow: `group:${dimension}:${row.key}` });
      return;
    }
    commitNavigation({
      filters,
      groupingChain: remaining,
      selectedRow: exploreState.current.workspace === 'files' ? null : `group:${dimension}:${row.key}`,
      activeRow: null,
      scrollAnchor: null,
    });
  }
  $effect(() => {
    keyboardHelpScopeCleanup?.();
    keyboardHelpScopeCleanup = keyboardHelpOpen ? appShortcuts.pushScope('everything-keyboard-help') : undefined;
  });
  // Hydrates the Relationships hub's detail pane from a URL-carried target
  // (initial load, or Back/Forward restoring a different one). The hub
  // itself never opens a target on its own — RelationshipsWorkspace's own
  // tests drive controller.openTarget explicitly — so AppShell, as the
  // controller's owner, is the one place that reacts to the URL field.
  // Tracked (not untracked) so a predicate change alone — e.g. filters set
  // in Everything/Files that carry over when the URL-carried target stays
  // the same — re-opens the target too, mirroring how Everything's own
  // inspector detail fingerprints include the predicate. Still guarded
  // against re-fetching: the ordinary in-hub click path (which calls
  // onTargetChange and controller.openTarget together, synchronously,
  // before this effect can flush) already opened this exact pair — that
  // guard reads controller.lastPredicateFingerprint (set by openTarget
  // itself) rather than a copy tracked here, so it stays correct no matter
  // which caller opened the target.
  $effect(() => {
    const workspace = exploreState.current.workspace;
    const target = exploreState.current.relationshipTarget;
    if (workspace !== 'relationships') return;
    if (target === null) {
      // URL is truth: once the target is gone (Esc, Back/Forward), drop the
      // hub's detail/timeline state too, so the center pane falls back to
      // its own empty placeholder instead of keeping the previous person or
      // domain on screen.
      if (relationshipsController.target !== null) relationshipsController.clearTarget();
      return;
    }
    const predicate = exploreState.predicate();
    if (
      relationshipsController.target === target &&
      relationshipsController.lastPredicateFingerprint === predicateFingerprint(predicate)
    ) {
      return;
    }
    void relationshipsController.openTarget(target, predicate);
  });
  /** Opens an archive contact's person page. A contact that has been saved
   * redirects to its saved person, so every way in lands on one page. */
  function openRelationship(participantID: number): void {
    commitNavigation({
      workspace: 'relationships',
      // Entering the hub never carries the text query (see
      // ExploreState.commitWorkspace): the relationships ranking and
      // cluster-timeline endpoints have no text-query input, so a carried
      // query would half-apply across the hub's surfaces.
      query: '',
      relationshipFacet: 'people',
      relationshipTarget: `cluster:${participantID}`,
      personTab: 'overview',
      relationshipFiles: false,
      relationshipShowAll: false,
      analysisTarget: null,
      selectedIdentifier: null,
      selectedRow: null,
      activeRow: null,
      conversationAnchor: null,
      scrollAnchor: null,
    });
  }
  // The page title names the surface (and the message, once loaded) so
  // browser history and tabs read as places.
  let pageSubject = $state('');
  $effect(() => {
    void exploreState.current.messageID;
    void exploreState.current.meetingID;
    pageSubject = '';
  });
  $effect(() => {
    const titled = exploreState.current.workspace === 'message' ||
      (exploreState.current.workspace === 'meetings' && exploreState.current.meetingID !== null);
    const surface = titled && pageSubject
      ? pageSubject
      : routeTitle(exploreState.current);
    document.title = `${surface} · msgvault`;
  });
  function leaveMessagePage(): void {
    if (exploreState.canGoBack()) window.history.back();
    else commitWorkspace('everything');
  }
  onMount(() => {
    pendingReviews.start();
    const detachShortcuts = initShortcuts();
    let disposed = false;
    const resyncEditableScope = (): void => {
      if (!disposed) syncEditableShortcutScope(document.activeElement);
    };
    const handleFocusIn = (event: FocusEvent): void => syncEditableShortcutScope(event.target);
    const handleFocusOut = (): void => queueMicrotask(resyncEditableScope);
    const handleHistoryFocus = (): void => {
      // Back/Forward restores committed state synchronously (ExploreState's
      // own popstate listener). A still-pending debounced typing patch must
      // not survive to later clobber that restored state, so it is discarded
      // rather than flushed.
      debouncedSearchPatch.cancel();
      void restoreHistoryFocus();
    };
    const editableObserver = new MutationObserver(() => queueMicrotask(resyncEditableScope));
    document.addEventListener('focusin', handleFocusIn, true);
    document.addEventListener('focusout', handleFocusOut, true);
    document.addEventListener('keydown', preserveNativeControlKey);
    window.addEventListener('popstate', handleHistoryFocus);
    editableObserver.observe(document.documentElement, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['contenteditable'],
    });
    resyncEditableScope();
    const unregister = commandRegistry.flatMap((command) =>
      command.combos.map((combo) => appShortcuts.register(combo, command.run, { description: command.label })),
    );
    return () => {
      disposed = true;
      document.removeEventListener('focusin', handleFocusIn, true);
      document.removeEventListener('focusout', handleFocusOut, true);
      document.removeEventListener('keydown', preserveNativeControlKey);
      window.removeEventListener('popstate', handleHistoryFocus);
      editableObserver.disconnect();
      editableScopeCleanup?.();
      editableScopeCleanup = undefined;
      keyboardHelpScopeCleanup?.();
      keyboardHelpScopeCleanup = undefined;
      for (const remove of unregister.reverse()) remove();
      detachShortcuts();
    };
  });
  onDestroy(() => {
    pendingReviews.stop();
    debouncedSearchPatch.cancel();
    loader.destroy();
    archivedMeeting.cancel();
    selectionPreflightController?.abort();
    editableScopeCleanup?.();
    editableScopeCleanup = undefined;
    keyboardHelpScopeCleanup?.();
    keyboardHelpScopeCleanup = undefined;
    appearance.destroy();
    relationshipsController.destroy();
    directoryController.destroy();
    peopleHub.destroy();
    directoryReviewController.destroy();
    relationshipReviewController.destroy();
    factLedgerController.destroy();
    operationsController.destroy();
    if (ownsState) exploreState.destroy();
  });
</script>

{#snippet browserControls()}
  <section class="browser-appearance" aria-label="This browser">
    <h3>This browser</h3>
    <p>Overrides for this browser only; the daemon defaults below apply everywhere else.</p>
    <div class="browser-appearance__controls">
      <ThemeToggle variant="segmented" />
      {#if appearance.temporary.theme !== undefined}
        <Button size="sm" surface="soft" label="Use daemon theme" onclick={() => appearance.clearTemporary('theme')} />
      {/if}
      <SelectDropdown
        title="Temporary density"
        value={appearance.temporary.density ?? 'daemon'}
        options={densityOptions}
        onchange={applyTemporaryDensity}
      />
    </div>
  </section>
{/snippet}

<div class="app-shell">
  <span class="kit-sr-only" role="status" aria-label="Operation status" aria-live="polite">
    {#key operationAnnouncement.key}<span>{operationAnnouncement.message}</span>{/key}
  </span>
  <TopBar
    {tabs}
    active={activeNavigation}
    ariaLabel="Primary"
    centerTabs
    onchange={(id) => openNavigation(id as NavigationID)}
  >
    {#snippet left()}
      <div class="brand" aria-label="msgvault home"><span aria-hidden="true">◇</span> msgvault</div>
    {/snippet}
    {#snippet search()}
      <!-- Inbox and Search have their own full search bar with modes and
           chips; everywhere else this field takes a query to Search. -->
      {#if exploreState.current.workspace !== 'everything'}
        <HeaderSearch
          {client}
          bind:inputEl={headerSearchInput}
          onSearch={searchArchive}
          onOpenSavedView={(state) => void openSavedView(state)}
          onManageSavedViews={() => openWorkspaceTab('saved_views')}
        />
      {/if}
    {/snippet}
    {#snippet right()}
      <Menu align="end">
        <MenuTrigger class="gear-trigger" ariaLabel="Settings and saved views" title="Settings and saved views">
          <SettingsIcon size={16} aria-hidden="true" />
        </MenuTrigger>
        <MenuContent ariaLabel="Settings and saved views">
          <MenuItem onselect={() => openWorkspaceTab('settings')}>Settings</MenuItem>
          <MenuItem onselect={() => openWorkspaceTab('saved_views')}>Saved Views</MenuItem>
        </MenuContent>
      </Menu>
      {@const archiveLabel = loader.loading
        ? 'Searching'
        : loader.error || loader.unavailable
          ? 'Archive needs attention'
          : 'Local archive ready'}
      <span class="archive-state" class:archive-state--error={Boolean(loader.error || loader.unavailable)} title={archiveLabel}>
        <span aria-hidden="true">
          <StatusDot status={loader.loading ? 'working' : loader.error || loader.unavailable ? 'unclean' : 'idle'} label={archiveLabel} />
        </span>
        <span class="kit-sr-only">{archiveLabel}</span>
      </span>
    {/snippet}
  </TopBar>

  {#if activeNavigation === 'activity'}
    <nav class="sub-tabs" aria-label="Activity">
    <div class="sub-tabs__list" role="tablist" aria-label="Activity sections">
      {#each activitySections as section (section.id)}
        <button
          type="button"
          role="tab"
          aria-selected={exploreState.current.workspace === section.id}
          tabindex={exploreState.current.workspace === section.id ? 0 : -1}
          onclick={() => openWorkspaceTab(section.id)}
          onkeydown={(event) => {
            const index = activitySections.findIndex((candidate) => candidate.id === section.id);
            const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
            if (!step) return;
            event.preventDefault();
            const next = activitySections[(index + step + activitySections.length) % activitySections.length]!;
            openWorkspaceTab(next.id);
            void tick().then(() => document.querySelector<HTMLButtonElement>('[aria-label="Activity sections"] [aria-selected="true"]')?.focus());
          }}
        >{section.label}</button>
      {/each}
    </div>
    </nav>
  {/if}

  {#if exploreState.current.workspace === 'settings'}
    {#if settings}{@render settings(cardDAVSettingsRequest, consumeCardDAVSettingsRequest, settingsNavigationTarget, {
      value: exploreState.current.settingsSection,
      change: (settingsSection) => replaceCommittedNavigation({ settingsSection }),
      browserControls,
    })}{/if}
  {:else if exploreState.current.workspace === 'meetings'}
    {#if exploreState.current.meetingID !== null}
      <MeetingPage
        {client}
        meetingID={exploreState.current.meetingID}
        onBack={leaveMeetingPage}
        onOpenPerson={openRelationship}
        onOpenMeeting={(meeting) => openMeetingPage(meeting.message_id)}
        onTitle={(title) => (pageSubject = title)}
      />
    {:else}
      <MeetingsWorkspace
        {client}
        person={exploreState.current.meetingPerson}
        source={exploreState.current.meetingSource}
        since={exploreState.current.meetingSince}
        onFiltersChange={(patch) => commitNavigation(patch)}
        onOpenMeeting={openMeetingPage}
      />
    {/if}
  {:else if exploreState.current.workspace === 'message' && exploreState.current.messageID !== null}
    <MessagePage
      {client}
      messageID={exploreState.current.messageID}
      onBack={leaveMessagePage}
      onSubject={(subject) => (pageSubject = subject)}
      onOpenPerson={openRelationship}
    />
  {:else if exploreState.current.workspace === 'saved_views'}
    <SavedViewsWorkspace
      {client}
      currentState={exploreState.current}
      selection={selection.snapshot()}
      onOpen={(state) => {
        void openSavedView(state);
      }}
    />
  {:else if exploreState.current.workspace === 'sources'}
    <SourcesWorkspace {client} onOpenOperations={() => openOperations('messages', 'source_sync')} />
  {:else if exploreState.current.workspace === 'operations'}
    <OperationsWorkspace
      {client}
      controller={operationsController}
      state={{
        operationLane: exploreState.current.operationLane,
        operationKind: exploreState.current.operationKind,
        operationState: exploreState.current.operationState,
        operationStartedFrom: exploreState.current.operationStartedFrom,
        operationStartedBefore: exploreState.current.operationStartedBefore,
        operationRunID: exploreState.current.operationRunID,
        operationStatus: exploreState.current.operationStatus
      }}
      onStateChange={(patch) => commitNavigation(patch)}
      onNavigate={openOperationAuthority}
      onConfigure={openOperationConfiguration}
      onAnnounce={announceOperation}
    />
  {:else if exploreState.current.workspace === 'deletions'}
    <DeletionsWorkspace
      {client}
      selection={apiSelection}
      reviewOnMount={pendingDeletionReview === apiSelection?.mode}
      onReviewStarted={() => {
        pendingDeletionReview = undefined;
      }}
    />
  {:else if exploreState.current.workspace === 'relationships'}
    <RelationshipsWorkspace
      {client}
      controller={relationshipsController}
      facet={exploreState.current.relationshipFacet}
      target={exploreState.current.relationshipTarget}
      showAll={exploreState.current.relationshipShowAll}
      filesOpen={exploreState.current.relationshipFiles}
      predicate={exploreState.predicate()}
      personFilePresentation={exploreState.current.personFilePresentation}
      personFileDirections={exploreState.current.personFileDirections}
      onFacetChange={(relationshipFacet) => commitNavigation({ relationshipFacet })}
      onTargetChange={(relationshipTarget) => {
        // Leaving a contact page (Esc) returns to the People list.
        if (relationshipTarget === null && exploreState.current.relationshipTarget?.startsWith('cluster:')) openPeopleList();
        else commitNavigation({ relationshipTarget, relationshipFiles: false });
      }}
      onShowAllChange={(relationshipShowAll) => commitNavigation({ relationshipShowAll })}
      onFilesToggle={(relationshipFiles) => commitNavigation({ relationshipFiles })}
      onPersonFilePresentationChange={(personFilePresentation) =>
        commitNavigation({
          personFilePresentation,
          activeRow: null,
          selectedRow: null,
          scrollAnchor: null,
        })}
      onPersonFileDirectionsChange={(personFileDirections) =>
        commitNavigation({
          personFileDirections,
          activeRow: null,
          selectedRow: null,
          scrollAnchor: null,
        })}
      onOpenEverything={() => commitWorkspace('everything')}
      onPromotePerson={promoteRelationshipParticipant}
      onOpenDirectoryPerson={openDirectoryPerson}
      onAnnounce={announceOperation}
      layout={exploreState.current.relationshipTarget?.startsWith('cluster:') ? 'contact' : 'hub'}
      personTab={exploreState.current.personTab}
      onTabChange={changePersonTab}
      onBack={openPeopleList}
      onOpenFileItem={openFileItem}
      onOpenFileConversation={openFileConversation}
      onOpenMeeting={(meeting) => void openArchivedMeeting(meeting)}
    />
  {:else if exploreState.current.workspace === 'directory'}
    {#if exploreState.current.directoryPersonID !== null}
      <SavedPersonPage
        {client}
        controller={directoryController}
        personID={exploreState.current.directoryPersonID}
        tab={exploreState.current.personTab}
        onTabChange={changePersonTab}
        onBack={openPeopleList}
        onOpenPerson={openDirectoryPerson}
        onOpenCardDAVConflict={openCardDAVConflict}
        onOpenCardDAVSettings={openCardDAVSettings}
        onAnnounce={announceOperation}
        onOpenMeeting={(meeting) => void openArchivedMeeting(meeting)}
        onOpenMessage={(messageID) => void openMessageByID(messageID)}
        onOpenMeetingPage={openMeetingPage}
      />
    {:else}
      <PeopleWorkspace
        hub={peopleHub}
        filters={peopleFilters}
        savedError={directoryController.error}
        savedPageError={directoryController.pageError}
        savedPageRecovery={directoryController.pageRecovery}
        onReloadSaved={() => void directoryController.reloadFirstPage()}
        onFiltersChange={changePeopleFilters}
        onOpen={openPeopleRow}
        onOpenDomains={() => commitNavigation({ workspace: 'relationships', relationshipFacet: 'domains', relationshipTarget: null })}
      />
    {/if}
  {:else if exploreState.current.workspace === 'directory_review'}
    <DirectoryReviewWorkspace
      controller={directoryReviewController}
      relationshipController={relationshipReviewController}
      factController={factLedgerController}
      directoryPersonID={exploreState.current.directoryPersonID}
      onOpenDirectory={() => commitWorkspace('directory')}
      onOpenPerson={openDirectoryPerson}
      onAnnounce={announceOperation}
      onDecided={() => void pendingReviews.refresh(true)}
    />
  {:else if exploreState.current.workspace === 'files'}
    <div class="files-shell">
      <ContextBar
        {client}
        query={exploreState.current.query}
        searchMode={exploreState.current.searchMode}
        filters={exploreState.current.filters}
        groupingChain={exploreState.current.groupingChain}
        totalCount={exploreState.current.groupingChain.length > 0 ? loader.result?.totalCount : undefined}
        presentation="files"
        onPresentationChange={(presentation) => {
          if (presentation === 'files') return;
          commitNavigation({
            workspace: 'everything',
            presentation,
            analysisTarget: null,
            selectedIdentifier: null,
            activeRow: null,
            selectedRow: null,
            conversationAnchor: null,
            scrollAnchor: null,
          });
        }}
        onAddGroup={(dimension) => commitGrouping(dimension)}
        onRemoveGroup={(index) =>
          commitNavigation({
            groupingChain: exploreState.current.groupingChain.filter((_, position) => position !== index),
            activeRow: null,
            scrollAnchor: null,
          })}
        onClearFilters={() => commitNavigation({ filters: [], activeRow: null, scrollAnchor: null })}
        onFiltersChange={(filters) =>
          commitNavigation({
            filters,
            activeRow: null,
            selectedRow: null,
            scrollAnchor: null,
          })}
      />
      <span class="kit-sr-only" role="status" aria-label="Sort status" aria-live="polite">{sortNotice}</span>
      {#if exploreState.current.groupingChain.length > 0}
        <GroupTable
          rows={loader.groupRows}
          dimension={exploreState.current.groupingChain[0]!}
          workspaceLabel="Files"
          loading={loader.loading}
          loadingMore={loader.loadingMore}
          hasMore={Boolean(loader.nextCursor)}
          totalCount={loader.result?.totalCount}
          generation={loader.resultGeneration}
          error={loader.error}
          pageError={loader.pageError}
          unavailable={loader.unavailable}
          drillable={groupingByDimension(exploreState.current.groupingChain[0]!).drillable}
          focusedKey={exploreState.current.activeRow}
          inspectedKey={readingTargetKey}
          scrollAnchor={exploreState.current.scrollAnchor}
          restoring={loader.restoring}
          onDrill={drillGroup}
          onInspect={drillGroup}
          onLoadMore={loader.loadMore}
          onLoadThroughEnd={loader.loadThroughEnd}
          onActiveKey={(activeRow) => replaceTransient({ activeRow })}
          onScrollAnchor={(key, offset) => replaceTransient({ scrollAnchor: { key, offset } })}
          onRetry={loader.retry}
        />
      {:else}
        <FilesWorkspace
          {client}
          predicate={{ ...exploreState.predicate(), grouping: undefined }}
          sort={exploreState.current.fileSort ?? { field: 'occurred_at', direction: 'desc' }}
          filenameQuery={exploreState.current.fileFilenameQuery}
          mimeFamilies={exploreState.current.fileMIMEFamilies}
          activeKey={exploreState.current.activeRow}
          selectedKey={exploreState.current.selectedRow}
          restorationEpoch={exploreState.restorationEpoch}
          onRestorationComplete={(epoch) => {
            exploreState.acknowledgeRestoration(epoch);
          }}
          onSortChange={(fileSort: FileSearchSort) =>
            commitNavigation({
              fileSort,
              activeRow: null,
              scrollAnchor: null,
            })}
          onFilenameQueryChange={(fileFilenameQuery) =>
            debouncedSearchPatch({
              fileFilenameQuery,
              activeRow: null,
              selectedRow: null,
              scrollAnchor: null,
            })}
          onMIMEFamiliesChange={(fileMIMEFamilies) =>
            commitNavigation({
              fileMIMEFamilies,
              activeRow: null,
              selectedRow: null,
              scrollAnchor: null,
            })}
          onActiveKey={(activeRow) => replaceTransient({ activeRow })}
          onSelectedKey={(selectedRow) =>
            selectedRow ? commitNavigation({ selectedRow }) : replaceCommittedNavigation({ selectedRow: null })}
          onOpenItem={openFileItem}
          onOpenConversation={openFileConversation}
        />
      {/if}
    </div>
  {:else}
    <EverythingWorkspace
      {client}
      {exploreState}
      {loader}
      session={everythingSession}
      {selection}
      {enabled}
      {readingTargetKey}
      {conversationAnchorId}
      {sortNotice}
      bind:searchInput
      {selectionPreflight}
      meetingSelection={apiSelection}
      exportSelection={() => void exportSelection()}
      {commitNavigation}
      {commitWorkspace}
      {commitGrouping}
      {commitSearch}
      {fixedSortNotice}
      {focusGrid}
      {openRow}
      {drillGroup}
      {openFileItem}
      {openContextualFile}
      closeReadingPane={() => void closeReadingPane()}
      {openRelationship}
      openAttachment={openAttachmentTarget}
      {embeddingsLocal}
      {followRow}
      {changeConversationAnchor}
      onOpenMeeting={(meeting) => void openArchivedMeeting(meeting)}
    />
  {/if}
</div>

{#if archiveMeetingID !== undefined}
  <ArchivedMeetingReader {client} message={archivedMeeting.detail?.id === archiveMeetingID ? archivedMeeting.detail : undefined}
    loading={archivedMeeting.loading} error={archivedMeeting.error} predicate={exploreState.predicate()}
    anchorID={conversationAnchorId} onClose={closeArchivedMeeting}
    onReload={() => void archivedMeeting.load(archiveMeetingID!)}
    onOpenMeeting={(meeting) => void openArchivedMeeting(meeting)} onAnchorChange={changeConversationAnchor} />
{:else if archivedMeeting.loading}
  <p class="archive-navigation-status" role="status">Loading archived meeting…</p>
{:else if archivedMeeting.error}
  <p class="archive-navigation-status" role="alert">{archivedMeeting.error}</p>
{/if}

<CommandPalette bind:open={paletteOpen} commands={paletteCommands} ariaLabel="Commands" onrun={runPalette} />

{#if keyboardHelpOpen}
  <KeyboardHelp
    commands={commandRegistry}
    onclose={() => {
      keyboardHelpOpen = false;
    }}
  />
{/if}

{#if contextualViewerFile}
  <FileViewer
    {client}
    file={contextualViewerFile}
    returnFocus={contextualViewerReturnFocus}
    onClose={() => {
      void closeContextualViewer();
    }}
    onOpenItem={(entryKey) => {
      contextualViewerFile = undefined;
      openFileItem(entryKey);
    }}
    onOpenConversation={(entryKey, messageID, conversationID) => {
      contextualViewerFile = undefined;
      openFileConversation(entryKey, messageID, conversationID);
    }}
  />
{/if}

<style>
  .archive-navigation-status { padding: var(--space-3) var(--space-5); color: var(--text-secondary); }

  .app-shell {
    display: flex;
    min-width: 0;
    min-height: 100vh;
    height: 100vh;
    flex-direction: column;
    overflow: hidden;
    background: var(--surface-canvas);
    color: var(--text-primary);
  }

  .brand {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--font-size-md);
    font-weight: 600;
    letter-spacing: 0.01em;
  }

  .brand span {
    color: var(--artifact-ink);
    font-size: var(--font-size-sm);
  }

  /* Integrated app-bar tabs: quiet text buttons with a soft active pill
   * instead of kit-ui's detached inset track. */
  .app-shell :global(.kit-top-bar__tabs) {
    gap: var(--space-1);
    padding: 0;
    background: transparent;
    border-radius: 0;
  }

  .app-shell :global(.kit-top-bar__tab) {
    padding: 5px 12px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-md);
  }

  /* The Reviews dot sits in the tab's corner so it never changes the
   * tab's width or the collapse measurement. */
  .app-shell :global(.kit-top-bar__tab) {
    position: relative;
  }

  .app-shell :global(.kit-top-bar__tab .kit-top-bar__tab-dot) {
    position: absolute;
    top: 3px;
    right: 3px;
  }

  .app-shell :global(.kit-top-bar__tab.active) {
    background: var(--surface-well);
    box-shadow: none;
  }

  .archive-state {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    margin-left: var(--space-3);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    white-space: nowrap;
  }

  .app-shell :global(.gear-trigger) {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border: 0;
    border-radius: var(--radius-md);
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
  }

  .app-shell :global(.gear-trigger:hover) {
    background: var(--surface-well);
    color: var(--text-primary);
  }

  .sub-tabs__list {
    display: flex;
    gap: var(--space-2);
  }

  .sub-tabs {
    display: flex;
    padding: 0 var(--space-7);
    border-bottom: 1px solid var(--hairline);
    background: var(--surface-canvas);
  }

  .sub-tabs [role='tab'] {
    margin-bottom: -1px;
    border: 0;
    border-bottom: 2px solid transparent;
    padding: var(--space-2) var(--space-3);
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    font-weight: 500;
    cursor: pointer;
  }

  .sub-tabs [role='tab']:hover { color: var(--text-primary); }
  .sub-tabs [role='tab'][aria-selected='true'] { border-bottom-color: var(--accent-blue); color: var(--text-primary); }
  .sub-tabs [role='tab']:focus-visible { outline: var(--focus-ring); outline-offset: -2px; }

  .browser-appearance {
    display: grid;
    gap: var(--space-2);
    margin-bottom: var(--space-5);
  }

  .browser-appearance h3,
  .browser-appearance p { margin: 0; }
  .browser-appearance p { color: var(--text-secondary); font-size: var(--font-size-sm); }

  .browser-appearance__controls {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
  }

  .files-shell {
    display: flex;
    width: 100%;
    max-width: 1760px;
    min-height: 0;
    flex: 1;
    flex-direction: column;
    gap: var(--space-4);
    margin-inline: auto;
    padding: var(--space-6) var(--space-7) var(--space-4);
  }
</style>
