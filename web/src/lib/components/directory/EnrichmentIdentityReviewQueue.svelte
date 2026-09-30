<script lang="ts">
  import { Button, Card, EmptyState, Spinner } from '@kenn-io/kit-ui';

  import {
    percent,
    personLabel,
    returnedIdentityLines,
    type EnrichmentReviewController
  } from '../../directory/enrichment-review-controller.svelte';
  import { labeledValueLinkInput } from '../../links/contact-links';
  import LinkedValue from '../common/LinkedValue.svelte';

  interface Props {
    controller: EnrichmentReviewController;
    onOpenPerson?: (personID: number) => void;
  }

  let { controller, onOpenPerson = () => undefined }: Props = $props();

  $effect(() => {
    if (!controller.loaded && !controller.loading && !controller.error) void controller.load();
  });
</script>

<section class="enrichment-review" aria-labelledby="enrichment-review-heading">
  <div class="toolbar">
    <h2 id="enrichment-review-heading" tabindex="-1">Enrichment identities to confirm</h2>
    <p>
      The identity check could not decide whether the provider found the right person. No claim from these
      lookups has been applied. Confirm only when the returned identity is this person.
    </p>
  </div>

  {#if controller.status}
    <p class="status" role="status" aria-live="polite">{controller.status}</p>
  {/if}
  {#if controller.decisionError}
    <p class="decision-error" role="alert">{controller.decisionError}</p>
  {/if}

  {#if controller.loading && controller.rows.length === 0}
    <p class="loading"><Spinner size={12} label="Loading enrichment identities" /> Loading enrichment identities…</p>
  {:else if controller.error}
    <div class="message" role="alert">
      <p>{controller.error}</p>
      <Button label="Retry enrichment identities" size="sm" onclick={() => void controller.load()} />
    </div>
  {:else if controller.rows.length === 0}
    <EmptyState
      title="No enrichment identities to confirm."
      description="Lookups whose identity check is uncertain appear here for a decision."
    />
  {:else}
    <div class="list">
      {#each controller.rows as review (review.attempt_id)}
        {@const pending = controller.isPending(review.attempt_id)}
        {@const heading = `enrichment-review-${review.attempt_id}-heading`}
        <Card level="default" padding="md">
          <article class="review" aria-labelledby={heading} aria-busy={pending}>
            <header>
              <div>
                <p class="provider">{review.provider_name} · attempt {review.attempt_id}</p>
                <h3 id={heading}>{personLabel(review)}</h3>
              </div>
              <Button
                label={`Open ${personLabel(review)}`}
                size="sm"
                surface="soft"
                onclick={() => onOpenPerson(review.person_id)}
              />
            </header>

            <section class="returned" aria-label={`Returned identity for attempt ${review.attempt_id}`}>
              <h4>Provider returned</h4>
              {#if returnedIdentityLines(review.returned).length > 0}
                <ul>
                  {#each returnedIdentityLines(review.returned) as line, index (index)}
                    <li>{line}</li>
                  {/each}
                </ul>
              {:else}
                <p class="muted">The provider's identity details were not kept for this lookup.</p>
              {/if}
            </section>

            <dl class="judgment" aria-label={`Identity check for attempt ${review.attempt_id}`}>
              <div><dt>Name compatible</dt><dd>{percent(review.name_compatible)}</dd></div>
              <div><dt>Same company</dt><dd>{percent(review.company_same)}</dd></div>
              <div><dt>Name conflict</dt><dd>{percent(review.name_conflict)}</dd></div>
              <div><dt>Matched exactly</dt><dd>{review.exact_class.replace('_', ' ')}</dd></div>
            </dl>

            {#if review.claims.length > 0}
              <section class="claims">
                <h4>Values that would be applied</h4>
                <ul aria-label={`Claims for attempt ${review.attempt_id}`}>
                  {#each review.claims as claim, index (index)}
                    <li><span>{claim.target.replaceAll('_', ' ')}</span> <LinkedValue input={labeledValueLinkInput(claim.target, claim.value)} copy={false} /></li>
                  {/each}
                </ul>
              </section>
            {/if}

            <div class="actions">
              <Button
                label="Not this person"
                size="sm"
                disabled={pending}
                onclick={() => void controller.reject(review.attempt_id)}
              />
              <Button
                label="Confirm identity"
                size="sm"
                tone="info"
                surface="solid"
                disabled={pending}
                onclick={() => void controller.confirm(review.attempt_id)}
              />
            </div>
          </article>
        </Card>
      {/each}
    </div>
  {/if}
</section>

<style>
  .enrichment-review, .toolbar, .list, .review, .returned, .claims { display: grid; gap: var(--space-3); }
  .enrichment-review { gap: var(--space-4); }
  h2, h3, h4, p, ul, dl, dd { margin: 0; }
  .toolbar p, .muted, .provider { color: var(--text-muted); }
  .provider { font-size: var(--font-size-xs); }
  h3 { font-size: var(--font-size-lg); color: var(--text-primary); }
  h4 { font-size: var(--font-size-sm); color: var(--text-secondary); }
  header { display: flex; align-items: start; justify-content: space-between; gap: var(--space-4); }
  header > div { display: grid; gap: var(--space-1); }
  ul { display: grid; gap: var(--space-1); padding-left: var(--space-5); color: var(--text-secondary); overflow-wrap: anywhere; }
  .claims li span { color: var(--text-muted); }
  .judgment { display: grid; grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr)); gap: var(--space-2) var(--space-4); }
  .judgment > div { display: grid; gap: var(--space-1); }
  dt { color: var(--text-muted); font-size: var(--font-size-xs); }
  dd { color: var(--text-secondary); }
  .status { color: var(--text-secondary); }
  .decision-error { color: var(--text-danger); }
  .loading { display: flex; align-items: center; gap: var(--space-2); color: var(--text-muted); }
  .message { display: grid; justify-items: start; gap: var(--space-2); padding: var(--space-3); border-left: 2px solid var(--accent-red); color: var(--text-secondary); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); }
</style>
