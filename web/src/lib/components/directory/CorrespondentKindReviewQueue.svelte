<script lang="ts">
  import { Button, Card, EmptyState, Spinner } from '@kenn-io/kit-ui';

  import { recordLabel, type CorrespondentReviewController } from '../../directory/correspondent-review-controller.svelte';
  import { percent } from '../../directory/enrichment-review-controller.svelte';
  import { JEV_OPTION_LABELS, type CorrespondentKind } from '../../people/correspondent-kind';
  import { focusReviewCard } from '../../directory/review-focus';

  interface Props {
    controller: CorrespondentReviewController;
    onOpenPerson?: (personID: number) => void;
    onDecided?: () => void;
  }

  let { controller, onOpenPerson = () => undefined, onDecided = () => undefined }: Props = $props();

  let list = $state<HTMLElement>();
  let queueHeading = $state<HTMLHeadingElement>();

  // A decision removes its card; focus moves to the card that took its
  // place, so the queue can be worked through without scrolling back.
  async function decide(index: number, run: () => Promise<{ ok: boolean }>): Promise<void> {
    const result = await run();
    if (!result.ok) return;
    onDecided();
    await focusReviewCard(list, index, queueHeading);
  }

  const decisions: readonly { kind: CorrespondentKind; label: string }[] = [
    { kind: 'shared_mailbox', label: 'Shared mailbox' },
    { kind: 'mailing_list', label: 'Mailing list' },
    { kind: 'automated', label: 'Automated sender' },
    { kind: 'ignored', label: 'Ignore' }
  ];

  $effect(() => {
    if (!controller.loaded && !controller.loading && !controller.error) void controller.load();
  });
</script>

<section class="correspondent-review" aria-labelledby="correspondent-review-heading">
  <div class="toolbar">
    <h2 bind:this={queueHeading} id="correspondent-review-heading" tabindex="-1">Unclear correspondents</h2>
    <p>
      Jev could not tell whether these identities are people. They stay in People and enrichment but are left
      out of relationship rankings until you decide.
    </p>
  </div>

  {#if controller.status}
    <p class="status" role="status" aria-live="polite">{controller.status}</p>
  {/if}
  {#if controller.decisionError}
    <p class="decision-error" role="alert">{controller.decisionError}</p>
  {/if}

  {#if controller.loading && controller.rows.length === 0}
    <p class="loading"><Spinner size={12} label="Loading unclear correspondents" /> Loading unclear correspondents…</p>
  {:else if controller.error}
    <div class="message" role="alert">
      <p>{controller.error}</p>
      <Button label="Retry unclear correspondents" size="sm" onclick={() => void controller.load()} />
    </div>
  {:else if controller.rows.length === 0}
    <EmptyState
      title="No unclear correspondents."
      description="Identities Jev could not classify appear here after msgvault kinds build."
    />
  {:else}
    <div class="list" bind:this={list}>
      {#each controller.rows as record, index (record.canonical_id)}
        {@const pending = controller.isPending(record.canonical_id)}
        {@const label = recordLabel(record)}
        {@const heading = `correspondent-review-${record.canonical_id}-heading`}
        <Card level="default" padding="md">
          <article class="review" data-review-card tabindex="-1" aria-labelledby={heading} aria-busy={pending}>
            <header>
              <h3 id={heading}>{label}</h3>
              {#if record.person}
                {@const person = record.person}
                <Button
                  label={`Open ${person.display_name?.trim() || label}`}
                  size="sm"
                  surface="soft"
                  onclick={() => onOpenPerson(person.id)}
                />
              {/if}
            </header>

            {#if record.addresses.length > 0}
              <ul class="addresses" aria-label={`Addresses of ${label}`}>
                {#each record.addresses.slice(0, 5) as address (address)}
                  <li>{address}</li>
                {/each}
              </ul>
            {/if}

            {#if record.probabilities}
              {@const probabilities = record.probabilities}
              <dl class="judgment" aria-label={`Jev judgment for ${label}`}>
                {#each JEV_OPTION_LABELS as option (option.option)}
                  {#if probabilities[option.option] !== undefined}
                    <div><dt>{option.label}</dt><dd>{percent(probabilities[option.option] ?? 0)}</dd></div>
                  {/if}
                {/each}
              </dl>
            {/if}

            <div class="actions">
              {#each decisions as decision (decision.kind)}
                <Button
                  label={decision.label}
                  ariaLabel={`Mark ${label} as ${decision.label.toLowerCase()}`}
                  size="sm"
                  disabled={pending}
                  onclick={() => void decide(index, () => controller.decide(record, decision.kind))}
                />
              {/each}
              <Button
                label="This is a person"
                ariaLabel={`${label} is a person`}
                size="sm"
                tone="info"
                surface="solid"
                disabled={pending}
                onclick={() => void decide(index, () => controller.decide(record, 'person'))}
              />
            </div>
          </article>
        </Card>
      {/each}
    </div>
  {/if}
</section>

<style>
  .correspondent-review, .toolbar, .list, .review { display: grid; gap: var(--space-3); }
  .correspondent-review { gap: var(--space-4); }
  h2, h3, p, ul, dl, dd { margin: 0; }
  .toolbar p { color: var(--text-muted); }
  h3 { font-size: var(--font-size-lg); color: var(--text-primary); overflow-wrap: anywhere; }
  header { display: flex; align-items: start; justify-content: space-between; gap: var(--space-4); }
  ul { display: grid; gap: var(--space-1); padding-left: var(--space-5); color: var(--text-secondary); overflow-wrap: anywhere; }
  .judgment { display: grid; grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr)); gap: var(--space-2) var(--space-4); }
  .judgment > div { display: grid; gap: var(--space-1); }
  dt { color: var(--text-muted); font-size: var(--font-size-xs); }
  dd { color: var(--text-secondary); }
  .status { color: var(--text-secondary); }
  .decision-error { color: var(--text-danger); }
  .loading { display: flex; align-items: center; gap: var(--space-2); color: var(--text-muted); }
  .message { display: grid; justify-items: start; gap: var(--space-2); padding: var(--space-3); border-left: 2px solid var(--accent-red); color: var(--text-secondary); }
  .actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--space-2); }
</style>
