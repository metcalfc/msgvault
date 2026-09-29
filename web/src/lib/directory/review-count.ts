import { listIdentityMatchCandidates, listPersonRelationshipReviews } from '../api/generated/api/api';
import type { APIClient } from '../api/client';

/** The badge caps here; the queue itself pages through the rest. */
export const REVIEW_COUNT_CAP = 99;

/**
 * How many decisions wait in Reviews: open identity match candidates
 * (candidates and conflicts) plus pending relationship reviews, capped at
 * REVIEW_COUNT_CAP + 1 so the badge can say "99+". Returns undefined when
 * neither queue answers, so a failed read never shows as "nothing to do".
 */
export async function pendingReviewCount(client: APIClient, signal?: AbortSignal): Promise<number | undefined> {
  const limit = REVIEW_COUNT_CAP + 1;
  const [identity, relationships] = await Promise.allSettled([
    listIdentityMatchCandidates({ state: 'candidate,conflict', limit }, { ...client, signal }),
    listPersonRelationshipReviews({ status: 'pending' }, { ...client, signal }),
  ]);
  const identityCount = identity.status === 'fulfilled' ? identity.value.data?.candidates.length : undefined;
  const relationshipCount = relationships.status === 'fulfilled' ? relationships.value.data?.reviews.length : undefined;
  if (identityCount === undefined && relationshipCount === undefined) return undefined;
  return Math.min(limit, (identityCount ?? 0) + (relationshipCount ?? 0));
}

export function reviewCountLabel(count: number): string {
  return count > REVIEW_COUNT_CAP ? `${REVIEW_COUNT_CAP}+` : String(count);
}
