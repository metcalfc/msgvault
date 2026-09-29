import { untrack } from 'svelte';
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
/** A settled answer older than this is still shown, and a render refreshes it. */
export const STALE_AFTER_MS = 5 * 60_000;

/**
 * One answer. `dueAt` is when a render should ask the server again: a
 * settled answer after STALE_AFTER_MS, a failed request (including a failed
 * refresh that keeps an older answer on screen) after RETRY_AFTER_MS.
 */
type NamedEntry = { state: 'named'; label: string; identity?: string; dueAt: number };
type Entry = NamedEntry | { state: 'unknown'; dueAt: number } | { state: 'failed'; dueAt: number };
type Queue = Record<EntityKind, Set<number>>;

const KINDS: readonly EntityKind[] = ['person', 'participant', 'organization'];
const PARAM: Record<EntityKind, keyof GetEntityLabelsParams> = {
  person: 'person',
  participant: 'participant',
  organization: 'organization'
};
const RESPONSE_FIELD = { person: 'people', participant: 'participants', organization: 'organizations' } as const;

// Svelte does not let a reaction depend on state created while it runs, and
// a resolver is often first asked for inside a template. Keeping every
// resolver's answers in one map created at module load keeps them reactive.
const settledAnswers = new SvelteMap<string, Entry>();
let resolverCount = 0;

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
 * answer (a name, or definitely none) is kept until it is invalidated or
 * grows stale; and a failed request is forgotten so a later render asks
 * again. A response never overwrites a name seeded or invalidated after its
 * request started. Labels never contain the ID.
 */
export class EntityNames {
  readonly #client: APIClient;
  readonly #entries = settledAnswers;
  readonly #scope = `${++resolverCount}`;
  readonly #pending = new Map<string, Promise<void>>();
  /** Bumped when a key is seeded or invalidated; an answer to an older version is dropped. */
  readonly #versions = new Map<string, number>();
  #queue: Queue = emptyQueue();
  #batch: Promise<void> | undefined;

  constructor(client: APIClient) {
    this.#client = client;
  }

  #key(kind: EntityKind, id: number): string {
    return `${this.#scope}:${kind}:${id}`;
  }

  #bump(key: string): void {
    this.#versions.set(key, (this.#versions.get(key) ?? 0) + 1);
  }

  /** Whether a render should ask the server: no answer, a stale one, or a failure long enough ago. */
  #due(entry: Entry | undefined, now = Date.now()): boolean {
    if (!entry) return true;
    return now >= entry.dueAt;
  }

  /**
   * Who an entity is. Reactive: a component that renders it updates when the
   * name arrives. Returns a neutral placeholder while loading,
   * UNAVAILABLE_LABEL after a failure, and "Unknown …" when the server has no
   * name for the ID. A stale answer stays on screen while it refreshes.
   */
  label(kind: EntityKind, id: number | null | undefined): string {
    return this.#text(kind, id, (entry) => entry.label);
  }

  /**
   * A participant's own identity: its own name and address. It tells several
   * identities of one person apart where their labels (led by that person's
   * name) would repeat. Falls back to the label when the server gave none.
   */
  identity(id: number | null | undefined): string {
    return this.#text('participant', id, (entry) => entry.identity || entry.label);
  }

  #text(kind: EntityKind, id: number | null | undefined, pick: (entry: NamedEntry) => string): string {
    if (!validID(id)) return UNKNOWN_LABELS[kind];
    const entry = this.#entries.get(this.#key(kind, id));
    if (this.#due(entry)) this.load(kind, [id]).catch(() => undefined);
    if (entry?.state === 'named') return pick(entry);
    if (entry?.state === 'unknown') return UNKNOWN_LABELS[kind];
    if (entry?.state === 'failed') return UNAVAILABLE_LABEL;
    return LOADING_LABEL;
  }

  /** The settled name, without requesting one. Reactive. */
  known(kind: EntityKind, id: number | null | undefined): string | undefined {
    if (!validID(id)) return undefined;
    const entry = this.#entries.get(this.#key(kind, id));
    return entry?.state === 'named' ? entry.label : undefined;
  }

  /** A name the page already holds when it has one, else the resolver's label. */
  name(kind: EntityKind, id: number | null | undefined, known?: string | null): string {
    return known?.trim() || this.label(kind, id);
  }

  /**
   * Waits for a settled name, for one-off text such as an announcement.
   * Returns the fallback when the server has no name or the lookup failed.
   */
  async settledLabel(kind: EntityKind, id: number | null | undefined, fallback: string): Promise<string> {
    try {
      await this.load(kind, [id]);
    } catch {
      // The fallback stands in for a name that could not be looked up.
    }
    return this.known(kind, id) ?? fallback;
  }

  /**
   * Records a name the page already has (from a listing or a save), so it is
   * not fetched. An answer already in flight for it will not overwrite it.
   */
  seed(kind: EntityKind, id: number | null | undefined, label: string | null | undefined): void {
    const name = label?.trim();
    if (!validID(id) || !name) return;
    const key = this.#key(kind, id);
    this.#bump(key);
    const current = untrack(() => this.#entries.get(key));
    if (current?.state === 'named' && current.label === name) {
      this.#entries.set(key, { ...current, dueAt: Date.now() + STALE_AFTER_MS });
      return;
    }
    // A participant's identity is its own; a new name for "who" leaves it to the server.
    this.#entries.set(key, { state: 'named', label: name, dueAt: Date.now() + STALE_AFTER_MS });
  }

  /**
   * Marks answers out of date after a change that can rename them (a rename,
   * merge, split, or link). The old text stays on screen while the next
   * render refreshes it, and answers already in flight are dropped. Without
   * IDs, every answer of the kind is marked.
   */
  invalidate(kind: EntityKind, ids?: Iterable<number>): void {
    untrack(() => {
      const prefix = `${this.#scope}:${kind}:`;
      // The kind-wide form also covers IDs whose first request is still in
      // flight: they have no entry yet, only a pending answer to drop.
      const keys = ids === undefined
        ? [...new Set([...this.#entries.keys(), ...this.#pending.keys()])].filter((key) => key.startsWith(prefix))
        : [...ids].filter(validID).map((id) => this.#key(kind, id));
      for (const key of keys) {
        this.#bump(key);
        this.#pending.delete(key);
        const entry = this.#entries.get(key);
        if (entry) this.#entries.set(key, { ...entry, dueAt: Number.NEGATIVE_INFINITY });
      }
    });
  }

  /**
   * Resolves the given IDs. Settles once every one has a current answer;
   * rejects when a request failed, leaving those IDs to be asked for again.
   */
  load(kind: EntityKind, ids: Iterable<number | null | undefined>): Promise<void> {
    // Loading never subscribes the caller: an effect that loads must not rerun,
    // and so retry at once, because an answer or failure arrived.
    return untrack(() => this.#load(kind, ids));
  }

  #load(kind: EntityKind, ids: Iterable<number | null | undefined>): Promise<void> {
    const waits: Promise<void>[] = [];
    const now = Date.now();
    for (const id of ids) {
      if (!validID(id)) continue;
      const key = this.#key(kind, id);
      let pending = this.#pending.get(key);
      if (!pending) {
        const entry = this.#entries.get(key);
        // An explicit load retries a failure at once; a render waits (see #text).
        if (entry && entry.state !== 'failed' && !this.#due(entry, now)) continue;
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
      const batch: Promise<void> = Promise.resolve().then(() => {
        this.#batch = undefined;
        const queue = this.#queue;
        this.#queue = emptyQueue();
        return this.#send(queue, batch);
      });
      // Callers observe failures through load(); keep the shared promise quiet.
      batch.catch(() => undefined);
      this.#batch = batch;
    }
    return this.#batch;
  }

  async #send(queue: Queue, batch: Promise<void>): Promise<void> {
    const lists = Object.fromEntries(KINDS.map((kind) => [kind, [...queue[kind]]])) as Record<EntityKind, number[]>;
    const rounds = Math.max(...KINDS.map((kind) => Math.ceil(lists[kind].length / MAX_IDS_PER_KIND)));
    const results = await Promise.all(
      Array.from({ length: rounds }, (_, round) => {
        const chunk = Object.fromEntries(
          KINDS.map((kind) => [kind, lists[kind].slice(round * MAX_IDS_PER_KIND, (round + 1) * MAX_IDS_PER_KIND)])
        ) as Record<EntityKind, number[]>;
        return this.#request(chunk, batch);
      })
    );
    if (results.some((ok) => !ok)) throw new Error('Name lookup failed');
  }

  async #request(chunk: Record<EntityKind, number[]>, batch: Promise<void>): Promise<boolean> {
    const params: GetEntityLabelsParams = {};
    for (const kind of KINDS) if (chunk[kind].length) params[PARAM[kind]] = chunk[kind];
    // The versions this request answers: a seed or invalidation after this
    // point makes its answer for that key out of date.
    const started = new Map<string, number>();
    for (const kind of KINDS) {
      for (const id of chunk[kind]) {
        const key = this.#key(kind, id);
        started.set(key, this.#versions.get(key) ?? 0);
      }
    }
    let answers: Record<EntityKind, EntityLabel[]> | undefined;
    try {
      const { data, response } = await getEntityLabels(params, this.#client);
      if (response.ok && data && KINDS.every((kind) => Array.isArray(data[RESPONSE_FIELD[kind]]))) {
        answers = { person: data.people, participant: data.participants, organization: data.organizations };
      }
    } catch {
      answers = undefined;
    }
    const now = Date.now();
    for (const kind of KINDS) {
      const found = new Map<number, { label: string; identity?: string }>();
      for (const answer of answers?.[kind] ?? []) {
        const label = typeof answer?.label === 'string' ? answer.label.trim() : '';
        const identity = typeof answer?.identity === 'string' ? answer.identity.trim() : '';
        if (validID(answer?.id) && label) found.set(answer.id, { label, ...(identity ? { identity } : {}) });
      }
      for (const id of chunk[kind]) {
        const key = this.#key(kind, id);
        if (this.#pending.get(key) === batch) this.#pending.delete(key);
        if ((this.#versions.get(key) ?? 0) !== started.get(key)) {
          // This answer predates a change. Without a newer request under way,
          // ask again so an ID still showing a placeholder gets a current name.
          if (!this.#pending.has(key) && this.#entries.get(key)?.state !== 'named') {
            this.#load(kind, [id]).catch(() => undefined);
          }
          continue;
        }
        const current = this.#entries.get(key);
        if (!answers) {
          // A failed refresh keeps the older answer on screen, and a later
          // render retries it as soon as it would retry any failure.
          const dueAt = now + RETRY_AFTER_MS;
          this.#entries.set(key, current && current.state !== 'failed' ? { ...current, dueAt } : { state: 'failed', dueAt });
          continue;
        }
        const name = found.get(id);
        const dueAt = now + STALE_AFTER_MS;
        this.#entries.set(key, name ? { state: 'named', ...name, dueAt } : { state: 'unknown', dueAt });
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

/**
 * Marks every person and participant name out of date after a change that
 * can rename them or move identities between people: a person rename, merge,
 * split, or identity link. Participant labels lead with the bound person's
 * name, and an unnamed person is named by its participants.
 */
export function invalidatePeopleNames(client: APIClient): void {
  const names = entityNames(client);
  names.invalidate('person');
  names.invalidate('participant');
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
