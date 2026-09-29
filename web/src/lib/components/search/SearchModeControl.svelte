<script lang="ts">
  import { SegmentedControl, type SegmentedControlOption } from '@kenn-io/kit-ui';

  import type { ExploreSearchMode } from '../../explore/models';
  import type { SearchCoverageStatus } from '../../search/modes';

  interface Props {
    requestedMode: ExploreSearchMode;
    /** Semantic index coverage; explains a degraded semantic/hybrid mode
     * in the segment's tooltip without taking the choice away. */
    status?: SearchCoverageStatus;
    onchange?: (mode: ExploreSearchMode) => void;
  }

  let { requestedMode, status = undefined, onchange = undefined }: Props = $props();

  const semanticHint = $derived.by((): string | undefined => {
    switch (status) {
      case 'disabled': return 'Semantic search is disabled in Settings';
      case 'initializing': return 'The semantic index is still initializing';
      case 'stale': return 'The semantic index is stale; results may miss recent items';
      case 'incomplete': return 'Only embedded items can appear in semantic results';
      case 'unavailable': return 'The semantic index is unavailable';
      default: return undefined;
    }
  });

  const options = $derived<SegmentedControlOption[]>([
    { value: 'full_text', label: 'Full text', title: 'Match words and operators exactly' },
    { value: 'semantic', label: 'Semantic', title: semanticHint ?? 'Match by meaning' },
    { value: 'hybrid', label: 'Hybrid', title: semanticHint ?? 'Blend exact and meaning matches' }
  ]);
</script>

<SegmentedControl
  ariaLabel="Search mode"
  {options}
  value={requestedMode}
  onchange={(mode) => onchange?.(mode as ExploreSearchMode)}
/>
