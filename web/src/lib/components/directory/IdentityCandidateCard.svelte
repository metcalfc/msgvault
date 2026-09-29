<script lang="ts">
  import { Button, Card } from '@kenn-io/kit-ui';

  import type {
    ContactMatchStatus,
    IdentityMatchCandidate,
    IdentityMatchEndpointSummary
  } from '../../directory/review-controller.svelte';
  import {
    contactMatchBlockedMessage,
    contactMatchSummary,
    endpointLabel,
    endpointRole
  } from '../../directory/identity-endpoints';

  interface Props {
    candidate: IdentityMatchCandidate;
    pending: boolean;
    left?: IdentityMatchEndpointSummary;
    right?: IdentityMatchEndpointSummary;
    contactMatch?: ContactMatchStatus;
    onAccept: () => void;
    onReject: () => void;
  }

  let { candidate, pending, left = undefined, right = undefined, contactMatch = undefined, onAccept, onReject }: Props = $props();
  const headingID = $derived(`identity-match-${candidate.id}-heading`);
  const evidence = $derived(candidate.evidence ?? []);
  const leftLabel = $derived(endpointLabel(candidate.left_kind, candidate.left_id, left));
  const rightLabel = $derived(endpointLabel(candidate.right_kind, candidate.right_id, right));
  const blockedMessage = $derived(contactMatchBlockedMessage(contactMatch));
  const endpoints = $derived([
    { side: 'left', kind: candidate.left_kind, id: candidate.left_id, label: leftLabel, summary: left },
    { side: 'right', kind: candidate.right_kind, id: candidate.right_id, label: rightLabel, summary: right }
  ]);
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
            {#if address !== endpoint.label}<span class="address">{address}</span>{/if}
          {/each}
          {#if endpoint.kind !== 'person' && endpoint.summary?.person_id !== undefined}
            <span class="owner">Profile: {endpoint.summary.person_display_name?.trim() || `Person ${endpoint.summary.person_id}`}</span>
          {/if}
          <span class="reference">{endpoint.kind} / {endpoint.id}</span>
        </Card>
      {/each}
    </section>

    {#if contactMatch}
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

    {#if candidate.state === 'candidate'}
      <div class="actions">
        <Button label="Keep separate" size="sm" disabled={pending} onclick={onReject} />
        <Button label="Link identities" size="sm" tone="info" surface="solid" disabled={pending || !!blockedMessage} onclick={onAccept} />
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
  .endpoints .reference { color: var(--text-muted); font-size: var(--font-size-xs); }
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
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); width: 100%; }
</style>
