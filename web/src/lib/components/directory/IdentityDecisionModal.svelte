<script lang="ts">
  import { appShortcuts, Button, Modal } from '@kenn-io/kit-ui';
  import { onDestroy, onMount, tick, untrack } from 'svelte';

  import type {
    DirectoryReviewContextSnapshot,
    DirectoryReviewController,
    IdentityMatchCandidate,
    PersonMergeRequiredError
  } from '../../directory/review-controller.svelte';
  import { contactMatchSummary, endpointLabel } from '../../directory/identity-endpoints';
  import { entityNames } from '../../names/entity-names.svelte';

  interface Props {
    controller: DirectoryReviewController;
    candidate: IdentityMatchCandidate;
    decision: 'accept' | 'reject';
    reviewContext: DirectoryReviewContextSnapshot;
    onClose: () => void;
    /** Called instead of onClose once the decision is recorded, so the
     * queue can move on to the next candidate. */
    onDecided?: () => void;
    onContextInvalidated: () => void;
    /** Called in place of this modal when accepting needs the two people
     * merged first; the merge modal then merges them. */
    onResolveMerge: (conflict: PersonMergeRequiredError) => void;
  }

  let {
    controller,
    candidate,
    decision,
    reviewContext,
    onClose,
    onDecided = undefined,
    onContextInvalidated,
    onResolveMerge
  }: Props = $props();
  const names = $derived(entityNames(controller.apiClient));
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let releaseShortcutScope: (() => void) | undefined;

  const pending = $derived(submitting || controller.isDecisionPending(candidate.id));
  const title = $derived(decision === 'accept' ? 'Link identities' : 'Keep separate');
  const draft = $derived(controller.getDecisionDraft(candidate.id));
  // Notes are optional and only stored with the decision, so the field
  // stays folded away unless a draft already exists.
  let notesOpen = $state(untrack(() => controller.getDecisionDraft(candidate.id) !== ''));
  let notesField = $state<HTMLTextAreaElement>();
  const leftLabel = $derived(endpointLabel(
    names, candidate.left_kind, candidate.left_id, controller.endpointFor(candidate.left_kind, candidate.left_id)));
  const rightLabel = $derived(endpointLabel(
    names, candidate.right_kind, candidate.right_id, controller.endpointFor(candidate.right_kind, candidate.right_id)));
  const contactMatch = $derived(controller.contactMatchFor(candidate.id));

  async function openNotes(): Promise<void> {
    notesOpen = true;
    await tick();
    notesField?.focus();
  }

  onMount(() => {
    releaseShortcutScope = appShortcuts.pushScope('identity-decision-modal');
  });

  onDestroy(() => releaseShortcutScope?.());

  function updateDraft(value: string): void {
    controller.setDecisionDraft(candidate.id, value);
  }

  function requestClose(): void {
    if (!controller.isReviewContextCurrent(reviewContext)) {
      onContextInvalidated();
      return;
    }
    if (pending) return;
    onClose();
  }

  async function submit(): Promise<void> {
    if (!controller.isReviewContextCurrent(reviewContext)) {
      onContextInvalidated();
      return;
    }
    if (pending) return;
    submitting = true;
    error = null;
    try {
      const result = decision === 'accept'
        ? await controller.acceptIdentity(candidate.id, undefined, reviewContext)
        : await controller.rejectIdentity(candidate.id, undefined, reviewContext);
      if (result.ok) {
        (onDecided ?? onClose)();
      } else if (result.kind === 'merge_required') {
        onResolveMerge(result.conflict);
      } else {
        error = result.message;
      }
    } finally {
      submitting = false;
    }
  }
</script>

<Modal
  {title}
  ariaLabel={title}
  closeLabel="Close identity decision"
  closable={!pending}
  closeOnOverlayClick={!pending}
  onclose={requestClose}
  maxWidth="min(560px, calc(100vw - 32px))"
>
  <div class="decision" aria-busy={pending}>
    <p class="candidate-context">
      <strong>{leftLabel}</strong>
      <span aria-hidden="true">↔</span>
      <strong>{rightLabel}</strong>
    </p>

    {#if decision === 'accept' && contactMatch}
      <p>{contactMatchSummary(contactMatch)}</p>
    {:else if decision === 'accept'}
      <p>Link these identities only when the supplied evidence shows they belong to the same person.</p>
    {:else}
      <p>Keep these identities separate when the supplied evidence does not establish that they belong to the same person.</p>
    {/if}

    {#if notesOpen}
      <label>
        <span>Decision notes <small>(optional)</small></span>
        <textarea
          bind:this={notesField}
          aria-label="Decision notes"
          rows="3"
          value={draft}
          disabled={pending}
          oninput={(event) => updateDraft(event.currentTarget.value)}
        ></textarea>
      </label>
    {:else}
      <div class="add-note">
        <Button
          size="sm"
          surface="soft"
          label="Add a note"
          ariaExpanded={false}
          disabled={pending}
          onclick={() => void openNotes()}
        />
      </div>
    {/if}

    {#if error}
      <p class="decision-error" role="alert">{error}</p>
    {/if}
  </div>

  {#snippet footer()}
    <Button surface="soft" label="Cancel" disabled={pending} onclick={requestClose} />
    <Button
      tone={decision === 'accept' ? 'info' : 'neutral'}
      surface="solid"
      label={title}
      disabled={pending}
      onclick={() => void submit()}
    />
  {/snippet}
</Modal>

<style>
  .decision { display: grid; gap: var(--space-4); min-width: min(28rem, calc(100vw - 64px)); }
  p { margin: 0; }
  .candidate-context { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); color: var(--text-primary); }
  .add-note { display: flex; }
  label small { color: var(--text-muted); font-weight: normal; }
  label { display: grid; gap: var(--space-2); color: var(--text-secondary); font-size: var(--font-size-sm); font-weight: var(--font-weight-medium, 500); }
  textarea { box-sizing: border-box; width: 100%; resize: vertical; padding: var(--space-3); border: var(--border-width) solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-inset); color: var(--text-primary); font: inherit; line-height: 1.45; }
  textarea:focus-visible { outline: var(--focus-ring); outline-offset: var(--focus-ring-offset, 2px); }
  textarea:disabled { opacity: var(--opacity-disabled); }
  .decision-error { color: var(--text-danger); }
</style>
