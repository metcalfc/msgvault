import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../api/client';
import { EnrichmentReviewController } from './enrichment-review-controller.svelte';
import { OrganizationReviewController } from './organization-review-controller.svelte';

const queues = [
  {
    name: 'organization',
    create: (client: ReturnType<typeof createAPIClient>) => new OrganizationReviewController(client),
    row: (id: number) => ({ id, organization_id: 100 + id, organization_name: 'Example Organization', proposed_name: `Example Alias ${id}` }),
    decision: (id: number) => ({ review_id: id, organization_id: 100 + id, decision: 'rejected' }),
  },
  {
    name: 'enrichment',
    create: (client: ReturnType<typeof createAPIClient>) => new EnrichmentReviewController(client),
    row: (id: number) => ({ attempt_id: id, person_id: 100 + id, person_display_name: `Example Person ${id}` }),
    decision: (id: number) => ({ attempt_id: id, person_id: 100 + id, decision: 'rejected', projections: 0 }),
  },
];

function deferredResponse() {
  let resolve!: (value: Response) => void;
  const promise = new Promise<Response>((next) => { resolve = next; });
  return { promise, resolve };
}

describe.each(queues)('$name review queue refill', ({ create, row, decision }) => {
  it('continues past the first 50 reviews and verifies when the queue is empty', async () => {
    let pending = Array.from({ length: 51 }, (_, index) => index + 1);
    let reads = 0;
    const controller = create(createAPIClient(vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      if (request.method === 'POST') {
        const id = Number(new URL(request.url).pathname.split('/').at(-2));
        pending = pending.filter((candidate) => candidate !== id);
        return Response.json(decision(id));
      }
      reads++;
      return Response.json({ reviews: pending.slice(0, 50).map((id) => row(id)), limit: 50 });
    })));
    await controller.load();
    expect(controller.rows).toHaveLength(50);
    for (let id = 1; id <= 50; id++) expect((await controller.reject(id)).ok).toBe(true);
    expect(controller.rows).toEqual([row(51)]);
    expect(reads).toBe(2);
    await controller.reject(51);
    expect(controller.rows).toEqual([]);
    expect(reads).toBe(3);
    expect(controller.error).toBeNull();
  });

  it('refills once after concurrent decisions finish', async () => {
    const first = deferredResponse();
    const second = deferredResponse();
    let reads = 0;
    const controller = create(createAPIClient(vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      if (request.method === 'POST') return request.url.includes('/1/') ? first.promise : second.promise;
      return Response.json({ reviews: ++reads === 1 ? [row(1), row(2)] : [row(3)] });
    })));
    await controller.load();
    const firstDecision = controller.reject(1);
    const secondDecision = controller.reject(2);
    first.resolve(Response.json(decision(1)));
    await firstDecision;
    expect(reads).toBe(1);
    second.resolve(Response.json(decision(2)));
    await secondDecision;
    expect(reads).toBe(2);
    expect(controller.rows).toEqual([row(3)]);
  });

});
