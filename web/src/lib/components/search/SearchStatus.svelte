<script lang="ts">
  import { Button } from '@kenn-io/kit-ui';

  import type { ExploreSearchMode } from '../../explore/models';
  import type { SearchCoverageAction, SearchCoverageValue } from '../../search/modes';
  import SearchCoverage from './SearchCoverage.svelte';

  interface Props {
    /** Rows loaded so far, and the exact total when the daemon counted it. */
    loadedCount: number;
    totalCount?: number;
    hasResult: boolean;
    /** The candidate pool hit its cap, so more results may match. */
    saturated?: boolean;
    /** The backend narrowed a semantic/hybrid search to active messages. */
    activeOnly?: boolean;
    requestedMode: ExploreSearchMode;
    coverage?: SearchCoverageValue;
    onRefine?: () => void;
    onCoverageAction?: (action: SearchCoverageAction) => void;
  }

  let {
    loadedCount,
    totalCount = undefined,
    hasResult,
    saturated = false,
    activeOnly = false,
    requestedMode,
    coverage = undefined,
    onRefine = undefined,
    onCoverageAction = undefined
  }: Props = $props();

  const coverageNote = $derived.by((): string | undefined => {
    if (!coverage) return undefined;
    switch (coverage.status) {
      case 'disabled': return 'semantic search off';
      case 'initializing': return 'semantic index initializing';
      case 'stale': return 'semantic index stale';
      case 'unavailable': return 'semantic index unavailable';
      case 'incomplete': return `semantic index ${Math.round(coverage.percentage)}%`;
      case 'ready': return undefined;
    }
  });

  const summary = $derived.by((): string => {
    const parts: string[] = [];
    if (saturated) parts.push(`${loadedCount.toLocaleString()} ${loadedCount === 1 ? 'result' : 'results'} shown`, 'more may match');
    else if (totalCount !== undefined) parts.push(`${totalCount.toLocaleString()} ${totalCount === 1 ? 'item' : 'items'}`);
    else if (hasResult) parts.push(`${loadedCount.toLocaleString()} loaded`);
    if (coverageNote) parts.push(coverageNote);
    if (activeOnly) parts.push('active messages only');
    return parts.join(' · ');
  });

  const hasDetails = $derived(saturated || activeOnly || Boolean(coverage));
</script>

<div class="search-status">
  <span class="search-status__summary" role="status" aria-label="Result status" aria-live="polite" data-mono>{summary}</span>
  {#if hasDetails}
    <details class="search-status__details">
      <summary>Details</summary>
      <div class="search-status__body kit-popover-card">
        {#if saturated}
          <p>
            <strong>More results may match.</strong>
            Narrow with from:alice@example.com, after:2025-01-01, or label:important.
          </p>
          {#if onRefine}<div><Button label="Refine search" size="sm" surface="soft" onclick={onRefine} /></div>{/if}
        {/if}
        {#if coverage}
          <SearchCoverage {requestedMode} {coverage} onaction={onCoverageAction} />
        {/if}
        {#if activeOnly}
          <p>Semantic search covers active messages only.</p>
        {/if}
      </div>
    </details>
  {/if}
</div>

<style>
  .search-status {
    position: relative;
    display: flex;
    min-height: 20px;
    align-items: center;
    gap: var(--space-3);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .search-status__summary {
    font-variant-numeric: tabular-nums;
  }

  .search-status__details summary {
    cursor: pointer;
  }

  .search-status__body {
    position: absolute;
    z-index: var(--z-popover);
    top: calc(100% + var(--space-2));
    left: 0;
    display: grid;
    max-width: min(560px, calc(100vw - var(--space-8)));
    gap: var(--space-3);
    padding: var(--space-4);
    color: var(--text-secondary);
  }

  .search-status__body p {
    margin: 0;
    line-height: 1.4;
  }

  .search-status__body strong {
    color: var(--text-primary);
  }
</style>
