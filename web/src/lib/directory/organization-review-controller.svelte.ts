import {
  acceptOrganizationMatchReview as generatedAccept,
  listOrganizationMatchReviews as generatedList,
  rejectOrganizationMatchReview as generatedReject,
} from '../api/generated/api/api';
import { failureMessage } from '../api/failure-message';
import { ReviewQueue, reviewDecision } from './review-queue.svelte';
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
export class OrganizationReviewController extends ReviewQueue<OrganizationMatchReview> {
  constructor(private readonly client: APIClient) {
    super((row) => row.id, async (signal) => {
      const response = await generatedList({ limit: ORGANIZATION_REVIEW_LIMIT }, { ...client, signal });
      if (!response.data) throw new Error(failureMessage(response.error, response.response.status));
      return response.data.reviews ?? [];
    }, true);
  }

  accept(reviewID: number): Promise<OrganizationDecisionResult> {
    return this.decide(reviewID, true);
  }

  reject(reviewID: number): Promise<OrganizationDecisionResult> {
    return this.decide(reviewID, false);
  }

  private decide(reviewID: number, accept: boolean): Promise<OrganizationDecisionResult> {
    return this.decideRow(reviewID, async () => {
      const request = accept ? generatedAccept : generatedReject;
      return reviewDecision(await request({ id: reviewID }, this.client));
    }, (decision, row) => {
      const proposed = row?.proposed_name ?? 'The name';
      const existing = row?.organization_name ?? `organization ${decision.organization_id}`;
      return accept
        ? `${proposed} now resolves to ${existing}.`
        : `${proposed} stays separate from ${existing}. It will not be proposed again.`;
    });
  }
}

/** An organization name with its domain, when one is known. */
export function organizationLabel(name: string, domain?: string): string {
  return domain?.trim() ? `${name} (${domain.trim()})` : name;
}
