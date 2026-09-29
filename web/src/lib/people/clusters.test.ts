import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../api/client';
import { resolveBoundClusters, validParticipantIDs } from './clusters';

function summary(id: number, canonical: number, members: number[], activity: number) {
  return {
    id, display_label: `Person ${canonical}`, identifiers: [{ type: 'email', value: `p${canonical}@example.test`, participant_id: canonical, is_primary: true, provenance: 'participant_identifiers' }],
    activity_count: activity, file_count: 0, meeting_count: 0, partial_label: false, current_relationship_temperature: 0,
    peak_relationship_temperature: 0, peak_relationship_year: 0, source_counts: [], first_at: '', last_at: '', cache_revision: 'c',
    cluster: { canonical_id: canonical, member_ids: members, edges: [] }
  };
}

describe('resolveBoundClusters', () => {
  it('looks bindings up in parallel, dedupes by canonical id, and ranks by activity', async () => {
    const requested: string[] = [];
    const client = createAPIClient(vi.fn<typeof fetch>(async (input) => {
      const path = new URL(input instanceof Request ? input.url : String(input)).pathname;
      requested.push(path);
      const id = Number(path.split('/').at(-1));
      if (id === 9) return Response.json(summary(9, 3, [3, 9], 5));
      if (id === 3) return Response.json(summary(3, 3, [3, 9], 5));
      if (id === 21) return Response.json(summary(21, 21, [21], 20));
      if (id === 40) return Response.json({ error: 'not_found', message: 'gone' }, { status: 404 });
      throw new Error('network down');
    }));

    const resolution = await resolveBoundClusters([9, 3, 3, 21, 40, 55, 0, -1], client);
    expect(requested.sort()).toEqual(['/api/v1/participants/21', '/api/v1/participants/3', '/api/v1/participants/40', '/api/v1/participants/55', '/api/v1/participants/9']);
    expect(resolution.clusters.map((cluster) => [cluster.canonicalID, cluster.boundIDs, cluster.activityCount])).toEqual([
      [21, [21], 20],
      [3, [3, 9], 5]
    ]);
    expect(resolution.clusters[1]?.memberIDs).toEqual([3, 9]);
    expect(resolution.clusters[1]?.identifiers).toHaveLength(1);
    expect(resolution.failedIDs).toEqual([40, 55]);
  });

  it('normalizes the bound id list', () => {
    expect(validParticipantIDs([9, 3, 3, 0, -2, Number.NaN])).toEqual([3, 9]);
  });
});
