<script lang="ts">
  import { onDestroy, type Snippet } from 'svelte';
  import { copyToClipboard } from '@kenn-io/kit-ui';

  import { reachRowLabel, reachRowMeta, type ReachEntry } from '../../people/reach';

  interface Props {
    entries: ReachEntry[];
    ariaLabel?: string;
    onAnnounce?: (message: string) => void;
    /** Extra per-row controls (an overflow menu, an inline confirm). */
    actions?: Snippet<[ReachEntry]>;
    /** Rows that belong in the same list after the contact methods, such
     * as the last-contact line. */
    after?: Snippet;
  }

  let { entries, ariaLabel = 'Contact methods', onAnnounce = undefined, actions = undefined, after = undefined }: Props = $props();

  let copiedKey = $state<string | null>(null);
  let resetTimer: ReturnType<typeof setTimeout> | undefined;
  onDestroy(() => { if (resetTimer !== undefined) clearTimeout(resetTimer); });

  async function copy(entry: ReachEntry): Promise<void> {
    const copied = await copyToClipboard(entry.value);
    onAnnounce?.(copied ? 'Contact method copied' : 'Could not copy contact method');
    if (!copied) return;
    copiedKey = entry.key;
    if (resetTimer !== undefined) clearTimeout(resetTimer);
    resetTimer = setTimeout(() => { copiedKey = null; }, 1500);
  }
</script>

{#if entries.length > 0 || after}
  <ul class="reach-block" data-fact-list aria-label={ariaLabel}>
    {#each entries as entry (entry.key)}
      <li class="reach-row" data-fact-row title={entry.title}>
        <span data-fact-label>{reachRowLabel(entry)}</span>
        {#if entry.opaque}
          <span data-fact-value>{entry.name ?? 'account'}</span>
        {:else}
          <span data-fact-value data-mono>{entry.display}</span>
        {/if}
        <span data-fact-meta>
          {#each reachRowMeta(entry) as part (part)}<span>{part}</span><span aria-hidden="true">{" · "}</span>{/each}
          <button
            type="button"
            aria-label={copiedKey === entry.key ? `Copied ${entry.label}` : `Copy ${entry.label}`}
            onclick={() => void copy(entry)}
          >{copiedKey === entry.key ? 'copied' : 'copy'}</button>
          {@render actions?.(entry)}
        </span>
      </li>
    {/each}
    {@render after?.()}
  </ul>
{/if}

