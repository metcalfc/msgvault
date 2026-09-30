<script lang="ts">
  import { searchParticipants as generatedSearchParticipants } from '../../api/generated/exploration/exploration';
  import { appShortcuts, Button, debounce, Modal, Typeahead, type TypeaheadOption } from '@kenn-io/kit-ui';
  import { onDestroy, onMount } from 'svelte';
  import type { APIClient } from '../../api/client';
  import type { PersonSummary } from '../../explore/models';
  import type { LinkOutcome } from '../../relationships/controller.svelte';
  import type { ValidatedPersonMergeRequired } from '../../directory/person-merge';
  const SEARCH_DEBOUNCE_MS = 250;
  const SEARCH_LIMIT = 20;
  interface Props {
    client: APIClient;
    /** The currently open cluster's own ID, excluded from results — linking
     * it to itself is rejected by the API and never a meaningful choice. */
    excludeID: number;
    /** The open person's display name, woven into the dialog title so the
     * flow reads in human terms ("Link another identity for Alice"). */
    personLabel: string;
    onConfirm: (participantID: number) => Promise<LinkOutcome>;
    onMergeRequired?: (conflict: ValidatedPersonMergeRequired) => void;
    onClose: () => void;
  }
  let { client, excludeID, personLabel, onConfirm, onMergeRequired = undefined, onClose }: Props = $props();
  let query = $state('');
  let results = $state<PersonSummary[]>([]);
  let searching = $state(false);
  let searchError = $state<string | null>(null);
  let selectedID = $state<number | null>(null);
  let confirming = $state(false);
  let confirmError = $state<string | null>(null);
  const options = $derived(
    results.map(
      (row): TypeaheadOption => ({
        name: String(row.id),
        label: row.display_label,
        meta: candidateSummary(row),
      }),
    ),
  );
  let searchAbort: AbortController | undefined;
  let searchGeneration = 0;
  let releaseShortcutScope: (() => void) | undefined;
  const debouncedSearch = debounce((value: string) => void runSearch(value), SEARCH_DEBOUNCE_MS);
  // Suspends the app's global shortcuts (command palette, grid navigation,
  // etc.) while the dialog is open, matching every other Modal consumer
  // (FileViewer, DeletionsWorkspace) instead of leaving it the one that
  // doesn't push a scope.
  onMount(() => {
    releaseShortcutScope = appShortcuts.pushScope('link-identity-dialog');
  });
  // Without this, closing the dialog mid-debounce (or mid-request) leaves
  // the pending timer and in-flight fetch running: the timer fires after
  // unmount and issues a search the user never sees, and the request
  // itself keeps a connection open for no reason. The generation counter in
  // runSearch already guards against a late response updating state, but it
  // doesn't stop the request from being sent at all.
  onDestroy(() => {
    debouncedSearch.cancel();
    searchAbort?.abort();
    releaseShortcutScope?.();
  });
  async function runSearch(value: string): Promise<void> {
    const trimmed = value.trim();
    searchAbort?.abort();
    if (trimmed === '') {
      results = [];
      searching = false;
      searchError = null;
      return;
    }
    const controller = new AbortController();
    searchAbort = controller;
    const generation = ++searchGeneration;
    searching = true;
    searchError = null;
    try {
      const { data, error, response } = await generatedSearchParticipants(
        {
          predicate: {},
          identity_query: trimmed,
          sort: { field: 'activity_count', direction: 'desc' },
          limit: SEARCH_LIMIT,
        },
        {
          ...client,
          signal: controller.signal,
        },
      );
      if (generation !== searchGeneration || controller.signal.aborted) return;
      if (data) {
        results = (data.rows ?? []).filter((row) => row.id !== excludeID);
        return;
      }
      searchError = messageFor(error, response.status);
    } catch (cause: unknown) {
      if (generation === searchGeneration && !controller.signal.aborted) searchError = messageFor(cause, 0);
    } finally {
      if (generation === searchGeneration) searching = false;
    }
  }
  function handleQueryInput(value: string): void {
    query = value;
    // Typeahead reports an empty query whenever it opens or closes, including
    // the focusout when the user moves on to the confirm button. Only typed
    // text replaces the choice; an empty query keeps it.
    if (value.trim() !== '') {
      selectedID = null;
      confirmError = null;
    }
    debouncedSearch(value);
  }
  function selectResult(id: number): void {
    selectedID = id;
    confirmError = null;
  }
  async function confirmLink(): Promise<void> {
    if (selectedID === null || confirming) return;
    confirming = true;
    confirmError = null;
    try {
      const outcome = await onConfirm(selectedID);
      if (outcome.ok) {
        onClose();
        return;
      }
      if (outcome.code === 'merge_required') {
        if (onMergeRequired) onMergeRequired(outcome.conflict);
        else confirmError = outcome.message;
        return;
      }
      confirmError =
        outcome.code === 'already_linked'
          ? 'Already linked — these two are treated as the same person.'
          : outcome.message;
    } finally {
      confirming = false;
    }
  }
  /** Every dismissal path (Cancel, Escape, backdrop, the × button) funnels
   * through here: while a confirm is in flight the dialog must stay visible
   * until the outcome is known — hiding it would let the link land (or fail)
   * invisibly. Same idiom as CreateTaskDialog. */
  function requestClose(): void {
    if (confirming) return;
    onClose();
  }
  /** One line that tells same-named candidates apart: their identifiers
   * (an email participant's address, or its stored identifier rows), how
   * much history they carry and over which years, and whether the cluster
   * already has a directory record — linking into that one keeps the
   * profile, linking two unpromoted clusters does not. */
  function candidateSummary(row: PersonSummary): string {
    const labels = (row.identifiers ?? []).map((identifier) => identifier.display_value?.trim() || identifier.value);
    const parts = [labels.length > 0 ? labels.join(', ') : 'No stored identifiers'];
    parts.push(`${row.activity_count.toLocaleString()} items`);
    const years = yearRange(row.first_at, row.last_at);
    if (years) parts.push(years);
    if (row.profile) parts.push('In directory');
    return parts.join(' · ');
  }

  function yearRange(firstAt: string, lastAt: string): string {
    const first = new Date(firstAt).getFullYear();
    const last = new Date(lastAt).getFullYear();
    if (Number.isNaN(first) && Number.isNaN(last)) return '';
    if (Number.isNaN(first) || Number.isNaN(last) || first === last) return String(Number.isNaN(first) ? last : first);
    return `${first}–${last}`;
  }
  function messageFor(value: unknown, status: number): string {
    if (typeof value === 'object' && value !== null && 'message' in value) {
      const message = (
        value as {
          message?: unknown;
        }
      ).message;
      if (typeof message === 'string' && message) return message;
    }
    return status ? `Search failed (${status})` : 'Search failed';
  }
</script>

<Modal
  title={`Link another identity for ${personLabel}`}
  ariaLabel={`Link another identity for ${personLabel}`}
  onclose={requestClose}
  maxWidth="min(960px, calc(100vw - 32px))"
>
  <div class="link-identity-dialog" aria-busy={confirming}>
    <Typeahead
      {options}
      value={selectedID === null ? '' : String(selectedID)}
      fallbackLabel="Choose a person"
      placeholder="Search people to link"
      title="Person to link"
      emptyLabel="No matching people found"
      loading={searching}
      loadingLabel="Searching…"
      remote
      onquery={handleQueryInput}
      error={searchError ?? ''}
      onselect={(value) => {
        selectResult(Number(value));
      }}
    />
    {#if confirmError}
      <p class="confirm-error" role="alert">{confirmError}</p>
    {/if}
  </div>
  {#snippet footer()}
    <Button surface="soft" label="Cancel" disabled={confirming} onclick={requestClose} />
    <Button
      tone="info"
      surface="solid"
      label="These are the same person"
      disabled={selectedID === null || confirming}
      onclick={() => void confirmLink()}
    />
  {/snippet}
</Modal>

<style>
  /* Candidates are told apart by their addresses, history, and years, so
   * the search fills a wide dialog and each result gets room to read. */
  .link-identity-dialog {
    display: flex;
    width: min(56rem, calc(100vw - 80px));
    flex-direction: column;
    gap: var(--space-3);
    --typeahead-min-width: 100%;
    --typeahead-max-width: none;
    --typeahead-control-height: 44px;
    --typeahead-control-padding: 0 var(--space-3);
    --typeahead-control-font-size: var(--font-size-md);
  }

  .link-identity-dialog :global(.kit-typeahead__panel) {
    max-height: 70vh;
  }

  .link-identity-dialog :global(.kit-typeahead__option) {
    gap: var(--space-4);
    padding: var(--space-2) var(--space-3);
    font-size: var(--font-size-sm);
  }

  /* Keep the name readable beside a long summary instead of shrinking it
   * to a few letters. */
  .link-identity-dialog :global(.kit-typeahead__option-label) {
    flex: 0 1 auto;
    min-width: min(16rem, 40%);
    color: var(--text-primary);
  }

  .confirm-error {
    margin: 0;
    color: var(--text-danger);
    font-size: var(--font-size-xs);
  }
</style>
