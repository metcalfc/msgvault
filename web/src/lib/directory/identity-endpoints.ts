import type { ContactMatchStatus, IdentityMatchEndpointSummary, SharedMailboxSignal } from '../api/generated/models';
import type { EntityNames } from '../names/entity-names.svelte';

const ENDPOINT_ROLES: Record<string, string> = {
  participant: 'Archive identity',
  person: 'Person profile',
  carddav_resource: 'Contact card',
  observation: 'Observed address',
  contact_point: 'Contact point',
};

/** The reviewer-facing role of an endpoint kind. */
export function endpointRole(kind: string): string {
  return ENDPOINT_ROLES[kind] ?? kind;
}

/**
 * The name a reviewer recognizes for an endpoint: its own display name, else
 * its first address, else the resolver's name for a person or participant,
 * else its owning profile's name, else its kind. Never its ID.
 */
export function endpointLabel(
  names: EntityNames, kind: string, id: number, summary?: IdentityMatchEndpointSummary
): string {
  const name = summary?.display_name?.trim();
  if (name) return name;
  const address = summary?.addresses?.[0]?.trim();
  if (address) return address;
  if (summary && !summary.found) return `${endpointRole(kind)} (removed)`;
  if (kind === 'person') return names.label('person', id);
  if (kind === 'participant') return names.identity(id);
  const personName = summary?.person_display_name?.trim();
  if (personName) return personName;
  return endpointRole(kind);
}

/** What accepting a participant-to-person match does now, in plain words. */
export function contactMatchSummary(status: ContactMatchStatus): string {
  switch (status.classification) {
    case 'bind':
      return 'Accepting links this archive identity to the profile.';
    case 'merge':
      return 'This archive identity already belongs to another profile. Accepting asks you to merge the two profiles.';
    case 'ambiguous':
      return 'This archive identity spans several profiles. Separate or merge them before linking.';
    case 'linked':
      return 'This archive identity is already linked to the profile.';
    case 'shared_mailbox':
      return 'This address looks like a shared mailbox, so nothing is linked through it.';
    default:
      return '';
  }
}

/** Why an address looks like a shared mailbox, in plain words. */
export function sharedMailboxReason(signal: SharedMailboxSignal): string {
  const reasons: string[] = [];
  if (signal.reasons.includes('role_address')) reasons.push(`${signal.address} is a role address, not a person's.`);
  if (signal.reasons.includes('several_names')) {
    const names = signal.names ?? [];
    reasons.push(names.length > 0
      ? `Different people wrote from ${signal.address}: ${names.join(', ')}.`
      : `Different people wrote from ${signal.address}.`);
  }
  return reasons.join(' ');
}

/** Why a merge this match needs would be refused, if it would be. */
export function contactMatchBlockedMessage(status: ContactMatchStatus | undefined): string | null {
  switch (status?.blocked_reason) {
    case 'published':
      return 'Blocked: a profile is published to CardDAV. Unpublish it before linking.';
    case 'carddav_conflict':
      return 'Blocked: a profile has an unresolved CardDAV conflict. Resolve it before linking.';
    default:
      return null;
  }
}
