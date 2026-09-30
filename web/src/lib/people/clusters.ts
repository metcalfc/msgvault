/** Resolves a Directory person's bound participants to the clusters they
 * belong to. Person–participant bindings are independent of participant
 * identity links, so the bound ids may sit in one cluster or several; each
 * is looked up in parallel and the results are deduplicated by canonical id
 * (every member of a cluster returns the same summary). Shared by the
 * person page's contact block and the Directory → timeline handoff. */
import { getParticipant } from '../api/generated/api/api';
import type { APIClient } from '../api/client';
import type { CorrespondentKindAssignment, PersonIdentifier, PersonSummary } from '../api/generated/models';

export interface BoundCluster {
  canonicalID: number;
  memberIDs: number[];
  /** The bound participant ids that resolved into this cluster. */
  boundIDs: number[];
  label: string;
  activityCount: number;
  identifiers: PersonIdentifier[];
  /** Set when the cluster is marked as not a person. */
  correspondentKind?: CorrespondentKindAssignment;
}

export interface BoundClusterResolution {
  /** Busiest first, then by canonical id. */
  clusters: BoundCluster[];
  /** Bound ids whose lookup failed or returned nothing. */
  failedIDs: number[];
}

/** Whether a resolution was computed for (at least) these bound ids: each
 * id is one a cluster was resolved from or lists as a member. A resolution
 * carried over from another person, or one with a failed lookup, does not
 * cover them and must be resolved again. */
export function resolutionCovers(resolution: BoundClusterResolution, participantIDs: readonly number[]): boolean {
  const ids = validParticipantIDs(participantIDs);
  return resolution.failedIDs.length === 0 && ids.every((id) =>
    resolution.clusters.some((cluster) => cluster.boundIDs.includes(id) || cluster.memberIDs.includes(id)));
}

export function validParticipantIDs(participantIDs: readonly number[]): number[] {
  return [...new Set(participantIDs.filter((id) => Number.isSafeInteger(id) && id > 0))].sort((a, b) => a - b);
}

export async function resolveBoundClusters(
  participantIDs: readonly number[],
  client: APIClient,
  signal?: AbortSignal
): Promise<BoundClusterResolution> {
  const ids = validParticipantIDs(participantIDs);
  const results = await Promise.all(ids.map(async (id): Promise<{ id: number; summary: PersonSummary | undefined }> => {
    try {
      return { id, summary: (await getParticipant({ id }, { ...client, ...(signal ? { signal } : {}) })).data };
    } catch {
      return { id, summary: undefined };
    }
  }));
  const clusters = new Map<number, BoundCluster>();
  const failedIDs: number[] = [];
  for (const { id, summary } of results) {
    if (!summary || !Number.isSafeInteger(summary.id)) { failedIDs.push(id); continue; }
    const canonicalID = summary.cluster?.canonical_id ?? summary.id;
    const existing = clusters.get(canonicalID);
    if (existing) { existing.boundIDs.push(id); continue; }
    clusters.set(canonicalID, {
      canonicalID,
      memberIDs: [...new Set([canonicalID, ...(summary.cluster?.member_ids ?? [summary.id])])],
      boundIDs: [id],
      label: summary.display_label,
      activityCount: summary.activity_count,
      identifiers: summary.identifiers ?? [],
      ...(summary.correspondent_kind ? { correspondentKind: summary.correspondent_kind } : {})
    });
  }
  return {
    clusters: [...clusters.values()].sort((a, b) => b.activityCount - a.activityCount || a.canonicalID - b.canonicalID),
    failedIDs
  };
}
