import { formatTimestamp } from '@kenn-io/kit-ui';

/** Superhuman-style compact timestamp for list rows: recent activity reads
 * as an age ("5m", "3h", "2d"), older activity this year as a short date
 * ("Jun 29"), and anything before this year collapses to the year ("2024").
 * Unparseable input passes through untouched so raw API values stay visible
 * instead of turning into "Invalid Date". */
export function compactDate(value: string, now: Date = new Date()): string {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return value;

  const elapsedMs = now.getTime() - date.getTime();
  const minuteMs = 60_000;
  const hourMs = 60 * minuteMs;
  const dayMs = 24 * hourMs;

  if (elapsedMs >= 0 && elapsedMs < hourMs) {
    return `${Math.max(1, Math.floor(elapsedMs / minuteMs))}m`;
  }
  if (elapsedMs >= 0 && elapsedMs < dayMs) {
    return `${Math.floor(elapsedMs / hourMs)}h`;
  }
  if (elapsedMs >= 0 && elapsedMs < 7 * dayMs) {
    return `${Math.floor(elapsedMs / dayMs)}d`;
  }
  if (date.getFullYear() === now.getFullYear()) {
    return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric' }).format(date);
  }
  return String(date.getFullYear());
}

/** Sentence-friendly timestamp for summaries and detail rows: "just now",
 * "25m ago", "3h ago", "2d ago", "in 3d" for the near future, then a short
 * date ("Jun 29") that carries the year once it differs from now's ("Jun 29,
 * 2024"). Empty input reads as "—"; unparseable input passes through.
 *
 * A thin wrapper: the age is compactDate's (mirrored for the near future)
 * and the calendar fallback is shortDate's. Kit's formatRelativeTime is not
 * used because it reads Date.now() and has no future tense, and callers
 * and tests inject `now`. */
export function humanizeDate(value: string | null | undefined, now: Date = new Date()): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return value;

  const elapsedMs = now.getTime() - date.getTime();
  const minuteMs = 60_000;
  const weekMs = 7 * 24 * 60 * minuteMs;
  if (Math.abs(elapsedMs) < minuteMs) return 'just now';
  if (Math.abs(elapsedMs) >= weekMs) return shortDate(value, now);
  return elapsedMs > 0
    ? `${compactDate(value, now)} ago`
    : `in ${compactDate(now.toISOString(), date)}`;
}

/** Kit's readable timestamp ("Aug 29, 01:00") for a stored ISO instant on a
 * detail row, carrying the year once it differs from now's ("Aug 29, 2024,
 * 01:00"). Empty input reads as "—"; unparseable input passes through so a
 * raw value stays visible instead of throwing on an invalid date. */
export function stampText(value: string | null | undefined, now: Date = new Date()): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return value;
  if (date.getFullYear() === now.getFullYear()) return formatTimestamp(value);
  return new Intl.DateTimeFormat(undefined, {
    month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit'
  }).format(date);
}

/** Short calendar date for crumbs and bounds: "Sep 22", or "Sep 22, 2024"
 * once the year differs from now's. Unparseable input passes through. */
export function shortDate(value: string, now: Date = new Date()): string {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return value;
  return new Intl.DateTimeFormat('en-US', {
    month: 'short', day: 'numeric', ...(date.getFullYear() === now.getFullYear() ? {} : { year: 'numeric' })
  }).format(date);
}
