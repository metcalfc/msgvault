import { describe, expect, it } from 'vitest';

import { looksUnnamed, mergePeople, savedRow, type PeopleRow } from './hub.svelte';

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
});
