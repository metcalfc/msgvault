<script lang="ts">
  import type { Snippet } from 'svelte';
  import AtSignIcon from '@lucide/svelte/icons/at-sign';
  import LinkIcon from '@lucide/svelte/icons/link';
  import MailIcon from '@lucide/svelte/icons/mail';
  import MessageCircleIcon from '@lucide/svelte/icons/message-circle';
  import PhoneIcon from '@lucide/svelte/icons/phone';
  import { Chip, CopyButton, copyToClipboard } from '@kenn-io/kit-ui';

  import { reachKindLabels, type ReachEntry, type ReachKind } from '../../people/reach';

  interface Props {
    entries: ReachEntry[];
    ariaLabel?: string;
    onAnnounce?: (message: string) => void;
    /** Extra per-row controls (an overflow menu, an inline confirm). */
    actions?: Snippet<[ReachEntry]>;
  }

  let { entries, ariaLabel = 'Contact methods', onAnnounce = undefined, actions = undefined }: Props = $props();

  const icons: Record<ReachKind, typeof MailIcon> = {
    email: MailIcon, phone: PhoneIcon, chat: MessageCircleIcon, handle: AtSignIcon, url: LinkIcon
  };

  let copiedKey = $state<string | null>(null);
  let resetTimer: ReturnType<typeof setTimeout> | undefined;

  async function copy(entry: ReachEntry): Promise<void> {
    const copied = await copyToClipboard(entry.value);
    onAnnounce?.(copied ? 'Contact method copied' : 'Could not copy contact method');
    if (!copied) return;
    copiedKey = entry.key;
    if (resetTimer !== undefined) clearTimeout(resetTimer);
    resetTimer = setTimeout(() => { copiedKey = null; }, 1500);
  }
</script>

{#if entries.length > 0}
  <ul class="reach-block" aria-label={ariaLabel}>
    {#each entries as entry (entry.key)}
      {@const Icon = icons[entry.kind]}
      <li class="reach-row" title={entry.title}>
        <span class="reach-icon" role="img" aria-label={reachKindLabels[entry.kind]}>
          <Icon size="14" aria-hidden="true" />
        </span>
        <span class="reach-main">
          {#if entry.opaque}
            <span class="reach-value reach-value--opaque">{entry.display}</span>
          {:else}
            <span class="reach-value" data-mono>{entry.display}</span>
          {/if}
          {#if entry.service && !entry.opaque}<span class="reach-meta">{entry.service}</span>{/if}
          {#if entry.name}<span class="reach-meta">{entry.name}</span>{/if}
          {#if entry.note}<span class="reach-meta">{entry.note}</span>{/if}
          {#if entry.observed}
            <Chip tone="muted" size="xs" title="Seen in the archive, not in the address book">observed</Chip>
          {/if}
        </span>
        <span class="reach-actions">
          <CopyButton
            revealOnHover
            copied={copiedKey === entry.key}
            ariaLabel={`Copy ${entry.label}`}
            copiedAriaLabel={`Copied ${entry.label}`}
            onclick={() => void copy(entry)}
          />
          {@render actions?.(entry)}
        </span>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .reach-block {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .reach-row {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--space-2);
    border-radius: var(--radius-sm);
    padding: var(--space-1) var(--space-2);
    margin-inline: calc(-1 * var(--space-2));
  }

  .reach-row:hover,
  .reach-row:focus-within {
    background: var(--bg-subtle);
  }

  /* The copy control stays quiet until the row is hovered or focused;
   * CopyButton's own rule keeps it visible on touch and when focused. */
  .reach-row:hover :global(.kit-copy-btn--reveal),
  .reach-row:focus-within :global(.kit-copy-btn--reveal) {
    opacity: 1;
  }

  .reach-icon {
    display: inline-flex;
    flex: none;
    color: var(--text-muted);
  }

  .reach-main {
    display: flex;
    min-width: 0;
    flex: 1;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1) var(--space-3);
  }

  .reach-value {
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    text-overflow: ellipsis;
    white-space: nowrap;
    user-select: text;
  }

  .reach-value--opaque {
    font-weight: 500;
  }

  .reach-meta {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .reach-actions {
    display: flex;
    flex: none;
    align-items: center;
    gap: var(--space-1);
  }
</style>
