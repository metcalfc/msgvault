import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../api/client';
import { withEntityLabels } from '../../test/entity-labels';
import { entityNames, LOADING_LABEL, UNKNOWN_LABELS } from '../names/entity-names.svelte';
import { filterNotPeople, looksUnnamed, mergePeople, ObservedContacts, savedRow, type PeopleRow } from './hub.svelte';

function row(kind: PeopleRow['kind'], id: number, lastContactAt?: string, name = `Person ${id}`): PeopleRow {
  return { kind, key: `${kind}:${id}`, id, name, lastContactAt, meta: [] };
}

describe('People list merge', () => {
  it('interleaves saved people and archive contacts newest contact first', () => {
    const { rows } = mergePeople({
      saved: { rows: [row('saved', 1, '2026-07-20T00:00:00Z'), row('saved', 2, '2026-07-10T00:00:00Z')], hasMore: false },
      observed: { rows: [row('observed', 3, '2026-07-15T00:00:00Z'), row('observed', 4)], hasMore: false },
    });
    expect(rows.map((item) => item.key)).toEqual(['saved:1', 'observed:3', 'saved:2', 'observed:4']);
  });

  it('holds back rows a later page of the other source could still precede', () => {
    const merged = mergePeople({
      saved: { rows: [row('saved', 1, '2026-07-20T00:00:00Z'), row('saved', 2, '2026-07-12T00:00:00Z')], hasMore: true },
      observed: { rows: [row('observed', 3, '2026-07-15T00:00:00Z'), row('observed', 4, '2026-07-01T00:00:00Z')], hasMore: false },
    });
    // Saved people have more pages at or after Jul 12: the Jul 1 contact waits.
    expect(merged.rows.map((item) => item.key)).toEqual(['saved:1', 'observed:3', 'saved:2']);
    expect(merged.limitedBy).toEqual(['saved']);
  });

  it('keeps saved people and paging when a filter hides every loaded contact', () => {
    const merged = mergePeople({
      saved: { rows: [row('saved', 1, '2026-07-20T00:00:00Z'), row('saved', 2, '2026-07-01T00:00:00Z')], hasMore: false },
      // "Has name" hid the whole loaded page, which reached back to Jul 10.
      observed: { rows: [], hasMore: true, loadedThrough: '2026-07-10T00:00:00Z' },
    });
    expect(merged.rows.map((item) => item.key)).toEqual(['saved:1']);
    expect(merged.limitedBy).toEqual(['observed']);
  });

  it('keeps each source in its own relevance order while searching', () => {
    const { rows } = mergePeople({
      saved: { rows: [row('saved', 1, '2020-01-01T00:00:00Z')], hasMore: true },
      observed: { rows: [row('observed', 3, '2026-07-15T00:00:00Z')], hasMore: true },
      ranked: true,
    });
    expect(rows.map((item) => item.key)).toEqual(['saved:1', 'observed:3']);
  });

  it('shows one identifier line for a saved person and names unnamed records', () => {
    const saved = savedRow({
      id: 9, revision: 1, contact_state: 'inactive', categories: ['Family'], organizations: ['Example Org'],
      primary_identifier: { kind: 'email', value: 'ada@example.test' },
    });
    expect(saved).toMatchObject({ name: 'ada@example.test', identifier: { value: 'ada@example.test' }, meta: ['Example Org', 'Family'] });
    expect(looksUnnamed('ada@example.test')).toBe(true);
    expect(looksUnnamed('+1 555 555 0100')).toBe(true);
    expect(looksUnnamed('Ada', { kind: 'email', value: 'ada@example.test' })).toBe(false);
  });
  it('names a saved person without a display name through the label lookup, never by ID', async () => {
    const client = createAPIClient(withEntityLabels(vi.fn<typeof fetch>(), { person: { 9: 'Ada Example' } }));
    const names = entityNames(client);
    const person = { id: 9, revision: 1, contact_state: 'inactive', categories: [], organizations: [] };
    expect(savedRow(person, names).name).toBe(LOADING_LABEL);
    await names.load('person', [9]);
    expect(savedRow(person, names).name).toBe('Ada Example');
    expect(savedRow({ ...person, id: 10 }).name).toBe(UNKNOWN_LABELS.person);
  });
});

describe('ObservedContacts', () => {
  it('starts over when saved people changed between pages instead of skipping a contact', async () => {
    const cursors: Array<string | undefined> = [];
    const client = createAPIClient(vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      const body = await request.clone().json() as { cursor?: string };
      cursors.push(body.cursor);
      if (body.cursor) return Response.json({ error: 'saved_people_changed', message: 'restart' }, { status: 409 });
      return Response.json({ rows: [{
        canonical_id: 3, display_label: 'Bo Example', last_at: '2026-07-01T00:00:00Z', member_ids: [3], score: 1,
        signals: { last_interaction_at: '2026-07-01T00:00:00Z', meeting_count: 0, meetings_together: 0, modalities: 1,
          received_from_them: 0, sent_count: 1, sent_to_them: 1 },
      }], total_count: 2, cache_revision: 'c', identity_revision: 1, ...(cursors.length === 1 ? { next_cursor: 'page-2' } : {}) });
    }));
    const contacts = new ObservedContacts(client);
    await contacts.load('');
    await contacts.loadMore();
    await vi.waitFor(() => expect(cursors).toEqual([undefined, 'page-2', undefined]));
    await vi.waitFor(() => expect(contacts.loading).toBe(false));
    expect(contacts.error).toBeNull();
    expect(contacts.rows.map((row) => row.name)).toEqual(['Bo Example']);
  });
});

describe('Not people search', () => {
  it('matches a record by name, address, or organization', () => {
    const records = [
      { canonical_id: 1, member_ids: [1], kind: 'organization' as const, addresses: ['orders@shop.example.test'], display_name: 'Orders', organization_name: 'Example Shop' },
      { canonical_id: 2, member_ids: [2], kind: 'ignored' as const, addresses: ['news@example.test'], display_name: 'Weekly News' },
    ];
    expect(filterNotPeople(records, '').map((record) => record.canonical_id)).toEqual([1, 2]);
    expect(filterNotPeople(records, 'shop').map((record) => record.canonical_id)).toEqual([1]);
    expect(filterNotPeople(records, 'NEWS@').map((record) => record.canonical_id)).toEqual([2]);
    expect(filterNotPeople(records, 'weekly').map((record) => record.canonical_id)).toEqual([2]);
  });
});
