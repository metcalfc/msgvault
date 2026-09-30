<script lang="ts">
  import { Chip } from '@kenn-io/kit-ui';

  import { suggestionAccessibleName, type QuerySuggestion } from '../../search/suggestions';

  interface Props {
    suggestions: QuerySuggestion[];
    onapply: (suggestion: QuerySuggestion) => void;
  }

  let { suggestions, onapply }: Props = $props();

  const announcement = $derived(
    suggestions.length === 0
      ? ''
      : `${suggestions.length} suggested ${suggestions.length === 1 ? 'filter' : 'filters'} for this search`
  );
</script>

<span class="kit-sr-only" role="status" aria-live="polite">{announcement}</span>
{#if suggestions.length > 0}
  <div class="query-suggestions" role="group" aria-label="Suggested filters">
    <span class="query-suggestions__lead" aria-hidden="true">Suggested</span>
    {#each suggestions as suggestion (`${suggestion.kind}|${suggestion.label}`)}
      <Chip
        interactive
        size="sm"
        tone="info"
        uppercase={false}
        title={suggestion.span ? `Replaces “${suggestion.span}”` : undefined}
        ariaLabel={suggestionAccessibleName(suggestion)}
        dataTestid="query-suggestion"
        onclick={() => onapply(suggestion)}
      >+ {suggestion.label}</Chip>
    {/each}
  </div>
{/if}

<style>
  .query-suggestions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
  }

  .query-suggestions__lead {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
</style>
