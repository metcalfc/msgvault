<script lang="ts">
  import { Button, Modal, Notice, TextInput } from '@kenn-io/kit-ui';
  import { untrack } from 'svelte';

  import type { APIClient } from '../../api/client';
  import type { CorrespondentKindPerson, SetCorrespondentKindResult } from '../../api/generated/models';
  import {
    NOT_A_PERSON_CHOICES,
    deleteSavedProfile,
    kindLabel,
    setKind,
    type NotAPersonKind
  } from '../../people/correspondent-kind';

  interface Props {
    client: APIClient;
    /** Participants whose identity clusters are marked together. */
    participantIDs: number[];
    /** The record's name, for the heading and the organization default. */
    label: string;
    /** Prefill for a new organization's name: a display name or a domain. */
    suggestedOrganization?: string;
    initialKind?: NotAPersonKind;
    onClose: () => void;
    /** Called after the record is marked, with every cluster's result. */
    onDone: (results: SetCorrespondentKindResult[], deletedPersonID?: number) => void;
  }

  let {
    client, participantIDs, label, suggestedOrganization = '', initialKind = undefined, onClose, onDone
  }: Props = $props();

  let kind = $state<NotAPersonKind | undefined>(untrack(() => initialKind));
  let organizationName = $state(untrack(() => suggestedOrganization));
  let step = $state<'choose' | 'profile' | 'confirm-delete'>('choose');
  let pending = $state(false);
  let error = $state<string | null>(null);
  let results = $state<SetCorrespondentKindResult[]>([]);
  // A saved profile made only of this record, which the user may choose to
  // delete in a separate, explicit step.
  let profile = $state<CorrespondentKindPerson>();

  const groupName = `not-a-person-${Math.random().toString(36).slice(2)}`;
  const profileName = $derived(profile?.display_name?.trim() || `Person ${profile?.id ?? ''}`);

  async function submit(): Promise<void> {
    if (!kind || pending) return;
    pending = true;
    error = null;
    const done: SetCorrespondentKindResult[] = [];
    try {
      for (const id of participantIDs) {
        const outcome = await setKind(client, id, kind, kind === 'organization' ? organizationName : undefined);
        if (!outcome.ok) {
          error = outcome.message;
          results = done;
          return;
        }
        done.push(outcome.result);
      }
      results = done;
      const onlyHere = done.map((result) => result.record.person).find((person) => person?.only_this_cluster);
      if (onlyHere && kind !== 'shared_mailbox') {
        profile = onlyHere;
        step = 'profile';
        return;
      }
      onDone(done);
    } finally {
      pending = false;
    }
  }

  async function removeProfile(): Promise<void> {
    if (!profile || pending) return;
    pending = true;
    error = null;
    try {
      const removed = await deleteSavedProfile(client, profile.id);
      if (!removed.ok) {
        error = removed.message;
        return;
      }
      onDone(results, profile.id);
    } finally {
      pending = false;
    }
  }

  function requestClose(): void {
    if (pending) return;
    // Closing after the record was marked still reports it.
    if (results.length > 0) onDone(results);
    else onClose();
  }
</script>

<Modal
  title={step === 'choose' ? `Not a person: ${label}` : step === 'profile' ? 'Keep the saved profile?' : 'Delete the saved profile?'}
  ariaLabel="Not a person"
  closeLabel="Close"
  closable={!pending}
  closeOnOverlayClick={!pending}
  onclose={requestClose}
  maxWidth="min(560px, calc(100vw - 32px))"
>
  <div class="not-a-person" aria-busy={pending}>
    {#if step === 'choose'}
      <p>Records that are not people leave People, Reviews, relationship rankings, and enrichment. Their messages stay searchable, and you can undo this any time.</p>
      <fieldset>
        <legend>What is this record?</legend>
        {#each NOT_A_PERSON_CHOICES as choice (choice.kind)}
          <label class="choice" class:selected={kind === choice.kind}>
            <input type="radio" name={groupName} value={choice.kind} checked={kind === choice.kind}
              disabled={pending} onchange={() => (kind = choice.kind)} />
            <span>
              <strong>{choice.label}</strong>
              <span class="explanation">{choice.explanation}</span>
            </span>
          </label>
        {/each}
      </fieldset>
      {#if kind === 'organization'}
        <label class="organization">
          <span>Organization name</span>
          <TextInput value={organizationName} ariaLabel="Organization name" block disabled={pending}
            placeholder="Found by name, or created" oninput={(value) => (organizationName = value)} />
          <span class="hint">An organization with this name is reused; otherwise one is created.</span>
        </label>
      {/if}
    {:else if step === 'profile'}
      <p><strong>{profileName}</strong> is a saved profile made only of this record. It was kept. Keep it, or delete it now? Messages stay either way.</p>
    {:else}
      <p>Delete the saved profile <strong>{profileName}</strong>? Its notes, fields, and history are removed permanently. Messages stay searchable, and the record stays marked as {kindLabel(kind).toLowerCase()}.</p>
    {/if}
    {#if error}<Notice tone="error" message={error} />{/if}
  </div>

  {#snippet footer()}
    {#if step === 'choose'}
      <Button surface="soft" label="Cancel" disabled={pending} onclick={requestClose} />
      <Button tone="info" surface="solid" disabled={!kind || pending || (kind === 'organization' && !organizationName.trim())}
        label={kind ? `Mark as ${kindLabel(kind).toLowerCase()}` : 'Mark as not a person'} onclick={() => void submit()} />
    {:else if step === 'profile'}
      <Button surface="soft" tone="danger" label="Delete profile…" disabled={pending} onclick={() => (step = 'confirm-delete')} />
      <Button tone="info" surface="solid" label="Keep profile" disabled={pending} onclick={() => onDone(results)} />
    {:else}
      <Button surface="soft" label="Cancel" disabled={pending} onclick={() => (step = 'profile')} />
      <Button tone="danger" surface="solid" label="Delete profile" disabled={pending} onclick={() => void removeProfile()} />
    {/if}
  {/snippet}
</Modal>

<style>
  .not-a-person { display: grid; gap: var(--space-4); min-width: min(28rem, calc(100vw - 64px)); }
  p { margin: 0; color: var(--text-secondary); }
  fieldset { display: grid; gap: var(--space-2); margin: 0; padding: 0; border: 0; }
  legend { margin-bottom: var(--space-2); color: var(--text-primary); font-weight: var(--font-weight-medium, 500); }
  .choice {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: var(--space-3);
    align-items: start;
    padding: var(--space-3);
    border: var(--border-width) solid var(--border-muted);
    border-radius: var(--radius-sm);
    cursor: pointer;
  }
  .choice.selected { border-color: var(--accent-blue); background: var(--surface-well); }
  .choice input { margin-top: 3px; }
  .choice input:focus-visible { outline: var(--focus-ring); outline-offset: 2px; }
  .choice > span { display: grid; gap: var(--space-1); }
  .choice strong { color: var(--text-primary); }
  .explanation, .hint { color: var(--text-muted); font-size: var(--font-size-sm); }
  .organization { display: grid; gap: var(--space-2); color: var(--text-secondary); font-size: var(--font-size-sm); }
</style>
