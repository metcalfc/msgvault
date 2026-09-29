/** Contact-method presentation shared by the Directory person page and the
 * Relationships hub header: one row per way to reach a person, grouped by
 * kind, deduplicated by normalized value across the address book (Directory
 * contact points) and the archive (participant identifiers and cluster
 * members). Presentation-only — nothing here is serialized back to an API. */
import type {
  PersonClusterEdge, PersonClusterMember, PersonContactPoint, PersonIdentifier
} from '../api/generated/models';
import { identityChipText, linkOriginSummary } from '../relationships/identity-chip';

export type ReachKind = 'email' | 'phone' | 'chat' | 'handle' | 'url';

/** Display order: the ways you'd actually reach someone first. */
export const REACH_KIND_ORDER: readonly ReachKind[] = ['email', 'phone', 'chat', 'handle', 'url'];

export const reachKindLabels: Record<ReachKind, string> = {
  email: 'Email', phone: 'Phone', chat: 'Chat', handle: 'Handle', url: 'Profile'
};

export interface ReachEntry {
  /** Dedupe key: kind plus normalized value (plus service for chat/handles). */
  key: string;
  kind: ReachKind;
  /** Exact text the copy button writes to the clipboard. */
  value: string;
  /** Visible text. Equals `value` unless the value is an opaque provider key. */
  display: string;
  /** Accessible name for the row's controls ("Copy <label>", "Actions for <label>"). */
  label: string;
  /** Service the method lives on, when it is not implied by the kind ("WhatsApp", "LinkedIn"). */
  service?: string;
  /** Display-name evidence attached to the value (an email's display name, a linked member's name). */
  name?: string;
  /** Relationship to the open profile ("this profile", "linked · matched from …"). */
  note?: string;
  /** Tooltip: provenance in human words, plus the raw key for opaque values. */
  title?: string;
  /** True when the value was seen in the archive rather than curated in the address book. */
  observed: boolean;
  /** Every cluster member that contributed this value. One visible row per
   * value, but each member keeps its own unlink control, so two members
   * sharing an address both stay detachable. */
  participantIDs: number[];
  /** The visible text is a service label because the value itself is an opaque key. */
  opaque?: boolean;
  /** The address book's type for the value ("work", "cell"), when it has one. */
  typeLabel?: string;
  /** Where an address-book value came from ("user", "enrichment", …). */
  source?: string;
}

/** The small-caps label a contact row leads with: the kind, a phone's
 * mobile type, or the service a chat or profile lives on. */
export function reachRowLabel(entry: ReachEntry): string {
  if (entry.kind === 'phone') return /cell|mobile|iphone/i.test(entry.typeLabel ?? '') ? 'Mobile' : 'Phone';
  if (entry.kind === 'email') return 'Email';
  return entry.service ?? reachKindLabels[entry.kind];
}

/** The quiet facts a contact row trails with, before its copy action. */
export function reachRowMeta(entry: ReachEntry): string[] {
  const type = entry.typeLabel?.trim().toLowerCase();
  return [
    type && !(entry.kind === 'phone' && /cell|mobile|iphone/.test(type)) ? type : '',
    entry.kind === 'phone' || entry.kind === 'email' ? entry.service ?? '' : '',
    entry.name ?? '',
    entry.note ?? '',
    entry.observed ? 'observed' : entry.source === 'enrichment' ? 'from enrichment' : '',
  ].filter(Boolean);
}

const serviceLabels: Record<string, string> = {
  imessage: 'iMessage', sms: 'SMS', whatsapp: 'WhatsApp', slack: 'Slack', discord: 'Discord',
  matrix: 'Matrix', signal: 'Signal', telegram: 'Telegram', beeper: 'Beeper', linkedin: 'LinkedIn',
  github: 'GitHub', twitter: 'Twitter', x: 'X', mastodon: 'Mastodon', facebook: 'Facebook', instagram: 'Instagram'
};

/** Human label for a service slug; email and phone are implied by their kind and get none. */
export function serviceLabelForSlug(slug: string | undefined): string | undefined {
  const trimmed = slug?.trim().toLowerCase();
  if (!trimmed || trimmed === 'email' || trimmed === 'phone' || trimmed === 'other') return undefined;
  return serviceLabels[trimmed] ?? trimmed.charAt(0).toUpperCase() + trimmed.slice(1).replaceAll('_', ' ');
}

/** Which reach kind a structured-profile `address_kind` maps to; postal and
 * biographical kinds are not ways to reach someone and return undefined. */
export function reachKindForAddressKind(addressKind: string): ReachKind | undefined {
  switch (addressKind) {
    case 'email': return 'email';
    case 'phone': return 'phone';
    case 'impp': return 'chat';
    case 'username': case 'social': case 'provider_identity': return 'handle';
    case 'url': case 'contact_uri': return 'url';
    default: return undefined;
  }
}

/** Participant identifier types are email, phone, or a messaging service key. */
export function reachKindForIdentifierType(type: string): ReachKind {
  if (type === 'email') return 'email';
  if (type === 'phone') return 'phone';
  return 'chat';
}

export function normalizeReachValue(kind: ReachKind, value: string): string {
  const trimmed = value.trim();
  if (kind === 'phone') return trimmed.replace(/\D+/g, '');
  return trimmed.toLowerCase();
}

/** The service part of a chat/handle key, spelled the same way whichever
 * source supplied it: a trimmed, lowercased slug, or the source's own kind
 * word (the address kind for a contact point, the identifier type for an
 * archive identifier) when no slug is stored. */
export function serviceKey(slug: string | undefined, fallback: string): string {
  return slug?.trim().toLowerCase() || fallback.trim().toLowerCase();
}

function keyFor(kind: ReachKind, value: string, service = ''): string {
  const normalized = normalizeReachValue(kind, value);
  return kind === 'chat' || kind === 'handle' ? `${kind}:${service}:${normalized}` : `${kind}:${normalized}`;
}

export function reachEntriesFromContactPoints(points: readonly PersonContactPoint[] | undefined): ReachEntry[] {
  const entries: ReachEntry[] = [];
  for (const point of points ?? []) {
    const kind = reachKindForAddressKind(point.address_kind);
    if (!kind) continue;
    const value = point.original_value.trim();
    if (!value) continue;
    const service = serviceLabelForSlug(point.service_slug);
    entries.push({
      key: keyFor(kind, point.normalized_value || value, serviceKey(point.service_slug, point.address_kind)),
      kind, value, display: value, label: value, service,
      title: point.envelope.type_label || undefined,
      observed: point.envelope.source === 'archive_observation',
      typeLabel: point.envelope.type_label || undefined,
      source: point.envelope.source,
      participantIDs: []
    });
  }
  return entries;
}

/** Provenance in human words — internal identifiers never reach user-visible text. */
export function identifierTooltip(identifier: Pick<PersonIdentifier, 'type' | 'is_primary' | 'provenance'>): string {
  const parts = [identifier.type, identifier.is_primary ? 'primary' : 'secondary'];
  if (identifier.provenance === 'participant_identifiers') parts.push('stored identifier');
  else if (identifier.provenance) parts.push(identifier.provenance.replaceAll('_', ' '));
  return parts.join(' · ');
}

export interface IdentifierReachInput {
  identifiers: readonly PersonIdentifier[] | undefined;
  /** The open participant cluster's canonical id, or undefined outside a cluster context. */
  ownID?: number;
  members?: readonly PersonClusterMember[];
  edges?: readonly PersonClusterEdge[];
  /** Whether the person is part of a linked cluster (drives the "this profile"/"linked" notes). */
  clustered?: boolean;
}

export function reachEntriesFromIdentifiers({ identifiers, ownID, members = [], edges = [], clustered = false }: IdentifierReachInput): ReachEntry[] {
  const entries: ReachEntry[] = [];
  for (const identifier of identifiers ?? []) {
    const kind = reachKindForIdentifierType(identifier.type);
    const opaque = kind === 'chat';
    const isOtherMember = clustered && ownID !== undefined && identifier.participant_id !== ownID;
    const memberEdges = isOtherMember
      ? edges.filter((edge) => edge.participant_a === identifier.participant_id || edge.participant_b === identifier.participant_id)
      : [];
    const member = members.find((candidate) => candidate.participant_id === identifier.participant_id);
    const text = identityChipText(identifier, member, [...memberEdges]);
    const memberName = identifier.participant_display_name || member?.display_name || '';
    const displayValue = identifier.display_value?.trim() ?? '';
    const origin = linkOriginSummary([...memberEdges]);
    const relation = isOtherMember ? (origin || 'linked') : clustered ? 'this profile' : '';
    const scope = identifier.scope_kind && identifier.scope_value && identifier.scope_kind !== 'account'
      ? `${identifier.scope_kind}: ${identifier.scope_value}` : '';
    const label = opaque
      ? `${text.title} identifier for ${memberName ? `${memberName} (profile ${identifier.participant_id})` : `profile ${identifier.participant_id}`}`
      : identifier.value;
    entries.push({
      key: keyFor(kind, identifier.value, serviceKey(identifier.service_slug, identifier.type)),
      kind,
      value: identifier.value,
      // A phone's display value is the same number formatted for reading.
      display: opaque ? text.title : kind === 'phone' && displayValue ? displayValue : identifier.value,
      label,
      service: opaque ? text.title : serviceLabelForSlug(identifier.service_slug),
      name: memberName && (opaque || memberName !== identifier.value) ? memberName
        : !opaque && kind !== 'phone' && displayValue && displayValue !== identifier.value ? displayValue : undefined,
      note: [scope, relation].filter(Boolean).join(' · ') || undefined,
      title: opaque ? `${text.detail} · ${identifierTooltip(identifier)}` : identifierTooltip(identifier),
      observed: true,
      participantIDs: [identifier.participant_id],
      opaque
    });
  }
  return entries;
}

/** Cluster members with no identifier row but a stored email or phone. */
export function reachEntriesFromMembers(members: readonly PersonClusterMember[], edges: readonly PersonClusterEdge[] = []): ReachEntry[] {
  const entries: ReachEntry[] = [];
  for (const member of members) {
    const memberEdges = edges.filter((edge) => edge.participant_a === member.participant_id || edge.participant_b === member.participant_id);
    const origin = linkOriginSummary([...memberEdges]);
    const pairs: Array<[ReachKind, string | undefined]> = [['email', member.email], ['phone', member.phone]];
    for (const [kind, raw] of pairs) {
      const value = raw?.trim();
      if (!value) continue;
      entries.push({
        key: keyFor(kind, value), kind, value, display: value, label: value,
        name: member.display_name || undefined, note: origin || 'linked',
        observed: true, participantIDs: [member.participant_id]
      });
    }
  }
  return entries;
}

/** Union of several sources, deduplicated by normalized value. The address
 * book wins over the archive for presentation; archive-only facts
 * (contributing participant ids, relationship note) are carried across,
 * and every contributing member is kept so none loses its controls. */
export function mergeReachEntries(...lists: ReachEntry[][]): ReachEntry[] {
  const byKey = new Map<string, ReachEntry>();
  for (const list of lists) {
    for (const entry of list) {
      const existing = byKey.get(entry.key);
      if (!existing) { byKey.set(entry.key, { ...entry }); continue; }
      const [keep, other] = existing.observed && !entry.observed ? [entry, existing] : [existing, entry];
      byKey.set(entry.key, {
        ...keep,
        name: keep.name ?? other.name,
        service: keep.service ?? other.service,
        note: keep.note ?? other.note,
        title: keep.title ?? other.title,
        participantIDs: [...new Set([...keep.participantIDs, ...other.participantIDs])],
        observed: keep.observed && other.observed
      });
    }
  }
  return [...byKey.values()].sort((a, b) => REACH_KIND_ORDER.indexOf(a.kind) - REACH_KIND_ORDER.indexOf(b.kind));
}
