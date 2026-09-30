import { tick } from 'svelte';

/**
 * After a review decision, focus the card now at `index` in the queue —
 * the next one to review — without scrolling the page. A queue with no
 * cards left focuses `fallback`, normally its heading.
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

/** The card to focus once a decided row settles: the row after it while
 * the decided row stays listed, otherwise whichever row took its place. */
export function nextReviewIndex<T>(rows: readonly T[], decided: (row: T) => boolean, originalIndex: number): number {
  const at = rows.findIndex(decided);
  if (at < 0) return originalIndex;
  return at + 1 < rows.length ? at + 1 : at;
}
