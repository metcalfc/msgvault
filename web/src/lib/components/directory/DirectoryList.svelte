<script lang="ts">
  import { Button, Chip, EmptyState } from '@kenn-io/kit-ui';

  import type { DirectoryPerson } from '../../directory/models';
  import { humanizeDate } from '../../util/dates';

  interface Props {
    rows: DirectoryPerson[];
    loading: boolean;
    loadingMore: boolean;
    error: string | null;
    pageError: string | null;
    pageRecovery: 'retry' | 'reload' | null;
    hasMore: boolean;
    selectedPersonID: number | null;
    onSelect: (personID: number) => void;
    onLoadMore: () => void;
    onReload: () => void;
  }

  let { rows, loading, loadingMore, error, pageError, pageRecovery, hasMore, selectedPersonID, onSelect, onLoadMore, onReload }: Props = $props();
  let gridElement = $state<HTMLDivElement>();
  let activeID = $state<number | null>(null);
  // Unnamed contact-only records ("Person 42" rows with nothing to tell
  // them apart) sit behind a chip so the list opens on recognizable people.
  // Client-side over the loaded pages (the chip says so): the server has no
  // such filter yet. The selected person always stays visible so the row,
  // the keyboard target, and the detail pane never disagree.
  let showQuiet = $state(false);
  function isQuiet(person: DirectoryPerson): boolean {
    return !person.display_name?.trim();
  }
  const quietCount = $derived(rows.filter(isQuiet).length);
  const visibleRows = $derived(
    showQuiet ? rows : rows.filter((person) => !isQuiet(person) || person.id === selectedPersonID)
  );
  const activeIndex = $derived(activeID === null ? -1 : visibleRows.findIndex((row) => row.id === activeID));

  $effect(() => {
    if (activeID !== null && visibleRows.some((row) => row.id === activeID)) return;
    activeID = visibleRows[0]?.id ?? null;
  });

  async function moveTo(index: number): Promise<void> {
    if (visibleRows.length === 0) return;
    const next = Math.max(0, Math.min(visibleRows.length - 1, index));
    activeID = visibleRows[next]!.id;
    await Promise.resolve();
    const row = gridElement?.querySelector<HTMLElement>(`[data-person-id="${activeID}"]`);
    row?.scrollIntoView({ block: 'nearest' });
    row?.focus();
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.metaKey || event.ctrlKey || event.altKey || visibleRows.length === 0) return;
    if (event.key === 'ArrowDown' || event.key === 'j') void moveTo(activeIndex + 1);
    else if (event.key === 'ArrowUp' || event.key === 'k') void moveTo(activeIndex - 1);
    else if (event.key === 'Home') void moveTo(0);
    else if (event.key === 'End') void moveTo(visibleRows.length - 1);
    else if ((event.key === 'Enter' || event.key === ' ') && activeID !== null) onSelect(activeID);
    else return;
    event.preventDefault();
  }
</script>

<section class="directory-list" aria-label="Directory results">
  {#if error && rows.length === 0}
    <div role="alert" class="notice">{error}</div>
  {:else}
    {#if pageError}
      <div role="alert" class="notice page-error">
        <span>{pageError}</span>
        {#if pageRecovery === 'retry' && hasMore}
          <Button size="sm" surface="outline" label="Retry loading more people" onclick={onLoadMore} />
        {:else if pageRecovery === 'reload'}
          <Button size="sm" surface="outline" label="Reload directory" onclick={onReload} />
        {/if}
      </div>
    {/if}
    {#if loading && rows.length > 0}
      <p role="status" class="empty">Updating people…</p>
    {/if}
    {#if quietCount > 0 && rows.length > 0}
      <div class="quiet-toggle">
        <Chip
          interactive
          tone="muted"
          size="sm"
          uppercase={false}
          expanded={showQuiet}
          ariaLabel={`${showQuiet ? 'Hide' : 'Show'} unnamed (${quietCount} loaded)`}
          onclick={() => { showQuiet = !showQuiet; }}
        >{showQuiet ? 'Hide' : 'Show'} unnamed ({quietCount.toLocaleString()} loaded)</Chip>
      </div>
    {/if}
    {#if loading && rows.length === 0}
      <p role="status" class="empty">Loading people…</p>
    {:else if rows.length === 0}
      <EmptyState title="No people found" description="Try a different search or filter." />
    {:else if visibleRows.length === 0}
      <p class="empty">Only unnamed records among the loaded people.</p>
    {:else}
      <div
        bind:this={gridElement}
        role="grid"
        aria-label="Directory people"
        aria-busy={loading || loadingMore}
        tabindex="-1"
      >
        {#each visibleRows as person (person.id)}
          <div
            role="row"
            data-list-row
            data-active={person.id === activeID}
            data-person-id={person.id}
            class:active={person.id === activeID}
            class:selected={person.id === selectedPersonID}
            aria-selected={person.id === selectedPersonID}
            tabindex={person.id === activeID ? 0 : -1}
            onkeydown={handleKeydown}
            onclick={() => { activeID = person.id; onSelect(person.id); }}
          >
            <span role="gridcell" class="row-main">
              <span class="name" data-row-title>{person.display_name ?? `Person ${person.id}`}</span>
              <span class="last-contact" data-mono>{#if person.last_contact_at}Last contact <time datetime={person.last_contact_at}>{humanizeDate(person.last_contact_at)}</time>{:else}Never contacted{/if}</span>
            </span>
            <span role="gridcell" class="meta" data-meta>{[
              person.primary_channel ?? 'No primary channel',
              person.contact_state,
              ...(person.organizations ?? []),
              ...(person.categories ?? [])
            ].join(' · ')}</span>
          </div>
        {/each}
      </div>
    {/if}
    {#if hasMore && rows.length > 0 && pageRecovery !== 'reload'}
      <div class="more"><Button label={loadingMore ? 'Loading more…' : 'Load more people'} disabled={loadingMore} onclick={onLoadMore} /></div>
    {/if}
  {/if}
</section>

<style>
  .directory-list { min-width: 0; display: flex; flex-direction: column; gap: var(--space-3); }
  /* The list is the Directory's panel; its rows follow the shared list-row
   * contract (anatomy.css): two lines, hairline below, bar and tint for
   * the cursor and the selection. */
  [role="grid"] { display: grid; align-content: start; outline: none; background: var(--surface-panel); }
  [role="row"] { display: grid; height: var(--row-height); align-content: center; gap: 1px; text-align: left; color: var(--text-primary); cursor: pointer; }
  [role="row"]:focus-visible, [role="grid"]:focus-visible { outline: var(--focus-ring); outline-offset: -2px; }
  .row-main { display: flex; align-items: baseline; justify-content: space-between; gap: var(--space-4); }
  .last-contact { flex: none; color: var(--text-muted); font-size: var(--font-size-xs); }
  .empty { color: var(--text-muted); font-size: var(--font-size-sm); }
  .notice { padding: var(--space-3); color: var(--text-secondary); background: var(--bg-inset); border-radius: var(--radius-sm); }
  .page-error { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
  .more { display: flex; justify-content: center; }
  .quiet-toggle { display: flex; }
</style>
