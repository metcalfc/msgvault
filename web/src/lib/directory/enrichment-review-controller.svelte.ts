import {
  confirmPersonEnrichmentIdentity as generatedConfirm,
  listPersonEnrichmentIdentityReviews as generatedList,
  rejectPersonEnrichmentIdentity as generatedReject,
} from '../api/generated/api/api';
import { SvelteSet } from 'svelte/reactivity';
import type { APIClient } from '../api/client';
import { entityNames, invalidatePeopleNames, type EntityNames } from '../names/entity-names.svelte';
import type {
  PersonEnrichmentIdentityDecision,
  PersonEnrichmentIdentityReview as GeneratedReview,
  PersonEnrichmentReturnedIdentity,
} from '../api/generated/models';

export type PersonEnrichmentIdentityReview = GeneratedReview;
export const ENRICHMENT_REVIEW_LIMIT = 50;

export type EnrichmentDecisionResult =
  | { ok: true; decision: PersonEnrichmentIdentityDecision }
  | { ok: false; message: string };

/**
 * Owns the "Enrichment identities to confirm" queue: attempts whose identity
 * check was uncertain. Confirm and reject are explicit user decisions.
 */
export class EnrichmentReviewController {
  rows = $state<PersonEnrichmentIdentityReview[]>([]);
  loading = $state(false);
  loaded = $state(false);
  error = $state<string | null>(null);
  decisionError = $state<string | null>(null);
  status = $state<string | null>(null);
  readonly pending = new SvelteSet<number>();
  /** Names a person whose review row carries no display name. */
  readonly names: EntityNames;
  private readonly client: APIClient;
  private abort: AbortController | undefined;
  private disposed = false;
  // A list read may have started before a successful decision committed.
  private readonly decided = new Set<number>();

  constructor(client: APIClient) {
    this.client = client;
    this.names = entityNames(client);
  }

  isPending(attemptID: number): boolean {
    return this.pending.has(attemptID);
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
        { limit: ENRICHMENT_REVIEW_LIMIT },
        { ...this.client, signal: abort.signal },
      );
      if (this.disposed || abort.signal.aborted) return;
      if (response.data) {
        this.rows = (response.data.reviews ?? []).filter((row) => !this.decided.has(row.attempt_id));
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

  confirm(attemptID: number): Promise<EnrichmentDecisionResult> {
    return this.decide(attemptID, true);
  }

  reject(attemptID: number): Promise<EnrichmentDecisionResult> {
    return this.decide(attemptID, false);
  }

  destroy(): void {
    this.disposed = true;
    this.abort?.abort();
    this.pending.clear();
  }

  private async decide(attemptID: number, confirm: boolean): Promise<EnrichmentDecisionResult> {
    if (this.disposed || this.pending.has(attemptID)) {
      return { ok: false, message: 'A decision is already pending.' };
    }
    this.pending.add(attemptID);
    this.decisionError = null;
    this.status = null;
    try {
      const request = confirm ? generatedConfirm : generatedReject;
      const response = await request({ id: attemptID }, this.client);
      if (this.disposed) return { ok: false, message: 'Review closed.' };
      if (response.data) {
        // Confirmed values can include the person's name.
        if (confirm) invalidatePeopleNames(this.client);
        const row = this.rows.find((candidate) => candidate.attempt_id === attemptID);
        const who = row?.person_display_name?.trim() ||
          await this.names.settledLabel('person', response.data.person_id, 'the person');
        if (this.disposed) return { ok: false, message: 'Review closed.' };
        this.decided.add(attemptID);
        this.rows = this.rows.filter((candidate) => candidate.attempt_id !== attemptID);
        this.status = confirm
          ? `Identity confirmed for ${who}. ${response.data.projections} value(s) applied.`
          : `Identity rejected for ${who}. It will not be proposed again.`;
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
      this.pending.delete(attemptID);
      // Wait for concurrent decisions, then refill before showing an empty queue.
      // load() keeps refill errors separate from the committed decision and
      // the queue's existing Retry control can recover without repeating it.
      if (!this.disposed && this.decided.has(attemptID) && this.pending.size === 0 && this.rows.length === 0) {
        await this.load();
      }
    }
  }
}

/** The person's name, else the durable label the resolver finds. Never their ID. */
export function personLabel(review: PersonEnrichmentIdentityReview, names: EntityNames): string {
  return names.name('person', review.person_id, review.person_display_name);
}

/** The returned identity in one line: name, roles, location, and host. */
export function returnedIdentityLines(returned: PersonEnrichmentReturnedIdentity): string[] {
  const lines: string[] = [];
  if (returned.name?.trim()) lines.push(returned.name.trim());
  for (const role of returned.current_roles ?? []) {
    const title = role.title?.trim();
    const company = role.company?.trim();
    if (title && company) lines.push(`${title} at ${company}`);
    else if (title || company) lines.push((title || company) as string);
  }
  if (returned.location?.trim()) lines.push(returned.location.trim());
  if (returned.profile_url_host?.trim()) lines.push(returned.profile_url_host.trim());
  return lines;
}

/** A probability as a whole percentage. */
export function percent(value: number): string {
  return `${Math.round(value * 100)}%`;
}

function failureMessage(error: unknown, status: number): string {
  if (typeof error === 'object' && error !== null && 'message' in error && typeof error.message === 'string') {
    return error.message;
  }
  if (error instanceof Error && error.message) return error.message;
  return status > 0 ? `Request failed (${status}).` : 'Request failed.';
}
