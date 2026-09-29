import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../api/client';
import { entityNames, LOADING_LABEL } from '../names/entity-names.svelte';
import {
  contactMatchBlockedMessage,
  contactMatchSummary,
  endpointLabel,
  endpointRole,
  sharedMailboxReason,
} from './identity-endpoints';

describe('identity endpoint labels', () => {
  it('prefers a display name, then an address, then the resolver, then the owning profile, then the kind', () => {
    const names = entityNames(createAPIClient(vi.fn<typeof fetch>(async () => Response.json({
      people: [], participants: [], organizations: []
    }))));
    expect(endpointLabel(names, 'participant', 4, {
      kind: 'participant', id: 4, found: true, display_name: ' Ada Example ', addresses: ['ada@example.test'],
    })).toBe('Ada Example');
    expect(endpointLabel(names, 'participant', 4, {
      kind: 'participant', id: 4, found: true, addresses: ['ada@example.test'],
    })).toBe('ada@example.test');
    expect(endpointLabel(names, 'carddav_resource', 9, {
      kind: 'carddav_resource', id: 9, found: true, addresses: [], person_id: 2, person_display_name: 'Card Owner',
    })).toBe('Card Owner');
    expect(endpointLabel(names, 'person', 5, { kind: 'person', id: 5, found: false, addresses: [] }))
      .toBe('Person profile (removed)');
    expect(endpointLabel(names, 'observation', 6)).toBe('Observed address');
    expect(endpointLabel(names, 'person', 5)).toBe(LOADING_LABEL);
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

  it('explains a shared mailbox signal', () => {
    expect(contactMatchSummary({ candidate_id: 1, cluster_person_ids: [], classification: 'shared_mailbox' }))
      .toContain('looks like a shared mailbox');
    expect(sharedMailboxReason({ address: 'billing@example.test', reasons: ['role_address'] }))
      .toBe("billing@example.test is a role address, not a person's.");
    expect(sharedMailboxReason({ address: 'desk@example.test', reasons: ['several_names'], names: ['Kai Mercer', 'Lena Ortiz'] }))
      .toBe('Different people wrote from desk@example.test: Kai Mercer, Lena Ortiz.');
  });
});
