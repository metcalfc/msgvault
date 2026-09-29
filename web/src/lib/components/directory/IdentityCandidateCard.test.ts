import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import type { IdentityMatchCandidate } from '../../directory/review-controller.svelte';
import IdentityCandidateCard from './IdentityCandidateCard.svelte';

function completeCandidate(state = 'candidate'): IdentityMatchCandidate {
  return {
    id: 17,
    left_id: 170,
    left_kind: 'beeper_user',
    right_id: 171,
    right_kind: 'participant',
    basis: 'stable_provider_id',
    normalized_value: 'synthetic@example.com',
    service_slug: 'synthetic-chat',
    scope_kind: 'workspace',
    scope_value: 'example-space',
    confidence: 0.82,
    source: 'synthetic_import',
    source_ref: 'fixture-17',
    state,
    created_at: '2026-08-01T10:00:00Z',
    updated_at: '2026-08-02T11:00:00Z',
    decided_at: '2026-08-03T12:00:00Z',
    decided_by: 'reviewer@example.com',
    notes: 'Verified from a synthetic account export.',
    evidence: [
      {
        id: 31,
        candidate_id: 17,
        evidence_kind: 'provider_identifier',
        source: 'synthetic_import',
        evidence_ref: 'evidence-31',
        detail: 'Both endpoints expose the same provider identifier.',
        created_at: '2026-08-01T10:05:00Z'
      },
      {
        id: 32,
        candidate_id: 17,
        evidence_kind: 'curated_observation',
        source: 'manual_review',
        created_at: '2026-08-01T10:06:00Z'
      }
    ]
  };
}

describe('IdentityCandidateCard', () => {
  it('renders every candidate field and each evidence record as labelled evidence', () => {
    render(IdentityCandidateCard, {
      candidate: completeCandidate(), pending: false, onAccept: vi.fn(), onReject: vi.fn()
    });

    const card = screen.getByRole('article', { name: 'Identity match 17' });
    expect(within(card).getByRole('region', { name: 'Candidate endpoints for identity match 17' })).toBeDefined();
    expect(within(card).getByText('beeper_user / 170')).toBeDefined();
    expect(within(card).getByText('participant / 171')).toBeDefined();
    expect(card.textContent).toContain('stable_provider_id');
    expect(card.textContent).toContain('synthetic@example.com');
    expect(card.textContent).toContain('synthetic-chat');
    expect(card.textContent).toContain('workspace / example-space');
    expect(card.textContent).toContain('0.82');
    expect(card.textContent).toContain('synthetic_import');
    expect(card.textContent).toContain('fixture-17');
    expect(card.textContent).toContain('2026-08-01T10:00:00Z');
    expect(card.textContent).toContain('2026-08-02T11:00:00Z');
    expect(card.textContent).toContain('2026-08-03T12:00:00Z');
    expect(card.textContent).toContain('reviewer@example.com');
    expect(card.textContent).toContain('Verified from a synthetic account export.');

    const evidence = within(card).getByRole('list', { name: 'Evidence for identity match 17' });
    const evidenceItems = within(evidence).getAllByRole('listitem');
    expect(evidenceItems).toHaveLength(2);
    const firstEvidenceTerms = within(evidenceItems[0]!).getAllByRole('term');
    expect(firstEvidenceTerms.map((term) => term.textContent)).toEqual([
      'Evidence ID', 'Candidate ID', 'Source', 'Reference', 'Detail', 'Recorded'
    ]);
    expect(firstEvidenceTerms.map((term) => term.nextElementSibling?.textContent)).toEqual([
      '31', '17', 'synthetic_import', 'evidence-31',
      'Both endpoints expose the same provider identifier.', '2026-08-01T10:05:00Z'
    ]);
    const secondEvidenceTerms = within(evidenceItems[1]!).getAllByRole('term');
    expect(secondEvidenceTerms.map((term) => term.textContent)).toEqual([
      'Evidence ID', 'Candidate ID', 'Source', 'Recorded'
    ]);
    expect(secondEvidenceTerms.map((term) => term.nextElementSibling?.textContent)).toEqual([
      '32', '17', 'manual_review', '2026-08-01T10:06:00Z'
    ]);
    expect(evidence.textContent).toContain('provider_identifier');
    expect(evidence.textContent).toContain('Both endpoints expose the same provider identifier.');
    expect(evidence.textContent).toContain('evidence-31');
    expect(evidence.textContent).toContain('curated_observation');
    expect(evidence.textContent).toContain('2026-08-01T10:06:00Z');
  });

  it('shows an explicit no-evidence message and hides decisions for reviewed rows', () => {
    const reviewed = { ...completeCandidate('accepted'), evidence: [] };
    render(IdentityCandidateCard, {
      candidate: reviewed, pending: false, onAccept: vi.fn(), onReject: vi.fn()
    });

    expect(screen.getByText('No evidence supplied.')).toBeDefined();
    expect(screen.queryByRole('button', { name: 'Link identities' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Keep separate' })).toBeNull();
  });

  it('offers both explicit candidate decisions and disables duplicates while pending', async () => {
    const onAccept = vi.fn();
    const onReject = vi.fn();
    const view = render(IdentityCandidateCard, {
      candidate: completeCandidate(), pending: false, onAccept, onReject
    });

    await fireEvent.click(screen.getByRole('button', { name: 'Link identities' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Keep separate' }));
    expect(onAccept).toHaveBeenCalledOnce();
    expect(onReject).toHaveBeenCalledOnce();

    await view.rerender({ candidate: completeCandidate(), pending: true, onAccept, onReject });
    expect(screen.getByRole('button', { name: 'Link identities' })).toHaveProperty('disabled', true);
    expect(screen.getByRole('button', { name: 'Keep separate' })).toHaveProperty('disabled', true);
  });

  it('names both endpoints of a contact match and explains the bind', () => {
    const contactMatch: IdentityMatchCandidate = {
      ...completeCandidate(), left_kind: 'participant', left_id: 40, right_kind: 'person', right_id: 41,
      basis: 'email', evidence: []
    };
    render(IdentityCandidateCard, {
      candidate: contactMatch,
      pending: false,
      left: { kind: 'participant', id: 40, found: true, display_name: 'Ada Sender', addresses: ['ada@example.test'] },
      right: {
        kind: 'person', id: 41, found: true, display_name: 'Ada Contact',
        addresses: ['ada@example.test', '+15550100100']
      },
      contactMatch: { candidate_id: 17, classification: 'bind', cluster_person_ids: [] },
      onAccept: vi.fn(),
      onReject: vi.fn()
    });

    const card = screen.getByRole('article', { name: 'Identity match 17' });
    const endpoints = within(card).getByRole('region', { name: 'Candidate endpoints for identity match 17' });
    expect(within(endpoints).getByText('Archive identity')).toBeDefined();
    expect(within(endpoints).getByText('Ada Sender')).toBeDefined();
    expect(within(endpoints).getByText('Person profile')).toBeDefined();
    expect(within(endpoints).getByText('Ada Contact')).toBeDefined();
    expect(within(endpoints).getByText('+15550100100')).toBeDefined();
    expect(card.textContent).toContain('Accepting links this archive identity to the profile.');
    expect(screen.getByRole('button', { name: 'Link identities' })).toHaveProperty('disabled', false);
  });

  it('disables linking when a merge the match needs would be refused', () => {
    render(IdentityCandidateCard, {
      candidate: { ...completeCandidate(), left_kind: 'participant', right_kind: 'person' },
      pending: false,
      contactMatch: {
        candidate_id: 17, classification: 'merge', blocked_reason: 'published', cluster_person_ids: [9]
      },
      onAccept: vi.fn(),
      onReject: vi.fn()
    });

    expect(screen.getByText(/Blocked: a profile is published to CardDAV/)).toBeDefined();
    expect(screen.getByRole('button', { name: 'Link identities' })).toHaveProperty('disabled', true);
    expect(screen.getByRole('button', { name: 'Keep separate' })).toHaveProperty('disabled', false);
  });
});
