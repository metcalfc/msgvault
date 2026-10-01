/** The daemon's error text, with a stable fallback for transport failures. */
export function failureMessage(error: unknown, status: number): string {
  if (typeof error === 'object' && error !== null && 'message' in error && typeof error.message === 'string') {
    return error.message;
  }
  if (error instanceof Error && error.message) return error.message;
  return status > 0 ? `Request failed (${status}).` : 'Request failed.';
}
