/** Owns one replaceable read. Aborting saves work; identity guards also cover
 * transports that complete after cancellation. Mutations need their own policy. */
export class RequestSlot {
  private current: AbortController | undefined;

  begin(): AbortController {
    this.cancel();
    const request = new AbortController();
    this.current = request;
    return request;
  }

  owns(request: AbortController): boolean {
    return this.current === request && !request.signal.aborted;
  }

  /** Only the active request may settle its owner's loading state. */
  finish(request: AbortController): boolean {
    if (!this.owns(request)) return false;
    this.current = undefined;
    return true;
  }

  cancel(): void {
    this.current?.abort();
    this.current = undefined;
  }
}
