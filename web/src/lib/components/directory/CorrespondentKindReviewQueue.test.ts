import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { CorrespondentReviewController } from '../../directory/correspondent-review-controller.svelte';
import CorrespondentKindReviewQueue from './CorrespondentKindReviewQueue.svelte';
import { focusAndClick } from '../../../test/kit-ui';

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

    await focusAndClick(within(card).getByRole('button', { name: 'Mark Front Desk as shared mailbox' }));
    expect(await screen.findByText('Front Desk marked as shared mailbox.')).toBeDefined();
    expect(puts).toEqual([{ path: '/api/v1/identity/correspondent-kinds/41', body: { kind: 'shared_mailbox' } }]);
    expect(screen.queryByRole('article', { name: 'Front Desk' })).toBeNull();
    // The queue stays put and moves on to the next record.
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('article', { name: 'desk-two@example.com' })));
    expect(onOpenPerson).toHaveBeenCalledOnce();
  });

  it.each([
    { record: unclear, name: 'Front Desk', organization: 'Front Desk' },
    { record: unnamed, name: 'desk-two@example.com', organization: 'example.com' }
  ])('marks $name as an organization named after it', async ({ record, name, organization }) => {
    const puts: unknown[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      if (request.method === 'PUT') {
        puts.push(await request.json());
        return Response.json({
          record: { ...record, kind: 'organization', source: 'user', organization_id: 7, organization_name: organization },
          resolved_candidates: 0, restored_candidates: 0, organization_created: true
        });
      }
      return Response.json({ records: [record] });
    });
    const controller = new CorrespondentReviewController(createAPIClient(fetchFn));
    render(CorrespondentKindReviewQueue, { controller });

    const card = await screen.findByRole('article', { name });
    await focusAndClick(within(card).getByRole('button', { name: `Mark ${name} as organization` }));

    expect(await screen.findByText(`${name} marked as organization (${organization}).`)).toBeDefined();
    expect(puts).toEqual([{ kind: 'organization', organization_name: organization }]);
  });
});
