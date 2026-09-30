<script lang="ts">
  import { Button, EmptyState, SegmentedControl, Spinner } from '@kenn-io/kit-ui';
  import { onDestroy, tick } from 'svelte';

  import type { DirectoryReviewKind, IdentityReviewOrigin, IdentityReviewState } from '../../explore/models';
  import type { FactLedgerController } from '../../directory/fact-ledger-controller.svelte';
  import type {
    DirectoryReviewContextSnapshot,
    DirectoryReviewController,
    IdentityMatchCandidate
  } from '../../directory/review-controller.svelte';
  import IdentityCandidateCard from './IdentityCandidateCard.svelte';
  import IdentityDecisionModal from './IdentityDecisionModal.svelte';
  import FactReviewPanel from './FactReviewPanel.svelte';
  import RelationshipReviewQueue from './RelationshipReviewQueue.svelte';
  import type { RelationshipReviewController } from '../../directory/relationship-review-controller.svelte';
  import PersonBindingConflictModal from './PersonBindingConflictModal.svelte';
  import EnrichmentIdentityReviewQueue from './EnrichmentIdentityReviewQueue.svelte';
  import { EnrichmentReviewController } from '../../directory/enrichment-review-controller.svelte';
  import type { PersonMergeSuccess, ValidatedPersonMergeRequired } from '../../directory/person-merge';
  import type { NotAPersonKind } from '../../people/correspondent-kind';

  interface Props {
    controller: DirectoryReviewController;
    relationshipController: RelationshipReviewController;
    factController?: FactLedgerController;
    directoryPersonID?: number | null;
    onOpenDirectory?: () => void;
    onOpenPerson?: (personID: number) => void;
    onAnnounce?: (message: string) => void;
  }

  let {
    controller,
    relationshipController,
    factController = undefined,
    directoryPersonID = null,
    onOpenDirectory = () => undefined,
    onOpenPerson = () => undefined,
    onAnnounce = () => undefined
  }: Props = $props();
  type ActiveModal =
    | { kind: 'decision'; candidate: IdentityMatchCandidate; decision: 'accept' | 'reject'; context: DirectoryReviewContextSnapshot }
    | { kind: 'not_a_person'; candidate: IdentityMatchCandidate; participantID: number; notAPersonKind: NotAPersonKind; context: DirectoryReviewContextSnapshot }
    | { kind: 'merge'; candidate: IdentityMatchCandidate; context: DirectoryReviewContextSnapshot; conflict: ValidatedPersonMergeRequired };
  let activeDecision = $state<ActiveModal>();
  // svelte-ignore state_referenced_locally
  const enrichmentController = new EnrichmentReviewController(controller.apiClient);
  onDestroy(() => enrichmentController.destroy());
  let identityReviewHeading = $state<HTMLHeadingElement>();

  const reviewKindOptions = [
    { value: 'identity', label: 'Identity matches' },
    { value: 'fact', label: 'Fact review' },
    { value: 'relationship', label: 'Imported relationships' },
    { value: 'enrichment', label: 'Enrichment identities' }
  ];
  const identityStateOptions = [
    { value: 'candidate', label: 'Candidate' },
    { value: 'conflict', label: 'Conflict' },
    { value: 'accepted', label: 'Accepted' },
    { value: 'rejected', label: 'Rejected' }
  ];

  function selectReviewKind(value: string): void {
    const kind = value as DirectoryReviewKind;
    controller.setReviewKind(kind);
    relationshipController?.applyContext(kind === 'relationship', relationshipController.state, false);
  }

  const identityOriginOptions = [
    { value: 'all', label: 'All matches' },
    { value: 'contact_match', label: 'Contacts that match your archive' }
  ];

  function selectIdentityState(value: string): void {
    controller.setIdentityState(value as IdentityReviewState);
  }

  function selectIdentityOrigin(value: string): void {
    controller.setIdentityOrigin(value as IdentityReviewOrigin);
  }

  function openDecision(candidate: IdentityMatchCandidate, decision: 'accept' | 'reject'): void {
    activeDecision = { kind: 'decision', candidate, decision, context: controller.reviewContextSnapshot() };
  }

  function openNotAPerson(candidate: IdentityMatchCandidate, participantID: number, notAPersonKind: NotAPersonKind): void {
    activeDecision = { kind: 'not_a_person', candidate, participantID, notAPersonKind, context: controller.reviewContextSnapshot() };
  }

  function resolveMerge(conflict: ValidatedPersonMergeRequired): void {
    if (!activeDecision || activeDecision.kind !== 'decision') return;
    activeDecision = {
      kind: 'merge', candidate: activeDecision.candidate, context: activeDecision.context, conflict
    };
  }

  function completeMerge(success: PersonMergeSuccess): void {
    if (!activeDecision || activeDecision.kind !== 'merge') return;
    const origin = activeDecision;
    void controller.completePersonMerge(origin.candidate.id, origin.context, success);
    activeDecision = undefined;
    const name = success.survivor.display_name?.trim() || `Person ${success.survivor.id}`;
    onAnnounce(`People merged into ${name}. Identity cache ${success.result.cache_state}.`);
    onOpenPerson(success.survivor.id);
  }

  async function focusCurrentReviewSurface(): Promise<void> {
    await tick();
    const target = controller.reviewKind === 'fact'
      ? document.getElementById('fact-review-heading')
      : controller.reviewKind === 'enrichment'
        ? document.getElementById('enrichment-review-heading')
      : controller.reviewKind === 'relationship'
        ? document.getElementById('relationship-review-heading')
        : identityReviewHeading;
    if (target?.isConnected) target.focus();
  }

  async function invalidateDecision(): Promise<void> {
    if (!activeDecision) return;
    activeDecision = undefined;
    await focusCurrentReviewSurface();
  }

  async function closeDecision(): Promise<void> {
    const closed = activeDecision;
    activeDecision = undefined;
    if (!closed) return;
    await tick();
    const card = document.getElementById(`identity-match-${closed.candidate.id}-card`);
    const label = closed.kind === 'decision' && closed.decision === 'reject' ? 'Keep separate'
      : closed.kind === 'not_a_person' ? 'Not a person' : 'Link identities';
    const action = Array.from(card?.querySelectorAll<HTMLButtonElement>('button') ?? [])
      .find((button) => button.textContent?.trim() === label);
    const target = action ?? card;
    if (target?.isConnected) {
      target.focus();
      return;
    }
    await focusCurrentReviewSurface();
  }

  $effect(() => {
    const decision = activeDecision;
    if (decision && !controller.isReviewContextCurrent(decision.context)) {
      void invalidateDecision();
    }
  });
</script>

<main class="review-centre" aria-label="Reviews">
  <header class="page-header">
    <div>
      <h1>Reviews</h1>
      <p>Inspect identity evidence and imported relationship review records.</p>
    </div>
    <SegmentedControl
      options={reviewKindOptions}
      value={controller.reviewKind}
      onchange={selectReviewKind}
      ariaLabel="Review type"
      disabled={!!activeDecision}
    />
  </header>

  {#if controller.reviewKind === 'identity'}
    <section class="identity-review" aria-labelledby="identity-review-heading">
      <div class="review-toolbar">
        <div>
          <h2 bind:this={identityReviewHeading} id="identity-review-heading" tabindex="-1">Identity matches</h2>
          <p>Review server-supplied evidence before linking or separating identities.</p>
        </div>
        <div class="filters">
          <SegmentedControl
            options={identityOriginOptions}
            value={controller.identityOrigin}
            onchange={selectIdentityOrigin}
            ariaLabel="Identity match source"
            disabled={!!activeDecision}
          />
          <SegmentedControl
            options={identityStateOptions}
            value={controller.identityState}
            onchange={selectIdentityState}
            ariaLabel="Identity review state"
            disabled={!!activeDecision}
          />
        </div>
      </div>

      {#if controller.status}
        <p class="status" role="status" aria-live="polite">{controller.status}</p>
      {/if}

      {#if controller.loading && controller.rows.length === 0}
        <p class="loading">
          <Spinner size={12} label="Loading identity matches" /> Loading identity matches…
        </p>
      {:else if controller.error}
        <div class="message" role="alert">
          <p>{controller.error}</p>
          <Button label="Retry identity matches" size="sm" onclick={() => void controller.retryPage()} />
        </div>
      {:else}
        {#if controller.pageError}
          <div class="message" role="alert">
            <p>{controller.pageError}</p>
            <Button label="Retry identity matches" size="sm" onclick={() => void controller.retryPage()} />
          </div>
        {/if}

        {#if controller.rows.length === 0}
          <EmptyState
            title="No identity matches in this queue."
            description={controller.identityOrigin === 'contact_match'
              ? 'No contact profiles match archive identities in this state. Matches refresh after each contact sync and daily.'
              : 'Choose another review state or return when new evidence is available.'}
          />
        {:else}
          <div class="queue" aria-busy={controller.loading}>
            {#if controller.loading}
              <div class="loading-overlay">
                <Spinner size={12} label="Loading next review page" /> Loading page…
              </div>
            {/if}
            <div class="candidate-list">
              {#each controller.rows as row (row.id)}
                <IdentityCandidateCard
                  candidate={row}
                  left={controller.endpointFor(row.left_kind, row.left_id)}
                  right={controller.endpointFor(row.right_kind, row.right_id)}
                  contactMatch={controller.contactMatchFor(row.id)}
                  pending={controller.isDecisionPending(row.id)}
                  onAccept={() => openDecision(row, 'accept')}
                  onReject={() => openDecision(row, 'reject')}
                  onNotAPerson={(participantID, notAPersonKind) => openNotAPerson(row, participantID, notAPersonKind)}
                />
              {/each}
            </div>
          </div>
        {/if}

        {#if controller.rows.length > 0 || controller.hasPreviousPage}
          <nav class="pagination" aria-label="Identity review pages">
            <Button
              label="Previous page"
              size="sm"
              disabled={controller.loading || !controller.hasPreviousPage}
              onclick={() => void controller.loadPreviousPage()}
            />
            <span>Offset {controller.offset}</span>
            <Button
              label="Next page"
              size="sm"
              disabled={controller.loading || !controller.hasNextPage}
              onclick={() => void controller.loadNextPage()}
            />
          </nav>
        {/if}
      {/if}
    </section>
  {:else if controller.reviewKind === 'enrichment'}
    <EnrichmentIdentityReviewQueue controller={enrichmentController} {onOpenPerson} />
  {:else if controller.reviewKind === 'fact'}
    {#if factController}
      <FactReviewPanel controller={factController} personID={directoryPersonID} {onOpenDirectory} {onOpenPerson} />
    {/if}
  {:else}
    <RelationshipReviewQueue controller={relationshipController} {onOpenPerson} />
  {/if}
</main>

{#if activeDecision?.kind === 'decision'}
  <IdentityDecisionModal
    {controller}
    candidate={activeDecision.candidate}
    decision={activeDecision.decision}
    reviewContext={activeDecision.context}
    onClose={() => void closeDecision()}
    onContextInvalidated={() => void invalidateDecision()}
    onResolveMerge={resolveMerge}
  />
{:else if activeDecision?.kind === 'not_a_person'}
  <IdentityDecisionModal
    {controller}
    candidate={activeDecision.candidate}
    decision="not_a_person"
    notAPerson={{ participantID: activeDecision.participantID, kind: activeDecision.notAPersonKind }}
    reviewContext={activeDecision.context}
    onClose={() => void closeDecision()}
    onContextInvalidated={() => void invalidateDecision()}
  />
{:else if activeDecision?.kind === 'merge'}
  <PersonBindingConflictModal
    client={controller.apiClient}
    conflict={activeDecision.conflict}
    onOpenProfile={onOpenPerson}
    onSuccess={completeMerge}
    onClose={() => void closeDecision()}
  />
{/if}

<style>
  .review-centre { display: grid; gap: var(--space-5); padding: var(--space-5); }
  .page-header, .review-toolbar { display: flex; align-items: start; justify-content: space-between; gap: var(--space-5); flex-wrap: wrap; }
  .page-header > div, .review-toolbar > div, .identity-review { display: grid; gap: var(--space-2); }
  .review-toolbar > .filters { display: flex; flex-wrap: wrap; gap: var(--space-2); justify-content: flex-end; }
  h1, h2, p { margin: 0; }
  .page-header p, .review-toolbar p { color: var(--text-muted); }
  .identity-review { gap: var(--space-4); }
  .status { color: var(--text-secondary); }
  .loading { display: flex; align-items: center; gap: var(--space-2); color: var(--text-muted); }
  .message { display: grid; justify-items: start; gap: var(--space-2); padding: var(--space-3); border-left: 2px solid var(--accent-red); color: var(--text-secondary); }
  .queue { position: relative; min-width: 0; }
  .candidate-list { display: grid; gap: var(--space-4); }
  .loading-overlay { position: sticky; z-index: 1; top: var(--space-2); display: flex; align-items: center; justify-content: center; gap: var(--space-2); width: fit-content; margin: 0 auto calc(-1 * var(--space-8)); padding: var(--space-2) var(--space-4); border: var(--border-width) solid var(--border-default); border-radius: var(--radius-pill); background: var(--bg-surface); box-shadow: var(--shadow-sm); color: var(--text-muted); }
  .pagination { display: flex; align-items: center; justify-content: center; gap: var(--space-3); color: var(--text-muted); font-size: var(--font-size-sm); }
  @media (max-width: 760px) {
    .review-centre { padding: var(--space-4); }
    .page-header :global(.kit-segmented), .review-toolbar :global(.kit-segmented) { width: 100%; }
    .review-toolbar > .filters { width: 100%; }
  }
</style>
