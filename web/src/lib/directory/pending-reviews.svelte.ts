import { getPendingReviews } from '../api/generated/api/api';
import type { APIClient } from '../api/client';

/** The fewest milliseconds between two unforced checks. */
export const PENDING_REVIEWS_MIN_INTERVAL_MS = 60_000;

/**
 * Whether anything waits in Reviews, for the navigation dot. The daemon
 * answers with one indexed lookup per queue. It checks on start, when the
 * window regains focus or the tab becomes visible, and on a poll, never more
 * often than once a minute; a review decision forces a check at once. A
 * hidden tab neither polls nor checks.
 */
export class PendingReviewsMonitor {
  /** Unit tests turn this off so shells they render send no extra
   * requests; tests of the dot turn it back on. */
  static autoStart = true;
  waiting = $state(false);
  private readonly client: APIClient;
  private readonly now: () => number;
  private readonly isHidden: () => boolean;
  private lastCheck = Number.NEGATIVE_INFINITY;
  private inFlight: AbortController | undefined;
  private timer: ReturnType<typeof setInterval> | undefined;
  private stopListening: (() => void) | undefined;

  constructor(
    client: APIClient,
    now: () => number = () => Date.now(),
    isHidden: () => boolean = () => typeof document !== 'undefined' && document.hidden
  ) {
    this.client = client;
    this.now = now;
    this.isHidden = isHidden;
  }

  start(target: Window = window, page: Document = document): void {
    if (this.stopListening !== undefined || !PendingReviewsMonitor.autoStart) return;
    const onFocus = () => void this.refresh();
    const onVisibility = () => {
      if (this.isHidden()) {
        this.stopPolling();
        return;
      }
      this.startPolling();
      void this.refresh();
    };
    target.addEventListener('focus', onFocus);
    page.addEventListener('visibilitychange', onVisibility);
    this.stopListening = () => {
      target.removeEventListener('focus', onFocus);
      page.removeEventListener('visibilitychange', onVisibility);
    };
    if (!this.isHidden()) {
      this.startPolling();
      void this.refresh(true);
    }
  }

  stop(): void {
    this.stopPolling();
    this.stopListening?.();
    this.stopListening = undefined;
    this.inFlight?.abort();
    this.inFlight = undefined;
  }

  /** Checks again unless the tab is hidden or the last check was under a
   * minute ago; force skips that wait (after a review decision). A failed
   * check keeps the last answer. */
  async refresh(force = false): Promise<void> {
    if (!PendingReviewsMonitor.autoStart || this.isHidden()) return;
    const now = this.now();
    if (!force && now - this.lastCheck < PENDING_REVIEWS_MIN_INTERVAL_MS) return;
    this.lastCheck = now;
    this.inFlight?.abort();
    const abort = new AbortController();
    this.inFlight = abort;
    try {
      const { data } = await getPendingReviews({ ...this.client, signal: abort.signal });
      if (abort.signal.aborted || !data) return;
      this.waiting = data.pending === true;
    } catch {
      // Keep the last answer; the next check tries again.
    } finally {
      if (this.inFlight === abort) this.inFlight = undefined;
    }
  }

  /** True while the minute poll runs. */
  get polling(): boolean {
    return this.timer !== undefined;
  }

  private startPolling(): void {
    if (this.timer !== undefined) return;
    this.timer = setInterval(() => void this.refresh(), PENDING_REVIEWS_MIN_INTERVAL_MS);
  }

  private stopPolling(): void {
    if (this.timer !== undefined) clearInterval(this.timer);
    this.timer = undefined;
  }
}
