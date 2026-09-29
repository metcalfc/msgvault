import { describe, expect, it } from 'vitest';

import type { PersonContactPoint, PersonIdentifier } from '../api/generated/models';
import {
  mergeReachEntries, normalizeReachValue, reachEntriesFromContactPoints, reachEntriesFromIdentifiers,
  reachEntriesFromMembers, reachKindForAddressKind, serviceKey, serviceLabelForSlug
} from './reach';

const when = '2026-01-01T00:00:00Z';

function contactPoint(overrides: Partial<PersonContactPoint>): PersonContactPoint {
  return {
    person_id: 7, address_kind: 'email', original_value: 'Person@Example.test', normalized_value: 'person@example.test',
    normalization: 'email', normalization_version: 1, service_slug: 'email',
    envelope: { id: 1, ordinal: 0, source: 'user', created_at: when, updated_at: when, vcard: {} },
    ...overrides
  };
}

function identifier(overrides: Partial<PersonIdentifier>): PersonIdentifier {
  return { type: 'email', value: 'person@example.test', participant_id: 12, is_primary: true, provenance: 'participant_identifiers', ...overrides };
}

describe('reach entries', () => {
  it('normalizes emails case-insensitively and phones to digits', () => {
    expect(normalizeReachValue('email', ' Person@Example.test ')).toBe('person@example.test');
    expect(normalizeReachValue('phone', '+1 (555) 010-0001')).toBe('15550100001');
  });

  it('maps address kinds to reach kinds and skips postal or biographical kinds', () => {
    expect(reachKindForAddressKind('email')).toBe('email');
    expect(reachKindForAddressKind('phone')).toBe('phone');
    expect(reachKindForAddressKind('impp')).toBe('chat');
    expect(reachKindForAddressKind('username')).toBe('handle');
    expect(reachKindForAddressKind('url')).toBe('url');
    expect(reachKindForAddressKind('postal')).toBeUndefined();
    expect(reachKindForAddressKind('birth_place')).toBeUndefined();
  });

  it('labels services in human words and leaves email and phone unlabelled', () => {
    expect(serviceLabelForSlug('imessage')).toBe('iMessage');
    expect(serviceLabelForSlug('linkedin')).toBe('LinkedIn');
    expect(serviceLabelForSlug('custom_thing')).toBe('Custom thing');
    expect(serviceLabelForSlug('email')).toBeUndefined();
    expect(serviceLabelForSlug('other')).toBeUndefined();
    expect(serviceLabelForSlug(undefined)).toBeUndefined();
  });

  it('marks address-book contact points as curated and archive observations as observed', () => {
    const entries = reachEntriesFromContactPoints([
      contactPoint({}),
      contactPoint({ address_kind: 'phone', original_value: '+1 555 010 0001', normalized_value: '+15550100001', service_slug: 'phone',
        envelope: { id: 2, ordinal: 0, source: 'archive_observation', created_at: when, updated_at: when, vcard: {} } }),
      contactPoint({ address_kind: 'postal', original_value: '1 Example Road', normalized_value: '1 example road' })
    ]);
    expect(entries.map((entry) => [entry.kind, entry.display, entry.observed])).toEqual([
      ['email', 'Person@Example.test', false],
      ['phone', '+1 555 010 0001', true]
    ]);
  });

  it('keeps opaque provider keys out of the visible text but in the copy value and tooltip', () => {
    const key = 'beeper:8:whatsapp:9:@user:x.y';
    const [entry] = reachEntriesFromIdentifiers({ identifiers: [identifier({
      type: 'beeper', value: key, display_value: key, is_primary: false, service_label: 'WhatsApp',
      participant_display_name: 'Alias Example', scope_kind: 'account', scope_value: 'local-whatsapp_ba_example'
    })] });
    expect(entry?.kind).toBe('chat');
    expect(entry?.opaque).toBe(true);
    expect(entry?.display).toBe('WhatsApp');
    expect(entry?.value).toBe(key);
    expect(entry?.name).toBe('Alias Example');
    expect(entry?.label).toBe('WhatsApp identifier for Alias Example (profile 12)');
    expect(entry?.title).toContain(key);
    expect(entry?.title).toContain('account: local-whatsapp_ba_example');
    expect(entry?.note ?? '').not.toContain('local-whatsapp_ba_example');
  });

  it('notes which cluster member a value belongs to and how it was linked', () => {
    const entries = reachEntriesFromIdentifiers({
      identifiers: [
        identifier({ display_value: 'Alice' }),
        identifier({ type: 'phone', value: '+15550100002', participant_id: 34 }),
        identifier({ value: 'carol@example.test', participant_id: 56 })
      ],
      ownID: 12, clustered: true,
      edges: [
        { participant_a: 12, participant_b: 34, link_origin: { kind: 'manual' } },
        { participant_a: 34, participant_b: 56 }
      ]
    });
    expect(entries.map((entry) => [entry.value, entry.name, entry.note])).toEqual([
      ['person@example.test', 'Alice', 'this profile'],
      ['+15550100002', undefined, 'linked manually'],
      ['carol@example.test', undefined, 'linked']
    ]);
    expect(entries[0]?.title).toBe('email · primary · stored identifier');
    expect(entries.every((entry) => entry.observed)).toBe(true);
  });

  it('turns bare cluster members into rows for their stored email or phone only', () => {
    const entries = reachEntriesFromMembers([
      { participant_id: 78, email: 'bare@example.test', display_name: 'Bare Example' },
      { participant_id: 79, display_name: 'No Address' }
    ]);
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({ kind: 'email', value: 'bare@example.test', name: 'Bare Example', note: 'linked', participantIDs: [78] });
  });

  it('keys the same chat handle identically whichever source spelled the service', () => {
    const merged = mergeReachEntries(
      reachEntriesFromContactPoints([contactPoint({
        address_kind: 'username', original_value: 'alice', normalized_value: 'alice', service_slug: ' Slack '
      })]),
      reachEntriesFromIdentifiers({ identifiers: [
        identifier({ type: 'slack', value: 'alice', service_slug: 'slack', participant_id: 12 }),
        identifier({ type: 'slack', value: 'ALICE', participant_id: 34 })
      ] })
    );
    // The contact point is a handle and the identifiers are chat keys, so
    // they stay separate rows; within a kind the service spelling never splits them.
    expect(merged.map((entry) => [entry.kind, entry.participantIDs])).toEqual([['chat', [12, 34]], ['handle', []]]);
    expect(serviceKey(' Slack ', 'username')).toBe('slack');
    expect(serviceKey(undefined, 'impp')).toBe('impp');
    expect(serviceKey('', 'beeper')).toBe('beeper');
  });

  it('keeps every member that shares a value on the one visible row', () => {
    const merged = mergeReachEntries(
      reachEntriesFromIdentifiers({ identifiers: [
        identifier({}),
        identifier({ value: 'PERSON@example.test', participant_id: 34 })
      ], ownID: 12, clustered: true, edges: [{ participant_a: 12, participant_b: 34 }] }),
      reachEntriesFromMembers([{ participant_id: 78, email: 'person@example.test' }])
    );
    expect(merged).toHaveLength(1);
    expect(merged[0]?.participantIDs).toEqual([12, 34, 78]);
  });

  it('deduplicates by normalized value, prefers the address book, and orders by kind', () => {
    const merged = mergeReachEntries(
      reachEntriesFromContactPoints([
        contactPoint({ address_kind: 'url', original_value: 'https://example.test/person', normalized_value: 'https://example.test/person', service_slug: 'linkedin' }),
        contactPoint({})
      ]),
      reachEntriesFromIdentifiers({ identifiers: [
        identifier({ value: 'PERSON@example.test', display_value: 'Person' }),
        identifier({ type: 'phone', value: '+1 (555) 010-0001' })
      ] }),
      reachEntriesFromMembers([{ participant_id: 34, phone: '15550100001' }])
    );
    expect(merged.map((entry) => [entry.kind, entry.display, entry.observed, entry.participantIDs])).toEqual([
      ['email', 'Person@Example.test', false, [12]],
      ['phone', '+1 (555) 010-0001', true, [12, 34]],
      ['url', 'https://example.test/person', false, []]
    ]);
    expect(merged[0]?.name).toBe('Person');
    expect(merged[2]?.service).toBe('LinkedIn');
  });
});
