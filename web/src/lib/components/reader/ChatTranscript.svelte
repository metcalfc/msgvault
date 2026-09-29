<script lang="ts" module>
  import type { MessageDetail } from '../../api/generated/models';

  export interface ChatRun {
    key: string;
    day: string;
    speaker: string;
    fromMe: boolean;
    messages: MessageDetail[];
  }

  const PHONE = /^[+()\d\s.-]{7,}$/;

  /**
   * Names each speaker for a transcript. A chat participant is often known
   * only by phone number; the name comes from, in order, the message's own
   * sender name, a name the same number carried on another message in the
   * window, and — in a one-to-one chat — the row's counterpart name.
   */
  export function speakerNames(messages: readonly MessageDetail[], counterpartLabel = ''): (message: MessageDetail) => string {
    const byNumber = new Map<string, string>();
    for (const message of messages) {
      const number = (message.from_phone || message.from_email || '').trim();
      const name = message.from_name?.trim();
      if (number && name && !PHONE.test(name)) byNumber.set(number, name);
    }
    const otherSenders = new Set(
      messages.filter((message) => !message.is_from_me).map((message) => (message.from_phone || message.from_email || message.from).trim())
    );
    const oneToOne = otherSenders.size === 1;
    return (message) => {
      if (message.is_from_me) return 'You';
      const name = message.from_name?.trim();
      if (name && !PHONE.test(name)) return name;
      const number = (message.from_phone || message.from_email || '').trim();
      const known = number ? byNumber.get(number) : undefined;
      if (known) return known;
      if (oneToOne && counterpartLabel.trim() && !PHONE.test(counterpartLabel.trim())) return counterpartLabel.trim();
      return message.from || number || 'Unknown sender';
    };
  }

  function localDay(value: string): string {
    const date = new Date(value);
    return Number.isNaN(date.valueOf())
      ? value
      : `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
  }

  /** Consecutive messages from one speaker on one day form a run, so the
   * speaker is named once per run and day dividers fall between runs. */
  export function chatRuns(messages: readonly MessageDetail[], speaker: (message: MessageDetail) => string): ChatRun[] {
    const runs: ChatRun[] = [];
    for (const message of messages) {
      const day = localDay(message.sent_at);
      const name = speaker(message);
      const fromMe = message.is_from_me ?? false;
      const last = runs.at(-1);
      if (last && last.day === day && last.speaker === name && last.fromMe === fromMe) last.messages.push(message);
      else runs.push({ key: `${message.id}`, day, speaker: name, fromMe, messages: [message] });
    }
    return runs;
  }
</script>

<script lang="ts">
  import type { FileViewerTarget } from '../../explore/models';
  import MessageAttachments from './MessageAttachments.svelte';

  interface Props {
    messages: MessageDetail[];
    anchorId: number;
    conversationId: number;
    /** The row's counterpart name, used for an unnamed one-to-one partner. */
    counterpartLabel?: string;
    onSelect?: (id: number) => void;
    onOpenAttachment?: (file: FileViewerTarget) => void;
  }

  let {
    messages,
    anchorId,
    conversationId,
    counterpartLabel = '',
    onSelect = undefined,
    onOpenAttachment = undefined
  }: Props = $props();

  const speaker = $derived(speakerNames(messages, counterpartLabel));
  const runs = $derived(chatRuns(messages, speaker));

  function dayLabel(value: string): string {
    const date = new Date(value);
    return Number.isNaN(date.valueOf())
      ? value
      : new Intl.DateTimeFormat(undefined, { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' }).format(date);
  }

  function time(value: string): string {
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat(undefined, { timeStyle: 'short' }).format(date);
  }
</script>

<div class="chat-transcript" role="list" aria-label="Chat transcript">
  {#each runs as run, index (run.key)}
    {#if index === 0 || runs[index - 1]!.day !== run.day}
      <div class="day-divider" role="listitem"><span>{dayLabel(run.messages[0]!.sent_at)}</span></div>
    {/if}
    <div class="run" class:run--mine={run.fromMe} role="listitem" aria-label={`${run.speaker}, ${run.messages.length} ${run.messages.length === 1 ? 'message' : 'messages'}`}>
      {#if !run.fromMe}<span class="speaker">{run.speaker}</span>{/if}
      {#each run.messages as message (message.id)}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_tabindex -- a bubble takes focus so its time shows; clicking marks it as the thread anchor, which h/l also move. -->
        <article
          class="bubble"
          class:bubble--anchor={message.id === anchorId}
          data-message-id={message.id}
          aria-current={message.id === anchorId ? 'true' : undefined}
          aria-label={`${run.speaker} at ${time(message.sent_at)}`}
          tabindex="0"
          onclick={() => onSelect?.(message.id)}
        >
          <p class="bubble__text">{message.body || message.snippet}</p>
          {#if (message.attachments ?? []).length > 0}
            <MessageAttachments
              attachments={(message.attachments ?? []).map((attachment) => ({
                id: attachment.id,
                filename: attachment.filename,
                mimeType: attachment.mime_type,
                sizeBytes: attachment.size_bytes
              }))}
              messageId={message.id}
              {conversationId}
              onOpen={onOpenAttachment}
            />
          {/if}
          <time class="bubble__time" datetime={message.sent_at} data-mono>{time(message.sent_at)}</time>
        </article>
      {/each}
    </div>
  {/each}
</div>

<style>
  .chat-transcript {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    padding: var(--space-4);
  }

  .day-divider {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .day-divider::before,
  .day-divider::after {
    height: 1px;
    flex: 1;
    background: var(--hairline);
    content: '';
  }

  .run {
    display: flex;
    max-width: 78%;
    align-items: flex-start;
    flex-direction: column;
    gap: var(--space-1);
  }

  .run--mine {
    align-self: flex-end;
    align-items: flex-end;
  }

  .speaker {
    padding: 0 var(--space-3);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .bubble {
    position: relative;
    max-width: 100%;
    padding: var(--space-2) var(--space-3);
    border-radius: 14px;
    background: var(--bg-inset);
    color: var(--text-primary);
    cursor: default;
    outline: none;
  }

  .run--mine .bubble {
    background: color-mix(in srgb, var(--accent-blue) 16%, var(--bg-surface));
  }

  .bubble--anchor {
    box-shadow: 0 0 0 2px var(--accent-blue);
  }

  .bubble:focus-visible {
    outline: var(--focus-ring);
    outline-offset: 2px;
  }

  .bubble__text {
    margin: 0;
    font-family: var(--font-reading);
    font-size: 14px;
    line-height: var(--leading-reading);
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }

  .bubble :global(.message-attachments) {
    padding: var(--space-2) 0 0;
  }

  /* Times stay out of the way until the reader points at or focuses a bubble. */
  .bubble__time {
    display: block;
    height: 0;
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    opacity: 0;
  }

  .bubble:hover .bubble__time,
  .bubble:focus-within .bubble__time,
  .bubble:focus .bubble__time {
    height: auto;
    margin-top: var(--space-1);
    opacity: 1;
  }
</style>
