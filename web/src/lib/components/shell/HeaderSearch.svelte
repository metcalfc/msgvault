<script lang="ts">
  import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger, SearchInput } from '@kenn-io/kit-ui';
  import BookmarkIcon from '@lucide/svelte/icons/bookmark';
  import { listSavedViews } from '../../api/generated/api/api';
  import type { SavedView } from '../../api/generated/models';
  import type { APIClient } from '../../api/client';
  import type { ExploreURLState } from '../../explore/models';
  import { exploreStateFromSavedView, savedViewIncompatibility } from '../../saved-views/canonical';

  interface Props {
    client: APIClient;
    /** Submits a query to Search from any surface. */
    onSearch: (query: string) => void;
    onOpenSavedView: (state: Partial<ExploreURLState>) => void;
    onManageSavedViews: () => void;
    inputEl?: HTMLInputElement;
  }

  let { client, onSearch, onOpenSavedView, onManageSavedViews, inputEl = $bindable(undefined) }: Props = $props();
  let query = $state('');
  let views = $state<SavedView[]>();
  let loadError = $state('');

  function submit(event: SubmitEvent): void {
    event.preventDefault();
    const text = query.trim();
    if (!text) return;
    onSearch(text);
    query = '';
  }

  // The list loads when the menu opens so it is fresh and costs nothing
  // until someone asks for it.
  async function loadViews(open: boolean): Promise<void> {
    if (!open) return;
    loadError = '';
    try {
      const { data } = await listSavedViews(client);
      if (!data) throw new Error('unavailable');
      views = data.saved_views ?? [];
    } catch {
      views = [];
      loadError = 'Saved Views are unavailable right now.';
    }
  }
</script>

<form class="header-search" role="search" aria-label="Search the archive" onsubmit={submit}>
  <SearchInput
    bind:inputEl
    bind:value={query}
    ariaLabel="Search the archive"
    placeholder="Search mail, messages, and files…"
    keys={['⌘', 'K']}
    size="sm"
    block
  />
  <Menu align="end" onopenchange={(open) => void loadViews(open)}>
    <MenuTrigger class="header-search__views" ariaLabel="Saved Views" title="Saved Views">
      <BookmarkIcon size={14} aria-hidden="true" />
    </MenuTrigger>
    <MenuContent ariaLabel="Saved Views">
      {#if views === undefined}
        <MenuItem disabled onselect={() => undefined}>Loading…</MenuItem>
      {:else if loadError}
        <MenuItem disabled onselect={() => undefined}>{loadError}</MenuItem>
      {:else if views.length === 0}
        <MenuItem disabled onselect={() => undefined}>No Saved Views yet</MenuItem>
      {:else}
        {#each views.slice(0, 12) as view (view.id)}
          <MenuItem disabled={Boolean(savedViewIncompatibility(view))} onselect={() => onOpenSavedView(exploreStateFromSavedView(view))}>
            {view.name}
          </MenuItem>
        {/each}
      {/if}
      <MenuSeparator />
      <MenuItem onselect={onManageSavedViews}>Manage Saved Views…</MenuItem>
    </MenuContent>
  </Menu>
</form>

<style>
  .header-search {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    width: 100%;
    min-width: 0;
  }

  .header-search :global(.header-search__views) {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    background: var(--surface-panel);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .header-search :global(.header-search__views:hover) {
    color: var(--text-primary);
  }
</style>
