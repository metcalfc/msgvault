<script lang="ts">
  import { Button, EmptyState, virtualSlice } from '@kenn-io/kit-ui';
  import { onDestroy, onMount, tick, untrack } from 'svelte';

  import type {
    EntryRow,
    ExploreCacheUnavailable,
    ExploreColumn,
    ExploreScrollAnchor,
    ExploreSearchMode
  } from '../../explore/models';
  import { hasFreeText } from '../../search/query';
  import { DEFAULT_EXPLORE_COLUMNS, EXPLORE_COLUMN_LABELS, isEmailMessageType } from '../../explore/models';
  import type { ExploreSelectionState } from '../../explore/state.svelte';
  import { rebaseVirtualScroll, RowGeometry, tableViewportHeight } from '../../theme/preferences.svelte';
  import PaperclipIcon from '@lucide/svelte/icons/paperclip';
  import IdentityBadge from './IdentityBadge.svelte';
  import {
    duplicateEventAccounts, highlightSegments, highlightTerms, listTime, rowPeople, threadRows, dayEventRows, type ThreadRole
  } from '../../explore/row-display';
  import { SvelteSet } from 'svelte/reactivity';
  import { decodeHTMLEntities } from '../../util/html-text';
  import RowKind, { rowModality } from './RowKind.svelte';

  interface Props {
    rows: EntryRow[];
    selection: ExploreSelectionState;
    columns?: ExploreColumn[];
    columnWidths?: Partial<Record<ExploreColumn, number>>;
    loading?: boolean;
    loadingMore?: boolean;
    hasMore?: boolean;
    totalCount?: number;
    generation?: number;
    error?: string;
    pageError?: string;
    unavailable?: ExploreCacheUnavailable;
    focusedKey?: string | null;
    inspectedKey?: string | null;
    scrollAnchor?: ExploreScrollAnchor | null;
    restoring?: boolean;
    onOpen?: (row: EntryRow) => void;
    onScrollAnchor?: (key: string, offset: number) => void;
    onLoadMore?: () => Promise<unknown>;
    onLoadThroughEnd?: () => Promise<void>;
    onActiveKey?: (key: string) => void;
    onVisibleRows?: (rowKeys: string[]) => void;
    onRetry?: () => void;
    /** The committed query and mode, so an empty result can offer the
     * other search modes and the operator syntax. */
    query?: string;
    searchMode?: ExploreSearchMode;
    onTrySearchMode?: (mode: ExploreSearchMode) => void;
    /** Called after a keyboard move (j/k, arrows, paging) lands on a row,
     * so an open reading pane can follow the selection. */
    onKeyboardMove?: (row: EntryRow) => void;
    /** With the reading pane open, ←/→ step within its thread instead of
     * expanding and collapsing grouped threads here. */
    readerOpen?: boolean;
    /** Collapse each day's calendar events into one line (the Inbox). */
    collapseDayEvents?: boolean;
  }

  let {
    rows: sourceRows,
    selection,
    columns: providedColumns = DEFAULT_EXPLORE_COLUMNS,
    columnWidths = {},
    loading = false,
    loadingMore = false,
    hasMore = false,
    totalCount = undefined,
    generation = 0,
    error = '',
    pageError = '',
    unavailable = undefined,
    focusedKey = null,
    inspectedKey = null,
    scrollAnchor = null,
    restoring = false,
    onOpen = undefined,
    onScrollAnchor = undefined,
    onLoadMore = undefined,
    onLoadThroughEnd = undefined,
    onActiveKey = undefined,
    onVisibleRows = undefined,
    onRetry = undefined,
    query = '',
    searchMode = 'full_text',
    onTrySearchMode = undefined,
    onKeyboardMove = undefined,
    readerOpen = false,
    collapseDayEvents = false
  }: Props = $props();

  // Email hits from one thread collapse into their newest match; the
  // other matches follow it inline once the thread is expanded. Keys are
  // untouched, so selection and the reading pane still address messages.
  const expandedThreads = new SvelteSet<string>();
  // Threads the user collapsed: they stay closed even while a member is
  // still focused or inspected, until the user expands them again.
  const collapsedThreads = new SvelteSet<string>();
  // Without a query (the Inbox), a day's calendar events collapse into one
  // "N events" line instead, and the same expand and collapse controls apply.
  const threaded = $derived(
    (query.trim() ? threadRows : collapseDayEvents ? dayEventRows : noGrouping)(
      sourceRows, expandedThreads,
      new Set([focusedKey, inspectedKey].filter((key): key is string => Boolean(key))), collapsedThreads
    )
  );
  function noGrouping(source: readonly EntryRow[]): { rows: EntryRow[]; roles: Map<string, ThreadRole>; hidden: number } {
    return { rows: [...source], roles: new Map<string, ThreadRole>(), hidden: 0 };
  }
  function isEventDay(role: ThreadRole | undefined): boolean {
    return Boolean(role?.threadKey.startsWith('events:'));
  }
  // A collapsed day line previews its events' titles.
  const eventDayTitles = $derived.by(() => {
    const titles = new Map<string, string[]>();
    for (const row of sourceRows) {
      const role = threaded.roles.get(row.key);
      if (!role || !isEventDay(role)) continue;
      titles.set(role.threadKey, [...(titles.get(role.threadKey) ?? []), row.title || '(untitled)']);
    }
    return titles;
  });
  const rows = $derived(threaded.rows);

  function setThreadOpen(threadKey: string, open: boolean): void {
    if (open) {
      collapsedThreads.delete(threadKey);
      expandedThreads.add(threadKey);
      return;
    }
    // Collapsing hides the thread's other matches, so focus and the reading
    // pane move from a hidden member to the thread's lead first.
    const lead = sourceRows.find((row) => {
      const role = threaded.roles.get(row.key);
      return role?.threadKey === threadKey && role.lead;
    });
    const hidden = (key: string | null): boolean => {
      const role = key ? threaded.roles.get(key) : undefined;
      return Boolean(role && role.threadKey === threadKey && !role.lead);
    };
    if (lead) {
      if (hidden(activeKey)) {
        activeKey = lead.key;
        onActiveKey?.(lead.key);
      }
      if (hidden(inspectedKey)) onOpen?.(lead);
    }
    expandedThreads.delete(threadKey);
    collapsedThreads.add(threadKey);
  }

  // Semantic and hybrid need free text to embed, so they are offered only
  // for a query that has some.
  const alternativeModes = $derived(
    onTrySearchMode && hasFreeText(query)
      ? (['semantic', 'hybrid'] as const).filter((mode) => mode !== searchMode)
      : []
  );
  const OPERATOR_HINTS: ReadonlyArray<{ syntax: string; meaning: string }> = [
    { syntax: 'from:alice@example.com', meaning: 'sent by' },
    { syntax: 'to:bob@example.com', meaning: 'sent to' },
    { syntax: 'subject:invoice', meaning: 'subject contains' },
    { syntax: 'has:attachment', meaning: 'has files' },
    { syntax: 'after:2025-01-01', meaning: 'on or after a date' },
    { syntax: 'before:2025-06-30', meaning: 'before a date' },
    { syntax: 'message_type:imessage', meaning: 'one kind of item' }
  ];

  const geometry = new RowGeometry();
  const rowHeight = $derived(geometry.height);
  const OVERSCAN = 6;

  let gridElement = $state<HTMLDivElement>();
  let headerElement = $state<HTMLDivElement>();
  let scrollTop = $state(0);
  let viewport = $state(360);
  let activeKey = $state<string | null>(untrack(() => focusedKey ?? rows[0]?.key ?? null));
  // The Columns picker lives in the ContextBar; the table renders whatever
  // set the workspace hands it.
  const visibleColumns = $derived(providedColumns);
  let restoredAnchor = '';
  let previousRowCount = untrack(() => rows.length);
  let suppressedScrollTop: number | undefined;
  let densityRebasing = false;
  let commandScrolling = false;
  let scrollIntent = 0;
  let visibleRowSignature = '';
  let previousRowHeight = untrack(() => rowHeight);

  $effect(() => {
    const nextHeight = rowHeight;
    const previousHeight = previousRowHeight;
    previousRowHeight = nextHeight;
    if (!gridElement || nextHeight === undefined || previousHeight === undefined || previousHeight === nextHeight) return;
    const element = gridElement;
    densityRebasing = true;
    scrollIntent += 1;
    const preservedKey = activeKey;
    const focusedIndex = activeKey ? rows.findIndex((row) => row.key === activeKey) : -1;
    const rebased = focusedIndex >= 0
      ? focusedIndex * nextHeight
      : rebaseVirtualScroll(scrollTop, previousHeight, nextHeight);
    const expectedScrollHeight = rows.length * nextHeight;
    requestAnimationFrame(() => applyDensityRebase(
      element, nextHeight, expectedScrollHeight, rebased, preservedKey
    ));
  });

  $effect(() => {
    if (!focusedKey) return;
    const index = rows.findIndex((row) => row.key === focusedKey);
    if (index >= 0) {
      const isRestoring = untrack(() => restoring);
      activeKey = focusedKey;
      if (!isRestoring || !scrollAnchor) scrollActiveIntoView(index);
      if (isRestoring && gridElement) suppressedScrollTop = gridElement.scrollTop;
    }
  });

  $effect(() => {
    const height = rowHeight;
    if (!gridElement || !scrollAnchor || height === undefined) return;
    const signature = anchorSignature(scrollAnchor);
    if (signature === restoredAnchor) return;
    const index = rows.findIndex((row) => row.key === scrollAnchor.key);
    if (index < 0) return;
    const element = gridElement;
    const target = index * height + scrollAnchor.offset;
    const intent = scrollIntent;
    restoredAnchor = signature;
    afterVirtualLayout(element, rows.length * height, () => {
      if (gridElement !== element || restoredAnchor !== signature || scrollIntent !== intent) return;
      element.scrollTop = target;
      scrollTop = element.scrollTop;
      suppressedScrollTop = element.scrollTop;
    });
  });

  $effect(() => {
    rows;
    if (rows.length < previousRowCount) restoredAnchor = '';
    previousRowCount = rows.length;
    if (rows.length === 0) activeKey = null;
    else {
      const key = untrack(() => activeKey);
      const index = key ? rows.findIndex((row) => row.key === key) : -1;
      const anchorWasRestored = scrollAnchor !== null &&
        restoredAnchor === anchorSignature(scrollAnchor);
      if (index < 0 && !restoring) {
        activeKey = rows[0]!.key;
        onActiveKey?.(activeKey);
      }
      else if (!anchorWasRestored) scrollActiveIntoView(index);
    }
  });

  const slice = $derived.by(() => {
    const height = rowHeight;
    if (height === undefined) return undefined;
    return virtualSlice({
      scrollTop,
      viewport,
      count: rows.length,
      overscan: OVERSCAN,
      fixedHeight: height,
      heightOf: () => height
    });
  });
  const renderedRows = $derived(slice ? rows.slice(slice.start, slice.end) : []);
  const accessibilityRowCount = $derived.by(() => {
    if (loading || loadingMore || unavailable || error || pageError) return undefined;
    if (rows.length === 0 || !slice || rowHeight === undefined) return 2;
    // Collapsed threads make the loaded count differ from the total.
    if (threaded.hidden > 0) return -1;
    return (totalCount ?? rows.length) + 1;
  });

  $effect(() => {
    const keys = renderedRows.filter((row) => row.kind === 'conversation').map((row) => row.key);
    const signature = keys.join('\u0000');
    if (signature === visibleRowSignature) return;
    visibleRowSignature = signature;
    onVisibleRows?.(keys);
  });
  const activeIndex = $derived(activeKey ? rows.findIndex((row) => row.key === activeKey) : -1);
  const activeRow = $derived(activeIndex >= 0 ? rows[activeIndex] : undefined);
  const template = $derived(
    visibleColumns
      .map((column) => {
        const configured = columnWidths[column];
        if (configured !== undefined) return `${configured}px`;
        // The kind reads as a colored glyph; its word is the accessible name.
        if (column === 'kind') return '40px';
        if (column === 'people') return 'minmax(150px, 1.2fr)';
        if (column === 'title') return 'minmax(180px, 1.5fr)';
        if (column === 'excerpt') return 'minmax(220px, 2fr)';
        if (column === 'time') return '112px';
        if (column === 'attachments') return '42px';
        return '76px';
      })
      .join(' ')
  );

  onMount(() => {
    if (!gridElement) return;
    viewport = measuredViewport(gridElement);
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => {
      if (gridElement) viewport = measuredViewport(gridElement);
    });
    observer.observe(gridElement);
    if (headerElement) observer.observe(headerElement);
    return () => observer.disconnect();
  });
  onDestroy(() => geometry.destroy());

  function rowId(row: EntryRow): string {
    const encoded = row.key.replace(
      /[^a-zA-Z0-9_-]/g,
      (character) => `-${character.codePointAt(0)?.toString(16) ?? 'x'}-`
    );
    return `everything-row-${encoded}`;
  }

  function anchorSignature(anchor: ExploreScrollAnchor): string {
    return `${generation}:${anchor.key}:${anchor.offset}:${rows.length}`;
  }

  // Lexical terms are highlighted only where the daemon matched text
  // lexically; a semantic-only excerpt is the strongest passage as ranked.
  const terms = $derived(searchMode === 'semantic' ? [] : highlightTerms(query));
  const alsoIn = $derived(duplicateEventAccounts(rows));

  function fullTime(value: string): string {
    const date = new Date(value);
    return Number.isNaN(date.valueOf())
      ? value
      : new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
  }

  function formatBytes(value: number): string {
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${Math.round(value / 1024)} KB`;
    return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  }

  function scrollActiveIntoView(index: number): void {
    const height = rowHeight;
    if (!gridElement || height === undefined) return;
    const top = index * height;
    const bottom = top + height;
    const visibleHeight = measuredViewport(gridElement);
    let target = gridElement.scrollTop;
    if (top < target) target = top;
    else if (bottom > target + visibleHeight) target = bottom - visibleHeight;
    scrollTop = target;
    gridElement.scrollTop = target;
    scrollTop = gridElement.scrollTop;
  }

  function measuredViewport(element: HTMLDivElement): number {
    return tableViewportHeight(
      element.clientHeight,
      headerElement?.offsetHeight || 30,
      window.innerHeight
    );
  }

  function applyDensityRebase(
    element: HTMLDivElement,
    height: number,
    expectedScrollHeight: number,
    rebased: number,
    preservedKey: string | null,
  ): void {
    afterVirtualLayout(element, expectedScrollHeight, () => {
      if (gridElement !== element || previousRowHeight !== height) return;
      suppressedScrollTop = rebased;
      element.scrollTop = rebased;
      scrollTop = element.scrollTop;
      suppressedScrollTop = element.scrollTop;
      if (preservedKey && rows.some((row) => row.key === preservedKey)) {
        activeKey = preservedKey;
        onActiveKey?.(preservedKey);
      }
      requestAnimationFrame(() => {
        if (previousRowHeight === height) densityRebasing = false;
      });
    });
  }

  function afterVirtualLayout(
    element: HTMLDivElement,
    expectedScrollHeight: number,
    callback: () => void
  ): void {
    if (!element.isConnected) return;
    if (element.scrollHeight === 0 || (
      element.scrollHeight >= expectedScrollHeight && element.clientHeight <= window.innerHeight
    )) {
      callback();
      return;
    }
    requestAnimationFrame(() => afterVirtualLayout(
      element, expectedScrollHeight, callback
    ));
  }

  function waitForVirtualLayout(element: HTMLDivElement, expectedScrollHeight: number): Promise<void> {
    return new Promise((resolve) => afterVirtualLayout(element, expectedScrollHeight, resolve));
  }

  async function moveTo(index: number): Promise<void> {
    const height = rowHeight;
    if (rows.length === 0 || height === undefined) return;
    if (index >= rows.length && hasMore && !loadingMore) await onLoadMore?.();
    commandScrolling = true;
    scrollIntent += 1;
    const nextIndex = Math.max(0, Math.min(rows.length - 1, index));
    const nextKey = rows[nextIndex]?.key ?? null;
    activeKey = nextKey;
    if (activeKey) onActiveKey?.(activeKey);
    await tick();
    if (!gridElement || gridElement.scrollHeight === 0) commandScrolling = false;
    else requestAnimationFrame(() => requestAnimationFrame(() => {
      commandScrolling = false;
    }));
    if (gridElement) await waitForVirtualLayout(gridElement, rows.length * height);
    scrollActiveIntoView(nextIndex);
    activeKey = nextKey;
    if (activeKey) onActiveKey?.(activeKey);
    const landed = rows[nextIndex];
    if (landed) onKeyboardMove?.(landed);
    await tick();
  }

  function editableTarget(target: EventTarget | null): boolean {
    const element = target as HTMLElement | null;
    return Boolean(
      element?.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"]), iframe')
    );
  }

  async function handleKeydown(event: KeyboardEvent): Promise<void> {
    const height = rowHeight;
    if (event.target !== gridElement || editableTarget(event.target) || rows.length === 0 || height === undefined) return;
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    if (event.key === 'j' || event.key === 'ArrowDown') await moveTo(activeIndex + 1);
    else if (event.key === 'k' || event.key === 'ArrowUp') await moveTo(activeIndex - 1);
    else if (!readerOpen && (event.key === 'ArrowRight' || event.key === 'ArrowLeft') && activeRow && threaded.roles.has(activeRow.key)) {
      const role = threaded.roles.get(activeRow.key)!;
      if (event.key === 'ArrowRight') setThreadOpen(role.threadKey, true);
      else setThreadOpen(role.threadKey, false);
    }
    else if (event.key === 'Home') await moveTo(0);
    else if (event.key === 'End') {
      if (hasMore) await onLoadThroughEnd?.();
      await moveTo(rows.length - 1);
    }
    else if (event.key === 'PageDown') await moveTo(activeIndex + Math.max(1, Math.floor(viewport / height)));
    else if (event.key === 'PageUp') await moveTo(activeIndex - Math.max(1, Math.floor(viewport / height)));
    else if (event.key === ' ' && activeRow) {
      selection.toggle(activeRow.key, activeIndex, rows.map((row) => row.key), event.shiftKey);
    } else if (event.key.toLowerCase() === 'a') {
      const firstVisible = Math.max(0, Math.ceil(scrollTop / height));
      const lastVisible = Math.min(
        rows.length - 1,
        Math.ceil((scrollTop + viewport) / height) - 1
      );
      selection.selectVisible(rows.slice(firstVisible, lastVisible + 1).map((row) => row.key));
    } else if (event.key === 'x') {
      selection.clear();
    } else if (event.key === 'Enter' && activeRow) {
      onOpen?.(activeRow);
    } else return;
    event.preventDefault();
  }

  function handleScroll(): void {
    scrollTop = gridElement?.scrollTop ?? 0;
    const height = rowHeight;
    if (height === undefined || !slice) return;
    if (densityRebasing || commandScrolling) return;
    if (suppressedScrollTop !== undefined) {
      const isProgrammaticRestoration = Math.abs(scrollTop - suppressedScrollTop) < 0.5;
      suppressedScrollTop = undefined;
      if (isProgrammaticRestoration) return;
    }
    const first = Math.min(rows.length - 1, Math.max(0, Math.floor(scrollTop / height)));
    const row = rows[first];
    if (row && !restoring) {
      const lastVisible = Math.min(
        rows.length - 1,
        Math.ceil((scrollTop + viewport) / height) - 1
      );
      if (activeIndex < first || activeIndex > lastVisible) {
        activeKey = row.key;
        onActiveKey?.(row.key);
      }
      onScrollAnchor?.(row.key, scrollTop - first * height);
    }
    if (!restoring && hasMore && !loadingMore && slice.end >= rows.length - OVERSCAN) void onLoadMore?.();
  }
</script>

<section class="everything-table" aria-label="Message table">
  <div
    class="table-grid"
    bind:this={gridElement}
    role="grid"
    data-scroll
    aria-label="Message results"
    aria-rowcount={accessibilityRowCount}
    aria-colcount={visibleColumns.length}
    aria-busy={loading || loadingMore}
    aria-activedescendant={activeRow ? rowId(activeRow) : undefined}
    tabindex="0"
    onkeydown={handleKeydown}
    onscroll={handleScroll}
  >
    <div class="table-header" bind:this={headerElement} role="row" style:grid-template-columns={template}>
      {#each visibleColumns as column (column)}
        <span
          role="columnheader"
          class={`header-cell header-cell--${column}`}
          aria-label={column === 'attachments' ? 'Attachments' : undefined}
        >
          {#if column === 'attachments'}<PaperclipIcon size={12} aria-hidden="true" /><span class="kit-sr-only">Attachments</span>{:else}{EXPLORE_COLUMN_LABELS[column]}{/if}
        </span>
      {/each}
    </div>
    <div
      class="table-body"
      role="rowgroup"
    >
      {#if unavailable}
        <div role="row"><div role="gridcell" aria-colspan={visibleColumns.length}><div class="cache-unavailable" role="alert">
            <strong>{unavailable.readiness === 'building' ? 'Preparing analytical cache' : 'Analytical cache unavailable'}</strong>
            <span>{unavailable.message}</span>
            {#if unavailable.readiness === 'building'}
              <span>This view will refresh automatically.</span>
            {:else}
              <span>Rebuild it with <code>{unavailable.recovery_action}</code>, then retry.</span>
              <div><Button label="Retry cache check" tone="info" surface="outline" onclick={() => onRetry?.()} /></div>
            {/if}
          </div>
        </div>
        </div>
      {:else if error}
        <div role="row"><div role="gridcell" aria-colspan={visibleColumns.length}><div class="request-error" role="alert">
            <span>{error}</span>
            <Button
              label={restoring ? 'Retry restoration' : 'Retry request'}
              tone="info"
              surface="outline"
              onclick={() => onRetry?.()}
            />
          </div>
        </div>
        </div>
      {:else if loading && rows.length === 0}
        <div class="skeletons">
          {#each { length: 10 } as _, index (index)}
            <div
              class="skeleton-row"
              data-testid="everything-skeleton"
              role="row"
              style:grid-template-columns={template}
            >
              {#each visibleColumns as column (column)}
                <span class="skeleton-cell" role="gridcell"><i></i></span>
              {/each}
            </div>
          {/each}
        </div>
      {:else if rows.length === 0}
        <div role="row"><div class="empty" role="gridcell" aria-colspan={visibleColumns.length}>
          <EmptyState title="No items match this view" description="Adjust the search or clear filters to widen the view.">
            {#if alternativeModes.length > 0}
              <div class="empty-actions">
                {#each alternativeModes as mode (mode)}
                  <Button
                    size="sm"
                    surface="outline"
                    tone="info"
                    label={mode === 'semantic' ? 'Try semantic' : 'Try hybrid'}
                    onclick={() => onTrySearchMode?.(mode)}
                  />
                {/each}
              </div>
            {/if}
            <dl class="operator-hints" aria-label="Search operators">
              {#each OPERATOR_HINTS as hint (hint.syntax)}
                <div><dt><code>{hint.syntax}</code></dt><dd>{hint.meaning}</dd></div>
              {/each}
            </dl>
          </EmptyState>
        </div></div>
      {:else if !slice || rowHeight === undefined}
        <div role="row"><div role="gridcell" aria-colspan={visibleColumns.length}><p class="empty" role="status">Preparing table layout…</p></div></div>
      {:else}
        <div class="virtual-spacer" style:height={`${slice.totalHeight}px`}>
          <div class="virtual-window" style:transform={`translateY(${slice.topPad}px)`}>
            {#each renderedRows as row, offset (row.key)}
              {@const index = slice.start + offset}
              <!-- svelte-ignore a11y_click_events_have_key_events -- Enter on
                   the focused grid opens the same row via handleKeydown. -->
              <div
                class="data-row"
                id={rowId(row)}
                data-list-row
                data-active={index === activeIndex}
                data-row-key={row.key}
                data-thread-member={threaded.roles.get(row.key) && !threaded.roles.get(row.key)!.lead ? 'true' : undefined}
                role="row"
                tabindex="-1"
                aria-rowindex={index + 2}
                aria-selected={selection.isSelected(row.key)}
                aria-current={inspectedKey === row.key ? 'true' : undefined}
                style:grid-template-columns={template}
                onpointerdown={() => {
                  activeKey = row.key;
                  onActiveKey?.(row.key);
                  gridElement?.focus();
                }}
                onclick={() => onOpen?.(row)}
              >
                {#each visibleColumns as column (column)}
                  <span class={`cell cell--${column}`} role="gridcell">
                    {#if column === 'kind'}
                      {#if selection.isSelected(row.key)}
                        <span class="selection-marker" aria-hidden="true">✓</span>
                      {/if}
                      <RowKind kind={row.kind} messageType={row.message_type} compact />
                    {:else if column === 'people'}
                      {@const who = rowPeople(row)}
                      <span class="people" title={who.title}>{who.primary}</span>
                      {#if who.others > 0}<span class="people-more">+{who.others}</span>{/if}
                      {#if alsoIn.has(row.key)}
                        <span class="also-in">also in {alsoIn.get(row.key)!.join(', ')}</span>
                      {/if}
                      {#if isEmailMessageType(row.message_type)}
                        <IdentityBadge
                          senderIdentities={row.matched_sender_identities}
                          recipientIdentities={row.matched_recipient_identities}
                        />
                      {/if}
                    {:else if column === 'title'}
                      {@const thread = threaded.roles.get(row.key)}
                      {@const open = thread?.lead ? rows.some((other) => other.key !== row.key && threaded.roles.get(other.key)?.threadKey === thread.threadKey) : false}
                      {#if thread && !thread.lead}<span class="thread-branch" aria-hidden="true">↳</span>{/if}
                      {#if thread?.lead && isEventDay(thread) && !open}
                        <strong data-row-title>{thread.count} events</strong>
                      {:else}
                        <strong data-row-title>{row.title || '(untitled)'}</strong>
                        {#if !thread && row.message_count > 1 && rowModality(row.kind, row.message_type) === 'email'}
                          <span class="in-thread">· {row.message_count.toLocaleString()} in thread</span>
                        {/if}
                      {/if}
                      {#if thread?.lead}
                        <button
                          type="button"
                          class="thread-toggle kit-control-states"
                          tabindex="-1"
                          aria-expanded={open}
                          aria-label={isEventDay(thread)
                            ? `${open ? 'Hide' : 'Show'} ${thread.count} events on this day`
                            : `${open ? 'Hide' : 'Show'} ${thread.count} matches in this thread`}
                          onpointerdown={(event) => event.stopPropagation()}
                          onclick={(event) => {
                            event.stopPropagation();
                            setThreadOpen(thread.threadKey, !open);
                          }}
                        >{isEventDay(thread) ? (open ? '· Hide' : '· Show') : `· ${thread.count} matches`}</button>
                      {/if}
                    {:else if column === 'excerpt' && threaded.roles.get(row.key)?.lead && isEventDay(threaded.roles.get(row.key)) &&
                      !rows.some((other) => other.key !== row.key && threaded.roles.get(other.key)?.threadKey === threaded.roles.get(row.key)?.threadKey)}
                      {eventDayTitles.get(threaded.roles.get(row.key)!.threadKey)?.join(' · ')}
                    {:else if column === 'excerpt'}
                      {#each highlightSegments(decodeHTMLEntities(row.match.strongest_excerpt || row.preview), terms) as segment, segmentIndex (segmentIndex)}
                        {#if segment.match}<mark>{segment.text}</mark>{:else}{segment.text}{/if}
                      {/each}
                      {#if row.match.lexical_match_count !== undefined}
                        <span class="match-count">{row.match.lexical_match_count} lexical matches</span>
                      {/if}
                    {:else if column === 'time'}
                      <time datetime={row.occurred_at} title={fullTime(row.occurred_at)}>{listTime(row.occurred_at)}</time>
                    {:else if column === 'attachments'}
                      {#if row.has_attachments}
                        <span class="attachment" aria-label={`${row.attachment_count} ${row.attachment_count === 1 ? 'attachment' : 'attachments'}`}>
                          <PaperclipIcon size={14} aria-hidden="true" />{#if row.attachment_count > 1}<span aria-hidden="true">{row.attachment_count}</span>{/if}
                        </span>
                      {:else}
                        <span aria-label="No attachments">—</span>
                      {/if}
                    {:else if column === 'size'}
                      <span data-mono>{formatBytes(row.attachment_size)}</span>
                    {/if}
                  </span>
                {/each}
              </div>
            {/each}
          </div>
        </div>
      {/if}
      {#if pageError}
        <!-- A failed cursor page keeps the rows already loaded. While the
             cursor survived (a transient failure), offer a quiet retry that
             re-attempts the same page; a terminal failure dropped the
             cursor, so the only recovery is reloading the view. -->
        <div role="row"><div role="gridcell" aria-colspan={visibleColumns.length}>
          <div class="page-error" role="alert">
            <span>{pageError}</span>
            {#if hasMore}
              <Button size="sm" surface="soft" label="Retry loading more" onclick={() => void onLoadMore?.()} />
            {:else}
              <Button size="sm" surface="soft" label="Reload view" onclick={() => onRetry?.()} />
            {/if}
          </div>
        </div></div>
      {/if}
      {#if loadingMore}
        <div role="row"><div role="gridcell" aria-colspan={visibleColumns.length}>
          <div class="page-progress" role="status">Loading more… {sourceRows.length.toLocaleString()} loaded</div>
        </div></div>
      {/if}
    </div>
  </div>
</section>

<style>
  /* The table is the one panel on the Everything canvas: the toolbars above
   * it sit on the canvas, the header row sits on the canvas, and the body
   * steps up to the panel surface without a border around it. */
  .everything-table {
    display: flex;
    min-height: 0;
    flex: 1;
    flex-direction: column;
    overflow: hidden;
  }

  /* Rows and the header share one grid template; the row carries the 8px
   * inset and the column gap so cells stay flush with the row's edges. */
  .table-header,
  .data-row,
  .skeleton-row {
    display: grid;
    align-items: center;
    column-gap: var(--space-4);
    padding: 0 var(--space-4);
  }

  .table-grid {
    display: flex;
    min-height: 0;
    flex: 1;
    flex-direction: column;
    overflow: auto;
    overflow-anchor: none;
    outline: none;
  }

  .table-grid:focus-visible {
    outline: none;
  }

  /* Column headers speak the small-caps label voice on the canvas, ruled
   * from the body by one hairline. */
  .table-header {
    position: sticky;
    z-index: 1;
    top: 0;
    flex: 0 0 auto;
    min-height: 30px;
    border-bottom: 1px solid var(--hairline);
    background: var(--surface-canvas);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  .header-cell--time,
  .header-cell--size {
    text-align: right;
  }

  .table-header span,
  .cell {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .table-body {
    position: relative;
    min-height: 200px;
    flex: 0 0 auto;
    background: var(--surface-panel);
  }

  .virtual-spacer {
    position: relative;
    min-width: 900px;
  }

  .virtual-window {
    position: absolute;
    inset: 0 0 auto;
  }

  /* The shared list-row contract (anatomy.css) owns inset, hairline, hover,
   * cursor, and the inspected row (aria-current). Bulk selection
   * (aria-selected) keeps its own teal tint so a checked row and the row
   * open in the reading pane stay distinguishable. */
  .data-row,
  .skeleton-row {
    height: var(--row-height);
  }

  .skeleton-row {
    border-bottom: 1px solid var(--hairline);
  }

  .data-row {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-variant-numeric: tabular-nums;
  }

  .table-grid:focus-visible .data-row[data-active='true']:not([aria-selected='true']):not([aria-current='true']) {
    background: color-mix(in srgb, var(--accent-blue) 8%, var(--bg-surface));
  }

  .data-row[aria-selected='true'] {
    background: color-mix(in srgb, var(--accent-teal) 12%, var(--bg-surface));
    box-shadow: inset 2px 0 0 var(--accent-blue);
  }

  /* The row open in the reading pane keeps its own tint even while it is
   * also bulk-checked, so checked and inspected stay distinguishable. */
  .data-row[aria-current='true'] {
    background: var(--selected-bg);
    box-shadow: inset 2px 0 0 var(--accent-blue);
  }

  /* Tabular data columns sit on the right edge, mono-aligned. */
  .cell--time,
  .cell--size {
    text-align: right;
  }

  .cell--time,
  .cell--attachments,
  .cell--size {
    color: var(--text-muted);
  }

  /* Dates read in sans with tabular figures: a monospace space reads as a
   * double gap in "Sep 27". */
  .cell--time {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  .match-count {
    margin-left: var(--space-3);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .cell--attachments {
    text-align: center;
  }

  .attachment {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    color: var(--artifact-ink);
    font-size: var(--font-size-2xs);
  }

  .thread-toggle {
    margin-left: var(--space-2);
    padding: 0 var(--space-1);
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-muted);
    cursor: pointer;
    font: inherit;
    font-size: var(--font-size-xs);
  }

  .thread-toggle:hover {
    color: var(--link-ink);
  }

  .thread-branch {
    margin-right: var(--space-2);
    color: var(--text-muted);
  }

  .data-row[data-thread-member='true'] {
    background: var(--surface-well);
  }

  .people-more,
  .also-in {
    margin-left: var(--space-2);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .also-in {
    padding: 0 var(--space-2);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
  }

  /* Row anatomy: counterpart in secondary ink, subject in primary with its
   * thread count muted beside it, and the excerpt muted with matches marked. */
  .cell--people { color: var(--text-secondary); }
  .cell--excerpt { color: var(--text-muted); }
  .in-thread { margin-left: var(--space-1); color: var(--text-muted); font-size: var(--font-size-xs); }

  .cell--excerpt mark {
    border-radius: 2px;
    background: color-mix(in srgb, var(--accent-amber) 24%, transparent);
    color: var(--text-primary);
  }

  .selection-marker {
    margin-right: var(--space-2);
    color: var(--active-ink);
    font-weight: 600;
  }

  .empty {
    margin: 0;
    padding: var(--space-8);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .skeletons {
    min-height: 200px;
  }

  .empty-actions {
    display: flex;
    justify-content: center;
    gap: var(--space-3);
  }

  .operator-hints {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: var(--space-2) var(--space-5);
    max-width: 720px;
    margin: var(--space-4) auto 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-align: left;
  }

  .operator-hints div {
    display: flex;
    align-items: baseline;
    gap: var(--space-3);
  }

  .operator-hints dd {
    margin: 0;
  }

  .skeleton-cell {
    min-width: 0;
  }

  .skeleton-cell i {
    display: block;
    height: 10px;
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
  }

  .cache-unavailable {
    display: grid;
    min-height: 200px;
    place-content: center;
    gap: var(--space-4);
    padding: var(--space-7);
    border: 1px solid var(--accent-amber);
    border-radius: var(--radius-md);
    background: var(--status-warning-bg);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .cache-unavailable strong {
    color: var(--text-primary);
    font-size: var(--font-size-lg);
  }

  .request-error {
    display: grid;
    min-height: 200px;
    place-content: center;
    padding: var(--space-7);
    border: 1px solid var(--accent-red);
    border-radius: var(--radius-md);
    background: var(--status-error-bg);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .page-error {
    position: sticky;
    bottom: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-4);
    background: var(--bg-surface);
    color: var(--text-danger);
    font-size: var(--font-size-xs);
  }

  .page-progress {
    position: sticky;
    bottom: 0;
    padding: var(--space-2) var(--space-4);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    text-align: right;
  }

  code {
    padding: var(--space-1) var(--space-2);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-primary);
    font-family: var(--font-mono);
  }

  @media (prefers-reduced-motion: reduce) {
    *,
    *::before,
    *::after {
      scroll-behavior: auto !important;
    }
  }
</style>
