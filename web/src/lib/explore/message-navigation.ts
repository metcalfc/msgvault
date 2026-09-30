import type { APIClient } from '../api/client';
import { getMessage } from '../api/generated/api/api';
import type { MessageDetail } from '../api/generated/models';
import { RequestSlot } from '../util/request-slot';
import { messageEntryKey, messageRowFilters } from './entry-key';
import type { ExploreURLState } from './models';

/** Resolves Directory message references into a restorable archive selection.
 * The shell supplies navigation and announcements; this owner handles reads. */
export class MessageNavigation {
  private readonly request = new RequestSlot();
  private origin: string | undefined;
  private disposed = false;

  constructor(
    private readonly client: APIClient,
    private readonly currentOrigin: () => string,
    private readonly navigate: (patch: Partial<ExploreURLState>) => void,
    private readonly announce: (message: string) => void
  ) {}

  reconcileOrigin(current: string): void {
    if (this.origin !== undefined && this.origin !== current) {
      this.request.cancel();
      this.origin = undefined;
    }
  }

  async open(messageID: number): Promise<void> {
    if (this.disposed) return;
    const origin = this.currentOrigin();
    const request = this.request.begin();
    this.origin = origin;
    let data: MessageDetail | undefined;
    let status: number | undefined;
    try {
      ({
        data,
        response: { status }
      } = await getMessage({ id: messageID }, { ...this.client, signal: request.signal }));
    } catch {
      data = undefined;
    }
    if (!this.request.finish(request)) return;
    this.origin = undefined;
    if (origin !== this.currentOrigin()) return;
    if (!data) {
      this.announce(
        status === 404
          ? "Couldn't open that message: it is no longer in the archive."
          : "Couldn't open that message: the archive did not respond."
      );
      return;
    }
    const key = messageEntryKey(data);
    if (!key) {
      this.announce("Couldn't open that message: the archive has no row for it.");
      return;
    }
    this.navigate({
      workspace: 'everything',
      presentation: 'table',
      query: '',
      groupingChain: [],
      filters: messageRowFilters(data),
      selectedRow: key,
      conversationAnchor: String(data.id),
      analysisTarget: null,
      selectedIdentifier: null,
      activeRow: null,
      scrollAnchor: null
    });
  }

  destroy(): void {
    this.disposed = true;
    this.request.cancel();
    this.origin = undefined;
  }
}
