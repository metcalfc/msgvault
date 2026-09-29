<script lang="ts">
  import { Button } from '@kenn-io/kit-ui';
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';

  import { getMessage } from '../../api/generated/api/api';
  import type { MeetingRef, MessageDetail } from '../../api/generated/models';
  import type { APIClient } from '../../api/client';
  import { humanizeDate } from '../../util/dates';
  import ReadingPane from '../reader/ReadingPane.svelte';
  import MeetingActions from './MeetingActions.svelte';

  interface Props {
    client: APIClient;
    /** The meeting's message id: a calendar event or a transcript. */
    meetingID: number;
    onBack: () => void;
    onOpenPerson?: (participantID: number) => void;
    onOpenMeeting?: (meeting: MeetingRef) => void;
    onTitle?: (title: string) => void;
  }

  let { client, meetingID, onBack, onOpenPerson = undefined, onOpenMeeting = undefined, onTitle = () => undefined }: Props = $props();
  let message = $state<MessageDetail>();
  let error = $state('');
  let retry = $state(0);
  let anchorID = $state<number>();

  $effect(() => {
    const id = meetingID;
    void retry;
    const controller = new AbortController();
    message = undefined;
    error = '';
    anchorID = undefined;
    void getMessage({ id }, { ...client, signal: controller.signal }).then(({ data, response }) => {
      if (controller.signal.aborted) return;
      if (!data) {
        error = response.status === 404 ? 'Meeting not found.' : 'Could not load this meeting.';
        return;
      }
      message = data;
      onTitle(data.subject || 'Meeting');
    }).catch(() => {
      if (!controller.signal.aborted) error = 'Could not load this meeting.';
    });
    return () => controller.abort();
  });

  const isTranscript = $derived(message?.message_type === 'meeting_transcript');
</script>

<main class="meeting-page" aria-label="Meeting">
  <header class="meeting-page__header">
    <Button size="sm" surface="soft" label="Meetings" ariaLabel="Back to Meetings" onclick={onBack}>
      <ArrowLeftIcon size={14} aria-hidden="true" />
    </Button>
    <div>
      <h1 data-page-title>{message?.subject || 'Meeting'}</h1>
      {#if message?.sent_at}
        <p class="meeting-page__meta">
          {isTranscript ? 'Transcript' : 'Calendar event'} · <time datetime={message.sent_at}>{humanizeDate(message.sent_at)}</time>
        </p>
      {/if}
    </div>
  </header>
  {#if error}
    <p role="alert">{error}</p>
    <Button label="Retry" onclick={() => retry += 1} />
  {:else if message}
    <div class="meeting-page__reader">
      <!-- Events render as the event card and transcripts as their text;
           attendees and speakers are person pills either way. -->
      <ReadingPane {client} selection={{ kind: 'archive', message }} predicate={{ filters: [], presentation: 'table' }}
        conversationAnchorId={anchorID} onConversationAnchorChange={(id) => (anchorID = id)}
        {onOpenPerson} {onOpenMeeting} />
    </div>
    {#if isTranscript}
      <section class="meeting-page__actions" aria-label="Action items">
        <MeetingActions {client} request={{ scope: { message_ids: [message.id] }, limit: 100 }} {onOpenMeeting} />
      </section>
    {/if}
  {:else}
    <p role="status">Loading meeting…</p>
  {/if}
</main>

<style>
  .meeting-page {
    display: flex;
    flex: 1;
    min-height: 0;
    flex-direction: column;
    gap: var(--space-4);
    width: 100%;
    max-width: 1080px;
    margin-inline: auto;
    padding: var(--space-5) var(--space-6);
    overflow: auto;
  }

  .meeting-page__header { display: flex; align-items: flex-start; gap: var(--space-3); }
  h1, p { margin: 0; }
  h1 { font-size: var(--font-size-lg); font-weight: 600; }
  .meeting-page__meta { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .meeting-page__reader { min-height: 360px; }
  .meeting-page__actions { display: grid; gap: var(--space-3); }

  @media (max-width: 760px) {
    .meeting-page { padding: var(--space-3); }
  }
</style>
