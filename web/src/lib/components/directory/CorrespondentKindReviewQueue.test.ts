import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { CorrespondentReviewController } from '../../directory/correspondent-review-controller.svelte';
import CorrespondentKindReviewQueue from './CorrespondentKindReviewQueue.svelte';

const unclear = {
  canonical_id: 41,
  member_ids: [41],
  kind: 'unclear',
  source: 'jev',
  display_name: 'Front Desk',
  addresses: ['frontdesk@example.com'],
  actor: 'jev:jev-1.13.0',
  classified_at: '2026-09-01T00:00:00Z',
  confidence: 0.45,
  probabilities: { individual_person: 0.45, shared_role_or_team_mailbox: 0.4, unclear: 0.15 },
  person: { id: 90, display_name: 'Front Desk Contact', revision: 2, only_this_cluster: true }
};

const unnamed = {
  canonical_id: 42,
  member_ids: [42],
  kind: 'unclear',
  source: 'jev',
  addresses: ['desk-two@example.com'],
  probabilities: { individual_person: 0.3, unclear: 0.7 }
};

describe('CorrespondentKindReviewQueue', () => {
  it('lists unclear judgments by name with their probabilities and records the decision', async () => {
    const puts: { path: string; body: unknown }[] = [];
    const lists: string[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      const url = new URL(request.url);
      if (request.method === 'PUT') {
        puts.push({ path: url.pathname, body: await request.json() });
        return Response.json({ record: { ...unclear, kind: 'shared_mailbox', source: 'user' }, resolved_candidates: 0, restored_candidates: 0, organization_created: false });
      }
      lists.push(url.search);
      return Response.json({ records: [unclear, unnamed] });
    });
    const onOpenPerson = vi.fn();
    const controller = new CorrespondentReviewController(createAPIClient(fetchFn));
    render(CorrespondentKindReviewQueue, { controller, onOpenPerson });

    const card = await screen.findByRole('article', { name: 'Front Desk' });
    expect(lists).toEqual(['?kind=unclear']);
    expect(card.textContent).toContain('45%');
    expect(card.textContent).toContain('Shared or team mailbox');
    expect(card.textContent).toContain('frontdesk@example.com');
    expect(card.textContent).not.toContain('41');
    expect(await screen.findByRole('article', { name: 'desk-two@example.com' })).toBeDefined();

    await fireEvent.click(within(card).getByRole('button', { name: 'Open Front Desk Contact' }));
    expect(onOpenPerson).toHaveBeenCalledWith(90);

    await fireEvent.click(within(card).getByRole('button', { name: 'Mark Front Desk as shared mailbox' }));
    expect(await screen.findByText('Front Desk marked as shared mailbox.')).toBeDefined();
    expect(puts).toEqual([{ path: '/api/v1/identity/correspondent-kinds/41', body: { kind: 'shared_mailbox' } }]);
    expect(screen.queryByRole('article', { name: 'Front Desk' })).toBeNull();
  });
});
