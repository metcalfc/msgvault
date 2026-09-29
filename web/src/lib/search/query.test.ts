import { describe, expect, it } from 'vitest';

import { effectiveSearchMode, freeTextTerms, hasFreeText, tokenizeQuery } from './query';

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
