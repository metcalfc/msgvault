import { SvelteSet } from 'svelte/reactivity';

import type { APIClient } from '../api/client';
import type { CorrespondentKindRecord } from '../api/generated/models';
import { kindLabel, listUnclear, setKind, type CorrespondentKind } from '../people/correspondent-kind';

export type CorrespondentDecisionResult = { ok: true } | { ok: false; message: string };

/**
 * Owns the "Unclear correspondents" queue: identities Jev could not
 * classify. Every decision is an explicit user classification, which
 * outranks any rule or Jev judgment.
 */
export class CorrespondentReviewController {
  rows = $state<CorrespondentKindRecord[]>([]);
  loading = $state(false);
  loaded = $state(false);
  error = $state<string | null>(null);
  decisionError = $state<string | null>(null);
  status = $state<string | null>(null);
  readonly pending = new SvelteSet<number>();
  private readonly client: APIClient;
  private abort: AbortController | undefined;
  private disposed = false;

  constructor(client: APIClient) {
    this.client = client;
  }

  isPending(canonicalID: number): boolean {
    return this.pending.has(canonicalID);
  }

  async load(): Promise<void> {
    if (this.disposed) return;
    this.abort?.abort();
    const abort = new AbortController();
    this.abort = abort;
    this.loading = true;
    this.error = null;
    try {
      const page = await listUnclear(this.client, abort.signal);
      if (this.disposed || abort.signal.aborted) return;
      if ('error' in page) {
        this.error = page.error;
        return;
      }
      this.rows = page.records;
      this.loaded = true;
    } finally {
      if (this.abort === abort) this.loading = false;
    }
  }

  async decide(record: CorrespondentKindRecord, kind: CorrespondentKind): Promise<CorrespondentDecisionResult> {
    const id = record.canonical_id;
    if (this.disposed || this.pending.has(id)) return { ok: false, message: 'A decision is already pending.' };
    this.pending.add(id);
    this.decisionError = null;
    this.status = null;
    try {
      const outcome = await setKind(this.client, id, kind);
      if (this.disposed) return { ok: false, message: 'Review closed.' };
      if (!outcome.ok) {
        this.decisionError = outcome.message;
        return { ok: false, message: outcome.message };
      }
      this.rows = this.rows.filter((row) => row.canonical_id !== id);
      this.status = kind === 'person'
        ? `${recordLabel(record)} is a person.`
        : `${recordLabel(record)} marked as ${kindLabel(kind).toLowerCase()}.`;
      return { ok: true };
    } finally {
      this.pending.delete(id);
    }
  }

  destroy(): void {
    this.disposed = true;
    this.abort?.abort();
    this.pending.clear();
  }
}

/** The identity's name, else its first address. Never an ID. */
export function recordLabel(record: Pick<CorrespondentKindRecord, 'display_name' | 'addresses'>): string {
  return record.display_name?.trim() || record.addresses[0]?.trim() || 'Unnamed identity';
}
