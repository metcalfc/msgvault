<script lang="ts">
  import { untrack, type Snippet } from 'svelte';
  import { Button, IconButton } from '@kenn-io/kit-ui';
  import XIcon from '@lucide/svelte/icons/x';
  import { getPersonProfileHistory } from '../../api/generated/api/api';
  import type {
    PersonContactPoint,
    PersonContactPointInputRequest,
    PersonProfilePatchRequest,
  } from '../../api/generated/models';
  import type { APIClient } from '../../api/client';
  import type { DirectoryProfileController } from '../../directory/profile-controller.svelte';
  import { splitFormerReach, type ReachEntry } from '../../people/reach';
  import { shortDate } from '../../util/dates';
  import PersonReachBlock from '../people/PersonReachBlock.svelte';

  interface Props {
    client: APIClient;
    personID: number;
    /** The person's name, for the "Not <name>" action. */
    displayName: string;
    /** Current rows: address-book contact points merged with archive identities. */
    entries: ReachEntry[];
    /** The person's current contact points, for retire and its undo. */
    contactPoints?: PersonContactPoint[];
    /** The person revision; history reloads when it changes. */
    revision?: number;
    profileController?: DirectoryProfileController | null;
    onAnnounce?: (message: string) => void;
    after?: Snippet;
  }

  let {
    client,
    personID,
    displayName,
    entries,
    contactPoints = [],
    revision = undefined,
    profileController = null,
    onAnnounce = () => undefined,
    after = undefined,
  }: Props = $props();

  type RowAction = { kind: 'retire' | 'detach'; label: string };
  type LastAction =
    | { kind: 'retire'; label: string; points: PersonContactPoint[] }
    | { kind: 'detach'; label: string; detachmentID: number };

  // Retired values come from profile history: superseded contact points.
  let retired = $state<PersonContactPoint[]>([]);
  $effect(() => {
    const id = personID;
    void revision;
    const abort = new AbortController();
    void untrack(() => getPersonProfileHistory({ id }, { ...client, signal: abort.signal }))
      .then((response) => {
        if (abort.signal.aborted || !response.data) return;
        retired = (response.data.contact_points ?? []).filter((point) => point.envelope.superseded_at);
      })
      .catch(() => undefined);
    return () => abort.abort();
  });

  const split = $derived(splitFormerReach(entries, retired));
  const former = $derived(
    split.former.map((entry) => ({
      ...entry,
      note: entry.retiredAt ? `retired ${shortDate(entry.retiredAt)}` : 'retired',
    })),
  );

  let pending = $state(false);
  let status = $state<string | null>(null);
  let error = $state<string | null>(null);
  let last = $state<LastAction>();

  /** A row backed by a current contact point is retired; a row that is
   * only archive identities is detached. A row with both is retired: its
   * message history belongs to the person. */
  function rowAction(entry: ReachEntry): RowAction | undefined {
    if (!profileController) return undefined;
    if (entry.contactPointIDs?.length) {
      return { kind: 'retire', label: `Retire ${entry.label} — stops syncing, keeps history` };
    }
    if (detachableIDs(entry).length > 0) {
      return { kind: 'detach', label: `Not ${displayName} — detach ${entry.label}` };
    }
    return undefined;
  }

  /** The row's participants that are bound to this person. A linked
   * cluster member the person does not hold has nothing to detach. */
  function detachableIDs(entry: ReachEntry): number[] {
    const bound = new Set(profileController?.person?.participant_ids ?? []);
    return entry.participantIDs.filter((id) => bound.has(id));
  }

  function report(message: string): void {
    status = message;
    error = null;
    onAnnounce(message);
  }

  function fail(message: string): void {
    error = message;
    status = null;
  }

  /** Runs one profile patch and reports whether it committed. A failed
   * patch leaves no draft behind on the overview. */
  async function patch(body: PersonProfilePatchRequest): Promise<string | null> {
    const controller = profileController;
    if (!controller) return 'Profile editing is unavailable here.';
    const blocked = await controller.patchProfile(body);
    if (blocked) return 'Wait for the current change to finish, then try again.';
    if (controller.draft === null && controller.conflict === null) return null;
    const message = controller.conflict?.message ?? 'The change was not saved.';
    controller.discardProfileDraft();
    return message;
  }

  async function act(entry: ReachEntry, action: RowAction): Promise<void> {
    if (pending || !profileController) return;
    pending = true;
    try {
      if (action.kind === 'retire') {
        const ids = entry.contactPointIDs ?? [];
        const points = contactPoints.filter((point) => ids.includes(point.envelope.id));
        const failure = await patch({ contact_points: { supersede: ids } });
        if (failure) return fail(`Could not retire ${entry.label}: ${failure}`);
        last = { kind: 'retire', label: entry.label, points };
        report(`Retired ${entry.label}. It no longer syncs and stays under Former.`);
        return;
      }
      const outcome = await profileController.detachParticipants(detachableIDs(entry));
      if (!outcome.ok) return fail(`Could not detach ${entry.label}: ${outcome.message}`);
      last = { kind: 'detach', label: entry.label, detachmentID: outcome.detachmentID };
      report(`Detached ${entry.label} from ${displayName}.`);
    } finally {
      pending = false;
    }
  }

  /** The same value comes back as a new current contact point with the
   * retired one's kind, label, and type. */
  function restoredInput(point: PersonContactPoint): PersonContactPointInputRequest {
    const envelope = point.envelope;
    return {
      address_kind: point.address_kind,
      original_value: point.original_value,
      ...(point.service_slug ? { service_slug: point.service_slug } : {}),
      ...(point.scope_kind ? { scope_kind: point.scope_kind } : {}),
      ...(point.scope_value ? { scope_value: point.scope_value } : {}),
      ...(point.uri ? { uri: point.uri } : {}),
      envelope: {
        source: envelope.source,
        ...(envelope.pref !== undefined ? { pref: envelope.pref } : {}),
        ...(envelope.type_label ? { type_label: envelope.type_label } : {}),
        ...(envelope.type_tokens?.length ? { type_tokens: envelope.type_tokens } : {}),
        ...(envelope.vcard?.property ? { vcard: envelope.vcard } : {}),
        ...(envelope.source_ref ? { source_ref: envelope.source_ref } : {}),
        ...(envelope.source_resource_uid ? { source_resource_uid: envelope.source_resource_uid } : {}),
        ...(envelope.confidence !== undefined ? { confidence: envelope.confidence } : {}),
      },
    };
  }

  async function undo(): Promise<void> {
    const action = last;
    if (pending || !action || !profileController) return;
    pending = true;
    try {
      if (action.kind === 'retire') {
        const failure = await patch({ contact_points: { add: action.points.map(restoredInput) } });
        if (failure) return fail(`Could not restore ${action.label}: ${failure}`);
      } else {
        const outcome = await profileController.reattachParticipants(action.detachmentID);
        if (!outcome.ok) return fail(`Could not restore ${action.label}: ${outcome.message}`);
      }
      last = undefined;
      report(`Restored ${action.label}.`);
    } finally {
      pending = false;
    }
  }
</script>

{#snippet rowActions(entry: ReachEntry)}
  {@const action = rowAction(entry)}
  {#if action}
    <span class="row-remove" data-detail-actions="hover">
      <IconButton size="sm" ariaLabel={action.label} disabled={pending} onclick={() => void act(entry, action)}>
        <XIcon size={12} aria-hidden="true" />
      </IconButton>
    </span>
  {/if}
{/snippet}

<PersonReachBlock entries={split.current} {onAnnounce} actions={rowActions} {after} />
{#if status}
  <div class="reach-status">
    <p role="status" aria-live="polite">{status}</p>
    {#if last}
      <Button label="Undo" ariaLabel={`Undo: restore ${last.label}`} size="sm" surface="soft"
        disabled={pending} onclick={() => void undo()} />
    {/if}
  </div>
{/if}
{#if error}
  <p class="reach-error" role="alert">{error}</p>
{/if}
{#if former.length > 0}
  <details class="former">
    <summary>Former ({former.length})</summary>
    <PersonReachBlock entries={former} ariaLabel="Former contact methods" {onAnnounce} />
  </details>
{/if}

<style>
  .row-remove { display: inline-flex; margin-left: var(--space-1); vertical-align: middle; }
  .reach-status { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); padding: var(--space-2) 0; }
  .reach-status p, .reach-error { margin: 0; color: var(--text-secondary); font-size: var(--font-size-sm); }
  .reach-error { padding: var(--space-2) 0; color: var(--accent-red); }
  .former { padding: var(--space-2) 0; }
  .former summary { color: var(--text-muted); font-size: 11px; cursor: pointer; }
  .former summary:focus-visible { border-radius: var(--radius-sm); outline: var(--focus-ring); outline-offset: 1px; }
  .former :global([data-fact-value]) { color: var(--text-secondary); }
</style>
