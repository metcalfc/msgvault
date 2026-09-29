<script lang="ts">
  import { Button, SettingsSection } from '@kenn-io/kit-ui';
  import { onDestroy, untrack } from 'svelte';

  import type { APIClient } from '../../api/client';
  import { createExploreAPI } from '../../explore/api';
  import type { SearchCoverageValue } from '../../search/modes';
  import { semanticCoverageSummary } from '../search/SearchCoverage.svelte';

  let { client }: { client: APIClient } = $props();

  const api = createExploreAPI(untrack(() => client));
  let coverage = $state<SearchCoverageValue>();
  let loadError = $state('');
  let confirming = $state(false);
  let running = $state(false);
  let checking = $state(false);
  let actionError = $state('');
  let controller: AbortController | undefined;

  async function load(): Promise<void> {
    controller?.abort();
    const current = new AbortController();
    controller = current;
    checking = true;
    try {
      coverage = await api.coverage([], current.signal);
      loadError = '';
    } catch (cause) {
      if (current.signal.aborted) return;
      loadError = cause instanceof Error ? cause.message : 'Semantic index status could not be loaded.';
    } finally {
      if (controller === current) checking = false;
    }
  }

  async function rebuild(): Promise<void> {
    confirming = false;
    running = true;
    actionError = '';
    try {
      await api.runCoverageAction('build_index', coverage?.status ?? 'unavailable');
      await load();
    } catch (cause) {
      actionError = cause instanceof Error ? cause.message : 'The semantic index rebuild failed.';
    } finally {
      running = false;
    }
  }

  // Status loads on request: Settings stays one request on open, and the
  // coverage count scans the index.
  onDestroy(() => controller?.abort());
</script>

<SettingsSection
  title="Semantic index"
  description="A full rebuild re-embeds every eligible item with the configured provider. It can take a long time and uses provider quota."
>
  <div class="semantic-index">
    <p role="status" aria-live="polite">
      {#if coverage}
        {semanticCoverageSummary(coverage)}.{#if coverage.detail}{' '}{coverage.detail}{/if}
      {:else if loadError}
        {loadError}
      {:else if checking}
        Checking semantic index status…
      {:else}
        Check the index to see how much of the archive is embedded.
      {/if}
    </p>
    {#if actionError}<p role="alert">{actionError}</p>{/if}
    <div class="semantic-index__actions">
      {#if confirming}
        <span>Start a full rebuild of the semantic index?</span>
        <Button label="Cancel" tone="neutral" surface="outline" size="sm" onclick={() => (confirming = false)} />
        <Button label="Confirm full rebuild" tone="info" surface="solid" size="sm" onclick={() => void rebuild()} />
      {:else}
        <Button
          label="Check index status"
          tone="neutral"
          surface="outline"
          size="sm"
          disabled={checking || running}
          onclick={() => void load()}
        />
        <Button
          label={running ? 'Rebuilding…' : 'Rebuild semantic index'}
          tone="info"
          surface="outline"
          size="sm"
          disabled={running || coverage?.status === 'disabled'}
          onclick={() => (confirming = true)}
        />
      {/if}
    </div>
  </div>
</SettingsSection>

<style>
  .semantic-index {
    display: grid;
    gap: var(--space-3);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .semantic-index p {
    margin: 0;
  }

  .semantic-index__actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
  }
</style>
