/** Marking archive identities as not a person: an organization, a shared
 * mailbox, or a record the user does not need. */
import {
  clearCorrespondentKind,
  deletePerson,
  getPersonProfile,
  listCorrespondentKinds,
  setCorrespondentKind
} from '../api/generated/api/api';
import type { APIClient } from '../api/client';
import { invalidatePeopleNames } from '../names/entity-names.svelte';
import type {
  CorrespondentKindAssignment,
  CorrespondentKindRecord,
  SetCorrespondentKindResult
} from '../api/generated/models';

/** The kinds a user picks when a record is not a person. */
export type NotAPersonKind = 'organization' | 'shared_mailbox' | 'ignored';
export type CorrespondentKind = NotAPersonKind | 'person';

export interface NotAPersonChoice {
  kind: NotAPersonKind;
  label: string;
  /** Short name for menus and buttons. */
  action: string;
  explanation: string;
}

export const NOT_A_PERSON_CHOICES: readonly NotAPersonChoice[] = [
  {
    kind: 'organization',
    label: 'Organization',
    action: 'Organization',
    explanation: 'A business or institution. Its messages are grouped under an organization and its address is added to that organization.'
  },
  {
    kind: 'shared_mailbox',
    label: 'Shared mailbox',
    action: 'Shared mailbox',
    explanation: 'An address several people write from, like a support desk. Nothing is merged through it; the people who wrote from it keep their own profiles.'
  },
  {
    kind: 'ignored',
    label: 'Ignored',
    action: 'Ignore',
    explanation: 'A record you do not need as a contact. It leaves People and Reviews; its messages stay searchable.'
  }
];

export function kindLabel(kind: string | undefined): string {
  return NOT_A_PERSON_CHOICES.find((choice) => choice.kind === kind)?.label ?? 'Person';
}

export function isNotAPerson(kind: string | undefined): kind is NotAPersonKind {
  return kind === 'organization' || kind === 'shared_mailbox' || kind === 'ignored';
}

/** "Shared mailbox" or "Organization · Example Shop". */
export function assignmentLabel(assignment: Pick<CorrespondentKindAssignment, 'kind' | 'organization_name'> | undefined): string {
  if (!assignment) return 'Person';
  const label = kindLabel(assignment.kind);
  return assignment.kind === 'organization' && assignment.organization_name?.trim()
    ? `${label} · ${assignment.organization_name.trim()}`
    : label;
}

export type KindResult =
  | { ok: true; result: SetCorrespondentKindResult }
  | { ok: false; status: number; message: string };

function failure(error: unknown, status: number): { ok: false; status: number; message: string } {
  if (typeof error === 'object' && error !== null && 'message' in error && typeof error.message === 'string' && error.message) {
    return { ok: false, status, message: error.message };
  }
  return { ok: false, status, message: status ? `The archive returned ${status}.` : 'The archive did not respond.' };
}

/** Marks the cluster containing participantID. */
export async function setKind(
  client: APIClient,
  participantID: number,
  kind: CorrespondentKind,
  organizationName?: string,
  signal?: AbortSignal
): Promise<KindResult> {
  const name = organizationName?.trim();
  try {
    const { data, error, response } = await setCorrespondentKind(
      { id: participantID },
      { kind, ...(kind === 'organization' && name ? { organization_name: name } : {}) },
      { ...client, ...(signal ? { signal } : {}) }
    );
    if (!data) return failure(error, response.status);
    // A participant's label leads with its bound person's name only while
    // it is a person, so every cached person and participant name is stale.
    invalidatePeopleNames(client);
    return { ok: true, result: data };
  } catch (cause) {
    return failure(cause, 0);
  }
}

/** "This is a person": clears the classification. */
export async function clearKind(client: APIClient, participantID: number): Promise<KindResult> {
  try {
    const { data, error, response } = await clearCorrespondentKind({ id: participantID }, client);
    if (!data) return failure(error, response.status);
    invalidatePeopleNames(client);
    return { ok: true, result: data };
  } catch (cause) {
    return failure(cause, 0);
  }
}

export async function listNotPeople(
  client: APIClient,
  signal?: AbortSignal
): Promise<{ records: CorrespondentKindRecord[] } | { error: string }> {
  try {
    const { data, error, response } = await listCorrespondentKinds(undefined, { ...client, ...(signal ? { signal } : {}) });
    return data ? { records: data.records } : { error: failure(error, response.status).message };
  } catch (cause) {
    return { error: failure(cause, 0).message };
  }
}

/** Deletes a saved profile after the user confirmed it separately. The
 * profile is read first so the delete carries its current revision tag. */
export async function deleteSavedProfile(client: APIClient, personID: number): Promise<{ ok: true } | { ok: false; message: string }> {
  try {
    const read = await getPersonProfile({ id: personID }, client);
    const etag = read.response.headers.get('ETag');
    if (!read.data || !etag) return { ok: false, message: failure(read.error, read.response.status).message };
    const removed = await deletePerson({ id: personID }, { ...client, headers: { 'If-Match': etag } });
    if (removed.response.status === 204) {
      invalidatePeopleNames(client);
      return { ok: true };
    }
    return { ok: false, message: failure(removed.error, removed.response.status).message };
  } catch (cause) {
    return { ok: false, message: failure(cause, 0).message };
  }
}
