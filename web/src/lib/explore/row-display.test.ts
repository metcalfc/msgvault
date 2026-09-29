import { describe, expect, it } from 'vitest';

import { highlightSegments, highlightTerms, listTime } from './row-display';

describe('listTime', () => {
  const now = new Date('2026-07-18T12:00:00Z');
  it.each([
    ['2026-07-18T11:59:30Z', 'Just now'],
    ['2026-07-18T11:15:00Z', '45m ago'],
    ['2026-07-18T02:00:00Z', '10h ago'],
    ['2026-07-17T06:00:00Z', 'Yesterday'],
    ['2026-07-14T12:00:00Z', '4d ago'],
    ['2026-07-20T13:00:00Z', 'in 2d'],
  ])('reads %s as %s within a week', (value, want) => {
    expect(listTime(value, now)).toBe(want);
  });

  it('falls back to a short date beyond a week', () => {
    expect(listTime('2026-07-01T12:00:00Z', now)).not.toMatch(/ago|in /);
  });
});

describe('highlight', () => {
  it('takes free-text words and phrases, not operators', () => {
    expect(highlightTerms('"q3 plan" budget a from:alice@example.com')).toEqual(['q3 plan', 'budget']);
  });

  it('splits text into matched and plain runs without regex injection', () => {
    expect(highlightSegments('Cost (a+b) and COST', ['cost', '(a+b)'])).toEqual([
      { text: 'Cost', match: true },
      { text: ' ', match: false },
      { text: '(a+b)', match: true },
      { text: ' and ', match: false },
      { text: 'COST', match: true },
    ]);
  });
});
