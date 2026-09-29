<script lang="ts" module>
  export type Modality = 'email' | 'text' | 'chat' | 'event' | 'meeting' | 'other';

  const TEXT_TYPES = new Set(['imessage', 'sms', 'mms', 'text']);

  /** Which modality a row belongs to, from the server's row kind first and
   * the message type second. Text messages (iMessage, SMS) are told apart
   * from other chat services so the two read differently in a list. */
  export function rowModality(kind: string, messageType: string): Modality {
    const normalizedKind = kind.toLowerCase();
    const normalizedType = messageType.toLowerCase();
    if (TEXT_TYPES.has(normalizedType)) return 'text';
    if (normalizedKind === 'email' || normalizedType === 'email') return 'email';
    if (normalizedKind === 'event' || normalizedType === 'calendar' || normalizedType === 'calendar_event') return 'event';
    if (normalizedKind === 'meeting' || normalizedType === 'meeting' || normalizedType === 'meeting_transcript') return 'meeting';
    if (normalizedKind === 'conversation' || normalizedType === 'chat') return 'chat';
    return 'other';
  }
</script>

<script lang="ts">
  import CalendarIcon from '@lucide/svelte/icons/calendar';
  import CircleDotIcon from '@lucide/svelte/icons/circle-dot';
  import FileIcon from '@lucide/svelte/icons/file';
  import MailIcon from '@lucide/svelte/icons/mail';
  import MessageCircleIcon from '@lucide/svelte/icons/message-circle';
  import MessagesSquareIcon from '@lucide/svelte/icons/messages-square';
  import VideoIcon from '@lucide/svelte/icons/video';

  import { isKnownMessageType, messageTypeLabel } from '../../util/labels';

  let { kind, messageType, compact = false }: { kind: string; messageType: string; compact?: boolean } = $props();

  const modality = $derived(rowModality(kind, messageType));
  /** The label prefers the humanized message type ("Text (iMessage)",
   * "Event") and falls back to the kind when the type is unknown. */
  const label = $derived.by(() => {
    const normalizedKind = kind.toLowerCase();
    const normalizedType = messageType.toLowerCase();
    const typeLabel = isKnownMessageType(normalizedType) ? messageTypeLabel(normalizedType) : '';
    if (typeLabel) return typeLabel;
    if (normalizedKind === 'email') return 'Email';
    if (normalizedKind === 'conversation') return 'Conversation';
    if (normalizedKind === 'event') return 'Event';
    if (normalizedKind === 'meeting') return 'Meeting';
    if (normalizedKind === 'file') return 'File';
    return 'Item';
  });
  const Icon = $derived(
    modality === 'email' ? MailIcon
      : modality === 'text' ? MessageCircleIcon
        : modality === 'chat' ? MessagesSquareIcon
          : modality === 'event' ? CalendarIcon
            : modality === 'meeting' ? VideoIcon
              : kind.toLowerCase() === 'file' ? FileIcon : CircleDotIcon
  );
</script>

<span class="row-kind" data-modality={modality} aria-label={label} title={label}>
  <Icon class="row-kind__icon" size={16} strokeWidth={2} aria-hidden="true" />
  {#if !compact}<span class="row-kind__label">{label}</span>{/if}
</span>

<style>
  .row-kind {
    --row-kind-ink: var(--modality-other);

    display: inline-flex;
    min-width: 0;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-secondary);
  }

  .row-kind[data-modality='email'] { --row-kind-ink: var(--modality-email); }
  .row-kind[data-modality='text'] { --row-kind-ink: var(--modality-text); }
  .row-kind[data-modality='chat'] { --row-kind-ink: var(--modality-chat); }
  .row-kind[data-modality='event'] { --row-kind-ink: var(--modality-event); }
  .row-kind[data-modality='meeting'] { --row-kind-ink: var(--modality-meeting); }

  .row-kind :global(.row-kind__icon) {
    flex: none;
    color: var(--row-kind-ink);
  }

  .row-kind__label {
    overflow: hidden;
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
