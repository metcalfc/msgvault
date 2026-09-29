<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { SegmentedControl } from '@kenn-io/kit-ui';

  import type { MeetingRef } from '../../api/generated/models';
  import type { APIClient } from '../../api/client';
  import type { BoundCluster } from '../../people/clusters';
  import { RelationshipsController } from '../../relationships/controller.svelte';
  import type { RelationshipTimelineRow } from '../../relationships/models';
  import ReadingPane, { type ReadingPaneSelection } from '../reader/ReadingPane.svelte';
  import RelationshipHeader from '../relationships/RelationshipHeader.svelte';
  import RelationshipTimeline from '../relationships/RelationshipTimeline.svelte';
  import { localDayBoundsUTC, timelineRowToSelection } from '../relationships/timeline-support';

  interface Props {
    client: APIClient;
    /** The saved person's archive identities, busiest first. */
    clusters: BoundCluster[];
    /** Timeline: mail, texts, and meetings interleaved. Identities: the
     * cluster's addresses with link, unlink, and "Same person…". */
    mode: 'timeline' | 'identities';
    onOpenMeeting?: (meeting: MeetingRef) => void;
    onAnnounce?: (message: string) => void;
    /** Identity changes can move bindings; the person page reloads. */
    onIdentitiesChanged?: () => void;
  }

  let { client, clusters, mode, onOpenMeeting = undefined, onAnnounce = undefined, onIdentitiesChanged = undefined }: Props = $props();

  // A panel of its own: the shell's controller serves the contact page, and
  // this one must not disturb it.
  const controller = new RelationshipsController(untrack(() => client), () => Intl.DateTimeFormat().resolvedOptions().timeZone);
  onDestroy(() => controller.destroy());

  let chosen = $state<number>();
  const canonicalID = $derived(clusters.some((cluster) => cluster.canonicalID === chosen) ? chosen! : clusters[0]?.canonicalID);
  $effect(() => {
    const id = canonicalID;
    untrack(() => {
      selection = undefined;
      if (id === undefined) controller.clearTarget();
      else if (controller.target !== `cluster:${id}`) void controller.openTarget(`cluster:${id}`, { filters: [], presentation: 'table' });
    });
  });

  let selection = $state<ReadingPaneSelection>();
  let anchorID = $state<number>();
  let bounds = $state<{ start: string; end: string }>();

  function openRow(row: RelationshipTimelineRow): void {
    selection = timelineRowToSelection(row);
    anchorID = row.anchor_message_id;
    bounds = row.kind === 'chat_burst' && row.conversation_id !== undefined && row.anchor_message_id !== undefined
      ? localDayBoundsUTC(row.first_at ?? row.occurred_at)
      : undefined;
  }
</script>

<section class="cluster-panel" aria-label={mode === 'timeline' ? 'Timeline' : 'Archive identities'}>
  {#if clusters.length === 0}
    <p class="state">{mode === 'timeline'
      ? 'No archive activity is linked to this person yet. Link an identity under Maintenance to see their timeline.'
      : 'No archive identities are linked to this person.'}</p>
  {:else}
    {#if clusters.length > 1}
      <SegmentedControl
        ariaLabel="Identity"
        value={String(canonicalID)}
        options={clusters.map((cluster) => ({ value: String(cluster.canonicalID), label: `${cluster.label} · ${cluster.activityCount.toLocaleString()}` }))}
        onchange={(value) => (chosen = Number(value))}
      />
    {/if}
    {#if mode === 'identities'}
      <RelationshipHeader
        detail={controller.detail}
        loading={controller.timelineLoading}
        filesOpen={false}
        onFilesToggle={() => undefined}
        showViewToggle={false}
        nameAsHeading={false}
        {client}
        {onAnnounce}
        capturePersonMergeContext={() => controller.personMergeContextSnapshot()}
        onReconcilePersonMerge={async (context) => { await controller.reconcilePersonMerge(context); onIdentitiesChanged?.(); }}
        onLinkParticipants={async (a, b) => { const outcome = await controller.linkParticipants(a, b); onIdentitiesChanged?.(); return outcome; }}
        onUnlinkParticipants={async (a, b) => { const outcome = await controller.unlinkParticipants(a, b); onIdentitiesChanged?.(); return outcome; }}
      />
    {:else}
      <div class="timeline" class:with-reader={selection !== undefined}>
        <RelationshipTimeline
          rows={controller.timelineRows}
          loading={controller.timelineLoading}
          loadingMore={controller.timelineLoadingMore}
          hasMore={Boolean(controller.timelineCursor)}
          error={controller.timelineError}
          restartNotice={controller.timelineRestartNotice}
          selectedKey={selection?.kind === 'entry' ? selection.row.key : null}
          onRowOpen={openRow}
          onLoadMore={() => void controller.loadMoreTimeline()}
        />
        {#if selection}
          <div class="reader">
            <ReadingPane
              {client}
              {selection}
              predicate={{ filters: [], presentation: 'table' }}
              {onOpenMeeting}
              onClose={() => { selection = undefined; }}
              conversationAnchorId={anchorID}
              conversationStart={bounds?.start}
              conversationEnd={bounds?.end}
              onConversationAnchorChange={(id) => (anchorID = id)}
            />
          </div>
        {/if}
      </div>
    {/if}
  {/if}
</section>

<style>
  .cluster-panel { display: grid; gap: var(--space-4); min-height: 0; }
  .state { margin: 0; color: var(--text-muted); font-size: var(--font-size-sm); }
  .timeline { display: grid; gap: var(--space-4); min-height: 360px; }
  .timeline :global([role='grid']) { max-height: 60vh; }
  .reader { min-height: 320px; max-height: 70vh; overflow: auto; border-top: 1px solid var(--hairline); }
</style>
