<script lang="ts">
  import { Button, EmptyState, SegmentedControl, Spinner } from '@kenn-io/kit-ui';
  import { onDestroy, tick } from 'svelte';

  import {
    DIRECTORY_REVIEW_KINDS,
    type DirectoryReviewKind,
    type IdentityReviewOrigin,
    type IdentityReviewState
  } from '../../explore/models';
  import type { FactLedgerController } from '../../directory/fact-ledger-controller.svelte';
  import type {
    DirectoryReviewContextSnapshot,
    DirectoryReviewController,
    IdentityMatchCandidate
  } from '../../directory/review-controller.svelte';
  import IdentityCandidateCard from './IdentityCandidateCard.svelte';
  import FactReviewPanel from './FactReviewPanel.svelte';
  import RelationshipReviewQueue from './RelationshipReviewQueue.svelte';
  import type { RelationshipReviewController } from '../../directory/relationship-review-controller.svelte';
  import PersonBindingConflictModal from './PersonBindingConflictModal.svelte';
  import EnrichmentIdentityReviewQueue from './EnrichmentIdentityReviewQueue.svelte';
  import { EnrichmentReviewController } from '../../directory/enrichment-review-controller.svelte';
  import { entityNames } from '../../names/entity-names.svelte';
  import OrganizationMatchReviewQueue from './OrganizationMatchReviewQueue.svelte';
  import { OrganizationReviewController } from '../../directory/organization-review-controller.svelte';
  import CorrespondentKindReviewQueue from './CorrespondentKindReviewQueue.svelte';
  import { CorrespondentReviewController } from '../../directory/correspondent-review-controller.svelte';
  import type { PersonMergeSuccess, ValidatedPersonMergeRequired } from '../../directory/person-merge';
  import type { NotAPersonKind } from '../../people/correspondent-kind';
  import { endpointLabel } from '../../directory/identity-endpoints';
  import { focusReviewCard, nextReviewIndex, reviewPosition, type ReviewPosition } from '../../directory/review-focus';

  interface Props {
    controller: DirectoryReviewController;
    relationshipController: RelationshipReviewController;
    factController?: FactLedgerController;
    directoryPersonID?: number | null;
    onOpenDirectory?: () => void;
    onOpenPerson?: (personID: number) => void;
    onAnnounce?: (message: string) => void;
    /** Called after any review decision is recorded. */
    onDecided?: () => void;
  }

  let {
    controller,
    relationshipController,
    factController = undefined,
    directoryPersonID = null,
    onOpenDirectory = () => undefined,
    onOpenPerson = () => undefined,
    onAnnounce = () => undefined,
    onDecided = () => undefined
  }: Props = $props();
  // position: where the candidate sat when it was decided, taken before
  // anything is sent because the queue reloads before it returns.
  type ActiveModal =
    { kind: 'merge'; candidate: IdentityMatchCandidate; context: DirectoryReviewContextSnapshot; conflict: ValidatedPersonMergeRequired; position: ReviewPosition<number> };
  const names = $derived(entityNames(controller.apiClient));
  let activeDecision = $state<ActiveModal>();
  // svelte-ignore state_referenced_locally
  const enrichmentController = new EnrichmentReviewController(controller.apiClient);
  onDestroy(() => enrichmentController.destroy());
  // svelte-ignore state_referenced_locally
  const organizationController = new OrganizationReviewController(controller.apiClient);
  onDestroy(() => organizationController.destroy());
  // svelte-ignore state_referenced_locally
  const correspondentController = new CorrespondentReviewController(controller.apiClient);
  onDestroy(() => correspondentController.destroy());
  let identityReviewHeading = $state<HTMLHeadingElement>();
  let candidateList = $state<HTMLElement>();
  let notAPersonError = $state<string | null>(null);
  // The profile a merge kept, offered as an explicit link beside the status.
  let mergedSurvivor = $state<{ id: number; name: string }>();

  const reviewKindLabels: Record<DirectoryReviewKind, string> = {
    identity: 'Identity matches',
    fact: 'Fact review',
    relationship: 'Imported relationships',
    enrichment: 'Enrichment identities',
    organization: 'Organization matches',
    correspondent: 'Unclear correspondents'
  };
  const reviewKindOptions = DIRECTORY_REVIEW_KINDS.map((kind) => ({ value: kind, label: reviewKindLabels[kind] }));
  const identityStateOptions = [
    { value: 'candidate', label: 'Waiting' },
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
    { value: 'contact_match', label: 'Contacts that match your archive' },
    { value: 'person_duplicate', label: 'Possible duplicate people' }
  ];

  const identityEmptyDescriptions: Record<IdentityReviewOrigin, string> = {
    all: 'Choose another review state or return when new evidence is available.',
    contact_match:
      'No contact profiles match archive identities in this state. Matches refresh after each contact sync and daily; exact email matches to one person are merged automatically.',
    person_duplicate:
      'No possible duplicate people in this state. Run msgvault person judge with the duplicate people Jev judgment on to look for them.'
  };

  function selectIdentityState(value: string): void {
    controller.setIdentityState(value as IdentityReviewState);
  }

  function selectIdentityOrigin(value: string): void {
    controller.setIdentityOrigin(value as IdentityReviewOrigin);
  }

  // The card's button names the decision, so it applies at once. Only an
  // accept that needs the two people merged opens a dialog.
  async function decide(candidate: IdentityMatchCandidate, decision: 'accept' | 'reject'): Promise<void> {
    if (activeDecision || controller.isDecisionPending(candidate.id)) return;
    mergedSurvivor = undefined;
    notAPersonError = null;
    const context = controller.reviewContextSnapshot();
    const position = positionOf(candidate.id);
    const result = decision === 'accept'
      ? await controller.acceptIdentity(candidate.id, undefined, context)
      : await controller.rejectIdentity(candidate.id, undefined, context);
    if (!controller.isReviewContextCurrent(context)) return;
    if (result.ok) {
      onDecided();
      await focusAfterDecision(candidate.id, position);
    } else if (result.kind === 'merge_required') {
      activeDecision = { kind: 'merge', candidate, context, conflict: result.conflict, position };
    }
  }

  /** Deciding stays in the queue: focus moves to the next candidate
   * without scrolling, and profiles open only from explicit links. */
  async function focusAfterDecision(candidateID: number, position: ReviewPosition<number>): Promise<void> {
    const index = nextReviewIndex(controller.rows, (row) => row.id, candidateID, position);
    await focusReviewCard(candidateList, index, identityReviewHeading);
  }

  function positionOf(candidateID: number): ReviewPosition<number> {
    return reviewPosition(controller.rows, (row) => row.id, candidateID);
  }

  // The organization name a marked identity gets: its display name, else
  // its email domain. The user can rename the organization later.
  function suggestedOrganization(participantID: number): string {
    const summary = controller.endpointFor('participant', participantID);
    const name = summary?.display_name?.trim();
    if (name && !name.includes('@')) return name;
    const address = summary?.addresses?.find((value) => value.includes('@')) ?? '';
    return address ? address.slice(address.lastIndexOf('@') + 1) : '';
  }

  // The card's menu already names the kind, so it applies at once; the
  // status line offers Undo.
  async function markNotAPerson(candidate: IdentityMatchCandidate, participantID: number, kind: NotAPersonKind): Promise<void> {
    const position = positionOf(candidate.id);
    const label = endpointLabel(names, 'participant', participantID, controller.endpointFor('participant', participantID));
    notAPersonError = null;
    mergedSurvivor = undefined;
    const failure = await controller.markNotAPerson(
      candidate.id, participantID, kind, label, suggestedOrganization(participantID) || undefined,
      controller.reviewContextSnapshot()
    );
    if (failure) {
      notAPersonError = failure;
      return;
    }
    onDecided();
    await focusAfterDecision(candidate.id, position);
  }

  async function undoNotAPerson(): Promise<void> {
    notAPersonError = await controller.undoNotAPerson(controller.reviewContextSnapshot());
    if (notAPersonError) return;
    onDecided();
    await focusReviewCard(candidateList, 0, identityReviewHeading);
  }

  async function completeMerge(success: PersonMergeSuccess): Promise<void> {
    if (!activeDecision) return;
    const origin = activeDecision;
    const completion = controller.completePersonMerge(origin.candidate.id, origin.context, success);
    activeDecision = undefined;
    void mergedName(success).then((name) => {
      mergedSurvivor = { id: success.survivor.id, name };
      onAnnounce(`People merged into ${name}. Undo it from ${name}'s merge history.`);
    });
    await completion;
    onDecided();
    await focusAfterDecision(origin.candidate.id, origin.position);
  }

  function mergedName(success: PersonMergeSuccess): Promise<string> {
    const known = success.survivor.display_name?.trim();
    return known ? Promise.resolve(known) : names.settledLabel('person', success.survivor.id, 'the surviving person');
  }

  async function focusCurrentReviewSurface(): Promise<void> {
    await tick();
    const target = controller.reviewKind === 'fact'
      ? document.getElementById('fact-review-heading')
      : controller.reviewKind === 'enrichment'
        ? document.getElementById('enrichment-review-heading')
      : controller.reviewKind === 'organization'
        ? document.getElementById('organization-review-heading')
      : controller.reviewKind === 'correspondent'
        ? document.getElementById('correspondent-review-heading')
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
    const action = Array.from(card?.querySelectorAll<HTMLButtonElement>('button') ?? [])
      .find((button) => button.textContent?.trim() === 'Link identities');
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
        <div class="status-row">
          <p class="status" role="status" aria-live="polite">{controller.status}</p>
          {#if mergedSurvivor}
            {@const survivor = mergedSurvivor}
            <Button
              label={`Open ${survivor.name} profile`}
              size="sm"
              surface="soft"
              onclick={() => onOpenPerson(survivor.id)}
            />
          {/if}
          {#if controller.lastNotAPerson}
            <Button
              label="Undo"
              ariaLabel={`Undo: ${controller.lastNotAPerson.label} is a person`}
              size="sm"
              surface="soft"
              onclick={() => void undoNotAPerson()}
            />
          {/if}
        </div>
      {/if}
      {#if notAPersonError}
        <p class="decision-error" role="alert">{notAPersonError}</p>
      {/if}
      {#if controller.decisionError}
        <p class="decision-error" role="alert">{controller.decisionError}</p>
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
            description={identityEmptyDescriptions[controller.identityOrigin]}
          />
        {:else}
          <div class="queue" aria-busy={controller.loading}>
            {#if controller.loading}
              <div class="loading-overlay">
                <Spinner size={12} label="Loading next review page" /> Loading page…
              </div>
            {/if}
            <div class="candidate-list" bind:this={candidateList}>
              {#each controller.rows as row (row.id)}
                <IdentityCandidateCard
                  candidate={row}
                  {names}
                  left={controller.endpointFor(row.left_kind, row.left_id)}
                  right={controller.endpointFor(row.right_kind, row.right_id)}
                  contactMatch={controller.contactMatchFor(row.id)}
                  pending={controller.isDecisionPending(row.id)}
                  note={controller.getDecisionDraft(row.id)}
                  onNoteInput={(value) => controller.setDecisionDraft(row.id, value)}
                  onAccept={() => void decide(row, 'accept')}
                  onReject={() => void decide(row, 'reject')}
                  onNotAPerson={(participantID, notAPersonKind) => void markNotAPerson(row, participantID, notAPersonKind)}
                  onIsPerson={(participantID) => {
                    mergedSurvivor = undefined;
                    void controller.confirmPerson(row.id, participantID, controller.reviewContextSnapshot())
                      .then((failure) => { if (!failure) onDecided(); });
                  }}
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
    <EnrichmentIdentityReviewQueue controller={enrichmentController} {onOpenPerson} {onDecided} />
  {:else if controller.reviewKind === 'organization'}
    <OrganizationMatchReviewQueue controller={organizationController} {onDecided} />
  {:else if controller.reviewKind === 'correspondent'}
    <CorrespondentKindReviewQueue controller={correspondentController} {onOpenPerson} {onDecided} />
  {:else if controller.reviewKind === 'fact'}
    {#if factController}
      <FactReviewPanel controller={factController} personID={directoryPersonID} {onOpenDirectory} {onOpenPerson} />
    {/if}
  {:else}
    <RelationshipReviewQueue controller={relationshipController} {onOpenPerson} />
  {/if}
</main>

{#if activeDecision}
  <PersonBindingConflictModal
    client={controller.apiClient}
    conflict={activeDecision.conflict}
    onOpenProfile={onOpenPerson}
    onSuccess={(success) => void completeMerge(success)}
    onClose={() => void closeDecision()}
  />
{/if}

<style>
  /* The app shell clips its overflow, so the review queue scrolls here; the
   * sticky loading overlay pins to this container. */
  .review-centre { display: grid; flex: 1; grid-template-columns: minmax(0, 1fr); align-content: start; gap: var(--space-5); min-width: 0; min-height: 0; padding: var(--space-5); overflow: auto; }
  .page-header, .review-toolbar { display: flex; align-items: start; justify-content: space-between; gap: var(--space-5); flex-wrap: wrap; }
  .page-header > div, .review-toolbar > div, .identity-review { display: grid; gap: var(--space-2); }
  .review-toolbar > .filters { display: flex; flex-wrap: wrap; gap: var(--space-2); justify-content: flex-end; }
  h1, h2, p { margin: 0; }
  .page-header p, .review-toolbar p { color: var(--text-muted); }
  .identity-review { grid-template-columns: minmax(0, 1fr); gap: var(--space-4); }
  .status { color: var(--text-secondary); }
  .status-row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); }
  .decision-error { color: var(--text-danger); }
  .loading { display: flex; align-items: center; gap: var(--space-2); color: var(--text-muted); }
  .message { display: grid; justify-items: start; gap: var(--space-2); padding: var(--space-3); border-left: 2px solid var(--accent-red); color: var(--text-secondary); }
  .queue { position: relative; min-width: 0; }
  .candidate-list { display: grid; gap: var(--space-4); }
  .loading-overlay { position: sticky; z-index: 1; top: var(--space-2); display: flex; align-items: center; justify-content: center; gap: var(--space-2); width: fit-content; margin: 0 auto calc(-1 * var(--space-8)); padding: var(--space-2) var(--space-4); border: var(--border-width) solid var(--border-default); border-radius: var(--radius-pill); background: var(--bg-surface); box-shadow: var(--shadow-sm); color: var(--text-muted); }
  .pagination { display: flex; align-items: center; justify-content: center; gap: var(--space-3); color: var(--text-muted); font-size: var(--font-size-sm); }
  @media (max-width: 760px) {
    .review-centre { padding: var(--space-4); }
    .page-header :global(.kit-segmented), .review-toolbar :global(.kit-segmented) { width: 100%; flex-wrap: wrap; }
    .review-toolbar > .filters { width: 100%; }
  }
</style>
