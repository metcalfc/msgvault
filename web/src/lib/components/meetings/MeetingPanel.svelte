<script lang="ts">
  import { Button, SelectDropdown, TextInput } from '@kenn-io/kit-ui';
  import { onDestroy, untrack } from 'svelte';
  import type { APIClient } from '../../api/client';
  import type { MeetingActionsRequest, MeetingRef } from '../../api/generated/models';
  import { canonicalFingerprint } from '../../explore/selection';
  import { createMeetingsAPI } from '../../meetings/api';
  import { MeetingsController, type MeetingPanelScope } from '../../meetings/controller.svelte';
  import MeetingActions from './MeetingActions.svelte';
  import MeetingMetrics from './MeetingMetrics.svelte';

  let { client, scope, refreshKey = '', collapsible = false, onReloadScope = undefined, onOpenMeeting = undefined }: {
    client: APIClient;
    scope: MeetingPanelScope;
    /** UI invalidation only; the server still resolves the durable identity. */
    refreshKey?: string;
    /** Render as a closed-by-default disclosure so the surrounding surface
     * (timeline, results, person overview) stays above the fold. A scope
     * with no meetings collapses to a single "No meetings" line. */
    collapsible?: boolean;
    /** Explore owners must reload the corresponding result before publishing new authority. */
    onReloadScope?: () => void;
    onOpenMeeting?: (meeting: MeetingRef) => void;
  } = $props();
  const controller = new MeetingsController(createMeetingsAPI(untrack(() => client)));
  const scopeFingerprint = $derived(canonicalFingerprint({ scope, refreshKey }));
  let sourceStatus = $state('');
  let assigneeEmail = $state('');
  const statusOptions = [ { value: '', label: 'All source statuses' },
    { value: 'pending', label: 'Pending' }, { value: 'completed', label: 'Completed' },
    { value: 'cancelled', label: 'Cancelled' }, { value: 'unknown', label: 'Unknown' } ];
  const errors = $derived.by(() => {
    const failures = [controller.metricsError, controller.actionsError].filter((error) => error !== undefined);
    return failures.filter((error, index) => failures.findIndex((candidate) => candidate.message === error.message) === index);
  });
  const canReload = $derived(errors.some((error) => error.recovery === 'reload' || error.recovery === 'retry'));
  const actionCount = $derived(controller.actions?.total_count ?? 0);
  const filtersActive = $derived(Boolean(controller.filters.status || controller.filters.assigneeEmail));
  // The action filters only matter once there is something to filter; they
  // stay visible while a filter is active so a zero-result filter can be reset.
  const showActionFilters = $derived(actionCount > 0 || filtersActive);
  const noMeetings = $derived(
    controller.metrics !== undefined && controller.metrics.totals.meeting_count === 0 && actionCount === 0 && !filtersActive
  );
  // One <details> for the whole lifecycle: the summary line carries the
  // loading, error, empty, and loaded states so the header never swaps
  // elements or shifts as the scope resolves.
  let open = $state(false);
  const summaryLabel = $derived.by(() => {
    if (errors.length > 0) return 'Meeting activity · could not load';
    const count = controller.metrics?.totals.meeting_count;
    if (count === undefined) return controller.metricsLoading ? 'Meeting activity · loading…' : 'Meeting activity and follow-ups';
    if (noMeetings) return 'No meetings';
    return `Meeting activity and follow-ups · ${count.toLocaleString()} meetings`;
  });
  // An error inside a closed disclosure would be invisible: open it so the
  // alert and its Reload control are on screen.
  $effect(() => {
    if (errors.length > 0) open = true;
  });

  $effect.pre(() => {
    void scopeFingerprint;
    untrack(() => { void controller.setScope(scope, refreshKey); });
  });
  onDestroy(() => controller.destroy());

  function reload(): void {
    if (scope.kind === 'explore' && onReloadScope) onReloadScope();
    else void controller.reload();
  }

  function applyFilters(event: SubmitEvent): void {
    event.preventDefault();
    void controller.setFilters({ status: (sourceStatus || undefined) as MeetingActionsRequest['status'], assigneeEmail });
  }
</script>

{#snippet body()}
  {#if controller.metricsLoading}<p role="status">Loading meeting metrics…</p>{/if}
  {#each errors as error (error.message)}<p role="alert">{error.message}</p>{/each}
  {#if canReload}<Button label="Reload meeting activity" size="sm" surface="outline" onclick={reload} />{/if}
  {#if controller.metrics}<MeetingMetrics metrics={controller.metrics} />{/if}
  {#if showActionFilters}
    <form class="action-filters" onsubmit={applyFilters}>
      <SelectDropdown title="Source status" value={sourceStatus} options={statusOptions} onchange={(value) => (sourceStatus = value)} />
      <TextInput value={assigneeEmail} ariaLabel="Assignee email" placeholder="Assignee email" oninput={(value) => (assigneeEmail = value)} />
      <Button type="submit" label="Apply action filters" size="sm" surface="outline" />
    </form>
  {/if}
  {#if controller.actions}
    <p>{controller.actions.total_count.toLocaleString()} matching action items</p>
    <MeetingActions {client} evidence={controller.actions} {onOpenMeeting} />
    {#if controller.actions.next_cursor && !controller.actionsError}
      <Button label="Load more action items" size="sm" surface="outline" disabled={controller.actionsLoading} onclick={() => void controller.loadMore()} />
    {/if}
  {/if}
  {#if controller.actionsLoading}<p role="status">Loading action evidence…</p>{/if}
{/snippet}

{#if collapsible}
  <details class="meeting-overview" bind:open>
    <summary>{summaryLabel}</summary>
    <!-- The open body scrolls within a bounded height so a long action
         list never squeezes the timeline or results beneath it. -->
    <section class="meeting-panel meeting-panel--nested" aria-label="Meeting activity" data-scroll>
      {@render body()}
    </section>
  </details>
{:else}
  <section class="meeting-panel" aria-label="Meeting activity">
    <header><h2>Meeting activity and follow-ups</h2></header>
    {@render body()}
  </section>
{/if}

<style>
  .meeting-panel { display: grid; gap: var(--space-3); padding: var(--space-4); min-width: 0; }
  .meeting-panel--nested { max-height: 42vh; padding-inline: 0; overflow: auto; }
  h2, p { margin: 0; font-size: var(--font-size-sm); color: var(--text-secondary); }
  h2 { color: var(--text-primary); }
  .action-filters { display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center; }
  .meeting-overview { flex: none; min-width: 0; }
  .meeting-overview summary { cursor: pointer; color: var(--text-secondary); font-size: var(--font-size-sm); }
</style>
