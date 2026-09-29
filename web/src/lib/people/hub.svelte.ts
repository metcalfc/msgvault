import { listRelationships, searchParticipants } from '../api/generated/exploration/exploration';
import type { APIClient } from '../api/client';
import type { PrimaryIdentifier } from '../api/generated/models';
import type { DirectoryController } from '../directory/controller.svelte';
import type { DirectoryPerson } from '../directory/models';

/** Which people the list shows: everyone, only saved, or only not saved. */
export type PeopleSavedFilter = '' | 'saved' | 'unsaved';

export interface PeopleFilters {
  query: string;
  saved: PeopleSavedFilter;
  hasName: boolean;
  category: string;
  organization: string;
}

/** One row of the People list: a saved Directory person or an archive
 * contact that has not been saved. */
export interface PeopleRow {
  kind: 'saved' | 'observed';
  /** Stable key across both sources. */
  key: string;
  /** The Directory person id (saved) or canonical participant id (observed). */
  id: number;
  name: string;
  identifier?: PrimaryIdentifier;
  lastContactAt?: string;
  /** Short facts for the second line: organizations, categories, channel. */
  meta: string[];
}

const OBSERVED_PAGE_LIMIT = 50;

function errorMessage(error: unknown, status: number): string {
  if (typeof error === 'object' && error !== null && 'message' in error && typeof error.message === 'string') {
    return error.message;
  }
  return status ? `The archive returned ${status}.` : 'The archive did not respond.';
}

/** Observed contacts' labels fall back to their address when the archive
 * never saw a name; those rows are the ones "Has name" hides. */
export function looksUnnamed(name: string, identifier?: PrimaryIdentifier): boolean {
  const text = name.trim();
  if (!text) return true;
  if (identifier && text.toLowerCase() === identifier.value.trim().toLowerCase()) return true;
  return text.includes('@') || /^[+\d\s().-]{5,}$/.test(text);
}

export function savedRow(person: DirectoryPerson): PeopleRow {
  const primary = person.primary_identifier as PrimaryIdentifier | undefined;
  return {
    kind: 'saved',
    key: `person:${person.id}`,
    id: person.id,
    name: person.display_name?.trim() || primary?.value || `Person ${person.id}`,
    identifier: primary,
    lastContactAt: person.last_contact_at ?? undefined,
    meta: [...(person.organizations ?? []), ...(person.categories ?? [])],
  };
}

/**
 * Archive contacts that are not saved to the Directory: the relationship
 * ranking restricted to unsaved clusters, newest contact first, or a
 * participant search while the list has a text query.
 */
export class ObservedContacts {
  rows = $state<PeopleRow[]>([]);
  cursor = $state<string | null>(null);
  loading = $state(false);
  loadingMore = $state(false);
  error = $state<string | null>(null);
  private readonly client: APIClient;
  private abort: AbortController | undefined;
  private generation = 0;
  private query = '';

  constructor(client: APIClient) {
    this.client = client;
  }

  reset(): void {
    this.abort?.abort();
    this.generation += 1;
    this.rows = [];
    this.cursor = null;
    this.loading = false;
    this.loadingMore = false;
    this.error = null;
  }

  async load(query: string): Promise<void> {
    this.reset();
    this.query = query.trim();
    const controller = new AbortController();
    this.abort = controller;
    const generation = this.generation;
    this.loading = true;
    try {
      const page = await this.fetchPage(undefined, controller.signal);
      if (generation !== this.generation) return;
      this.rows = page.rows;
      this.cursor = page.cursor;
      this.error = page.error;
    } finally {
      if (generation === this.generation) this.loading = false;
    }
  }

  async loadMore(): Promise<void> {
    if (this.loading || this.loadingMore || !this.cursor || !this.abort) return;
    const controller = this.abort;
    const generation = this.generation;
    this.loadingMore = true;
    try {
      const page = await this.fetchPage(this.cursor, controller.signal);
      if (generation !== this.generation) return;
      const seen = new Set(this.rows.map((row) => row.key));
      this.rows = [...this.rows, ...page.rows.filter((row) => !seen.has(row.key))];
      this.cursor = page.cursor;
      this.error = page.error;
    } finally {
      if (generation === this.generation) this.loadingMore = false;
    }
  }

  destroy(): void {
    this.abort?.abort();
  }

  private async fetchPage(cursor: string | undefined, signal: AbortSignal):
    Promise<{ rows: PeopleRow[]; cursor: string | null; error: string | null }> {
    try {
      if (this.query) {
        const { data, error, response } = await searchParticipants({
          predicate: { filters: [] }, identity_query: this.query,
          sort: { field: 'activity_count', direction: 'desc' }, limit: OBSERVED_PAGE_LIMIT,
          ...(cursor ? { cursor } : {}),
        }, { ...this.client, signal });
        if (!data) return { rows: [], cursor: null, error: errorMessage(error, response.status) };
        return {
          rows: data.rows.filter((person) => !person.profile?.id).map((person) => ({
            kind: 'observed', key: `contact:${person.id}`, id: person.id,
            name: person.display_label, lastContactAt: person.last_at,
            identifier: person.identifiers?.[0]
              ? { kind: person.identifiers[0].type === 'phone' ? 'phone' : person.identifiers[0].type === 'email' ? 'email' : 'handle', value: person.identifiers[0].value }
              : undefined,
            meta: [],
          } satisfies PeopleRow)),
          cursor: data.next_cursor ?? null,
          error: null,
        };
      }
      const { data, error, response } = await listRelationships({
        unsaved_only: true, sort: 'last_contact', limit: OBSERVED_PAGE_LIMIT, ...(cursor ? { cursor } : {}),
      }, { ...this.client, signal });
      if (!data) return { rows: [], cursor: null, error: errorMessage(error, response.status) };
      return {
        rows: data.rows.filter((row) => !row.profile?.id).map((row) => ({
          kind: 'observed', key: `contact:${row.canonical_id}`, id: row.canonical_id,
          name: row.display_label, identifier: row.primary_identifier, lastContactAt: row.last_at, meta: [],
        } satisfies PeopleRow)),
        cursor: data.next_cursor ?? null,
        error: null,
      };
    } catch (cause) {
      if (signal.aborted) return { rows: [], cursor: null, error: null };
      return { rows: [], cursor: null, error: errorMessage(cause, 0) };
    }
  }
}

function contactTime(row: PeopleRow): number {
  const parsed = row.lastContactAt ? Date.parse(row.lastContactAt) : Number.NaN;
  return Number.isNaN(parsed) ? Number.NEGATIVE_INFINITY : parsed;
}

function byLastContact(left: PeopleRow, right: PeopleRow): number {
  const difference = contactTime(right) - contactTime(left);
  if (difference !== 0 && !Number.isNaN(difference)) return difference > 0 ? 1 : -1;
  return left.name.localeCompare(right.name) || left.key.localeCompare(right.key);
}

export interface PeopleSources {
  saved?: { rows: PeopleRow[]; hasMore: boolean };
  observed?: { rows: PeopleRow[]; hasMore: boolean };
  /** A text query orders each source by relevance; the list then shows
   * saved matches before archive matches instead of interleaving by date. */
  ranked?: boolean;
}

/**
 * Merges saved people and archive contacts newest-contact first. Both
 * sources arrive in that order a page at a time, so a row is shown only
 * once every source that still has pages has loaded past its date: the
 * list never shows a row that a later page would have to insert above.
 */
export function mergePeople(sources: PeopleSources): { rows: PeopleRow[]; limitedBy: Array<'saved' | 'observed'> } {
  const saved = sources.saved?.rows ?? [];
  const observed = sources.observed?.rows ?? [];
  if (sources.ranked) return { rows: [...saved, ...observed], limitedBy: [] };
  let cutoff = Number.NEGATIVE_INFINITY;
  const limitedBy: Array<'saved' | 'observed'> = [];
  for (const [name, source] of [['saved', sources.saved], ['observed', sources.observed]] as const) {
    if (!source?.hasMore) continue;
    const last = source.rows.at(-1);
    const time = last ? contactTime(last) : Number.POSITIVE_INFINITY;
    if (time > cutoff) {
      cutoff = time;
      limitedBy.splice(0, limitedBy.length, name);
    } else if (time === cutoff) {
      limitedBy.push(name);
    }
  }
  const rows = [...saved, ...observed].sort(byLastContact).filter((row) => contactTime(row) >= cutoff);
  return { rows, limitedBy };
}

/**
 * The People list: saved Directory people (paged by the shell's Directory
 * controller, which also serves the person page) and archive contacts not
 * yet saved, merged into one list.
 */
export class PeopleHub {
  readonly observed: ObservedContacts;
  filters = $state<PeopleFilters>({ query: '', saved: '', hasName: false, category: '', organization: '' });
  private readonly directory: DirectoryController;
  private observedKey: string | undefined;

  constructor(client: APIClient, directory: DirectoryController) {
    this.observed = new ObservedContacts(client);
    this.directory = directory;
  }

  /** Category and organization belong to saved people only. */
  get includesObserved(): boolean {
    return this.filters.saved !== 'saved' && !this.filters.category.trim() && !this.filters.organization.trim();
  }

  get includesSaved(): boolean {
    return this.filters.saved !== 'unsaved';
  }

  apply(filters: PeopleFilters): void {
    this.filters = filters;
    const key = this.includesObserved ? `observed|${filters.query.trim()}` : 'none';
    if (key === this.observedKey) return;
    this.observedKey = key;
    if (this.includesObserved) void this.observed.load(filters.query);
    else this.observed.reset();
  }

  /** Reloads archive contacts, e.g. after one was saved. */
  refresh(): void {
    this.observedKey = undefined;
    this.apply(this.filters);
  }

  get merged(): { rows: PeopleRow[]; limitedBy: Array<'saved' | 'observed'> } {
    const observed = this.filters.hasName
      ? this.observed.rows.filter((row) => !looksUnnamed(row.name, row.identifier))
      : this.observed.rows;
    return mergePeople({
      saved: this.includesSaved
        ? { rows: this.directory.rows.map(savedRow), hasMore: this.directory.cursor !== null }
        : undefined,
      observed: this.includesObserved ? { rows: observed, hasMore: this.observed.cursor !== null } : undefined,
      ranked: Boolean(this.filters.query.trim()) || this.directory.sort !== 'last_contact_desc',
    });
  }

  get hasMore(): boolean {
    return (this.includesSaved && this.directory.cursor !== null) ||
      (this.includesObserved && this.observed.cursor !== null);
  }

  get loading(): boolean {
    return (this.includesSaved && this.directory.loading) || (this.includesObserved && this.observed.loading);
  }

  get loadingMore(): boolean {
    return this.directory.loadingMore || this.observed.loadingMore;
  }

  async loadMore(): Promise<void> {
    const { limitedBy } = this.merged;
    const targets = limitedBy.length > 0 ? limitedBy : [
      ...(this.includesSaved && this.directory.cursor !== null ? ['saved' as const] : []),
      ...(this.includesObserved && this.observed.cursor !== null ? ['observed' as const] : []),
    ];
    await Promise.all(targets.map((target) =>
      target === 'saved' ? this.directory.loadNextPage() : this.observed.loadMore()));
  }

  destroy(): void {
    this.observed.destroy();
  }
}
