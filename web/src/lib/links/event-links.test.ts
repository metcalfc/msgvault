import { describe, expect, it } from 'vitest';

import { eventCalendarLink, eventJoinLink } from './event-links';

describe('eventJoinLink', () => {
  it.each([
    ['https://meet.google.com/abc-defg-hij', 'https://meet.google.com/abc-defg-hij'],
    ['https://zoom.us/j/123456789', 'https://zoom.us/j/123456789'],
    ['https://example.zoom.us/j/123456789?pwd=x', 'https://example.zoom.us/j/123456789?pwd=x'],
    ['https://teams.microsoft.com/l/meetup-join/abc', 'https://teams.microsoft.com/l/meetup-join/abc'],
    ['https://teams.live.com/meet/123', 'https://teams.live.com/meet/123'],
    ['http://meet.google.com/abc-defg-hij', 'https://meet.google.com/abc-defg-hij'],
    ['https://MEET.Google.COM/abc-defg-hij', 'https://meet.google.com/abc-defg-hij']
  ])('allows %s', (raw, href) => {
    expect(eventJoinLink(raw)).toEqual({ href, label: 'Join meeting', external: true });
  });

  it.each([
    'https://evil.example/meet.google.com',
    'https://meet.google.com.evil.example/abc',
    'https://notzoom.us/j/1',
    'https://zoom.us.evil.example/j/1',
    'https://user:pass@meet.google.com/abc',
    'https://meet.google.com@evil.example/abc',
    'https://meet.google.com./abc',
    'https://evil.zoom.us./j/1',
    'https://zoom.us:443@evil.example/j/1',
    'javascript:alert(1)',
    '',
    undefined
  ])('refuses %s', (raw) => {
    expect(eventJoinLink(raw)).toBeUndefined();
  });
});

describe('eventCalendarLink', () => {
  it('allows Google Calendar event pages', () => {
    expect(eventCalendarLink('https://www.google.com/calendar/event?eid=abc')?.href)
      .toBe('https://www.google.com/calendar/event?eid=abc');
    expect(eventCalendarLink('https://calendar.google.com/calendar/event?eid=abc')?.label).toBe('Open in Calendar');
  });

  it('refuses other hosts and non-calendar Google paths', () => {
    expect(eventCalendarLink('https://www.google.com/url?q=https://evil.example')).toBeUndefined();
    expect(eventCalendarLink('https://calendar.evil.example/event')).toBeUndefined();
    expect(eventCalendarLink('data:text/html,hi')).toBeUndefined();
    expect(eventCalendarLink('https://user:secret@calendar.google.com/calendar/event?eid=abc')).toBeUndefined();
    expect(eventCalendarLink('https://calendar.google.com@evil.example/calendar/event')).toBeUndefined();
  });
});
