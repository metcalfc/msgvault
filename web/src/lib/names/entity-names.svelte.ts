import { SvelteMap } from 'svelte/reactivity';

import type { APIClient } from '../api/client';
import { getEntityLabels } from '../api/generated/api/api';
import type { EntityLabel, GetEntityLabelsParams } from '../api/generated/models';

/** The named records the server can label by ID. */
export type EntityKind = 'person' | 'participant' | 'organization';

/** Shown while a name is on its way. */
export const LOADING_LABEL = 'Loading name…';
/** Shown after a lookup failed; a later render past RETRY_AFTER_MS asks again. */
export const UNAVAILABLE_LABEL = 'Name unavailable';
export const UNKNOWN_LABELS: Readonly<Record<EntityKind, string>> = {
  person: 'Unknown person',
  participant: 'Unknown contact',
  organization: 'Unknown organization'
};
/** The server answers at most this many distinct IDs of one kind per request. */
export const MAX_IDS_PER_KIND = 500;
/** A failed lookup is not repeated by the render its failure caused, only by a later one. */
export const RETRY_AFTER_MS = 10_000;

type Entry = { state: 'named'; label: string } | { state: 'unknown' } | { state: 'failed'; at: number };
type Queue = Record<EntityKind, Set<number>>;

const KINDS: readonly EntityKind[] = ['person', 'participant', 'organization'];
const PARAM: Record<EntityKind, keyof GetEntityLabelsParams> = {
  person: 'person',
  participant: 'participant',
  organization: 'organization'
};
const RESPONSE_FIELD = { person: 'people', participant: 'participants', organization: 'organizations' } as const;

function keyOf(kind: EntityKind, id: number): string {
  return `${kind}:${id}`;
}

function validID(id: unknown): id is number {
  return typeof id === 'number' && Number.isSafeInteger(id) && id > 0;
}

function emptyQueue(): Queue {
  return { person: new Set(), participant: new Set(), organization: new Set() };
}

/**
 * Names people, contacts, and organizations the page only knows by ID.
 *
 * One resolver serves one API client. IDs asked for in the same tick go to the
 * server in one request; a pending ID is never requested twice; a settled
 * answer (a name, or definitely none) is kept; and a failed request is
 * forgotten so a later render asks again. Labels never contain the ID.
 */
export class EntityNames {
  readonly #client: APIClient;
  readonly #entries = new SvelteMap<string, Entry>();
  readonly #pending = new Map<string, Promise<void>>();
  #queue: Queue = emptyQueue();
  #batch: Promise<void> | undefined;

  constructor(client: APIClient) {
    this.#client = client;
  }

  /**
   * The display label for an entity. Reactive: a component that renders it
   * updates when the name arrives. Returns a neutral placeholder while
   * loading, UNAVAILABLE_LABEL after a failure, and "Unknown …" when the
   * server has no name for the ID.
   */
  label(kind: EntityKind, id: number | null | undefined): string {
    if (!validID(id)) return UNKNOWN_LABELS[kind];
    const entry = this.#entries.get(keyOf(kind, id));
    if (entry?.state === 'named') return entry.label;
    if (entry?.state === 'unknown') return UNKNOWN_LABELS[kind];
    if (entry?.state === 'failed') {
      if (Date.now() - entry.at >= RETRY_AFTER_MS) this.load(kind, [id]).catch(() => undefined);
      return UNAVAILABLE_LABEL;
    }
    this.load(kind, [id]).catch(() => undefined);
    return LOADING_LABEL;
  }

  /** The settled name, without requesting one. Reactive. */
  known(kind: EntityKind, id: number | null | undefined): string | undefined {
    if (!validID(id)) return undefined;
    const entry = this.#entries.get(keyOf(kind, id));
    return entry?.state === 'named' ? entry.label : undefined;
  }

  /** Records a name the page already has (from a listing), so it is not fetched. */
  seed(kind: EntityKind, id: number | null | undefined, label: string | null | undefined): void {
    const name = label?.trim();
    if (!validID(id) || !name) return;
    const key = keyOf(kind, id);
    const current = this.#entries.get(key);
    if (current?.state === 'named' && current.label === name) return;
    this.#entries.set(key, { state: 'named', label: name });
  }

  /**
   * Resolves the given IDs. Settles once every one has an answer; rejects
   * when a request failed, leaving those IDs to be asked for again.
   */
  load(kind: EntityKind, ids: Iterable<number | null | undefined>): Promise<void> {
    const waits: Promise<void>[] = [];
    for (const id of ids) {
      if (!validID(id)) continue;
      const key = keyOf(kind, id);
      const entry = this.#entries.get(key);
      if (entry?.state === 'named' || entry?.state === 'unknown') continue;
      let pending = this.#pending.get(key);
      if (!pending) {
        this.#queue[kind].add(id);
        pending = this.#schedule();
        this.#pending.set(key, pending);
      }
      waits.push(pending);
    }
    return Promise.all(waits).then(() => undefined);
  }

  #schedule(): Promise<void> {
    if (!this.#batch) {
      const batch = Promise.resolve().then(() => {
        this.#batch = undefined;
        const queue = this.#queue;
        this.#queue = emptyQueue();
        return this.#send(queue);
      });
      // Callers observe failures through load(); keep the shared promise quiet.
      batch.catch(() => undefined);
      this.#batch = batch;
    }
    return this.#batch;
  }

  async #send(queue: Queue): Promise<void> {
    const lists = Object.fromEntries(KINDS.map((kind) => [kind, [...queue[kind]]])) as Record<EntityKind, number[]>;
    const rounds = Math.max(...KINDS.map((kind) => Math.ceil(lists[kind].length / MAX_IDS_PER_KIND)));
    const results = await Promise.all(
      Array.from({ length: rounds }, (_, round) => {
        const chunk = Object.fromEntries(
          KINDS.map((kind) => [kind, lists[kind].slice(round * MAX_IDS_PER_KIND, (round + 1) * MAX_IDS_PER_KIND)])
        ) as Record<EntityKind, number[]>;
        return this.#request(chunk);
      })
    );
    if (results.some((ok) => !ok)) throw new Error('Name lookup failed');
  }

  async #request(chunk: Record<EntityKind, number[]>): Promise<boolean> {
    const params: GetEntityLabelsParams = {};
    for (const kind of KINDS) if (chunk[kind].length) params[PARAM[kind]] = chunk[kind];
    let answers: Record<EntityKind, EntityLabel[]> | undefined;
    try {
      const { data, response } = await getEntityLabels(params, this.#client);
      if (response.ok && data && KINDS.every((kind) => Array.isArray(data[RESPONSE_FIELD[kind]]))) {
        answers = { person: data.people, participant: data.participants, organization: data.organizations };
      }
    } catch {
      answers = undefined;
    }
    const failedAt = Date.now();
    for (const kind of KINDS) {
      const names = new Map<number, string>();
      for (const answer of answers?.[kind] ?? []) {
        const label = typeof answer?.label === 'string' ? answer.label.trim() : '';
        if (validID(answer?.id) && label) names.set(answer.id, label);
      }
      for (const id of chunk[kind]) {
        const key = keyOf(kind, id);
        this.#pending.delete(key);
        // A seeded name that arrived meanwhile is fresher than a failure.
        if (!answers) {
          if (this.#entries.get(key)?.state !== 'named') this.#entries.set(key, { state: 'failed', at: failedAt });
          continue;
        }
        const label = names.get(id);
        this.#entries.set(key, label ? { state: 'named', label } : { state: 'unknown' });
      }
    }
    return answers !== undefined;
  }
}

const resolvers = new WeakMap<APIClient, EntityNames>();

/** The shared resolver for one API client. */
export function entityNames(client: APIClient): EntityNames {
  let resolver = resolvers.get(client);
  if (!resolver) {
    resolver = new EntityNames(client);
    resolvers.set(client, resolver);
  }
  return resolver;
}

export function personLabel(client: APIClient, id: number | null | undefined): string {
  return entityNames(client).label('person', id);
}

export function participantLabel(client: APIClient, id: number | null | undefined): string {
  return entityNames(client).label('participant', id);
}

export function organizationLabel(client: APIClient, id: number | null | undefined): string {
  return entityNames(client).label('organization', id);
}
