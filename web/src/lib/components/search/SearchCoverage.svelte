<script lang="ts">
  import { Button } from '@kenn-io/kit-ui';

  import type { ExploreSearchMode } from '../../explore/models';
  import type { SearchCoverageAction, SearchCoverageValue } from '../../search/modes';

  interface Props {
    requestedMode?: ExploreSearchMode;
    coverage: SearchCoverageValue;
    /** Only Retry is offered here. A full index rebuild lives in Settings →
     * Search (SemanticIndexControl), away from the search bar. */
    onaction?: (action: SearchCoverageAction) => void;
  }

  let { requestedMode = 'full_text', coverage, onaction = undefined }: Props = $props();

  const actions = $derived(coverage.actions ?? []);
  const summary = $derived(semanticCoverageSummary(coverage));
</script>

<script lang="ts" module>
  import type { SearchCoverageValue as CoverageValue } from '../../search/modes';

  /** One sentence naming the semantic index state, shared with Settings. */
  export function semanticCoverageSummary(coverage: CoverageValue): string {
    const count = coverage.eligible_count.toLocaleString();
    const percentage = Math.round(coverage.percentage).toLocaleString();
    switch (coverage.status) {
      case 'disabled': return 'Semantic search is disabled';
      case 'initializing': return 'Semantic index is initializing';
      case 'stale': return 'Semantic index is stale';
      case 'unavailable': return 'Semantic index is unavailable';
      case 'incomplete':
      case 'ready': return `Semantic index: ${percentage}% of ${count} items`;
    }
  }
</script>

<div class="coverage" role="status" aria-live="polite">
  <span>{summary}.</span>
  {#if coverage.detail}<span>{coverage.detail}</span>{/if}
  {#if requestedMode === 'semantic' && coverage.status === 'incomplete'}
    <span>Unembedded items cannot appear in Semantic results.</span>
  {/if}
  {#if actions.includes('retry')}
    <Button label="Retry" tone="info" surface="outline" size="sm" onclick={() => onaction?.('retry')} />
  {/if}
  {#if actions.includes('build_index')}
    <span>Rebuild the index from Settings → Search.</span>
  {/if}
</div>

<style>
  .coverage {
    display: flex;
    min-height: 28px;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
  }
</style>
