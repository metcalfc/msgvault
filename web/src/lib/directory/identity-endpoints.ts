import type { ContactMatchStatus, IdentityMatchEndpointSummary } from '../api/generated/models';

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
 * its first address, else its owning profile's name, else the kind and ID.
 */
export function endpointLabel(kind: string, id: number, summary?: IdentityMatchEndpointSummary): string {
  const name = summary?.display_name?.trim();
  if (name) return name;
  const address = summary?.addresses?.[0]?.trim();
  if (address) return address;
  const personName = summary?.person_display_name?.trim();
  if (personName) return personName;
  if (summary && !summary.found) return `${endpointRole(kind)} ${id} (removed)`;
  return `${endpointRole(kind)} ${id}`;
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
    default:
      return '';
  }
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
