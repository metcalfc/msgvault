<script lang="ts">
  import type { APIClient } from '../../api/client';
  import type { ArchiveMessageDetail } from '../../archive/types';
  import { humanizeWhen, linkify, parseEventBody } from '../../reader/event-body';
  import { looksLikeHTML } from '../../util/html-text';
  import ContentFrame from './ContentFrame.svelte';
  import ParticipantPill from './ParticipantPill.svelte';

  interface Props {
    message: ArchiveMessageDetail;
    client?: APIClient;
    onOpenPerson?: (participantID: number) => void;
    onFilterPerson?: (participantID: number, label: string) => void;
  }

  let { message, client = undefined, onOpenPerson = undefined, onFilterPerson = undefined }: Props = $props();

  const parsed = $derived(parseEventBody(message.body, message.subject, message.sentAt));
  const organizer = $derived(message.from?.trim() || '');
</script>

<section class="event-card" aria-label="Event details">
  <dl class="event-facts">
    {#if parsed.when}
      <div><dt>When</dt><dd>{humanizeWhen(parsed.when)}</dd></div>
    {/if}
    {#if parsed.location}
      <div>
        <dt>Where</dt>
        <dd>
          {#each linkify(parsed.location) as segment, index (index)}
            {#if segment.href}<a href={segment.href} target="_blank" rel="noopener noreferrer">{segment.text}</a>{:else}{segment.text}{/if}
          {/each}
        </dd>
      </div>
    {/if}
    {#if organizer}
      <div>
        <dt>Organizer</dt>
        <dd><ParticipantPill value={organizer} {client} {onOpenPerson} {onFilterPerson} /></dd>
      </div>
    {/if}
    {#if message.recipients.length > 0}
      <div>
        <dt>Attendees</dt>
        <dd class="event-attendees">
          {#each message.recipients as attendee, index (`${index}:${attendee}`)}
            <ParticipantPill value={attendee} {client} {onOpenPerson} {onFilterPerson} />
          {/each}
        </dd>
      </div>
    {/if}
  </dl>

  {#if parsed.description}
    <!-- The description takes the same sanitized-frame path as an email
         body when it carries markup (ContentFrame runs lib/content/sanitize). -->
    {#if looksLikeHTML(parsed.description)}
      <ContentFrame {client} messageId={message.id} html={parsed.description} title="Event description" />
    {:else}
      <p class="event-description">
        {#each linkify(parsed.description) as segment, index (index)}
          {#if segment.href}<a href={segment.href} target="_blank" rel="noopener noreferrer">{segment.text}</a>{:else}{segment.text}{/if}
        {/each}
      </p>
    {/if}
  {/if}
</section>

<style>
  .event-card {
    display: grid;
    gap: var(--space-4);
  }

  .event-facts {
    display: grid;
    gap: var(--space-2);
    margin: 0;
  }

  .event-facts div {
    display: grid;
    grid-template-columns: 88px minmax(0, 1fr);
    align-items: baseline;
    gap: var(--space-3);
  }

  dt {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  dd {
    min-width: 0;
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    overflow-wrap: anywhere;
  }

  .event-attendees {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }

  a {
    color: var(--link-ink);
  }

  .event-description {
    max-width: 70ch;
    margin: 0;
    color: var(--text-primary);
    font-family: var(--font-reading);
    font-size: 14px;
    line-height: var(--leading-reading);
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }
</style>
