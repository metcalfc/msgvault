<script lang="ts">
  import { Button, Card, EmptyState, Spinner } from '@kenn-io/kit-ui';

  import { percent } from '../../directory/enrichment-review-controller.svelte';
  import {
    organizationLabel,
    type OrganizationReviewController
  } from '../../directory/organization-review-controller.svelte';
  import { focusReviewCard } from '../../directory/review-focus';

  interface Props {
    controller: OrganizationReviewController;
  }

  let { controller }: Props = $props();

  let list = $state<HTMLElement>();
  let queueHeading = $state<HTMLHeadingElement>();

  // A decision removes its card; focus moves to the card that took its
  // place, so the queue can be worked through without scrolling back.
  async function decide(index: number, run: () => Promise<{ ok: boolean }>): Promise<void> {
    const result = await run();
    if (result.ok) await focusReviewCard(list, index, queueHeading);
  }

  $effect(() => {
    if (!controller.loaded && !controller.loading && !controller.error) void controller.load();
  });
</script>

<section class="organization-review" aria-labelledby="organization-review-heading">
  <div class="toolbar">
    <h2 bind:this={queueHeading} id="organization-review-heading" tabindex="-1">Organization matches to confirm</h2>
    <p>
      The organization check could not decide whether these names are an organization you already have.
      Confirming merges any separate organization created for the name and makes the name resolve to the
      existing one from now on.
    </p>
  </div>

  {#if controller.status}
    <p class="status" role="status" aria-live="polite">{controller.status}</p>
  {/if}
  {#if controller.decisionError}
    <p class="decision-error" role="alert">{controller.decisionError}</p>
  {/if}

  {#if controller.loading && controller.rows.length === 0}
    <p class="loading"><Spinner size={12} label="Loading organization matches" /> Loading organization matches…</p>
  {:else if controller.error}
    <div class="message" role="alert">
      <p>{controller.error}</p>
      <Button label="Retry organization matches" size="sm" onclick={() => void controller.load()} />
    </div>
  {:else if controller.rows.length === 0}
    <EmptyState
      title="No organization matches to confirm."
      description="Organization names the check is unsure about appear here for a decision."
    />
  {:else}
    <div class="list" bind:this={list}>
      {#each controller.rows as review, index (review.id)}
        {@const pending = controller.isPending(review.id)}
        {@const heading = `organization-review-${review.id}-heading`}
        <Card level="default" padding="md">
          <article class="review" data-review-card tabindex="-1" aria-labelledby={heading} aria-busy={pending}>
            <h3 id={heading}>{review.proposed_name}</h3>
            <dl class="judgment" aria-label={`Organization match ${review.id}`}>
              <div><dt>Proposed name</dt><dd>{organizationLabel(review.proposed_name, review.proposed_domain)}</dd></div>
              <div><dt>Existing organization</dt><dd>{organizationLabel(review.organization_name, review.organization_domain)}</dd></div>
              <div><dt>Same organization</dt><dd>{percent(review.probability)}</dd></div>
              <div>
                <dt>If confirmed</dt>
                <dd>{review.proposed_organization_id ? 'Merge the separate organization' : 'Add the name as an alias'}</dd>
              </div>
            </dl>
            <div class="actions">
              <Button
                label="Different organization"
                size="sm"
                disabled={pending}
                onclick={() => void decide(index, () => controller.reject(review.id))}
              />
              <Button
                label="Same organization"
                size="sm"
                tone="info"
                surface="solid"
                disabled={pending}
                onclick={() => void decide(index, () => controller.accept(review.id))}
              />
            </div>
          </article>
        </Card>
      {/each}
    </div>
  {/if}
</section>

<style>
  .organization-review, .toolbar, .list, .review { display: grid; gap: var(--space-3); }
  .organization-review { gap: var(--space-4); }
  h2, h3, p, dl, dd { margin: 0; }
  .toolbar p { color: var(--text-muted); }
  h3 { font-size: var(--font-size-lg); color: var(--text-primary); overflow-wrap: anywhere; }
  .judgment { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); gap: var(--space-2) var(--space-4); }
  .judgment > div { display: grid; gap: var(--space-1); }
  dt { color: var(--text-muted); font-size: var(--font-size-xs); }
  dd { color: var(--text-secondary); overflow-wrap: anywhere; }
  .status { color: var(--text-secondary); }
  .decision-error { color: var(--text-danger); }
  .loading { display: flex; align-items: center; gap: var(--space-2); color: var(--text-muted); }
  .message { display: grid; justify-items: start; gap: var(--space-2); padding: var(--space-3); border-left: 2px solid var(--accent-red); color: var(--text-secondary); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); }
</style>
