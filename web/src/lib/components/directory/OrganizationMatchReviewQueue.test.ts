import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { OrganizationReviewController } from '../../directory/organization-review-controller.svelte';
import OrganizationMatchReviewQueue from './OrganizationMatchReviewQueue.svelte';

const review = {
  id: 4,
  organization_id: 40,
  organization_name: 'Example Labs',
  organization_domain: 'labs.example',
  proposed_name: 'Example Labs Europe',
  proposed_domain: 'eu.labs.example',
  proposed_organization_id: 41,
  probability: 0.7,
  model: 'jev-1.13.0',
  created_at: '2026-09-01T00:00:00Z',
};

describe('OrganizationMatchReviewQueue', () => {
  it('shows both organizations and the probability, and accepts on request', async () => {
    const posts: string[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      if (request.method === 'POST') {
        posts.push(new URL(request.url).pathname);
        return Response.json({
          review_id: 4, decision: 'accepted', organization_id: 40, merged_organization_id: 41,
        });
      }
      return Response.json({ reviews: [review], limit: 50 });
    });
    const controller = new OrganizationReviewController(createAPIClient(fetchFn));
    render(OrganizationMatchReviewQueue, { controller });

    const card = await screen.findByRole('article', { name: 'Example Labs Europe' });
    expect(card.textContent).toContain('Example Labs Europe (eu.labs.example)');
    expect(card.textContent).toContain('Example Labs (labs.example)');
    expect(card.textContent).toContain('70%');
    expect(card.textContent).toContain('Merge the separate organization');

    await fireEvent.click(within(card).getByRole('button', { name: 'Same organization' }));
    expect(await screen.findByText('Example Labs Europe now resolves to Example Labs.')).toBeDefined();
    expect(posts).toEqual(['/api/v1/organization-match-reviews/4/accept']);
    expect(await screen.findByText('No organization matches to confirm.')).toBeDefined();
  });

  it('rejects on request and says the pair will not come back', async () => {
    const posts: string[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      if (request.method === 'POST') {
        posts.push(new URL(request.url).pathname);
        return Response.json({ review_id: 4, decision: 'rejected', organization_id: 40 });
      }
      return Response.json({ reviews: [{ ...review, proposed_organization_id: undefined }], limit: 50 });
    });
    const controller = new OrganizationReviewController(createAPIClient(fetchFn));
    render(OrganizationMatchReviewQueue, { controller });

    const card = await screen.findByRole('article', { name: 'Example Labs Europe' });
    expect(card.textContent).toContain('Add the name as an alias');
    await fireEvent.click(within(card).getByRole('button', { name: 'Different organization' }));
    expect(await screen.findByText(/stays separate from Example Labs/)).toBeDefined();
    expect(posts).toEqual(['/api/v1/organization-match-reviews/4/reject']);
  });
});
