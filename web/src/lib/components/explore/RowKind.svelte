<script lang="ts">
  import { isKnownMessageType, messageTypeLabel } from '../../util/labels';

  let { kind, messageType }: { kind: string; messageType: string } = $props();

  /** The icon follows the server's row kind; the label prefers the
   * humanized message type ("Text (iMessage)", "Event") and falls back to
   * the kind when the type is unknown. */
  const presentation = $derived.by(() => {
    const normalizedKind = kind.toLowerCase();
    const normalizedType = messageType.toLowerCase();
    const typeLabel = isKnownMessageType(normalizedType) ? messageTypeLabel(normalizedType) : '';
    if (normalizedKind === 'email') return { icon: '✉', label: typeLabel || 'Email' };
    if (normalizedKind === 'conversation') return { icon: '◌', label: typeLabel || 'Conversation' };
    if (normalizedKind === 'event') return { icon: '□', label: typeLabel || 'Event' };
    if (normalizedKind === 'meeting') return { icon: '◫', label: typeLabel || 'Meeting' };
    if (normalizedKind === 'file') return { icon: '▱', label: typeLabel || 'File' };
    if (normalizedType === 'email') return { icon: '✉', label: 'Email' };
    if (normalizedType === 'chat' || normalizedType === 'text') return { icon: '◌', label: typeLabel || 'Conversation' };
    if (normalizedType === 'calendar' || normalizedType === 'calendar_event') return { icon: '□', label: 'Event' };
    if (normalizedType === 'meeting' || normalizedType === 'meeting_transcript') return { icon: '◫', label: 'Meeting' };
    return { icon: '◇', label: typeLabel || 'Item' };
  });
</script>

<span class="row-kind" aria-label={presentation.label} title={presentation.label}>
  <span class="row-kind__icon" aria-hidden="true">{presentation.icon}</span>
  <span class="row-kind__label">{presentation.label}</span>
</span>

<style>
  .row-kind {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-secondary);
  }

  .row-kind__icon {
    width: 14px;
    color: var(--artifact-ink);
    font-size: var(--font-size-sm);
    line-height: 1;
    text-align: center;
  }

  .row-kind__label {
    overflow: hidden;
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
