import { describe, expect, it } from 'vitest';

import { compactDate, humanizeDate, shortDate } from './dates';

const now = new Date('2026-07-19T12:00:00Z');

describe('compactDate', () => {
  it('formats sub-hour ages in minutes with a floor of one minute', () => {
    expect(compactDate('2026-07-19T11:59:59Z', now)).toBe('1m');
    expect(compactDate('2026-07-19T11:35:00Z', now)).toBe('25m');
  });

  it('formats same-day ages in hours', () => {
    expect(compactDate('2026-07-19T09:00:00Z', now)).toBe('3h');
    expect(compactDate('2026-07-18T13:00:00Z', now)).toBe('23h');
  });

  it('formats the past week in days', () => {
    expect(compactDate('2026-07-17T12:00:00Z', now)).toBe('2d');
    expect(compactDate('2026-07-12T12:00:01Z', now)).toBe('6d');
  });

  // Day labels are rendered in the local timezone, so these assert the
  // month + day-number shape (the day can shift ±1 across timezones)
  // rather than one exact day.
  it('elides the year for older dates within the current year', () => {
    expect(compactDate('2026-06-15T12:00:00Z', now)).toMatch(/^Jun 1[456]$/);
    expect(compactDate('2026-01-15T12:00:00Z', now)).toMatch(/^Jan 1[456]$/);
  });

  it('collapses prior years to just the year', () => {
    expect(compactDate('2024-11-05T12:00:00Z', now)).toBe('2024');
    expect(compactDate('1999-06-15T12:00:00Z', now)).toBe('1999');
  });

  it('passes unparseable input through unchanged', () => {
    expect(compactDate('not-a-date', now)).toBe('not-a-date');
    expect(compactDate('', now)).toBe('');
  });

  it('renders slightly-future timestamps (clock skew) as a short date, never a negative age', () => {
    expect(compactDate('2026-07-19T12:05:00Z', now)).toMatch(/^Jul (18|19|20)$/);
  });
});

describe('humanizeDate', () => {
  it('reads recent past as an age and the near future as a countdown', () => {
    expect(humanizeDate('2026-07-19T11:59:30Z', now)).toBe('just now');
    expect(humanizeDate('2026-07-19T11:35:00Z', now)).toBe('25m ago');
    expect(humanizeDate('2026-07-19T09:00:00Z', now)).toBe('3h ago');
    expect(humanizeDate('2026-07-17T12:00:00Z', now)).toBe('2d ago');
    expect(humanizeDate('2026-07-19T15:00:00Z', now)).toBe('in 3h');
    expect(humanizeDate('2026-07-22T12:00:00Z', now)).toBe('in 3d');
  });

  it('falls back to a short date, adding the year only when it differs', () => {
    expect(humanizeDate('2026-06-15T12:00:00Z', now)).toMatch(/^Jun 1[456]$/);
    expect(humanizeDate('2027-05-11T00:00:00Z', now)).toMatch(/^May 1[012], 2027$/);
    expect(humanizeDate('2024-01-15T12:00:00Z', now)).toMatch(/^Jan 1[456], 2024$/);
  });

  it('renders empty input as a dash and keeps unparseable input visible', () => {
    expect(humanizeDate(undefined, now)).toBe('—');
    expect(humanizeDate('', now)).toBe('—');
    expect(humanizeDate('not a date', now)).toBe('not a date');
  });
});

describe('shortDate', () => {
  it('shows month and day, adding the year only when it differs', () => {
    expect(shortDate('2026-06-15T12:00:00Z', now)).toMatch(/^Jun 1[456]$/);
    expect(shortDate('2027-05-11T12:00:00Z', now)).toMatch(/^May 1[012], 2027$/);
    expect(shortDate('garbage', now)).toBe('garbage');
  });
});
