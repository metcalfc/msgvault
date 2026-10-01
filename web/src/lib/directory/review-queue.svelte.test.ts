import { describe, expect, it, vi } from 'vitest';
import { createAPIClient, type APIClient } from '../api/client';
import { CorrespondentReviewController } from './correspondent-review-controller.svelte';
import { EnrichmentReviewController } from './enrichment-review-controller.svelte';
import { OrganizationReviewController } from './organization-review-controller.svelte';

const correspondent = (id: number) => ({
  canonical_id: id, member_ids: [id], kind: 'unclear' as const, addresses: [`record${id}@example.test`],
});

const queues = [
  {
    name: 'organization', listKey: 'reviews',
    create(client: APIClient) {
      const controller = new OrganizationReviewController(client);
      return { controller, decide: (id: number) => controller.reject(id) };
    },
    row: (id: number) => ({ id, organization_id: id + 100, proposed_name: `Alias ${id}`, organization_name: 'Example Organization' }),
    decision: (id: number) => ({ review_id: id, organization_id: id + 100, decision: 'rejected' }),
  },
  {
    name: 'enrichment', listKey: 'reviews',
    create(client: APIClient) {
      const controller = new EnrichmentReviewController(client);
      return { controller, decide: (id: number) => controller.reject(id) };
    },
    row: (id: number) => ({ attempt_id: id, person_id: id + 100, person_display_name: `Example Person ${id}` }),
    decision: (id: number) => ({ attempt_id: id, person_id: id + 100, decision: 'rejected', projections: 0 }),
  },
  {
    name: 'correspondent', listKey: 'records',
    create(client: APIClient) {
      const controller = new CorrespondentReviewController(client);
      return { controller, decide: (id: number) => controller.decide(correspondent(id), 'person') };
    },
    row: correspondent,
    decision: (id: number) => ({ record: { ...correspondent(id), kind: 'person' }, resolved_candidates: 0, restored_candidates: 0, organization_created: false }),
  },
];

function deferredResponse() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((next) => { resolve = next; });
  return { promise, resolve };
}

describe.each(queues)('$name review lifecycle', ({ create, row, decision, listKey }) => {
  it('does not restore a decided row from an older list response', async () => {
    const stale = deferredResponse();
    let reads = 0;
    const { controller, decide } = create(createAPIClient(vi.fn<typeof fetch>(async (input) => {
      const request = input as Request;
      if (request.method !== 'GET') return Response.json(decision(1));
      return ++reads === 1 ? Response.json({ [listKey]: [row(1), row(2)] }) : stale.promise;
    })));
    await controller.load();
    const refresh = controller.load();
    expect((await decide(1)).ok).toBe(true);
    stale.resolve(Response.json({ [listKey]: [row(1), row(2)] }));
    await refresh;
    expect(controller.rows).toEqual([row(2)]);
    controller.destroy();
  });

  it('retains a failed decision for retry and prevents duplicate pending writes', async () => {
    const pending = deferredResponse();
    let writes = 0;
    const { controller, decide } = create(createAPIClient(vi.fn<typeof fetch>(async (input) => {
      const request = input as Request;
      if (request.method === 'GET') return Response.json({ [listKey]: [row(1), row(2)] });
      return ++writes === 1 ? pending.promise : Response.json(decision(1));
    })));
    await controller.load();
    const first = decide(1);
    expect(controller.isPending(1)).toBe(true);
    expect((await decide(1)).ok).toBe(false);
    expect(writes).toBe(1);
    pending.resolve(Response.json({ message: 'Try again.' }, { status: 503 }));
    expect((await first).ok).toBe(false);
    expect(controller.rows).toEqual([row(1), row(2)]);
    expect(controller.decisionError).toBe('Try again.');
    expect(controller.isPending(1)).toBe(false);
    expect((await decide(1)).ok).toBe(true);
    expect(controller.rows).toEqual([row(2)]);
    expect(controller.decisionError).toBeNull();
    controller.destroy();
  });

  it('ignores read completion after destruction even if the transport ignores abort', async () => {
    const pending = deferredResponse();
    let signal: AbortSignal | undefined;
    const { controller } = create(createAPIClient(vi.fn<typeof fetch>(async (input) => {
      signal = (input as Request).signal;
      return pending.promise;
    })));
    const read = controller.load();
    controller.destroy();
    expect(signal?.aborted).toBe(true);
    pending.resolve(Response.json({ [listKey]: [row(1)] }));
    await read;
    expect(controller.rows).toEqual([]);
    expect(controller.loading).toBe(false);
    expect(controller.error).toBeNull();
  });

  it('does not publish a mutation completion after destruction', async () => {
    const pending = deferredResponse();
    const { controller, decide } = create(createAPIClient(vi.fn<typeof fetch>(async (input) => {
      return (input as Request).method === 'GET'
        ? Response.json({ [listKey]: [row(1)] })
        : pending.promise;
    })));
    await controller.load();
    const write = decide(1);
    controller.destroy();
    pending.resolve(Response.json(decision(1)));
    expect((await write).ok).toBe(false);
    expect(controller.rows).toEqual([row(1)]);
    expect(controller.status).toBeNull();
    expect(controller.pending.size).toBe(0);
  });
});
