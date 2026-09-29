import { describe, expect, it } from 'vitest';

import {
  effectiveSearchMode, extractQueryFilters, freeTextTerms, hasFreeText, queryOperatorChips, tokenizeQuery,
  withAttachmentOperator, withoutQueryToken
} from './query';

describe('extractQueryFilters', () => {
  it('moves operators with a filter dimension into chips and keeps the rest as text', () => {
    const existing = [{ dimension: 'after' as const, values: ['2020-01-01T00:00:00.000Z'] }];
    const extracted = extractQueryFilters(
      'budget after:2025-01-01 before:2025/06/30 message_type:imessage from:alice@example.com subject:"q3 plan"',
      existing,
    );

    expect(extracted.moved).toBe(true);
    expect(extracted.query).toBe('budget from:alice@example.com subject:"q3 plan"');
    expect(extracted.filters).toEqual([
      { dimension: 'after', values: [new Date(2025, 0, 1).toISOString()] },
      // before: excludes its day, so the bound is that day's first instant.
      { dimension: 'before', values: [new Date(2025, 5, 30).toISOString()] },
      { dimension: 'message_type', values: ['imessage'] },
    ]);
  });

  it('keeps list:/list-id: as text, since the daemon matches them as substrings', () => {
    const extracted = extractQueryFilters('list:golang-nuts list-id:Team.Example budget', []);
    expect(extracted).toEqual({ query: 'list:golang-nuts list-id:Team.Example budget', filters: [], moved: false });
    expect(queryOperatorChips(extracted.query).map((chip) => chip.label)).toEqual(['List: golang-nuts', 'List: Team.Example']);
  });

  it('merges repeated message types and leaves unparseable dates as text', () => {
    const extracted = extractQueryFilters('message_type:sms message_type=imessage after:yesterday', [
      { dimension: 'message_type', values: ['sms'] },
    ]);
    expect(extracted.query).toBe('after:yesterday');
    expect(extracted.filters).toEqual([{ dimension: 'message_type', values: ['sms', 'imessage'] }]);
  });

  it.each(['after:2025-02-30', 'before:2025-13-01', 'after:2025-00-10', 'before:2024-02-29x'])(
    'leaves the impossible date %s as query text for the daemon to reject',
    (query) => {
      expect(extractQueryFilters(query, [])).toEqual({ query, filters: [], moved: false });
    }
  );

  it('accepts a real leap day', () => {
    expect(extractQueryFilters('after:2024-02-29', []).filters).toEqual([
      { dimension: 'after', values: [new Date(2024, 1, 29).toISOString()] }
    ]);
  });

  it('leaves a query with nothing to move untouched', () => {
    const extracted = extractQueryFilters("has:attachment 'exact words'", []);
    expect(extracted).toEqual({ query: "has:attachment 'exact words'", filters: [], moved: false });
  });
});

describe('query operator chips', () => {
  it('labels each text operator and removes one token at a time', () => {
    const query = 'budget from:alice@example.com has:attachment larger:5M';
    expect(queryOperatorChips(query).map((chip) => chip.label)).toEqual([
      'From: alice@example.com', 'Has attachment', 'Larger than: 5M',
    ]);
    const [from] = queryOperatorChips(query);
    expect(withoutQueryToken(query, from!.index)).toBe('budget has:attachment larger:5M');
  });

  it('toggles has:attachment without duplicating it', () => {
    expect(withAttachmentOperator('notes', true)).toBe('notes has:attachment');
    expect(withAttachmentOperator('notes has:attachment', true)).toBe('notes has:attachment');
    expect(withAttachmentOperator('notes has:attachments', false)).toBe('notes');
  });
});

describe('tokenizeQuery', () => {
  it.each([
    ['alpha beta', ['alpha', 'beta']],
    ['"quarterly plan" from:alice@example.com', ['"quarterly plan"', 'from:alice@example.com']],
    ['subject:"board meeting" notes', ['subject:"board meeting"', 'notes']],
    ["'single quoted'", ['"single quoted"']],
    ['word"phrase here"', ['word', '"phrase here"']],
    ['  spaced   out  ', ['spaced', 'out']],
    ['"unterminated phrase', ['unterminated phrase']],
  ])('splits %j like the daemon', (query, want) => {
    expect(tokenizeQuery(query)).toEqual(want);
  });
});

describe('free text', () => {
  it.each([
    ['from:alice@example.com after:2025-01-01', []],
    ['has:attachment subject:"board meeting"', []],
    ['message_type=imessage', []],
    ['vacation plans from:alice@example.com', ['vacation', 'plans']],
    ['"from:alice"', ['"from:alice"']],
    ['https://example.com/path', ['https://example.com/path']],
    ['unknown:thing', ['unknown:thing']],
  ])('finds the free text in %j', (query, want) => {
    expect(freeTextTerms(query)).toEqual(want);
    expect(hasFreeText(query)).toBe(want.length > 0);
  });
});

describe('effectiveSearchMode', () => {
  it.each([
    ['from:alice@example.com', 'hybrid', 'full_text'],
    ['from:alice@example.com', 'semantic', 'full_text'],
    ['from:alice@example.com', 'full_text', 'full_text'],
    ['trip ideas from:alice@example.com', 'hybrid', 'hybrid'],
    ['', 'semantic', 'semantic'],
  ] as const)('runs %j chosen as %s as %s', (query, mode, want) => {
    expect(effectiveSearchMode(query, mode)).toBe(want);
  });
});
