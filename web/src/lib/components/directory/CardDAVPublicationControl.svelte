<script lang="ts">
  import StatusNotice from '../common/StatusNotice.svelte';
  import { Button, Spinner, Toggle } from '@kenn-io/kit-ui';
  import { onDestroy, tick, untrack } from 'svelte';

  import type { APIClient } from '../../api/client';
  import { CardDAVPublicationController } from '../../carddav/publication-controller.svelte';

  interface Props {
    client: APIClient;
    personID: number;
    onOpenConflict?: (conflictID: number) => void;
    onOpenSettings?: () => void;
    onAnnounce?: (message: string) => void;
  }

  let {
    client,
    personID,
    onOpenConflict = () => undefined,
    onOpenSettings = () => undefined,
    onAnnounce = () => undefined
  }: Props = $props();
  const controller = new CardDAVPublicationController(untrack(() => client));
  let root = $state<HTMLElement>();

  $effect.pre(() => {
    const nextPersonID = personID;
    untrack(() => { void controller.setPerson(nextPersonID); });
  });

  onDestroy(() => controller.destroy());

  function stateText(state: NonNullable<typeof controller.publication>['state']): string {
    switch (state) {
      case 'unpublished': return 'Not published';
      case 'published': return 'Published';
      case 'pending': return 'Publication pending';
      case 'conflict': return 'Publication conflict';
    }
  }

  function pendingText(operation: NonNullable<NonNullable<typeof controller.publication>['pending_operation']>): string {
    switch (operation) {
      case 'create': return 'CardDAV publication is waiting to create this contact.';
      case 'update': return 'CardDAV publication is waiting to update this contact.';
      case 'delete': return 'CardDAV publication is waiting to remove this contact.';
    }
  }

  async function togglePublication(checked: boolean): Promise<void> {
    const publication = controller.publication;
    if (!publication) return;
    const outcome = publication.state === 'unpublished' && checked
      ? await controller.publish()
      : publication.state === 'published' && !checked
        ? await controller.unpublish()
        : { kind: 'ignored' as const };
    if (outcome.kind !== 'confirmed' && outcome.kind !== 'reconciled') return;
    if (controller.announcement) onAnnounce(controller.announcement);
    if (outcome.kind !== 'confirmed') return;
    await tick();
    const target = root?.querySelector<HTMLElement>('input:not(:disabled)') ??
      root?.querySelector<HTMLElement>('h3');
    if (target?.isConnected) target.focus();
  }
</script>

<section bind:this={root} class="publication" data-section aria-labelledby={`person-${personID}-carddav-publication-heading`}>
  <header data-section-header>
    <div>
      <h3 id={`person-${personID}-carddav-publication-heading`} data-section-title tabindex="-1">CardDAV publication</h3>
      <p data-meta>Publish this person to the selected CardDAV address book.</p>
    </div>
    {#if controller.loading}
      <span class="working" aria-label="Loading CardDAV publication" aria-busy="true">
        <Spinner size={14} label="Loading CardDAV publication" />
      </span>
    {/if}
  </header>

  {#if controller.error}
    <StatusNotice>
      <span>{controller.error}</span>
      <Button
        size="sm"
        surface="soft"
        label={controller.stateUnknown ? 'Retry CardDAV publication state' : 'Retry CardDAV publication'}
        disabled={controller.loading}
        onclick={() => void controller.retryState()}
      />
    </StatusNotice>
  {/if}

  {#if controller.unavailable}
    <div class="state-copy">
      <p>CardDAV publication is unavailable. Configure or repair it in CardDAV settings.</p>
      <Button label="Open CardDAV settings" surface="soft" onclick={onOpenSettings} />
    </div>
  {:else if controller.publication}
    {@const publication = controller.publication}
    <dl data-detail-list>
      <div data-detail-row>
        <dt data-detail-label>State</dt>
        <dd data-detail-value><strong>{stateText(publication.state)}</strong></dd>
        <dd data-detail-actions>
          {#if publication.state === 'unpublished' && publication.address_book}
            <Toggle
              checked={controller.pendingAction === 'publish'}
              ariaLabel="Publish person to CardDAV"
              disabled={!controller.canPublish()}
              onchange={(checked) => void togglePublication(checked)}
            />
          {:else if publication.state === 'published'}
            <Toggle
              checked={controller.pendingAction !== 'unpublish'}
              ariaLabel="Remove person from CardDAV"
              disabled={!controller.canUnpublish()}
              onchange={(checked) => void togglePublication(checked)}
            />
          {:else if publication.state === 'pending' || publication.state === 'conflict'}
            <Toggle
              checked={publication.desired}
              ariaLabel={publication.desired ? 'Publish person to CardDAV' : 'Remove person from CardDAV'}
              disabled
            />
          {/if}
        </dd>
      </div>
      <div data-detail-row>
        <dt data-detail-label>Desired publication</dt>
        <dd data-detail-value>{publication.desired ? 'Published' : 'Unpublished'}</dd>
      </div>
      <div data-detail-row>
        <dt data-detail-label>Address book</dt>
        <dd data-detail-value>
          {#if publication.address_book}{publication.address_book.name}{:else}No publish address book is selected.{/if}
        </dd>
        <dd data-detail-actions>
          {#if !publication.address_book}
            <Button size="sm" surface="soft" label="Open CardDAV settings" onclick={onOpenSettings} />
          {/if}
        </dd>
      </div>
    </dl>

    {#if controller.pendingAction}
      <p class="working" role="status" aria-busy="true">
        <Spinner size={14} label="Updating CardDAV publication" />
        {controller.pendingAction === 'publish' ? 'Publishing this person to CardDAV…' : 'Removing this person from CardDAV…'}
      </p>
    {/if}

    {#if publication.state === 'pending'}
      <p data-meta>{publication.pending_operation ? pendingText(publication.pending_operation) : 'CardDAV publication is pending.'}</p>
    {:else if publication.state === 'conflict'}
      {#if publication.conflict_id}
        <div class="state-copy">
          <Button
            tone="workflow"
            surface="solid"
            label={`Review CardDAV conflict ${publication.conflict_id}`}
            onclick={() => onOpenConflict(publication.conflict_id!)}
          />
        </div>
      {:else}
        <p data-meta>CardDAV conflict details are unavailable.</p>
      {/if}
    {/if}
  {:else if !controller.loading && !controller.error}
    <p data-meta>CardDAV publication state is unavailable.</p>
  {/if}
</section>

<style>
  h3, p, dl, dd { margin: 0; }
  .state-copy { display: grid; justify-items: start; gap: var(--space-3); min-width: 0; }
  .working { display: flex; align-items: center; gap: var(--space-2); }
</style>
