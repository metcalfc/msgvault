import {
  confirmPersonEnrichmentIdentity as generatedConfirm,
  listPersonEnrichmentIdentityReviews as generatedList,
  rejectPersonEnrichmentIdentity as generatedReject,
} from '../api/generated/api/api';
import { failureMessage } from '../api/failure-message';
import { ReviewQueue, reviewDecision } from './review-queue.svelte';
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
export class EnrichmentReviewController extends ReviewQueue<PersonEnrichmentIdentityReview> {
  /** Names a person whose review row carries no display name. */
  readonly names: EntityNames;

  constructor(private readonly client: APIClient) {
    super((row) => row.attempt_id, async (signal) => {
      const response = await generatedList({ limit: ENRICHMENT_REVIEW_LIMIT }, { ...client, signal });
      if (!response.data) throw new Error(failureMessage(response.error, response.response.status));
      return response.data.reviews ?? [];
    }, true);
    this.names = entityNames(client);
  }

  confirm(attemptID: number): Promise<EnrichmentDecisionResult> {
    return this.decide(attemptID, true);
  }

  reject(attemptID: number): Promise<EnrichmentDecisionResult> {
    return this.decide(attemptID, false);
  }

  private decide(attemptID: number, confirm: boolean): Promise<EnrichmentDecisionResult> {
    return this.decideRow(attemptID, async () => {
      const request = confirm ? generatedConfirm : generatedReject;
      return reviewDecision(await request({ id: attemptID }, this.client));
    }, async (decision, row) => {
      // Confirmed values can include the person's name.
      if (confirm) invalidatePeopleNames(this.client);
      const who = row?.person_display_name?.trim() ||
        await this.names.settledLabel('person', decision.person_id, 'the person');
      return confirm
        ? `Identity confirmed for ${who}. ${decision.projections} value(s) applied.`
        : `Identity rejected for ${who}. It will not be proposed again.`;
    });
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
