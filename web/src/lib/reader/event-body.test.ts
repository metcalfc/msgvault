import { describe, expect, it } from 'vitest';

import { humanizeWhen, parseEventBody } from './event-body';

describe('parseEventBody', () => {
  // A 10:00–10:30 meeting in New York (UTC−4 in July), written in its own zone.
  const body = 'Planning\nWhen: 2026-07-18 10:00 - 2026-07-18 10:30\nLocation: Room 4\nBring the deck';

  it('takes the start instant from the sent time and only the duration from the When line', () => {
    const parsed = parseEventBody(body, 'Planning', '2026-07-18T14:00:00Z');
    expect(parsed.when?.start.toISOString()).toBe('2026-07-18T14:00:00.000Z');
    expect(parsed.when?.end?.toISOString()).toBe('2026-07-18T14:30:00.000Z');
    expect(parsed.location).toBe('Room 4');
    expect(parsed.description).toBe('Bring the deck');
  });

  it.each([
    ['America/New_York', /10:00\s?AM.*10:30\s?AM/],
    ['America/Los_Angeles', /7:00\s?AM.*7:30\s?AM/],
    ['Europe/London', /3:00\s?PM.*3:30\s?PM/],
  ])('shows the event in the viewer zone %s', (zone, pattern) => {
    const parsed = parseEventBody(body, 'Planning', '2026-07-18T14:00:00Z');
    expect(humanizeWhen(parsed.when!, zone)).toMatch(pattern);
  });

  it('keeps an all-day date as written', () => {
    const parsed = parseEventBody('Offsite\nWhen: 2026-07-18 (all day)', 'Offsite', '2026-07-18T04:00:00Z');
    expect(parsed.when?.allDay).toBe(true);
    expect(parsed.when?.start.getDate()).toBe(18);
  });
});
