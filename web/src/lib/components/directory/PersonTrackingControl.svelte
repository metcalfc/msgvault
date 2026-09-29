<script lang="ts">
  import { Button, Chip, EmptyState, Spinner, Toggle, formatTimestamp } from '@kenn-io/kit-ui';
  import { onDestroy, tick, untrack } from 'svelte';

  import type { APIClient } from '../../api/client';
  import { PersonTrackingController } from '../../directory/person-tracking-controller.svelte';

  interface Props {
    client: APIClient;
    personID: number;
    onAnnounce?: (message: string) => void;
  }

  let { client, personID, onAnnounce = () => undefined }: Props = $props();
  const controller = new PersonTrackingController(untrack(() => client));
  let root = $state<HTMLElement>();
  let catalogRetryIncludesSensitive = $state(false);

  $effect.pre(() => {
    const nextPersonID = personID;
    catalogRetryIncludesSensitive = false;
    untrack(() => { void controller.setPerson(nextPersonID); });
  });

  onDestroy(() => controller.destroy());

  function kindLabel(kind: string): string {
    return kind.length === 0 ? 'Unknown' : `${kind[0]!.toUpperCase()}${kind.slice(1)}`;
  }

  async function finishInContext(contextToken: number, announcement: string | null = null): Promise<void> {
    if (!controller.isContextCurrent(contextToken)) return;
    await tick();
    if (!controller.isContextCurrent(contextToken)) return;
    if (announcement) onAnnounce(announcement);
    const target = root?.querySelector<HTMLElement>('input:not(:disabled)') ??
      root?.querySelector<HTMLElement>('h3');
    if (target?.isConnected) target.focus();
  }

  async function changeTracking(desired: boolean): Promise<void> {
    const contextToken = controller.contextToken;
    const outcome = await controller.setTracked(desired);
    if (outcome.kind !== 'confirmed' && outcome.kind !== 'reconciled') return;
    if (!controller.isContextCurrent(contextToken)) return;
    const announcement = controller.announcement;
    await finishInContext(contextToken, announcement);
  }

  async function retryTracking(): Promise<void> {
    const contextToken = controller.contextToken;
    await controller.retryTracking();
    if (!controller.isContextCurrent(contextToken)) return;
    if (!controller.trackingError && controller.tracking) await finishInContext(contextToken);
  }

  async function loadSensitiveCatalog(): Promise<void> {
    catalogRetryIncludesSensitive = true;
    await controller.retryCatalog(true);
  }
</script>

<section
  bind:this={root}
  class="maintenance"
  data-section
  aria-labelledby={`person-${personID}-profile-maintenance-heading`}
>
  <header data-section-header>
    <div>
      <h3 id={`person-${personID}-profile-maintenance-heading`} data-section-title tabindex="-1">Profile maintenance</h3>
      <p data-meta>Tracking makes this person eligible for future automatic profile maintenance.</p>
    </div>
    {#if controller.trackingLoading || controller.catalogLoading}
      <span class="working" aria-label="Loading profile maintenance" aria-busy="true">
        <Spinner size={14} label="Loading profile maintenance" />
      </span>
    {/if}
  </header>

  {#if controller.trackingError}
    <div class="notice" role="alert">
      <span>{controller.trackingError}</span>
      <Button
        size="sm"
        surface="soft"
        label={controller.stateUnknown ? 'Retry profile maintenance state' : 'Retry profile maintenance'}
        disabled={controller.trackingLoading || controller.pending}
        onclick={() => void retryTracking()}
      />
    </div>
  {/if}

  {#if controller.tracking}
    <div class="tracking-row">
      <Toggle
        checked={controller.tracking.tracked}
        ariaLabel="Track this person for profile maintenance"
        disabled={controller.trackingLoading || controller.pending || controller.stateUnknown}
        onchange={(checked) => void changeTracking(checked)}
      />
      {#if controller.tracking.tracked_at}
        <span class="tracked-time" data-meta>
          Tracked since
          <time datetime={controller.tracking.tracked_at}>{formatTimestamp(controller.tracking.tracked_at)}</time>
        </span>
      {/if}
    </div>
  {:else if !controller.trackingLoading && !controller.trackingError}
    <p data-meta>Profile maintenance state is unavailable.</p>
  {/if}

  {#if controller.pending}
    <p class="working" role="status" aria-busy="true">
      <Spinner size={14} label="Updating profile maintenance" />
      Updating profile maintenance…
    </p>
  {/if}

  <details class="catalog-disclosure">
    <summary>What can be maintained?</summary>
    <div class="catalog" data-section aria-labelledby={`person-${personID}-eligible-fields-heading`}>
      <div data-section-header>
        <div>
          <h4 id={`person-${personID}-eligible-fields-heading`} data-row-title>Eligible profile fields</h4>
          <p data-meta>Definitions describe fields the maintenance system may update; they do not show this person's values.</p>
        </div>
        {#if !controller.catalogIncludesSensitive}
          <Button
            size="sm"
            surface="soft"
            label="Show sensitive eligible fields"
            disabled={controller.catalogLoading}
            onclick={() => void loadSensitiveCatalog()}
          />
        {:else}
          <span data-meta>Sensitive eligible fields are shown.</span>
        {/if}
      </div>

      {#if controller.catalogError}
        <div class="notice" role="alert">
          <span>{controller.catalogError}</span>
          <Button
            size="sm"
            surface="soft"
            label="Retry eligible profile fields"
            disabled={controller.catalogLoading}
            onclick={() => void controller.retryCatalog(catalogRetryIncludesSensitive)}
          />
        </div>
      {/if}

      {#if controller.targets.length > 0}
        <ul class="target-list" data-detail-list>
          {#each controller.targets as target}
            <li data-detail-row>
              <span data-detail-label>{target.description}</span>
              <span data-detail-value>{kindLabel(target.kind)} · {target.value_type} · {target.cardinality}</span>
              <span data-detail-actions>
                {#if target.sensitive}<Chip tone="warning" size="xs" uppercase={false}>Sensitive</Chip>{/if}
              </span>
            </li>
          {/each}
        </ul>
      {:else if !controller.catalogLoading && !controller.catalogError}
        <EmptyState
          title="No eligible profile fields"
          description="The server catalog does not currently expose fields for automatic maintenance."
        />
      {/if}
    </div>
  </details>
</section>

<style>
  h3, h4, p { margin: 0; }
  .tracking-row, .notice { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
  .tracking-row { justify-content: flex-start; }
  .working, .tracked-time { display: flex; align-items: center; gap: var(--space-2); }
  .catalog-disclosure summary { cursor: pointer; color: var(--text-secondary); font-size: var(--font-size-sm); }
  .catalog-disclosure[open] summary { margin-bottom: var(--space-3); }
  /* A failure reads as a toned row with a 2px bar, not a box. */
  .notice { padding: var(--space-2) var(--space-3); border-left: 2px solid var(--status-error-ink); background: var(--status-error-bg); color: var(--status-error-ink); font-size: var(--font-size-sm); }

  @media (max-width: 760px) {
    .notice { align-items: stretch; flex-direction: column; }
  }
</style>
