import { describe, expect, it } from 'vitest';

import {
  contactMatchBlockedMessage,
  contactMatchSummary,
  endpointLabel,
  endpointRole,
} from './identity-endpoints';

describe('identity endpoint labels', () => {
  it('prefers a display name, then an address, then the owning profile, then the kind and ID', () => {
    expect(endpointLabel('participant', 4, {
      kind: 'participant', id: 4, found: true, display_name: ' Ada Example ', addresses: ['ada@example.test'],
    })).toBe('Ada Example');
    expect(endpointLabel('participant', 4, {
      kind: 'participant', id: 4, found: true, addresses: ['ada@example.test'],
    })).toBe('ada@example.test');
    expect(endpointLabel('carddav_resource', 9, {
      kind: 'carddav_resource', id: 9, found: true, addresses: [], person_id: 2, person_display_name: 'Card Owner',
    })).toBe('Card Owner');
    expect(endpointLabel('person', 5, { kind: 'person', id: 5, found: false, addresses: [] }))
      .toBe('Person profile 5 (removed)');
    expect(endpointLabel('observation', 6)).toBe('Observed address 6');
    expect(endpointRole('unknown_kind')).toBe('unknown_kind');
  });

  it('describes each contact match verdict and blocker in plain words', () => {
    const base = { candidate_id: 1, cluster_person_ids: [] };
    expect(contactMatchSummary({ ...base, classification: 'bind' })).toContain('links this archive identity');
    expect(contactMatchSummary({ ...base, classification: 'merge' })).toContain('merge the two profiles');
    expect(contactMatchSummary({ ...base, classification: 'ambiguous' })).toContain('several profiles');
    expect(contactMatchSummary({ ...base, classification: 'linked' })).toContain('already linked');
    expect(contactMatchBlockedMessage({ ...base, classification: 'bind', blocked_reason: 'published' }))
      .toContain('published to CardDAV');
    expect(contactMatchBlockedMessage({ ...base, classification: 'merge', blocked_reason: 'carddav_conflict' }))
      .toContain('unresolved CardDAV conflict');
    expect(contactMatchBlockedMessage({ ...base, classification: 'bind' })).toBeNull();
    expect(contactMatchBlockedMessage(undefined)).toBeNull();
  });
});
