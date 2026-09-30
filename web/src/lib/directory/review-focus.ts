import { tick } from 'svelte';

/**
 * After a review decision, focus the card now at `index` in the queue —
 * the next one to review — without scrolling the page. An index past the
 * end focuses the last card; a queue with no cards left focuses
 * `fallback`, normally its heading.
 */
export async function focusReviewCard(
  list: HTMLElement | undefined,
  index: number,
  fallback: HTMLElement | null | undefined,
): Promise<void> {
  await tick();
  const cards = Array.from(list?.querySelectorAll<HTMLElement>('[data-review-card]') ?? []);
  const target = cards[Math.min(Math.max(index, 0), cards.length - 1)] ?? fallback;
  if (target?.isConnected) target.focus({ preventScroll: true });
}

/** Where a decided row sat before the decision was sent: its index and
 * the row after it. Capture it before submitting, since the queue reloads
 * before the decision returns. */
export interface ReviewPosition<K> {
  index: number;
  nextKey?: K;
}

export function reviewPosition<T, K>(rows: readonly T[], key: (row: T) => K, decided: K): ReviewPosition<K> {
  const index = Math.max(0, rows.findIndex((row) => key(row) === decided));
  const next = rows[index + 1];
  return next === undefined ? { index } : { index, nextKey: key(next) };
}

/** The card to focus once the queue settles: the row after the decided
 * one while it stays listed; otherwise the row that followed it; otherwise
 * whichever row took its place (the last one when the queue got shorter). */
export function nextReviewIndex<T, K>(
  rows: readonly T[],
  key: (row: T) => K,
  decided: K,
  position: ReviewPosition<K>,
): number {
  const at = rows.findIndex((row) => key(row) === decided);
  if (at >= 0) return at + 1 < rows.length ? at + 1 : at;
  if (position.nextKey !== undefined) {
    const next = rows.findIndex((row) => key(row) === position.nextKey);
    if (next >= 0) return next;
  }
  return Math.min(position.index, Math.max(rows.length - 1, 0));
}
