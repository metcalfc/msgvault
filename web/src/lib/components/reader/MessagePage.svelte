<script lang="ts">
  import { Button } from '@kenn-io/kit-ui';
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
  import { getMessage } from '../../api/generated/api/api';
  import type { MessageDetail } from '../../api/generated/models';
  import type { APIClient } from '../../api/client';
  import ConversationView from './ConversationView.svelte';

  interface Props {
    client: APIClient;
    messageID: number;
    /** Returns to the previous view: browser Back when the page was opened
     * from inside the app, else the Inbox. */
    onBack?: () => void;
    /** Reports the loaded subject so the shell can title the page. */
    onSubject?: (subject: string) => void;
    /** Opens a participant's person page from their name. */
    onOpenPerson?: (participantID: number) => void;
  }

  let {
    client,
    messageID,
    onBack = () => window.location.assign('/inbox'),
    onSubject = () => undefined,
    onOpenPerson = undefined,
  }: Props = $props();
  let message = $state<MessageDetail>();
  let error = $state('');
  let retry = $state(0);

  $effect(() => {
    const id = messageID;
    void retry;
    const controller = new AbortController();
    message = undefined;
    error = '';
    void getMessage({ id }, { ...client, signal: controller.signal }).then(({ data, response }) => {
      if (controller.signal.aborted) return;
      if (!data) {
        error = response.status === 404 ? 'Message not found.' : 'Could not load this message.';
        return;
      }
      message = data;
      onSubject(data.subject || 'Message');
    }).catch(() => {
      if (!controller.signal.aborted) error = 'Could not load this message.';
    });
    return () => controller.abort();
  });
</script>

<main aria-label="Linked message" class="message-page">
  <header>
    <Button size="sm" surface="soft" label="Back" onclick={onBack}>
      <ArrowLeftIcon size={14} aria-hidden="true" />
    </Button>
    <h1 data-page-title>{message?.subject || 'Message'}</h1>
  </header>
  {#if error}
    <p role="alert">{error}</p>
    <Button onclick={() => retry += 1}>Retry</Button>
  {:else if message}
    <ConversationView {client} conversationId={message.conversation_id!} anchorId={message.id!} {onOpenPerson} />
  {:else}
    <p role="status">Loading message…</p>
  {/if}
</main>

<style>
  .message-page { display: flex; flex: 1; min-height: 0; flex-direction: column; width: 100%; max-width: 1080px; margin-inline: auto; padding: var(--space-5) var(--space-6); }
  header { display: flex; align-items: center; gap: var(--space-3); margin-bottom: var(--space-4); }
  h1 { margin: 0; font-size: var(--font-size-lg); font-weight: 600; }
</style>
