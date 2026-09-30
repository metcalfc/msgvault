import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { EnrichmentReviewController } from '../../directory/enrichment-review-controller.svelte';
import EnrichmentIdentityReviewQueue from './EnrichmentIdentityReviewQueue.svelte';

const review = {
  attempt_id: 7,
  person_id: 70,
  person_display_name: 'Synthetic Reviewee',
  provider_name: 'exa',
  provider_kind: 'exa',
  exact_class: 'current_company',
  name_compatible: 0.7,
  company_same: 0.99,
  name_conflict: 0.1,
  model: 'jev-1.13.0',
  judged_at: '2026-09-01T00:00:00Z',
  provider_person_id_known: true,
  returned: {
    name: 'S. Reviewee',
    current_roles: [{ title: 'Engineer', company: 'Example Labs' }],
    profile_url_host: 'profiles.example.test',
  },
  claims: [{ target: 'ask_me_about', value: 'sailing' }],
};

describe('EnrichmentIdentityReviewQueue', () => {
  it('shows the person, returned identity, judgment, and claims, and confirms on request', async () => {
    const posts: string[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      if (request.method === 'POST') {
        posts.push(new URL(request.url).pathname);
        return Response.json({
          attempt_id: 7, person_id: 70, decision: 'rejected', reason: 'user_rejected',
          attempt_state: 'identity_rejected', projections: 0, provider_identities_attached: 0, negatives: 1,
        });
      }
      return Response.json({ reviews: [review], limit: 50 });
    });
    const onOpenPerson = vi.fn();
    const controller = new EnrichmentReviewController(createAPIClient(fetchFn));
    render(EnrichmentIdentityReviewQueue, { controller, onOpenPerson });

    const card = await screen.findByRole('article', { name: 'Synthetic Reviewee' });
    const returned = within(card).getByRole('region', { name: 'Returned identity for attempt 7' });
    expect(returned.textContent).toContain('S. Reviewee');
    expect(returned.textContent).toContain('Engineer at Example Labs');
    expect(returned.textContent).toContain('profiles.example.test');
    expect(card.textContent).toContain('70%');
    expect(card.textContent).toContain('99%');
    expect(card.textContent).toContain('sailing');

    await fireEvent.click(within(card).getByRole('button', { name: 'Open Synthetic Reviewee' }));
    expect(onOpenPerson).toHaveBeenCalledWith(70);

    await fireEvent.click(within(card).getByRole('button', { name: 'Not this person' }));
    expect(await screen.findByText(/Identity rejected for Synthetic Reviewee/)).toBeDefined();
    expect(posts).toEqual(['/api/v1/person-enrichment/identity-reviews/7/reject']);
    expect(await screen.findByText('No enrichment identities to confirm.')).toBeDefined();
  });
});
