/**
 * Calendar events arrive as a serialized body (internal/calsync
 * serializeBody): the title, a "When:" line, a "Location:" line, the
 * description, and an "Attendees:" line of display names. The organizer is
 * the message sender and attendees are its recipients. This splits the
 * body back into those parts for a structured event header.
 */
export interface EventBody {
  when?: EventWhen;
  location: string;
  description: string;
}

export interface EventWhen {
  start: Date;
  end?: Date;
  allDay: boolean;
}

/** "YYYY-MM-DD HH:MM" as the event's wall-clock time. */
function wallClock(value: string): Date | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?: (\d{2}):(\d{2}))?$/.exec(value.trim());
  if (!match) return undefined;
  const [, year, month, day, hour = '0', minute = '0'] = match;
  return new Date(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute));
}

function parseWhen(value: string): EventWhen | undefined {
  const allDay = /\(all day\)\s*$/.test(value);
  const [startText = '', endText] = value.replace(/\(all day\)\s*$/, '').split(' - ');
  const start = wallClock(startText);
  if (!start) return undefined;
  const end = endText ? wallClock(endText) : undefined;
  return { start, end, allDay };
}

/**
 * The "When:" line is written in the event's own time zone with no offset,
 * so it cannot be read as the viewer's local time. A timed event's start
 * comes from the message's sent time (the event's start instant); the line
 * contributes only the duration. An all-day event keeps its calendar date.
 */
function anchorWhen(when: EventWhen | undefined, startInstant: string | undefined): EventWhen | undefined {
  if (!when || when.allDay || !startInstant) return when;
  const start = new Date(startInstant);
  if (Number.isNaN(start.valueOf())) return when;
  const duration = when.end ? when.end.getTime() - when.start.getTime() : undefined;
  return {
    start,
    end: duration !== undefined && duration >= 0 ? new Date(start.getTime() + duration) : undefined,
    allDay: false
  };
}

export function parseEventBody(body: string, title: string, startInstant: string | undefined = undefined): EventBody {
  const lines = body.split('\n');
  if (lines[0]?.trim() && lines[0].trim() === title.trim()) lines.shift();
  let when: EventWhen | undefined;
  let location = '';
  const kept: string[] = [];
  for (const [index, line] of lines.entries()) {
    if (!when && line.startsWith('When: ')) {
      when = parseWhen(line.slice(6));
      if (when) continue;
    }
    if (!location && line.startsWith('Location: ')) {
      location = line.slice(10).trim();
      continue;
    }
    // Attendee names are shown from the recipients as pills instead.
    if (index === lines.length - 1 && line.startsWith('Attendees: ')) continue;
    kept.push(line);
  }
  return { when: anchorWhen(when, startInstant), location, description: kept.join('\n').trim() };
}

/** "Tue, Jul 18, 2026 · 10:00 – 10:30", or across days, both ends. */
export function humanizeWhen(when: EventWhen, timeZone: string | undefined = undefined): string {
  if (when.allDay) {
    // An all-day date is a calendar day, not an instant: show it as written.
    const allDay = new Intl.DateTimeFormat(undefined, { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric' });
    return `${allDay.format(when.start)} · All day`;
  }
  const day = new Intl.DateTimeFormat(undefined, { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric', timeZone });
  const clock = new Intl.DateTimeFormat(undefined, { timeStyle: 'short', timeZone });
  const dayKey = new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', day: '2-digit', timeZone });
  if (!when.end) return `${day.format(when.start)} · ${clock.format(when.start)}`;
  if (dayKey.format(when.end) === dayKey.format(when.start)) {
    return `${day.format(when.start)} · ${clock.format(when.start)} – ${clock.format(when.end)}`;
  }
  return `${day.format(when.start)} ${clock.format(when.start)} – ${day.format(when.end)} ${clock.format(when.end)}`;
}

export interface LinkSegment {
  text: string;
  href?: string;
}

/** Splits text into plain runs and http(s) links, for rendering as text
 * nodes and anchors rather than injected HTML. */
export function linkify(text: string): LinkSegment[] {
  const segments: LinkSegment[] = [];
  let last = 0;
  for (const found of text.matchAll(/https?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)\]]/g)) {
    const start = found.index ?? 0;
    if (start > last) segments.push({ text: text.slice(last, start) });
    segments.push({ text: found[0], href: found[0] });
    last = start + found[0].length;
  }
  if (last < text.length) segments.push({ text: text.slice(last) });
  return segments;
}
