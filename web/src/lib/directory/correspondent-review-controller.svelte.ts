import { ReviewQueue } from './review-queue.svelte';

import type { APIClient } from '../api/client';
import type { CorrespondentKindRecord } from '../api/generated/models';
import { kindLabel, listUnclear, setKind, type CorrespondentKind } from '../people/correspondent-kind';

export type CorrespondentDecisionResult = { ok: true } | { ok: false; message: string };

/**
 * Owns the "Unclear correspondents" queue: identities Jev could not
 * classify. Every decision is an explicit user classification, which
 * outranks any rule or Jev judgment.
 */
export class CorrespondentReviewController extends ReviewQueue<CorrespondentKindRecord> {
  constructor(private readonly client: APIClient) {
    super((row) => row.canonical_id, async (signal) => {
      const page = await listUnclear(client, signal);
      if ('error' in page) throw new Error(page.error);
      return page.records;
    });
  }

  decide(record: CorrespondentKindRecord, kind: CorrespondentKind): Promise<CorrespondentDecisionResult> {
    return this.decideRow(record.canonical_id, async () => {
      const outcome = await setKind(this.client, record.canonical_id, kind);
      return outcome.ok ? { ok: true, decision: outcome.result } : outcome;
    }, () => kind === 'person'
      ? `${recordLabel(record)} is a person.`
      : `${recordLabel(record)} marked as ${kindLabel(kind).toLowerCase()}.`);
  }
}

/** The identity's name, else its first address. Never an ID. */
export function recordLabel(record: Pick<CorrespondentKindRecord, 'display_name' | 'addresses'>): string {
  return record.display_name?.trim() || record.addresses[0]?.trim() || 'Unnamed identity';
}
