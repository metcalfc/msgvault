<script lang="ts">
  import { Button, Modal, TextInput } from '@kenn-io/kit-ui';
  import { untrack } from 'svelte';

  import { createSavedView } from '../../api/generated/api/api';
  import type { APIClient } from '../../api/client';
  import type { ExploreFilter, ExploreURLState } from '../../explore/models';
  import { groupingDimensionLabel } from '../../grouping/catalog';
  import { SAVED_VIEW_SCHEMA_VERSION, canonicalSavedViewState } from '../../saved-views/canonical';

  interface Props {
    client: APIClient;
    /** The Everything state to save, exactly as the Saved Views list stores it. */
    view: ExploreURLState;
    /** How each filter reads as a chip in the context bar. */
    describeFilter: (filter: ExploreFilter) => string[];
    onclose: () => void;
    onsaved?: (name: string) => void;
  }

  let { client, view, describeFilter, onclose, onsaved = undefined }: Props = $props();

  const MODE_LABELS = { full_text: 'Full text', semantic: 'Semantic', hybrid: 'Hybrid' } as const;
  const PRESENTATION_LABELS = { table: 'Table', timeline: 'Timeline', files: 'Files' } as const;

  // Captured when the sheet opens, so what is shown is what is saved.
  const snapshot = untrack(() => $state.snapshot(view) as ExploreURLState);
  const stored = canonicalSavedViewState(snapshot);
  const query = snapshot.query.trim();
  const chips = untrack(() => snapshot.filters.flatMap((filter) => describeFilter(filter)));
  let name = $state('');
  let saving = $state(false);
  let error = $state('');

  async function save(): Promise<void> {
    if (!name.trim() || saving) return;
    saving = true;
    error = '';
    try {
      const { data, error: responseError } = await createSavedView(
        { name: name.trim(), canonical_state: stored, schema_version: SAVED_VIEW_SCHEMA_VERSION },
        client,
      );
      if (!data) {
        throw new Error(typeof responseError === 'object' && responseError !== null && 'message' in responseError
          ? String(responseError.message) : 'Unable to save this view.');
      }
      onsaved?.(data.name);
      onclose();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Unable to save this view.';
    } finally {
      saving = false;
    }
  }
</script>

<Modal title="Save view" {onclose} width="440px">
  <form class="save-view" onsubmit={(event) => { event.preventDefault(); void save(); }}>
    <dl class="save-view__state" aria-label="What this view stores">
      <div>
        <dt>Search</dt>
        <dd>{query ? `“${query}” · ${MODE_LABELS[snapshot.searchMode]}` : 'No search'}</dd>
      </div>
      <div>
        <dt>Filters</dt>
        <dd>
          {#if chips.length === 0}None{:else}
            <ul>{#each chips as chip, index (`${index}:${chip}`)}<li>{chip}</li>{/each}</ul>
          {/if}
        </dd>
      </div>
      <div>
        <dt>Grouping</dt>
        <dd>{snapshot.groupingChain.length > 0 ? snapshot.groupingChain.map(groupingDimensionLabel).join(' → ') : 'None'}</dd>
      </div>
      <div>
        <dt>Show as</dt>
        <dd>{PRESENTATION_LABELS[snapshot.presentation]}</dd>
      </div>
    </dl>
    <label class="save-view__name">
      <span>Name</span>
      <TextInput bind:value={name} ariaLabel="Saved view name" block autofocus placeholder="e.g. Board threads this quarter" />
    </label>
    {#if error}<p class="save-view__error" role="alert">{error}</p>{/if}
    <div class="save-view__actions">
      <Button label="Cancel" surface="outline" onclick={onclose} />
      <Button type="submit" label={saving ? 'Saving…' : 'Save'} tone="info" surface="solid" disabled={!name.trim() || saving} />
    </div>
  </form>
</Modal>

<style>
  .save-view {
    display: grid;
    gap: var(--space-4);
  }

  .save-view__state {
    display: grid;
    gap: var(--space-2);
    margin: 0;
  }

  .save-view__state div {
    display: grid;
    grid-template-columns: 88px minmax(0, 1fr);
    gap: var(--space-3);
  }

  dt {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  dd {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    overflow-wrap: anywhere;
  }

  ul {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  li {
    padding: 0 var(--space-2);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-xs);
  }

  .save-view__name {
    display: grid;
    gap: var(--space-1);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .save-view__actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-3);
  }

  .save-view__error {
    margin: 0;
    color: var(--text-danger);
    font-size: var(--font-size-sm);
  }
</style>
