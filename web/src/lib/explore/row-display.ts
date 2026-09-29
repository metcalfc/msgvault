import type { EntryRow } from './models';
import { freeTextTerms, unquoteValue } from '../search/query';

/** Who a row is with: the counterpart first (never the archive owner),
 * and how many others share it. Rows whose owner is unknown fall back to
 * the participant labels as the daemon listed them. */
export function rowPeople(row: EntryRow): { primary: string; others: number; title: string } {
  const labels = (row.participant_labels ?? []).map((label) => label.trim()).filter((label) => label !== '');
  const title = labels.join(', ');
  const counterpart = typeof row.counterpart_label === 'string' ? row.counterpart_label.trim() : '';
  if (counterpart) {
    // participant_ids includes the owner, so everyone else past the
    // counterpart is "others".
    const ids = row.participant_ids?.length ?? labels.length;
    return { primary: counterpart, others: Math.max(0, ids - 2), title: title || counterpart };
  }
  if (labels.length === 0) return { primary: row.source_identifier, others: 0, title: row.source_identifier };
  return { primary: labels.join(', '), others: 0, title };
}

/** The words and phrases a lexical search matched on, for highlighting.
 * Operators, one-letter words, and unknown op:value tokens are left out. */
export function highlightTerms(query: string): string[] {
  const terms = freeTextTerms(query)
    .map((token) => unquoteValue(token).trim())
    .filter((term) => term.length >= 2 && !term.includes(':'));
  return [...new Set(terms.map((term) => term.toLowerCase()))].sort((a, b) => b.length - a.length);
}

export interface TextSegment {
  text: string;
  match: boolean;
}

/** Splits text into plain and matched runs, case-insensitively. Rendering
 * the runs as text nodes and <mark> elements keeps archived content out of
 * innerHTML. */
export function highlightSegments(text: string, terms: readonly string[]): TextSegment[] {
  if (!text || terms.length === 0) return [{ text, match: false }];
  const pattern = new RegExp(terms.map((term) => term.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|'), 'giu');
  const segments: TextSegment[] = [];
  let last = 0;
  for (const found of text.matchAll(pattern)) {
    const start = found.index ?? 0;
    if (found[0].length === 0) continue;
    if (start > last) segments.push({ text: text.slice(last, start), match: false });
    segments.push({ text: found[0], match: true });
    last = start + found[0].length;
  }
  if (last < text.length) segments.push({ text: text.slice(last), match: false });
  return segments.length > 0 ? segments : [{ text, match: false }];
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** "5m ago", "3h ago", "Yesterday", "4d ago" (and "in 2d" for upcoming
 * events) within seven days; a short date beyond that. */
export function listTime(value: string, now: Date = new Date()): string {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return value;
  const delta = now.getTime() - date.getTime();
  const distance = Math.abs(delta);
  if (distance < 7 * DAY) {
    const future = delta < 0;
    if (distance < MINUTE) return future ? 'In a moment' : 'Just now';
    if (distance < HOUR) {
      const minutes = Math.floor(distance / MINUTE);
      return future ? `in ${minutes}m` : `${minutes}m ago`;
    }
    if (distance < DAY) {
      const hours = Math.floor(distance / HOUR);
      return future ? `in ${hours}h` : `${hours}h ago`;
    }
    const days = Math.floor(distance / DAY);
    if (days === 1) return future ? 'Tomorrow' : 'Yesterday';
    return future ? `in ${days}d` : `${days}d ago`;
  }
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    year: date.getFullYear() === now.getFullYear() ? undefined : 'numeric'
  }).format(date);
}

/** The same calendar event synced from more than one account shows as two
 * rows. Entry rows carry no iCalendar UID, so within the loaded page an
 * event with the same title and start time in another account counts as
 * the same event. Returns, per row key, the other accounts it also appears in. */
export function duplicateEventAccounts(rows: readonly EntryRow[]): Map<string, string[]> {
  const byEvent = new Map<string, EntryRow[]>();
  for (const row of rows) {
    if (row.message_type !== 'calendar_event' || !row.title) continue;
    const key = `${row.title.trim().toLowerCase()}\u0000${row.occurred_at}`;
    byEvent.set(key, [...(byEvent.get(key) ?? []), row]);
  }
  const result = new Map<string, string[]>();
  for (const group of byEvent.values()) {
    if (new Set(group.map((row) => row.source_id)).size < 2) continue;
    for (const row of group) {
      const others = [...new Set(group.filter((other) => other.source_id !== row.source_id).map((other) => other.source_identifier))];
      if (others.length > 0) result.set(row.key, others);
    }
  }
  return result;
}
