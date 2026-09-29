<script lang="ts">
  import { Button, KbdBadge } from '@kenn-io/kit-ui';

  import type { APIClient } from '../../api/client';
  import type {
    ExplorePreflightResponse as GeneratedExplorePreflightResponse,
    ExploreSelection as GeneratedExploreSelection,
  } from '../../api/generated/models';
  import type { AllMatchingExploreSelection } from '../../explore/models';
  import type { ExploreSelectionState } from '../../explore/state.svelte';
  import MeetingContextExport from '../meetings/MeetingContextExport.svelte';

  type Preflight = GeneratedExplorePreflightResponse;

  let {
    selection,
    totalCount,
    allMatching = undefined,
    preflight = undefined,
    client = undefined,
    meetingSelection = undefined,
    onExport = undefined,
    onOpenInSource = undefined,
  }: {
    selection: ExploreSelectionState;
    totalCount?: number;
    allMatching?: AllMatchingExploreSelection;
    preflight?: Preflight;
    client?: APIClient;
    meetingSelection?: GeneratedExploreSelection;
    onExport?: () => void;
    onOpenInSource?: () => void;
  } = $props();

  const exportReason = $derived(preflight?.unavailable_actions.find((item) => item.action === 'export')?.reason);
  const openReason = $derived(preflight?.unavailable_actions.find((item) => item.action === 'open_in_source')?.reason);
  const exportTarget = $derived(preflight?.action_targets?.find((item) => item.action === 'export'));
  const contextSelectionCount = $derived(
    meetingSelection?.mode === 'explicit'
      ? (meetingSelection.row_keys?.length ?? 0)
      : totalCount === undefined
        ? undefined
        : Math.max(0, totalCount - (meetingSelection?.exclusions?.length ?? 0)),
  );
  const contextDisabledReason = $derived(
    contextSelectionCount !== undefined && contextSelectionCount > 100
      ? 'Meeting context accepts at most 100 meetings.'
      : '',
  );

  const message = $derived.by(() => {
    if (selection.mode === 'all_matching') {
      const total = totalCount === undefined ? 'matching' : totalCount.toLocaleString();
      const except = selection.exclusions.size;
      return `All ${total} matching items selected${except > 0 ? `, except ${except}` : ''}`;
    }
    if (selection.count === 0) return 'No items selected';
    return `${selection.count.toLocaleString()} selected`;
  });
</script>

<div class="selection-bar" class:selection-bar--active={selection.mode === 'all_matching' || selection.count > 0}>
    <span role="status" aria-live="polite">{message}</span>
    <span class="shortcut"><KbdBadge keys={['Space']} /> toggle</span>
    <span class="shortcut"><KbdBadge keys={['A']} /> visible</span>
    {#if allMatching && selection.mode === 'explicit' && selection.count > 0}
      <Button
        size="sm"
        tone="info"
        surface="soft"
        label={`Select all ${totalCount?.toLocaleString() ?? ''} matching items`.replace(
          'all  matching',
          'all matching',
        )}
        onclick={() => selection.selectAllMatching(allMatching)}
      />
    {/if}
    {#if preflight && !exportReason && exportTarget && onExport}
      <Button size="sm" tone="info" surface="soft" label="Export selection" onclick={onExport} />
    {:else if exportReason}
      <span class="action-reason">Export: {exportReason}</span>
    {/if}
    {#if preflight && !openReason && onOpenInSource}
      <Button size="sm" tone="info" surface="soft" label="Open selection in source" onclick={onOpenInSource} />
    {:else if openReason}
      <span class="action-reason">Open in source: {openReason}</span>
    {/if}
    {#if client && meetingSelection}
      <MeetingContextExport
        {client}
        request={{ selection: meetingSelection }}
        disabledReason={contextDisabledReason}
      />
    {/if}
    <Button
      size="sm"
      surface="soft"
      label="Clear selection"
      disabled={selection.mode === 'explicit' && selection.count === 0}
      onclick={() => selection.clear()}
    />
</div>

<style>
  /* A borderless 32px toolbar row on the canvas. An active selection is
   * announced by the app-wide selection language — the 2px accent bar —
   * rather than by boxing the row. */
  .selection-bar {
    display: flex;
    min-height: 32px;
    align-items: center;
    gap: var(--space-5);
    padding: 0 var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .selection-bar--active {
    box-shadow: inset 2px 0 0 var(--accent-blue);
  }

  .selection-bar--active [role='status'] {
    color: var(--text-primary);
  }

  [role='status'] {
    margin-right: auto;
    font-weight: 600;
  }

  .shortcut {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    white-space: nowrap;
  }

  .action-reason {
    max-width: 18rem;
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }

  @media (max-width: 760px) {
    .shortcut {
      display: none;
    }
  }
</style>
