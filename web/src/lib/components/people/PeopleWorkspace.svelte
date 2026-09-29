<script lang="ts">
  import { Button, EmptyState, Notice, SearchInput, TextInput } from '@kenn-io/kit-ui';
  import { onDestroy, untrack } from 'svelte';

  import type { PeopleFilters, PeopleHub, PeopleRow, PeopleSavedFilter } from '../../people/hub.svelte';
  import { bufferedCallback } from '../../util/buffered-callback';
  import { humanizeDate } from '../../util/dates';

  interface Props {
    hub: PeopleHub;
    filters: PeopleFilters;
    /** Writes list filters to the address: typed text replaces the current
     * history entry, chips add one. */
    onFiltersChange: (patch: Partial<PeopleFilters>, history: 'push' | 'replace') => void;
    /** Opens a row's person page. */
    onOpen: (row: PeopleRow) => void;
    onOpenDomains?: () => void;
    /** Directory load errors, which belong to the saved half of the list. */
    savedError?: string | null;
    /** A failed later page of saved people, and how to recover from it. */
    savedPageError?: string | null;
    savedPageRecovery?: 'retry' | 'reload' | null;
    onReloadSaved?: () => void;
  }

  let {
    hub, filters, onFiltersChange, onOpen, onOpenDomains = undefined, savedError = null,
    savedPageError = null, savedPageRecovery = null, onReloadSaved = undefined,
  }: Props = $props();

  const TEXT_DEBOUNCE_MS = 250;
  type TextKey = 'query' | 'category' | 'organization';
  let text = $state<Record<TextKey, string>>(untrack(() => ({
    query: filters.query, category: filters.category, organization: filters.organization,
  })));
  // Follow the address (Back/Forward) without clobbering text being typed.
  $effect(() => {
    text = { query: filters.query, category: filters.category, organization: filters.organization };
  });
  const debouncedText = bufferedCallback((patch: Record<TextKey, string>) => onFiltersChange(patch, 'replace'), TEXT_DEBOUNCE_MS);
  onDestroy(() => debouncedText.flush());

  function editText(key: TextKey, value: string): void {
    text[key] = value;
    debouncedText({ ...text });
  }

  function toggleSaved(value: Exclude<PeopleSavedFilter, ''>): void {
    debouncedText.flush();
    onFiltersChange({ saved: filters.saved === value ? '' : value }, 'push');
  }

  function toggleHasName(): void {
    debouncedText.flush();
    onFiltersChange({ hasName: !filters.hasName }, 'push');
  }

  const merged = $derived(hub.merged);
  // "Has name" can hide a whole page of archive contacts; keep loading
  // until a named one shows or the pages end.
  $effect(() => {
    if (hub.needsMoreObserved) void hub.observed.loadMore();
  });
  const observedError = $derived(hub.includesObserved ? hub.observed.error : null);
  const hasFilters = $derived(Boolean(filters.query.trim() || filters.saved || filters.hasName ||
    filters.category.trim() || filters.organization.trim()));

  function href(row: PeopleRow): string {
    return row.kind === 'saved' ? `/people/${row.id}` : `/people/contact-${row.id}`;
  }

  function open(event: MouseEvent, row: PeopleRow): void {
    // Let the browser handle new-tab and new-window clicks.
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onOpen(row);
  }
</script>

<main class="people-workspace" aria-label="People">
  <header class="people-header">
    <div>
      <h1 data-page-title>People</h1>
      <p>Everyone you have been in touch with, most recent first. Saved people have a Directory record.</p>
    </div>
    {#if onOpenDomains}
      <Button size="sm" surface="soft" label="Domains" onclick={onOpenDomains} />
    {/if}
  </header>

  <div class="people-filters">
    <SearchInput
      value={text.query}
      ariaLabel="Search people"
      placeholder="Search by name, email, or organization…"
      block
      oninput={(value) => editText('query', value)}
    />
    <div class="chips" role="group" aria-label="People filters">
      <button type="button" class="filter-chip" aria-pressed={filters.saved === 'saved'} onclick={() => toggleSaved('saved')}>Saved</button>
      <button type="button" class="filter-chip" aria-pressed={filters.saved === 'unsaved'} onclick={() => toggleSaved('unsaved')}>Not saved</button>
      <button type="button" class="filter-chip" aria-pressed={filters.hasName} onclick={toggleHasName}>Has name</button>
      <TextInput size="sm" value={text.category} ariaLabel="Category filter" placeholder="Category"
        oninput={(value) => editText('category', value)} />
      <TextInput size="sm" value={text.organization} ariaLabel="Organization filter" placeholder="Organization"
        oninput={(value) => editText('organization', value)} />
    </div>
  </div>

  {#if savedError && hub.includesSaved}<Notice tone="error" message={`Saved people: ${savedError}`} />{/if}
  {#if savedPageError && hub.includesSaved}
    <div class="page-error" role="alert">
      <span>{savedPageError}</span>
      {#if savedPageRecovery === 'reload' && onReloadSaved}
        <Button size="sm" surface="soft" label="Reload people" onclick={onReloadSaved} />
      {:else if savedPageRecovery === 'retry'}
        <Button size="sm" surface="soft" label="Retry loading more people" onclick={() => void hub.loadMore()} />
      {/if}
    </div>
  {/if}
  {#if observedError}<Notice tone="warning" message={`Archive contacts are unavailable: ${observedError}`} />{/if}

  <section class="people-list" aria-label="People results" aria-busy={hub.loading || hub.loadingMore}>
    {#if merged.rows.length === 0}
      {#if hub.loading}
        <p class="state" role="status">Loading people…</p>
      {:else}
        <EmptyState title="No people found" description={hasFilters ? 'Try a different search or filter.' : 'People appear here as the archive fills.'} />
      {/if}
    {:else}
      {#if hub.loading}<p class="state" role="status">Updating people…</p>{/if}
      <ul>
        {#each merged.rows as row (row.key)}
          <li>
            <a class="person-row" data-list-row href={href(row)} onclick={(event) => open(event, row)}>
              <span class="row-main">
                <span class="name" data-row-title>{row.name}</span>
                {#if row.kind === 'observed'}<span class="not-saved">Not saved</span>{/if}
                <span class="last-contact" data-mono>
                  {#if row.lastContactAt}<time datetime={row.lastContactAt}>{humanizeDate(row.lastContactAt)}</time>{:else}No contact{/if}
                </span>
              </span>
              <span class="row-meta" data-meta>
                {[row.identifier?.value, ...row.meta].filter(Boolean).join(' · ') || ' '}
              </span>
            </a>
          </li>
        {/each}
      </ul>
    {/if}
    {#if hub.hasMore && (merged.rows.length > 0 || !hub.loading)}
      <div class="more">
        <Button surface="soft" label={hub.loadingMore ? 'Loading more…' : 'Load more people'} disabled={hub.loadingMore}
          onclick={() => void hub.loadMore()} />
      </div>
    {/if}
  </section>
</main>

<style>
  .people-workspace {
    display: flex;
    flex: 1;
    min-height: 0;
    flex-direction: column;
    gap: var(--space-4);
    width: 100%;
    max-width: 1080px;
    margin-inline: auto;
    padding: var(--space-6) var(--space-7) var(--space-4);
    overflow: auto;
  }

  .people-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--space-4);
  }

  h1, p { margin: 0; }
  .people-header p { color: var(--text-muted); font-size: var(--font-size-sm); }

  .people-filters { display: grid; gap: var(--space-3); }

  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
  }

  .chips :global(.kit-text-input) { width: 160px; }

  .filter-chip {
    border: 1px solid var(--border-muted);
    border-radius: 999px;
    padding: 3px var(--space-3);
    background: var(--surface-panel);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .filter-chip:hover { color: var(--text-primary); }
  .filter-chip[aria-pressed='true'] {
    border-color: var(--accent-blue);
    background: var(--surface-well);
    color: var(--text-primary);
  }
  .filter-chip:focus-visible { outline: var(--focus-ring); outline-offset: 2px; }

  ul { margin: 0; padding: 0; list-style: none; }

  .person-row {
    display: grid;
    gap: 2px;
    padding: var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--hairline);
    color: var(--text-primary);
    text-decoration: none;
  }

  .person-row:hover { background: var(--surface-well); }
  .person-row:focus-visible { outline: var(--focus-ring); outline-offset: -2px; }

  .row-main {
    display: flex;
    align-items: baseline;
    gap: var(--space-3);
  }

  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
  .not-saved {
    flex: none;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    padding: 0 var(--space-1);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }
  .last-contact { flex: none; margin-left: auto; color: var(--text-muted); font-size: var(--font-size-xs); }
  .row-meta { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-secondary); font-size: var(--font-size-sm); }
  .state { color: var(--text-muted); font-size: var(--font-size-sm); }
  .page-error {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-3);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
  }
  .more { display: flex; justify-content: center; padding: var(--space-3); }

  @media (max-width: 760px) {
    .people-workspace { padding: var(--space-4) var(--space-3); }
  }
</style>
