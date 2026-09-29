import { describe, expect, it } from 'vitest';

import { highlightSegments, highlightTerms, listTime } from './row-display';

describe('listTime', () => {
  // Local times, so calendar days are the viewer's days in any zone.
  const now = new Date(2026, 6, 18, 12, 0);
  const weekday = (date: Date) => new Intl.DateTimeFormat(undefined, { weekday: 'short' }).format(date);
  it.each([
    [new Date(2026, 6, 18, 11, 59, 30), 'Just now'],
    [new Date(2026, 6, 18, 11, 15), '45m ago'],
    [new Date(2026, 6, 18, 2, 0), '10h ago'],
    [new Date(2026, 6, 17, 6, 0), 'Yesterday'],
    [new Date(2026, 6, 19, 9, 0), 'Tomorrow'],
    [new Date(2026, 6, 20, 13, 0), weekday(new Date(2026, 6, 20))],
    [new Date(2026, 6, 14, 12, 0), weekday(new Date(2026, 6, 14))],
  ])('reads %s by the calendar within a week', (date, want) => {
    expect(listTime(date.toISOString(), now)).toBe(want);
  });

  it('counts calendar days, not 24-hour spans', () => {
    // At 08:00, a message 47 hours old was sent the day before yesterday.
    const morning = new Date(2026, 6, 18, 8, 0);
    const sent = new Date(morning.getTime() - 47 * 3_600_000);
    expect(listTime(sent.toISOString(), morning)).toBe(weekday(sent));
    // Late last night is Yesterday even though under 24 hours have passed.
    expect(listTime(new Date(2026, 6, 17, 23, 0).toISOString(), morning)).toBe('Yesterday');
  });

  it('falls back to a short date beyond a week', () => {
    expect(listTime(new Date(2026, 6, 1, 12, 0).toISOString(), now)).not.toMatch(/ago|in |day|Mon|Tue|Wed|Thu|Fri|Sat|Sun/);
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
