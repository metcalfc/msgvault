import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../api/client';
import {
  EnrichmentReviewController,
  percent,
  personLabel,
  returnedIdentityLines,
  type PersonEnrichmentIdentityReview,
} from './enrichment-review-controller.svelte';

function review(attemptID: number): PersonEnrichmentIdentityReview {
  return {
    attempt_id: attemptID,
    person_id: attemptID * 10,
    person_display_name: `Synthetic Person ${attemptID}`,
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
      name: 'Ada E.',
      current_roles: [{ title: 'Engineer', company: 'Example Labs' }],
      location: 'Example City',
      profile_url_host: 'profiles.example.test',
    },
    claims: [{ target: 'location', value: 'Example City' }],
  };
}

function requestOf(input: RequestInfo | URL): Request {
  return input instanceof Request ? input : new Request(input);
}

describe('EnrichmentReviewController', () => {
  it('lists reviews, confirms one, and removes it from the queue', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      if (request.method === 'POST') {
        return Response.json({
          attempt_id: 1, person_id: 10, decision: 'confirmed', reason: 'user_confirmed',
          attempt_state: 'succeeded', projections: 2, provider_identities_attached: 1, negatives: 0,
        });
      }
      return Response.json({ reviews: [review(1), review(2)], limit: 50 });
    });
    const controller = new EnrichmentReviewController(createAPIClient(fetchFn));

    await controller.load();
    expect(new URL(requests[0]!.url).pathname).toBe('/api/v1/person-enrichment/identity-reviews');
    expect(new URL(requests[0]!.url).searchParams.get('limit')).toBe('50');
    expect(controller.rows.map((row) => row.attempt_id)).toEqual([1, 2]);

    const result = await controller.confirm(1);
    expect(result.ok).toBe(true);
    expect(new URL(requests[1]!.url).pathname).toBe('/api/v1/person-enrichment/identity-reviews/1/confirm');
    expect(controller.rows.map((row) => row.attempt_id)).toEqual([2]);
    expect(controller.status).toBe('Identity confirmed for Synthetic Person 1. 2 value(s) applied.');
  });

  it('keeps the row and reports the server message when a rejection fails', async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      if (request.method === 'POST') {
        return Response.json(
          { error: 'enrichment_review_state_changed', message: 'The attempt is no longer awaiting an identity decision' },
          { status: 409 },
        );
      }
      return Response.json({ reviews: [review(3)], limit: 50 });
    });
    const controller = new EnrichmentReviewController(createAPIClient(fetchFn));
    await controller.load();

    const result = await controller.reject(3);
    expect(result).toEqual({ ok: false, message: 'The attempt is no longer awaiting an identity decision' });
    expect(controller.rows).toHaveLength(1);
    expect(controller.decisionError).toBe('The attempt is no longer awaiting an identity decision');
    expect(controller.isPending(3)).toBe(false);
  });

  it('formats the returned identity, person, and probabilities', () => {
    expect(returnedIdentityLines(review(1).returned)).toEqual([
      'Ada E.', 'Engineer at Example Labs', 'Example City', 'profiles.example.test',
    ]);
    expect(returnedIdentityLines({ current_roles: [] })).toEqual([]);
    expect(personLabel({ ...review(4), person_display_name: undefined })).toBe('Person 40');
    expect(percent(0.704)).toBe('70%');
  });
});
