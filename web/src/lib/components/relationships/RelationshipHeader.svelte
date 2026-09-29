<script lang="ts">
  import { untrack } from 'svelte';
  import EllipsisIcon from '@lucide/svelte/icons/ellipsis';
  import { Button, Menu, MenuContent, MenuItem, MenuTrigger, SegmentedControl } from '@kenn-io/kit-ui';

  import type { APIClient } from '../../api/client';
  import type { PersonAttributeGroup, PersonContactPoint } from '../../api/generated/models';
  import type { DomainSummary, PersonSummary } from '../../explore/models';
  import type { LinkOutcome, RelationshipsMergeContext } from '../../relationships/controller.svelte';
  import type { RelationshipSiblingCluster } from '../../relationships/models';
  import { identityChipText } from '../../relationships/identity-chip';
  import type { PersonMergeSuccess, ValidatedPersonMergeRequired } from '../../directory/person-merge';
  import type { DirectoryPromotionResult } from '../../directory/models';
  import {
    mergeReachEntries, reachEntriesFromContactPoints, reachEntriesFromIdentifiers, reachEntriesFromMembers,
    type ReachEntry
  } from '../../people/reach';
  import IdentityAvatar from '../common/IdentityAvatar.svelte';
  import AttributeSummary from '../directory/AttributeSummary.svelte';
  import PersonBindingConflictModal from '../directory/PersonBindingConflictModal.svelte';
  import PersonReachBlock from '../people/PersonReachBlock.svelte';
  import LinkIdentityDialog from './LinkIdentityDialog.svelte';

  const STALE_CACHE_MESSAGE =
    'Identity saved; the cache refresh failed — groupings may be stale until a rebuild. Retrying is safe.';

  interface Props {
    detail: PersonSummary | DomainSummary | null;
    loading?: boolean;
    filesOpen: boolean;
    onFilesToggle: (value: boolean) => void;
    client: APIClient;
    onLinkParticipants: (a: number, b: number) => Promise<LinkOutcome>;
    onUnlinkParticipants: (a: number, b: number) => Promise<LinkOutcome>;
    /** Promotes the loaded, API-validated participant cluster to a durable
     * Directory person. The caller owns navigation on success; the header
     * reports failures in place. */
    onPromotePerson?: (participantID: number) => Promise<DirectoryPromotionResult>;
    capturePersonMergeContext?: () => RelationshipsMergeContext;
    onReconcilePersonMerge?: (context: RelationshipsMergeContext) => Promise<void>;
    onOpenDirectoryPerson?: (personID: number) => void;
    /** Clusters a Directory person is bound to besides the open one. */
    siblingClusters?: RelationshipSiblingCluster[];
    onOpenSibling?: (target: string) => void;
    loadAttributes?: (personID: number) => Promise<PersonAttributeGroup[]>;
    /** The Directory profile's curated contact points, merged into the
     * contact block so address-book values sit beside archive-observed ones. */
    loadContactPoints?: (personID: number) => Promise<PersonContactPoint[]>;
    onAnnounce?: (message: string) => void;
  }

  let {
    detail,
    loading = false,
    filesOpen,
    onFilesToggle,
    client,
    onLinkParticipants,
    onUnlinkParticipants,
    onPromotePerson = undefined,
    capturePersonMergeContext = undefined,
    onReconcilePersonMerge = undefined,
    onOpenDirectoryPerson = undefined,
    siblingClusters = [],
    onOpenSibling = undefined,
    loadAttributes = undefined,
    loadContactPoints = undefined,
    onAnnounce = undefined
  }: Props = $props();

  type LinkMutation = { kind: 'link' | 'unlink'; a: number; b: number };

  type ActiveDialog =
    | { kind: 'link'; context?: RelationshipsMergeContext }
    | { kind: 'merge'; context?: RelationshipsMergeContext; conflict: ValidatedPersonMergeRequired };
  let activeDialog = $state<ActiveDialog>();
  let staleBanner = $state<'identity_cache_stale' | null>(null);
  let lastMutation = $state<LinkMutation | null>(null);
  let retrying = $state(false);
  let confirmingParticipantID = $state<number | null>(null);
  let unlinking = $state(false);
  let unlinkError = $state<string | null>(null);
  let promoting = $state(false);
  let promotionFailure = $state<Extract<DirectoryPromotionResult, { ok: false }> | null>(null);
  let attributeGroups = $state<PersonAttributeGroup[]>([]);
  let contactPoints = $state<PersonContactPoint[]>([]);

  function isPersonDetail(value: PersonSummary | DomainSummary): value is PersonSummary {
    return 'identifiers' in value;
  }

  /** The open person's id, or null for a domain/empty detail. Read again
   * after every `await` in the mutation flows below instead of trusting a
   * value captured before the await: `detail` is a reactive prop, and
   * navigating to a different person (or a domain, or clearing the target)
   * while a link/unlink call is in flight replaces it out from under the
   * pending promise. */
  function currentPersonID(): number | null {
    return detail && isPersonDetail(detail) ? detail.id : null;
  }

  function displayLabel(value: PersonSummary | DomainSummary): string {
    return isPersonDetail(value) ? value.display_label : value.domain;
  }

  function formatDate(value: string): string {
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? value : date.toLocaleDateString();
  }

  /** Cluster members (from PersonCluster.member_ids) with no row in
   * `identifiers` at all — e.g. linked purely by a manual participant link
   * with no stored email/phone evidence. Without a fallback chip for these,
   * such a member has no detach control anywhere in the UI: the identifier
   * loop below never renders anything for it. */
  const unrepresentedMembers = $derived.by((): number[] => {
    if (!detail || !isPersonDetail(detail) || !detail.cluster) return [];
    const known = new Set((detail.identifiers ?? []).map((identifier) => identifier.participant_id));
    return (detail.cluster.member_ids ?? []).filter((id) => id !== detail.id && !known.has(id));
  });
  /** Every way to reach the open person: the Directory profile's contact
   * points (address book) merged with the cluster's identifiers and member
   * addresses (archive), deduplicated by normalized value. */
  const reachEntries = $derived.by((): ReachEntry[] => {
    if (!detail || !isPersonDetail(detail)) return [];
    const members = detail.cluster?.members ?? [];
    const edges = detail.cluster?.edges ?? [];
    return mergeReachEntries(
      reachEntriesFromContactPoints(contactPoints),
      reachEntriesFromIdentifiers({
        identifiers: detail.identifiers, ownID: detail.id, members, edges, clustered: Boolean(detail.cluster)
      }),
      reachEntriesFromMembers(
        unrepresentedMembers.map((id) => memberFor(id) ?? { participant_id: id }), edges
      )
    );
  });

  /** The other clusters of the person who opened this target, shown only
   * while the open target is one of the set. */
  const otherIdentities = $derived.by((): RelationshipSiblingCluster[] => {
    if (!detail || !isPersonDetail(detail)) return [];
    const current = `cluster:${detail.id}`;
    if (!siblingClusters.some((cluster) => cluster.target === current)) return [];
    return siblingClusters.filter((cluster) => cluster.target !== current);
  });

  /** Unrepresented members that no contact row covers (no stored address,
   * or one that was never merged into a row) are listed below the block so
   * their unlink control remains reachable. */
  const bareMembers = $derived(unrepresentedMembers.filter((id) =>
    !reachEntries.some((entry) => entry.participantIDs.includes(id))
  ));

  function isOtherMember(participantID: number): boolean {
    return Boolean(detail && isPersonDetail(detail) && detail.cluster && participantID !== detail.id);
  }

  /** The linked members contributing to a row — each gets its own Unlink. */
  function otherMembersOf(entry: ReachEntry): number[] {
    return entry.participantIDs.filter(isOtherMember);
  }

  function memberLabel(participantID: number): string {
    const member = memberFor(participantID);
    return member?.display_name ? `${member.display_name} (${participantID})` : `profile ${participantID}`;
  }

  function memberFor(participantID: number) {
    return detail && isPersonDetail(detail)
      ? detail.cluster?.members?.find((member) => member.participant_id === participantID) : undefined;
  }

  function edgesFor(participantID: number) {
    return detail && isPersonDetail(detail)
      ? (detail.cluster?.edges ?? []).filter((edge) => edge.participant_a === participantID || edge.participant_b === participantID)
      : [];
  }

  // Navigating to a different person must not leave behind a stale banner
  // (or its Retry) bound to the previous cluster's IDs, and must not leave
  // a pending unlink confirm open on a chip that no longer belongs to the
  // now-open detail.
  let lastPersonID: number | null = null;
  let profileID = $state<number>();
  $effect(() => {
    // Reloads briefly clear detail; keep the current person's UI state.
    if (!detail) return;
    profileID = isPersonDetail(detail) ? detail.profile?.id : undefined;
    const currentID = isPersonDetail(detail) ? detail.id : null;
    if (currentID === lastPersonID) return;
    lastPersonID = currentID;
    staleBanner = null;
    lastMutation = null;
    confirmingParticipantID = null;
    unlinkError = null;
    promotionFailure = null;
    activeDialog = undefined;
  });

  $effect(() => {
    const id = profileID;
    attributeGroups = [];
    if (!id) return;
    let cancelled = false;
    void untrack(() => loadAttributes?.(id))
      ?.then((groups) => { if (!cancelled) attributeGroups = groups; })
      .catch(() => {
        // This summary is best-effort; failed requests leave it empty.
      });
    return () => { cancelled = true; };
  });

  $effect(() => {
    const id = profileID;
    contactPoints = [];
    if (!id) return;
    let cancelled = false;
    void untrack(() => loadContactPoints?.(id))
      ?.then((points) => { if (!cancelled) contactPoints = points; })
      .catch(() => {
        // Best-effort: the archive-derived rows still render without it.
      });
    return () => { cancelled = true; };
  });

  // ok/ready clears any earlier stale banner; ok/stale (re)raises it and
  // remembers the mutation so Retry can safely re-invoke the identical,
  // idempotent link/unlink call.
  function applyOutcome(outcome: LinkOutcome, kind: 'link' | 'unlink', a: number, b: number): void {
    if (!outcome.ok) return;
    if (outcome.cacheState === 'stale') {
      staleBanner = 'identity_cache_stale';
      lastMutation = { kind, a, b };
    } else {
      staleBanner = null;
      lastMutation = null;
    }
  }

  async function confirmLink(participantID: number): Promise<LinkOutcome> {
    if (!detail || !isPersonDetail(detail)) throw new Error('Link identity requires an open person cluster');
    const id = detail.id;
    const outcome = await onLinkParticipants(id, participantID);
    // If navigation replaced `detail` mid-flight, this outcome belongs to a
    // person that is no longer open — don't repopulate the banner/lastMutation
    // for whoever is showing now with a result that was never about them.
    if (currentPersonID() === id) applyOutcome(outcome, 'link', id, participantID);
    return outcome;
  }

  async function promote(): Promise<void> {
    if (!detail || !isPersonDetail(detail) || !onPromotePerson || promoting) return;
    const id = detail.id;
    promoting = true;
    promotionFailure = null;
    try {
      const result = await onPromotePerson(id);
      // A failure for a person no longer open must not surface under
      // whoever is showing now.
      if (!result.ok && currentPersonID() === id) promotionFailure = result;
    } finally {
      promoting = false;
    }
  }

  function openLinkDialog(): void {
    activeDialog = { kind: 'link', context: capturePersonMergeContext?.() };
  }

  function resolveMerge(conflict: ValidatedPersonMergeRequired): void {
    if (activeDialog?.kind !== 'link') return;
    activeDialog = { kind: 'merge', context: activeDialog.context, conflict };
  }

  function completeMerge(success: PersonMergeSuccess): void {
    if (activeDialog?.kind !== 'merge') return;
    const context = activeDialog.context;
    activeDialog = undefined;
    const name = success.survivor.display_name?.trim() || `Person ${success.survivor.id}`;
    onAnnounce?.(`People merged into ${name}. Identity cache ${success.result.cache_state}.`);
    if (context && onReconcilePersonMerge) void onReconcilePersonMerge(context);
    onOpenDirectoryPerson?.(success.survivor.id);
  }

  async function retryRefresh(): Promise<void> {
    if (!lastMutation || retrying) return;
    const id = currentPersonID();
    retrying = true;
    try {
      const { kind, a, b } = lastMutation;
      const outcome = kind === 'link' ? await onLinkParticipants(a, b) : await onUnlinkParticipants(a, b);
      if (currentPersonID() === id) applyOutcome(outcome, kind, a, b);
    } finally {
      retrying = false;
    }
  }

  function startUnlink(participantID: number): void {
    confirmingParticipantID = participantID;
    unlinkError = null;
  }

  function cancelUnlink(): void {
    confirmingParticipantID = null;
    unlinkError = null;
  }

  // Detaching one identity means removing every link edge that touches it,
  // not just one. A cluster built from hand-linked pairs can be a chain
  // rather than a star (a-b, b-c, c-d), so a member in the middle can be a
  // cut vertex joined to the rest through more than one edge; leaving any
  // incident edge in place would keep it joined via that edge even though
  // the user asked to detach it. Edges are removed sequentially and each
  // call is independently idempotent, so a retry after a partial failure
  // (network error mid-sequence) is always safe — already-removed edges
  // 200 as no-ops.
  async function confirmUnlink(participantID: number): Promise<void> {
    if (!detail || !isPersonDetail(detail) || !detail.cluster || unlinking) return;
    const id = detail.id;
    const incident = (detail.cluster.edges ?? []).filter(
      (edge) => edge.participant_a === participantID || edge.participant_b === participantID
    );
    if (incident.length === 0) {
      confirmingParticipantID = null;
      return;
    }
    unlinking = true;
    unlinkError = null;
    try {
      // The edge set was captured above, and the whole sequence runs to
      // completion even if the user navigates away mid-loop: stopping after
      // the current edge would persist a half-split cluster (some aliases
      // detached, others still merged) with no error anywhere. Navigating
      // away only makes the outcomes stale for THIS component's state
      // (which the $effect above already reset for whoever is open now), so
      // staleness suppresses the UI writes, never the mutations.
      for (const edge of incident) {
        const outcome = await onUnlinkParticipants(edge.participant_a, edge.participant_b);
        const stale = currentPersonID() !== id;
        if (!stale) applyOutcome(outcome, 'unlink', edge.participant_a, edge.participant_b);
        if (!outcome.ok) {
          // A genuine failure still ends the sequence — every call is
          // idempotent, so re-confirming retries the remaining edges safely.
          if (!stale) unlinkError = outcome.message;
          return;
        }
      }
      if (currentPersonID() === id) confirmingParticipantID = null;
    } finally {
      unlinking = false;
    }
  }
</script>

{#snippet memberActions(participantIDs: number[], label: string)}
  {@const confirming = participantIDs.find((id) => id === confirmingParticipantID)}
  {#if confirming !== undefined}
    <span class="chip-confirm" role="group" aria-label={`Confirm unlinking ${participantIDs.length > 1 ? memberLabel(confirming) : label}`}>
      <span>Not the same person?</span>
      <Button
        label="Unlink"
        tone="danger"
        surface="solid"
        size="sm"
        disabled={unlinking}
        onclick={() => void confirmUnlink(confirming)}
      />
      <Button label="Cancel" surface="soft" size="sm" disabled={unlinking} onclick={cancelUnlink} />
    </span>
  {:else}
    <Menu align="end">
      <MenuTrigger class="row-menu-trigger" ariaLabel={`Actions for ${label}`} title="More actions">
        <EllipsisIcon size="14" aria-hidden="true" />
      </MenuTrigger>
      <MenuContent ariaLabel={`Actions for ${label}`}>
        {#each participantIDs as participantID (participantID)}
          <MenuItem tone="danger" onselect={() => startUnlink(participantID)}>
            {participantIDs.length > 1 ? `Unlink ${memberLabel(participantID)}` : 'Unlink'}
          </MenuItem>
        {/each}
      </MenuContent>
    </Menu>
  {/if}
{/snippet}

<header class="relationship-header" class:has-detail={Boolean(detail)} aria-label="Relationship detail">
  {#if !detail}
    <p class="header-empty" role="status">
      {loading ? 'Loading relationship…' : 'Select a person or domain to see your shared history.'}
    </p>
  {:else}
    <div class="title-row">
      <IdentityAvatar
        label={displayLabel(detail)}
        seed={isPersonDetail(detail) ? `cluster:${detail.id}` : `domain:${detail.domain}`}
        shape={isPersonDetail(detail) ? 'person' : 'domain'}
        size={36}
      />
      <h2>{displayLabel(detail)}</h2>
      <div class="actions">
        <SegmentedControl
          ariaLabel="Relationship view"
          value={filesOpen ? 'files' : 'messages'}
          options={[
            { value: 'messages', label: 'Messages' },
            { value: 'files', label: `Files ${detail.file_count.toLocaleString()}` }
          ]}
          onchange={(value) => onFilesToggle(value === 'files')}
        />
        {#if isPersonDetail(detail)}
          {#if detail.profile?.id && onOpenDirectoryPerson}
            <Button
              label="Contact record"
              ariaLabel={`Open contact record for ${displayLabel(detail)}`}
              surface="outline"
              onclick={() => onOpenDirectoryPerson(detail.profile!.id)}
            />
          {:else if !detail.profile?.id && onPromotePerson}
            <Button
              label="Save to Directory"
              tone="workflow"
              disabled={promoting}
              onclick={() => void promote()}
            />
          {/if}
          <Button
            label="Same person…"
            ariaLabel="Same person…"
            surface="outline"
            onclick={openLinkDialog}
          />
        {/if}
      </div>
    </div>
    {#if staleBanner === 'identity_cache_stale'}
      <section class="named-state" role="alert">
        <span>{STALE_CACHE_MESSAGE}</span>
        <Button label="Retry" surface="outline" size="sm" disabled={retrying} onclick={() => void retryRefresh()} />
      </section>
    {/if}
    {#if promotionFailure}
      <section class="named-state" role="alert">
        <span>
          {promotionFailure.message}
          {#if promotionFailure.code === 'person_binding_conflict'} This participant already belongs to another Directory person; resolve that binding before saving it.{/if}
        </span>
      </section>
    {/if}
    <p class="counts" data-mono>
      {detail.activity_count.toLocaleString()} items · {detail.file_count.toLocaleString()} files ·
      {formatDate(detail.first_at)} – {formatDate(detail.last_at)}
      {#if !isPersonDetail(detail)}
        · {detail.person_count.toLocaleString()} people
      {/if}
    </p>
    {#if otherIdentities.length > 0}
      <p class="sibling-note" role="note">
        <span>This person's history continues under other identities:</span>
        {#each otherIdentities as sibling (sibling.target)}
          <Button
            size="sm"
            surface="soft"
            label={`${sibling.label} · ${sibling.activityCount.toLocaleString()} items`}
            ariaLabel={`Open identity ${sibling.label}`}
            onclick={() => onOpenSibling?.(sibling.target)}
          />
        {/each}
      </p>
    {/if}
    {#if isPersonDetail(detail) && detail.profile?.id}
      <AttributeSummary
        groups={attributeGroups}
        onEdit={onOpenDirectoryPerson ? () => onOpenDirectoryPerson(detail.profile!.id) : undefined}
      />
    {/if}
    {#if isPersonDetail(detail) && (reachEntries.length > 0 || bareMembers.length > 0)}
      {#key detail.id}
      <div class="reach">
        <PersonReachBlock entries={reachEntries} ariaLabel="Contact methods" {onAnnounce}>
          {#snippet actions(entry)}
            {@const members = otherMembersOf(entry)}
            {#if members.length > 0}
              {@render memberActions(members, entry.label)}
            {/if}
          {/snippet}
        </PersonReachBlock>
        {#each bareMembers as memberID (memberID)}
          {@const member = memberFor(memberID)}
          {@const text = identityChipText(undefined, member, edgesFor(memberID))}
          {@const label = `profile ${member?.display_name ? `${text.title} (${memberID})` : memberID}`}
          <div class="linked-profile" aria-label={`Linked ${label}`}>
            <span class="linked-profile-text"><strong>{text.title}</strong> <small>{text.subtitle}</small></span>
            {@render memberActions([memberID], label)}
          </div>
        {/each}
        {#if unlinkError}
          <p class="unlink-error" role="alert">{unlinkError}</p>
        {/if}
      </div>
      {/key}
    {/if}
    {#if activeDialog?.kind === 'link' && isPersonDetail(detail)}
      <LinkIdentityDialog
        {client}
        excludeID={detail.id}
        personLabel={detail.display_label}
        onConfirm={confirmLink}
        onMergeRequired={resolveMerge}
        onClose={() => (activeDialog = undefined)}
      />
    {:else if activeDialog?.kind === 'merge'}
      <PersonBindingConflictModal
        {client}
        conflict={activeDialog.conflict}
        onOpenProfile={(personID) => onOpenDirectoryPerson?.(personID)}
        onSuccess={completeMerge}
        onClose={() => (activeDialog = undefined)}
      />
    {/if}
  {/if}
</header>

<style>
  /* Spacing sits on the 4px grid: 8px between header lines, 16px of air
   * before the hairline that closes the header. */
  .relationship-header {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .relationship-header.has-detail {
    padding-bottom: var(--space-6);
    border-bottom: 1px solid var(--border-muted);
  }

  .header-empty {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .title-row {
    display: flex;
    align-items: center;
    gap: var(--space-4);
  }

  .title-row h2 {
    flex: 1;
  }

  h2 {
    overflow: hidden;
    margin: 0;
    font-size: var(--font-size-xl);
    font-weight: 650;
    line-height: 1.2;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .actions {
    display: flex;
    flex: none;
    gap: var(--space-2);
  }

  .counts {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .sibling-note {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .named-state {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    border: 1px solid var(--edge);
    border-radius: var(--radius-md);
    padding: var(--space-3);
    font-size: var(--font-size-sm);
  }

  .reach {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }

  .linked-profile {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-1) 0;
    font-size: var(--font-size-sm);
  }

  .linked-profile-text {
    flex: 1;
    min-width: 0;
  }

  .linked-profile small {
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .reach :global(.row-menu-trigger) {
    display: inline-flex;
    width: 26px;
    height: 26px;
    align-items: center;
    justify-content: center;
    border: none;
    border-radius: var(--radius-sm);
    padding: 0;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }

  .reach :global(.row-menu-trigger:hover),
  .reach :global(.row-menu-trigger[aria-expanded="true"]) {
    background: var(--bg-surface-hover);
    color: var(--text-secondary);
  }

  .chip-confirm {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-1);
    font-size: var(--font-size-2xs);
  }

  .unlink-error {
    margin: 0;
    color: var(--text-danger);
    font-size: var(--font-size-xs);
  }
</style>
