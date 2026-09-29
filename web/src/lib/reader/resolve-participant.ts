import { completeParticipants } from '../api/generated/api/api';
import type { APIClient } from '../api/client';

function normalized(value: string): string {
  const trimmed = value.trim().toLowerCase();
  const digits = trimmed.replace(/[^0-9]/g, '');
  // Phone numbers compare by digits so "+1 (555) 555-0101" matches "+15555550101".
  return /^[+()\d\s.-]+$/.test(trimmed) && digits.length >= 7 ? digits : trimmed;
}

/**
 * Finds the observed person behind an archived address or phone number.
 * The reader's message detail carries addresses, not participant IDs, so
 * this asks the participant completions index for an exact match on the
 * address and returns its participant ID, or undefined when none matches.
 */
export async function resolveParticipantID(client: APIClient, address: string, signal?: AbortSignal): Promise<number | undefined> {
  const wanted = normalized(address);
  if (!wanted) return undefined;
  const { data } = await completeParticipants({ query: address.trim(), limit: 20 }, { ...client, signal });
  const match = (data?.rows ?? []).find((row) => normalized(row.value) === wanted);
  return match?.participant_id;
}
