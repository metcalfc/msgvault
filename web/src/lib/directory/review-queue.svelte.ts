import { SvelteSet } from 'svelte/reactivity';
import { failureMessage } from '../api/failure-message';
import type { APIResponse } from '../api/runtime';
import { RequestSlot } from '../util/request-slot';

export type ReviewDecision<T> = { ok: true; decision: T } | { ok: false; message: string };

export function reviewDecision<T>(response: APIResponse<T>): ReviewDecision<T> {
  return response.data !== undefined
    ? { ok: true, decision: response.data }
    : { ok: false, message: failureMessage(response.error, response.response.status) };
}

/** Shared lifecycle for queues whose successful decisions remove a row.
 * Reads may be replaced, but mutations finish independently. A late read
 * cannot restore a decided row. Paged queues refill once all decisions settle. */
export class ReviewQueue<Row> {
  rows = $state<Row[]>([]);
  loading = $state(false);
  loaded = $state(false);
  error = $state<string | null>(null);
  decisionError = $state<string | null>(null);
  status = $state<string | null>(null);
  readonly pending = new SvelteSet<number>();
  private readonly read = new RequestSlot();
  private readonly decided = new Set<number>();
  private disposed = false;

  constructor(
    private readonly rowID: (row: Row) => number,
    private readonly readRows: (signal: AbortSignal) => Promise<Row[]>,
    private readonly refillWhenEmpty = false,
  ) {}

  isPending(id: number): boolean {
    return this.pending.has(id);
  }

  async load(): Promise<void> {
    if (this.disposed) return;
    const request = this.read.begin();
    this.loading = true;
    this.error = null;
    try {
      const rows = await this.readRows(request.signal);
      if (!this.read.owns(request)) return;
      this.rows = rows.filter((row) => !this.decided.has(this.rowID(row)));
      this.loaded = true;
    } catch (cause: unknown) {
      if (this.read.owns(request)) this.error = failureMessage(cause, 0);
    } finally {
      if (this.read.finish(request)) this.loading = false;
    }
  }

  destroy(): void {
    this.disposed = true;
    this.read.cancel();
    this.loading = false;
    this.pending.clear();
  }

  protected async decideRow<T>(
    id: number,
    mutate: () => Promise<ReviewDecision<T>>,
    describe: (decision: T, row: Row | undefined) => string | Promise<string>,
  ): Promise<ReviewDecision<T>> {
    if (this.disposed || this.pending.has(id)) {
      return { ok: false, message: 'A decision is already pending.' };
    }
    const row = this.rows.find((candidate) => this.rowID(candidate) === id);
    this.pending.add(id);
    this.decisionError = null;
    this.status = null;
    try {
      const outcome = await mutate();
      if (this.disposed) return { ok: false, message: 'Review closed.' };
      if (!outcome.ok) {
        this.decisionError = outcome.message;
        return outcome;
      }
      this.decided.add(id);
      this.rows = this.rows.filter((candidate) => this.rowID(candidate) !== id);
      const status = await describe(outcome.decision, row);
      if (this.disposed) return { ok: false, message: 'Review closed.' };
      this.status = status;
      return outcome;
    } catch (cause: unknown) {
      const message = failureMessage(cause, 0);
      if (!this.disposed) this.decisionError = message;
      return { ok: false, message };
    } finally {
      this.pending.delete(id);
      if (this.refillWhenEmpty && !this.disposed && this.decided.has(id) && this.pending.size === 0 && this.rows.length === 0) {
        // A refill failure belongs to the list's Retry control; never repeat
        // a committed mutation to recover a read.
        await this.load();
      }
    }
  }
}
