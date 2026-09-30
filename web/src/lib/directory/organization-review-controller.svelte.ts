import {
  acceptOrganizationMatchReview as generatedAccept,
  listOrganizationMatchReviews as generatedList,
  rejectOrganizationMatchReview as generatedReject,
} from '../api/generated/api/api';
import { SvelteSet } from 'svelte/reactivity';
import type { APIClient } from '../api/client';
import type {
  OrganizationMatchDecision,
  OrganizationMatchReview as GeneratedReview,
} from '../api/generated/models';

export type OrganizationMatchReview = GeneratedReview;
export const ORGANIZATION_REVIEW_LIMIT = 50;

export type OrganizationDecisionResult =
  | { ok: true; decision: OrganizationMatchDecision }
  | { ok: false; message: string };

/**
 * Owns the "Organization matches to confirm" queue: organization names the
 * organization resolution judgment was unsure about. Accept and reject are
 * explicit user decisions.
 */
export class OrganizationReviewController {
  rows = $state<OrganizationMatchReview[]>([]);
  loading = $state(false);
  loaded = $state(false);
  error = $state<string | null>(null);
  decisionError = $state<string | null>(null);
  status = $state<string | null>(null);
  readonly pending = new SvelteSet<number>();
  private readonly client: APIClient;
  private abort: AbortController | undefined;
  private disposed = false;
  // A list read may have started before a successful decision committed.
  private readonly decided = new Set<number>();

  constructor(client: APIClient) {
    this.client = client;
  }

  isPending(reviewID: number): boolean {
    return this.pending.has(reviewID);
  }

  async load(): Promise<void> {
    if (this.disposed) return;
    this.abort?.abort();
    const abort = new AbortController();
    this.abort = abort;
    this.loading = true;
    this.error = null;
    try {
      const response = await generatedList(
        { limit: ORGANIZATION_REVIEW_LIMIT },
        { ...this.client, signal: abort.signal },
      );
      if (this.disposed || abort.signal.aborted) return;
      if (response.data) {
        this.rows = (response.data.reviews ?? []).filter((row) => !this.decided.has(row.id));
        this.loaded = true;
        return;
      }
      this.error = failureMessage(response.error, response.response.status);
    } catch (cause: unknown) {
      if (this.disposed || abort.signal.aborted) return;
      this.error = failureMessage(cause, 0);
    } finally {
      if (this.abort === abort) this.loading = false;
    }
  }

  accept(reviewID: number): Promise<OrganizationDecisionResult> {
    return this.decide(reviewID, true);
  }

  reject(reviewID: number): Promise<OrganizationDecisionResult> {
    return this.decide(reviewID, false);
  }

  destroy(): void {
    this.disposed = true;
    this.abort?.abort();
    this.pending.clear();
  }

  private async decide(reviewID: number, accept: boolean): Promise<OrganizationDecisionResult> {
    if (this.disposed || this.pending.has(reviewID)) {
      return { ok: false, message: 'A decision is already pending.' };
    }
    this.pending.add(reviewID);
    this.decisionError = null;
    this.status = null;
    try {
      const request = accept ? generatedAccept : generatedReject;
      const response = await request({ id: reviewID }, this.client);
      if (this.disposed) return { ok: false, message: 'Review closed.' };
      if (response.data) {
        const row = this.rows.find((candidate) => candidate.id === reviewID);
        const proposed = row?.proposed_name ?? 'The name';
        const existing = row?.organization_name ?? `organization ${response.data.organization_id}`;
        this.decided.add(reviewID);
        this.rows = this.rows.filter((candidate) => candidate.id !== reviewID);
        this.status = accept
          ? `${proposed} now resolves to ${existing}.`
          : `${proposed} stays separate from ${existing}. It will not be proposed again.`;
        return { ok: true, decision: response.data };
      }
      const message = failureMessage(response.error, response.response.status);
      this.decisionError = message;
      return { ok: false, message };
    } catch (cause: unknown) {
      const message = failureMessage(cause, 0);
      this.decisionError = message;
      return { ok: false, message };
    } finally {
      this.pending.delete(reviewID);
      // Wait for concurrent decisions, then refill before showing an empty queue.
      // load() keeps refill errors separate from the committed decision and
      // the queue's existing Retry control can recover without repeating it.
      if (!this.disposed && this.decided.has(reviewID) && this.pending.size === 0 && this.rows.length === 0) {
        await this.load();
      }
    }
  }
}

/** An organization name with its domain, when one is known. */
export function organizationLabel(name: string, domain?: string): string {
  return domain?.trim() ? `${name} (${domain.trim()})` : name;
}

function failureMessage(error: unknown, status: number): string {
  if (typeof error === 'object' && error !== null && 'message' in error && typeof error.message === 'string') {
    return error.message;
  }
  if (error instanceof Error && error.message) return error.message;
  return status > 0 ? `Request failed (${status}).` : 'Request failed.';
}
