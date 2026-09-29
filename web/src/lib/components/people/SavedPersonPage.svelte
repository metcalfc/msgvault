<script lang="ts">
  import { Button, Notice } from '@kenn-io/kit-ui';
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';

  import type { MeetingRef } from '../../api/generated/models';
  import type { APIClient } from '../../api/client';
  import type { DirectoryController } from '../../directory/controller.svelte';
  import type { PersonTab } from '../../routing/routes';
  import PersonDetail from '../directory/PersonDetail.svelte';

  interface Props {
    client: APIClient;
    controller: DirectoryController;
    personID: number;
    tab: PersonTab;
    onTabChange: (tab: PersonTab) => void;
    onBack: () => void;
    onOpenPerson: (personID: number) => void;
    onOpenCardDAVConflict?: (conflictID: number) => void;
    onOpenCardDAVSettings?: () => void;
    onAnnounce?: (message: string) => void;
    onOpenMeeting?: (meeting: MeetingRef) => void;
    onOpenMessage?: (messageID: number) => void;
    onOpenMeetingPage?: (meetingID: number) => void;
  }

  let {
    client, controller, personID, tab, onTabChange, onBack, onOpenPerson,
    onOpenCardDAVConflict = undefined, onOpenCardDAVSettings = undefined, onAnnounce = undefined,
    onOpenMeeting = undefined, onOpenMessage = undefined, onOpenMeetingPage = undefined,
  }: Props = $props();
</script>

<main class="person-page" aria-label="Person">
  <h1 class="kit-sr-only">Person</h1>
  <nav class="person-page__nav" aria-label="Person navigation">
    <Button size="sm" surface="soft" label="People" ariaLabel="Back to People" onclick={onBack}>
      <ArrowLeftIcon size={14} aria-hidden="true" />
    </Button>
  </nav>
  {#if controller.detailLoading && !controller.detail}
    <p class="state" role="status">Loading person…</p>
  {:else if controller.detail && controller.selectedPersonID === personID}
    <PersonDetail
      {client}
      bundle={controller.detail}
      {personID}
      profileController={controller.profile}
      entityController={controller.entity}
      {tab}
      {onTabChange}
      {onOpenPerson}
      onSplitCommitted={(context) => controller.reconcilePersonSplit(context)}
      onReload={() => void controller.reloadSelection()}
      {onOpenCardDAVConflict}
      {onOpenCardDAVSettings}
      {onAnnounce}
      {onOpenMeeting}
      {onOpenMessage}
      {onOpenMeetingPage}
    />
  {:else if controller.error}
    <Notice tone="error" message={controller.error} />
  {:else}
    <p class="state" role="status">Loading person…</p>
  {/if}
</main>

<style>
  .person-page {
    display: flex;
    flex: 1;
    min-height: 0;
    flex-direction: column;
    gap: var(--space-2);
    width: 100%;
    max-width: 1080px;
    margin-inline: auto;
    padding: var(--space-4) var(--space-6);
    overflow: auto;
  }

  .person-page__nav { display: flex; }
  .state { color: var(--text-muted); font-size: var(--font-size-sm); }

  @media (max-width: 760px) {
    .person-page { padding: var(--space-3); }
  }
</style>
