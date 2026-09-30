<script lang="ts">
  import {
    getPersonProfile as generatedGetPersonProfile,
    mergePersons as generatedMergePersons,
  } from '../../api/generated/api/api';
  import {
    appShortcuts,
    Button,
    formatTimestamp,
    Modal,
    SegmentedControl,
    type SegmentedControlOption,
  } from '@kenn-io/kit-ui';
  import { onDestroy, onMount, untrack } from 'svelte';
  import type { APIClient } from '../../api/client';
  import { entityNames, invalidatePeopleNames } from '../../names/entity-names.svelte';
  import {
    isMatchingPersonETag,
    isPersonMergeRevisionConflict,
    validatePersonMergeRequired,
    type PersonMergeSuccess,
    type ValidatedPersonMergeRequired,
  } from '../../directory/person-merge';
  type Profile = ValidatedPersonMergeRequired['profiles'][number];
  interface Props {
    client: APIClient;
    conflict: ValidatedPersonMergeRequired;
    onOpenProfile: (personID: number) => void;
    onSuccess: (success: PersonMergeSuccess) => void | Promise<void>;
    onClose: () => void;
  }
  let { client, conflict, onOpenProfile, onSuccess, onClose }: Props = $props();
  const names = $derived(entityNames(client));
  let profiles = $state<[Profile, Profile]>(untrack(() => [conflict.profiles[0], conflict.profiles[1]]));
  // The profile with more identities, then the older one, then the lower
  // ID survives by default; the user can still pick the other.
  let survivorID = $state<number | null>(untrack(() => defaultSurvivorID(conflict.profiles)));
  // True once the user picked the survivor; a reload keeps that choice.
  let survivorChosen = false;
  let pending = $state(false);
  let completed = $state(false);
  let error = $state<string | null>(null);
  let idempotencyKey = $state<string | null>(null);
  let reloadRequired = $state(false);
  // The modal merges into the default survivor on open and shows the choice
  // only when that automatic merge cannot complete.
  let choosing = $state(false);
  let requestGeneration = 0;
  let disposed = false;
  let abortController: AbortController | undefined;
  let releaseShortcutScope: (() => void) | undefined;
  const options = $derived<SegmentedControlOption[]>(
    profiles.map((profile) => ({
      value: String(profile.person.id),
      label: profileLabel(profile),
    })),
  );
  onMount(() => {
    releaseShortcutScope = appShortcuts.pushScope('person-binding-conflict-modal');
    void mergeAutomatically();
  });
  onDestroy(() => {
    disposed = true;
    requestGeneration += 1;
    abortController?.abort();
    releaseShortcutScope?.();
  });
  function defaultSurvivorID(candidates: readonly Profile[]): number | null {
    const ranked = [...candidates].sort((left, right) => {
      const identities = (right.person.participant_ids?.length ?? 0) - (left.person.participant_ids?.length ?? 0);
      if (identities !== 0) return identities;
      const leftCreated = Date.parse(left.person.created_at);
      const rightCreated = Date.parse(right.person.created_at);
      if (Number.isFinite(leftCreated) && Number.isFinite(rightCreated) && leftCreated !== rightCreated) {
        return leftCreated - rightCreated;
      }
      return left.person.id - right.person.id;
    });
    return ranked[0]?.person.id ?? null;
  }
  function profileName(profile: Profile): string {
    return names.name('person', profile.person.id, profile.person.display_name);
  }
  function profileDetail(profile: Profile): string {
    const identities = profile.person.participant_ids?.length ?? 0;
    return `Created ${formatTimestamp(profile.person.created_at)} · ${identities} ${identities === 1 ? 'identity' : 'identities'} · revision ${profile.person.revision}`;
  }
  // Two profiles can share a name; the creation time tells them apart.
  function profileLabel(profile: Profile): string {
    const name = profileName(profile);
    const twin = profiles.some((other) => other.person.id !== profile.person.id && profileName(other) === name);
    return twin ? `${name} (created ${formatTimestamp(profile.person.created_at)})` : name;
  }
  function selectSurvivor(value: string): void {
    const nextID = Number(value);
    survivorChosen = true;
    if (nextID === survivorID) return;
    survivorID = nextID;
    idempotencyKey = null;
    error = null;
  }
  function requestClose(): void {
    if (pending || completed) return;
    onClose();
  }
  async function reloadProfiles(generation: number, signal: AbortSignal): Promise<void> {
    try {
      const requestedProfiles = profiles;
      const results = await Promise.all(
        requestedProfiles.map((profile) =>
          generatedGetPersonProfile(
            { id: profile.person.id },
            {
              ...client,
              signal,
            },
          ),
        ),
      );
      if (disposed || generation !== requestGeneration) return;
      const refreshed = results.map((result, index) => {
        const expectedPersonID = requestedProfiles[index]!.person.id;
        const etag = result.response.headers.get('ETag');
        if (!result.data || result.data.id !== expectedPersonID || !isMatchingPersonETag(etag, expectedPersonID))
          return null;
        return { person: result.data, etag };
      });
      if (!refreshed[0] || !refreshed[1]) {
        error = 'The merge was stale, but could not load both current profile revisions. Try again.';
        return;
      }
      const next = validatePersonMergeRequired({
        error: 'person_merge_required',
        message: conflict.message,
        profiles: refreshed,
      });
      if (!next) {
        error = 'The merge was stale, but could not load both current profile revisions. Try again.';
        return;
      }
      profiles = next.profiles;
      // Keep the user's explicit survivor while it still exists; otherwise
      // fall back to the default for the current profiles.
      const keepChoice = survivorChosen && next.profiles.some((profile) => profile.person.id === survivorID);
      if (!keepChoice) {
        survivorChosen = false;
        survivorID = defaultSurvivorID(next.profiles);
      }
      idempotencyKey = null;
      reloadRequired = false;
      error = 'Profiles changed — check the survivor, then merge again.';
    } catch (cause) {
      if (disposed || generation !== requestGeneration || signal.aborted) return;
      error = 'The merge was stale, but could not load both current profile revisions. Try again.';
    }
  }
  async function retryProfileReload(): Promise<void> {
    if (pending || completed || !reloadRequired) return;
    pending = true;
    error = null;
    abortController = new AbortController();
    const generation = ++requestGeneration;
    try {
      await reloadProfiles(generation, abortController.signal);
    } finally {
      if (!disposed && generation === requestGeneration) pending = false;
    }
  }
  // Merges into the default survivor, reloading and retrying once when the
  // profiles changed underneath; any other outcome hands the choice to the user.
  async function mergeAutomatically(): Promise<void> {
    let outcome = await attemptMerge();
    if (outcome === 'reloaded') outcome = await attemptMerge();
    if (disposed || outcome === 'merged' || outcome === 'superseded') return;
    choosing = true;
  }
  async function submit(): Promise<void> {
    await attemptMerge();
  }
  type MergeOutcome = 'merged' | 'reloaded' | 'failed' | 'superseded' | 'skipped';
  async function attemptMerge(): Promise<MergeOutcome> {
    if (pending || completed || reloadRequired || survivorID === null) return 'skipped';
    const survivor = profiles.find((profile) => profile.person.id === survivorID);
    const absorbed = profiles.find((profile) => profile.person.id !== survivorID);
    if (!survivor || !absorbed) return 'skipped';
    const key = idempotencyKey ?? crypto.randomUUID();
    idempotencyKey = key;
    pending = true;
    error = null;
    abortController = new AbortController();
    const generation = ++requestGeneration;
    try {
      const response = await generatedMergePersons(
        { id: survivor.person.id },
        { absorbed_person_id: absorbed.person.id },
        {
          ...client,
          signal: abortController.signal,
          headers: {
            'If-Match': `${survivor.etag}, ${absorbed.etag}`,
            'Idempotency-Key': key,
          },
        },
      );
      if (disposed || generation !== requestGeneration) return 'superseded';
      if (response.data) {
        completed = true;
        // The absorbed person's identities now belong to the survivor.
        invalidatePeopleNames(client);
        const responseETag = response.response.headers.get('ETag');
        try {
          await onSuccess({
            result: response.data,
            survivor: response.data.person,
            responseETag: isMatchingPersonETag(responseETag, response.data.person.id) ? responseETag : null,
          });
        } catch {
          error = 'People were merged, but the surrounding view could not be refreshed.';
        }
        return 'merged';
      }
      idempotencyKey = null;
      if (isPersonMergeRevisionConflict(response.error)) {
        reloadRequired = true;
        await reloadProfiles(generation, abortController.signal);
        if (disposed || generation !== requestGeneration) return 'superseded';
        return reloadRequired ? 'failed' : 'reloaded';
      }
      error = response.error?.message ?? 'The people could not be merged.';
      return 'failed';
    } catch (cause) {
      if (disposed || generation !== requestGeneration || abortController.signal.aborted) return 'superseded';
      error = cause instanceof Error ? cause.message : 'The merge request could not be sent.';
      return 'failed';
    } finally {
      if (!disposed && generation === requestGeneration) pending = false;
    }
  }
</script>

{#if choosing}
  <Modal
    title="Resolve person merge"
    ariaLabel="Resolve person merge"
    closeLabel="Close person merge"
    closable={!pending && !completed}
    closeOnOverlayClick={!pending && !completed}
    onclose={requestClose}
    maxWidth="min(680px, calc(100vw - 32px))"
  >
    <div class="conflict" aria-busy={pending}>
      <p>
        These people could not be merged automatically. Inspect both profiles, then choose the one to keep.
      </p>

      <div class="profiles">
        {#each profiles as profile (profile.person.id)}
          <article>
            <strong>{profileName(profile)}</strong>
            <span>{profileDetail(profile)}</span>
            <Button
              surface="soft"
              label={`Open ${profileName(profile)} profile`}
              disabled={pending}
              onclick={() => onOpenProfile(profile.person.id)}
            />
          </article>
        {/each}
      </div>

      <SegmentedControl
        ariaLabel="Merge survivor"
        {options}
        value={survivorID === null ? '' : String(survivorID)}
        onchange={selectSurvivor}
        disabled={pending || completed || reloadRequired}
        block
      />

      <p class="undo-hint">
        You can undo a merge later: open the kept person's Maintenance tab, find it under Merge history, and choose
        Split merged profile.
      </p>

      {#if error}<p class="error" role="alert">{error}</p>{/if}
      {#if completed}<p role="status">People merged.</p>{/if}
    </div>

    {#snippet footer()}
      <Button surface="soft" label="Cancel" disabled={pending || completed} onclick={requestClose} />
      {#if !completed}
        {#if reloadRequired}
          <Button
            surface="soft"
            label="Retry profile reload"
            disabled={pending}
            onclick={() => void retryProfileReload()}
          />
        {/if}
        <Button
          tone="info"
          surface="solid"
          label="Merge into selected survivor"
          disabled={pending || reloadRequired || survivorID === null}
          onclick={() => void submit()}
        />
      {/if}
    {/snippet}
  </Modal>
{:else}
  <!-- The automatic merge cannot be dismissed mid-request; it either
       completes or falls back to the choice above. Only a merge whose
       surrounding view failed to refresh leaves it open to close. -->
  <Modal
    title="Merging people"
    ariaLabel="Merging people"
    closeLabel="Close"
    closable={completed && error !== null}
    closeOnOverlayClick={false}
    onclose={() => {
      if (completed && error !== null) onClose();
    }}
    maxWidth="min(420px, calc(100vw - 32px))"
  >
    <div class="automatic" aria-busy={!completed}>
      {#if completed}
        <p role="status">People merged.</p>
      {:else}
        <p role="status">Merging people…</p>
      {/if}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
    </div>
  </Modal>
{/if}

<style>
  .conflict {
    display: grid;
    gap: var(--space-4);
    min-width: min(34rem, calc(100vw - 64px));
  }
  p {
    margin: 0;
  }
  .automatic {
    display: grid;
    gap: var(--space-3);
  }
  .profiles {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--space-3);
  }
  article {
    display: grid;
    align-content: start;
    gap: var(--space-2);
    padding: var(--space-4);
    border: var(--border-width) solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
  }
  article span {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
  .undo-hint {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
  .error {
    color: var(--text-danger);
  }
  @media (max-width: 640px) {
    .profiles {
      grid-template-columns: 1fr;
    }
  }
</style>
