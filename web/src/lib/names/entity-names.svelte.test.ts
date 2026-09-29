import { afterEach, describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../api/client';
import {
  EntityNames,
  LOADING_LABEL,
  MAX_IDS_PER_KIND,
  RETRY_AFTER_MS,
  UNAVAILABLE_LABEL,
  entityNames,
  organizationLabel,
  participantLabel,
  personLabel
} from './entity-names.svelte';

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

type Names = Record<string, Record<number, string>>;

function labelsServer(names: Names = {}) {
  const requests: URL[] = [];
  let fail = false;
  const fetchFn = vi.fn<typeof fetch>(async (input) => {
    const request = input instanceof Request ? input : new Request(input);
    const url = new URL(request.url);
    requests.push(url);
    if (fail) return Response.json({ error: 'unavailable', message: 'down' }, { status: 503 });
    const answer = (kind: string) =>
      url.searchParams.getAll(kind).map(Number).filter((id) => names[kind]?.[id]).map((id) => ({ id, label: names[kind][id] }));
    return Response.json({
      people: answer('person'),
      participants: answer('participant'),
      organizations: answer('organization')
    });
  });
  return {
    client: createAPIClient(fetchFn),
    requests,
    setFailing(value: boolean) {
      fail = value;
    }
  };
}

async function settle(): Promise<void> {
  await vi.waitFor(() => undefined);
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

describe('EntityNames', () => {
  it('batches every ID asked for in one tick into one request and names them', async () => {
    const server = labelsServer({
      person: { 7: 'Avery Example' },
      participant: { 70: 'Blair Example' },
      organization: { 3: 'Example Works' }
    });
    const names = new EntityNames(server.client);

    expect(names.label('person', 7)).toBe(LOADING_LABEL);
    expect(names.label('participant', 70)).toBe(LOADING_LABEL);
    expect(names.label('organization', 3)).toBe(LOADING_LABEL);
    expect(names.label('person', 8)).toBe(LOADING_LABEL);
    await names.load('person', [7, 8]);

    expect(server.requests).toHaveLength(1);
    const params = server.requests[0].searchParams;
    expect(server.requests[0].pathname).toBe('/api/v1/entity-labels');
    expect(params.getAll('person').sort()).toEqual(['7', '8']);
    expect(params.getAll('participant')).toEqual(['70']);
    expect(params.getAll('organization')).toEqual(['3']);
    expect(names.label('person', 7)).toBe('Avery Example');
    expect(names.label('participant', 70)).toBe('Blair Example');
    expect(names.label('organization', 3)).toBe('Example Works');
    expect(names.label('person', 8)).toBe('Unknown person');
  });

  it('shares a pending lookup and keeps a settled answer', async () => {
    const server = labelsServer({ person: { 7: 'Avery Example' } });
    const names = new EntityNames(server.client);

    const first = names.load('person', [7]);
    const second = names.load('person', [7, 7]);
    names.label('person', 7);
    await Promise.all([first, second]);
    await names.load('person', [7]);
    names.label('person', 7);
    names.label('person', 7);
    await settle();

    expect(server.requests).toHaveLength(1);
    expect(server.requests[0].searchParams.getAll('person')).toEqual(['7']);
  });

  it('forgets a failed lookup so a later render asks again', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    const server = labelsServer({ person: { 7: 'Avery Example' } });
    const names = new EntityNames(server.client);
    server.setFailing(true);

    await expect(names.load('person', [7])).rejects.toThrow();
    expect(names.label('person', 7)).toBe(UNAVAILABLE_LABEL);
    await settle();
    expect(server.requests).toHaveLength(1);

    server.setFailing(false);
    vi.setSystemTime(Date.now() + RETRY_AFTER_MS);
    expect(names.label('person', 7)).toBe(UNAVAILABLE_LABEL);
    await settle();

    expect(server.requests).toHaveLength(2);
    expect(names.label('person', 7)).toBe('Avery Example');
  });

  it('treats a malformed answer as a failure rather than as no name', async () => {
    const fetchFn = vi.fn<typeof fetch>(async () => Response.json({ unrelated: true }));
    const names = new EntityNames(createAPIClient(fetchFn));

    await expect(names.load('organization', [3])).rejects.toThrow();

    expect(names.label('organization', 3)).toBe(UNAVAILABLE_LABEL);
  });

  it('splits more than the per-kind limit across requests', async () => {
    const server = labelsServer();
    const names = new EntityNames(server.client);
    const ids = Array.from({ length: MAX_IDS_PER_KIND + 1 }, (_, index) => index + 1);

    await names.load('participant', ids);

    expect(server.requests).toHaveLength(2);
    expect(server.requests.map((url) => url.searchParams.getAll('participant').length).sort((a, b) => a - b)).toEqual([1, MAX_IDS_PER_KIND]);
    expect(names.label('participant', MAX_IDS_PER_KIND + 1)).toBe('Unknown contact');
  });

  it('uses a seeded name without asking the server', async () => {
    const server = labelsServer();
    const names = new EntityNames(server.client);

    names.seed('person', 7, '  Avery Example ');
    names.seed('person', 8, '   ');
    await names.load('person', [7]);

    expect(names.label('person', 7)).toBe('Avery Example');
    expect(names.known('person', 8)).toBeUndefined();
    expect(server.requests).toHaveLength(0);
  });

  it('never returns a label that contains the ID', async () => {
    const server = labelsServer({ person: { 4242: 'Avery Example' } });
    const client = server.client;
    const ids = [4242, 5151, 6161];

    const loading = [
      ...ids.map((id) => personLabel(client, id)),
      participantLabel(client, 5151),
      organizationLabel(client, 6161)
    ];
    await entityNames(client).load('person', ids);
    await settle();
    const settled = [
      ...ids.map((id) => personLabel(client, id)),
      participantLabel(client, 5151),
      organizationLabel(client, 6161),
      personLabel(client, undefined),
      personLabel(client, 0)
    ];

    for (const label of [...loading, ...settled]) {
      for (const id of ids) expect(label).not.toContain(String(id));
    }
    expect(settled).toEqual(['Avery Example', 'Unknown person', 'Unknown person', 'Unknown contact', 'Unknown organization', 'Unknown person', 'Unknown person']);
  });

  it('shares one resolver per client', () => {
    const client = labelsServer().client;

    expect(entityNames(client)).toBe(entityNames(client));
    expect(entityNames(labelsServer().client)).not.toBe(entityNames(client));
  });
});
