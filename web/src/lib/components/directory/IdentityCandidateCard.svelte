<script lang="ts">
  import { Button, Card, Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@kenn-io/kit-ui';
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';

  import type {
    ContactMatchStatus,
    IdentityMatchCandidate,
    IdentityMatchEndpointSummary
  } from '../../directory/review-controller.svelte';
  import {
    contactMatchBlockedMessage,
    contactMatchSummary,
    endpointLabel,
    endpointRole,
    sharedMailboxReason
  } from '../../directory/identity-endpoints';
  import { addressLinkInput } from '../../links/contact-links';
  import type { EntityNames } from '../../names/entity-names.svelte';
  import { NOT_A_PERSON_CHOICES, type NotAPersonKind } from '../../people/correspondent-kind';
  import LinkedValue from '../common/LinkedValue.svelte';

  interface Props {
    candidate: IdentityMatchCandidate;
    names: EntityNames;
    pending: boolean;
    left?: IdentityMatchEndpointSummary;
    right?: IdentityMatchEndpointSummary;
    contactMatch?: ContactMatchStatus;
    onAccept: () => void;
    onReject: () => void;
    /** Marks one of the candidate's archive identities as not a person. */
    onNotAPerson?: (participantID: number, kind: NotAPersonKind) => void;
    /** Says a shared-looking address is a person after all. */
    onIsPerson?: (participantID: number) => void;
  }

  let {
    candidate, names, pending, left = undefined, right = undefined, contactMatch = undefined, onAccept, onReject,
    onNotAPerson = undefined, onIsPerson = undefined
  }: Props = $props();
  const headingID = $derived(`identity-match-${candidate.id}-heading`);
  const evidence = $derived(candidate.evidence ?? []);
  const leftLabel = $derived(endpointLabel(names, candidate.left_kind, candidate.left_id, left));
  const rightLabel = $derived(endpointLabel(names, candidate.right_kind, candidate.right_id, right));
  const blockedMessage = $derived(contactMatchBlockedMessage(contactMatch));
  const endpoints = $derived([
    { side: 'left', kind: candidate.left_kind, id: candidate.left_id, label: leftLabel, summary: left },
    { side: 'right', kind: candidate.right_kind, id: candidate.right_id, label: rightLabel, summary: right }
  ]);
  const participants = $derived(endpoints.filter((endpoint) => endpoint.kind === 'participant'));
  const sharedMailbox = $derived(contactMatch?.classification === 'shared_mailbox' ? contactMatch.shared_mailbox : undefined);
  const open = $derived(candidate.state === 'candidate' || candidate.state === 'conflict');
</script>

<Card level="default" padding="md">
  <article id={`identity-match-${candidate.id}-card`} class="candidate" aria-labelledby={headingID} aria-busy={pending} tabindex="-1">
    <header>
      <div>
        <p class="state">{candidate.state}</p>
        <h3 id={headingID}>Identity match {candidate.id}</h3>
        <p class="names">{leftLabel} <span aria-hidden="true">↔</span><span class="kit-sr-only">and</span> {rightLabel}</p>
      </div>
      {#if pending}<span class="pending">Decision pending…</span>{/if}
    </header>

    <section class="endpoints" aria-label={`Candidate endpoints for identity match ${candidate.id}`}>
      {#each endpoints as endpoint (endpoint.side)}
        <Card level="inset" padding="sm">
          <span>{endpointRole(endpoint.kind)}</span>
          <strong>{endpoint.label}</strong>
          {#each endpoint.summary?.addresses ?? [] as address (address)}
            {#if address !== endpoint.label}<span class="address"><LinkedValue input={addressLinkInput(address)} text={address} /></span>{/if}
          {/each}
          {#if endpoint.kind !== 'person' && endpoint.summary?.person_id !== undefined}
            <span class="owner">Profile: {names.name('person', endpoint.summary.person_id, endpoint.summary.person_display_name)}</span>
          {/if}
        </Card>
      {/each}
    </section>

    {#if sharedMailbox}
      <div class="shared-hint" role="note">
        <strong>Looks like a shared mailbox</strong>
        <span>{sharedMailboxReason(sharedMailbox)} Nothing is linked through it. Mark it as not a person below, or say it is a person to link it.</span>
        {#if onIsPerson && candidate.left_kind === 'participant'}
          <div><Button size="sm" surface="soft" label="This is a person" disabled={pending}
            ariaLabel={`${leftLabel} is a person`} onclick={() => onIsPerson?.(candidate.left_id)} /></div>
        {/if}
      </div>
    {:else if contactMatch}
      <p class="match-summary" class:blocked={!!blockedMessage}>
        {blockedMessage ?? contactMatchSummary(contactMatch)}
      </p>
    {/if}

    <dl class="metadata">
      <div><dt>Basis</dt><dd>{candidate.basis}</dd></div>
      {#if candidate.normalized_value}<div><dt>Normalized value</dt><dd>{candidate.normalized_value}</dd></div>{/if}
      {#if candidate.service_slug}<div><dt>Service</dt><dd>{candidate.service_slug}</dd></div>{/if}
      {#if candidate.scope_kind || candidate.scope_value}
        <div><dt>Scope</dt><dd>{[candidate.scope_kind, candidate.scope_value].filter(Boolean).join(' / ')}</dd></div>
      {/if}
      {#if candidate.confidence !== undefined}<div><dt>Confidence</dt><dd>{candidate.confidence}</dd></div>{/if}
      <div><dt>Source</dt><dd>{candidate.source}</dd></div>
      {#if candidate.source_ref}<div><dt>Source reference</dt><dd>{candidate.source_ref}</dd></div>{/if}
      <div><dt>Created</dt><dd><time datetime={candidate.created_at}>{candidate.created_at}</time></dd></div>
      <div><dt>Updated</dt><dd><time datetime={candidate.updated_at}>{candidate.updated_at}</time></dd></div>
      {#if candidate.decided_at}<div><dt>Decided</dt><dd><time datetime={candidate.decided_at}>{candidate.decided_at}</time></dd></div>{/if}
      {#if candidate.decided_by}<div><dt>Decision actor</dt><dd>{candidate.decided_by}</dd></div>{/if}
      {#if candidate.notes}<div><dt>Decision notes</dt><dd>{candidate.notes}</dd></div>{/if}
    </dl>

    <section class="evidence-section">
      <h4>Evidence</h4>
      {#if evidence.length > 0}
        <ul aria-label={`Evidence for identity match ${candidate.id}`}>
          {#each evidence as item (item.id)}
            <li>
              <strong>{item.evidence_kind}</strong>
              <dl>
                <div><dt>Evidence ID</dt><dd>{item.id}</dd></div>
                <div><dt>Candidate ID</dt><dd>{item.candidate_id}</dd></div>
                <div><dt>Source</dt><dd>{item.source}</dd></div>
                {#if item.evidence_ref}<div><dt>Reference</dt><dd>{item.evidence_ref}</dd></div>{/if}
                {#if item.detail}<div><dt>Detail</dt><dd>{item.detail}</dd></div>{/if}
                <div><dt>Recorded</dt><dd><time datetime={item.created_at}>{item.created_at}</time></dd></div>
              </dl>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty-evidence">No evidence supplied.</p>
      {/if}
    </section>

    {#if open && (candidate.state === 'candidate' || (onNotAPerson && participants.length > 0))}
      <div class="actions">
        {#if onNotAPerson && participants.length > 0}
          <Menu align="end">
            <MenuTrigger class={sharedMailbox ? 'not-a-person-trigger prominent' : 'not-a-person-trigger'}
              ariaLabel={`Not a person: identity match ${candidate.id}`} disabled={pending}>
              Not a person <ChevronDownIcon size={14} aria-hidden="true" />
            </MenuTrigger>
            <MenuContent ariaLabel="Not a person">
              {#each participants as participant, index (participant.id)}
                {#if index > 0}<MenuSeparator />{/if}
                {#each NOT_A_PERSON_CHOICES as choice (choice.kind)}
                  <MenuItem onselect={() => onNotAPerson?.(participant.id, choice.kind)}
                    textValue={choice.action}>
                    {participants.length > 1 ? `${choice.action}: ${participant.label}` : choice.action}
                  </MenuItem>
                {/each}
              {/each}
            </MenuContent>
          </Menu>
        {/if}
        {#if candidate.state === 'candidate'}
          <Button label="Keep separate" size="sm" disabled={pending} onclick={onReject} />
          <Button label="Link identities" size="sm" tone="info" surface="solid"
            disabled={pending || !!blockedMessage || !!sharedMailbox} onclick={onAccept} />
        {/if}
      </div>
    {/if}
  </article>
</Card>

<style>
  .candidate { display: grid; gap: var(--space-4); }
  header { display: flex; align-items: start; justify-content: space-between; gap: var(--space-4); }
  header > div { display: grid; gap: var(--space-1); }
  h3, h4, p, dl, dd, ul { margin: 0; }
  h3 { font-size: var(--font-size-lg); color: var(--text-primary); }
  h4 { font-size: var(--font-size-sm); color: var(--text-secondary); }
  .state { color: var(--text-muted); font-size: var(--font-size-xs); font-weight: var(--font-weight-semibold, 600); text-transform: uppercase; letter-spacing: 0.04em; }
  .pending { color: var(--text-muted); font-size: var(--font-size-sm); }
  .endpoints { display: grid; grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr)); gap: var(--space-3); }
  .endpoints :global(.kit-card__body) { display: grid; gap: var(--space-1); }
  .endpoints span, dt { color: var(--text-muted); font-size: var(--font-size-xs); }
  .endpoints strong, dd { color: var(--text-secondary); overflow-wrap: anywhere; }
  .endpoints .address, .endpoints .owner { color: var(--text-secondary); font-size: var(--font-size-sm); overflow-wrap: anywhere; }
  .names { color: var(--text-primary); font-weight: var(--font-weight-medium, 500); overflow-wrap: anywhere; }
  .match-summary { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .match-summary.blocked { color: var(--text-danger); }
  .metadata { display: grid; grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr)); gap: var(--space-3) var(--space-5); }
  .metadata > div, li dl > div { display: grid; gap: var(--space-1); }
  .evidence-section { display: grid; gap: var(--space-2); }
  ul { display: grid; gap: var(--space-2); padding: 0; list-style: none; }
  li { display: grid; gap: var(--space-2); padding: var(--space-3); border-left: 2px solid var(--border-default); background: var(--bg-inset); }
  li dl { display: grid; grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr)); gap: var(--space-2) var(--space-4); }
  .empty-evidence { color: var(--text-muted); font-size: var(--font-size-sm); }
  .actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--space-2); width: 100%; }
  .shared-hint {
    display: grid;
    gap: var(--space-1);
    padding: var(--space-3);
    border-left: 2px solid var(--accent-amber);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }
  .shared-hint strong { color: var(--text-primary); font-size: var(--font-size-md); }
  .actions :global(.not-a-person-trigger) {
    gap: var(--space-1);
    min-height: 24px;
    padding: 0 var(--space-3);
    border: var(--border-width) solid var(--border-default);
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
  }
  .actions :global(.not-a-person-trigger:hover) { color: var(--text-primary); }
  .actions :global(.not-a-person-trigger.prominent) {
    border-color: var(--accent-amber);
    color: var(--text-primary);
    font-weight: var(--font-weight-medium, 500);
  }
</style>
