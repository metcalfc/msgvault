/**
 * Turns a stored contact value (an email, a phone number, a profile handle,
 * a URL) into a link the browser may follow, or refuses it.
 *
 * Every value here is archive data: an address book, an enrichment
 * provider, or a message sender may have written it. The rules are
 * deliberately narrow:
 *
 * - Only `mailto:`, `tel:`, and `https:` links are produced. `http:` is
 *   upgraded to `https:`. Every other scheme (javascript:, data:, vbscript:,
 *   file:, sms:, tg:, matrix:, …) yields no link.
 * - `tel:` links come only from an E.164 number (`+` then 7–15 digits).
 * - A web link never carries credentials and never targets a loopback,
 *   private, or single-label host (see `prohibitedRemoteHost`).
 * - Profile links come from, in order: a stored `uri`, a value that is
 *   already a URL, the service's `profile_url_template`, then a small
 *   built-in table of well-known services.
 *
 * Message bodies are never linked; this module is only for structured
 * contact facts.
 */
import { prohibitedRemoteHost } from '../content/url-safety';

export interface ContactLink {
  href: string;
  /** Accessible description of where the link goes ("Email ada@example.com"). */
  label: string;
  /** True for web links, which open in a new tab; mailto/tel stay in place. */
  external: boolean;
}

/** Per-service link facts the daemon reports (communication_services). */
export interface ContactServiceLinkInfo {
  profile_url_template?: string;
  uri_scheme?: string;
}

export interface ContactLinkInput {
  /** Reach kind (email, phone, chat, handle, url), structured-profile
   * address kind (social, username, impp, contact_uri, …), or identifier type. */
  kind: string;
  /** Communication service slug ("github", "mastodon"). */
  service?: string;
  value: string;
  /** The service-normalized value, when the source stored one. */
  normalized?: string;
  /** A stored URI for the value, when the source stored one. */
  uri?: string;
  /** Link facts keyed by service slug. */
  services?: Readonly<Record<string, ContactServiceLinkInfo | undefined>>;
}

const E164 = /^\+[1-9]\d{6,14}$/;
const EMAIL = /^[^\s@<>()[\]",;:\\]+@[^\s@<>()[\]",;:\\]+\.[^\s@<>()[\]",;:\\]+$/;
const SCHEME = /^([a-z][a-z0-9+.-]*):/i;
/** A handle that can be dropped into a URL path segment as-is. */
const PLAIN_HANDLE = /^[^\s/?#\\]+$/;

function clean(value: string | undefined): string {
  return (value ?? '').trim();
}

function stripAt(handle: string): string {
  return handle.replace(/^@/, '');
}

/** Parses and vets a web URL; returns the https form or undefined. */
export function safeWebURL(raw: string): string | undefined {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch {
    return undefined;
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') return undefined;
  if (url.username || url.password) return undefined;
  if (prohibitedRemoteHost(url.hostname)) return undefined;
  if (url.protocol === 'http:') url.protocol = 'https:';
  // An explicit :80 meant the plain-http port; drop it with the upgrade.
  if (url.port === '80') url.port = '';
  return url.href;
}

function emailLink(raw: string): ContactLink | undefined {
  const trimmed = clean(raw);
  const isURI = /^mailto:/i.test(trimmed);
  // A mailto URI's headers (?subject=, ?bcc=) are dropped: the link only
  // ever opens a message to the one stored address.
  const address = isURI ? trimmed.slice('mailto:'.length).split('?')[0] ?? '' : trimmed;
  let decoded = address;
  if (isURI) {
    try {
      decoded = decodeURIComponent(address);
    } catch {
      return undefined;
    }
  }
  if (!EMAIL.test(decoded)) return undefined;
  const [local = '', domain = ''] = splitEmail(decoded);
  return {
    href: `mailto:${encodeURIComponent(local)}@${encodeURIComponent(domain)}`,
    label: `Email ${decoded}`,
    external: false
  };
}

function splitEmail(address: string): [string, string] {
  const at = address.lastIndexOf('@');
  return [address.slice(0, at), address.slice(at + 1)];
}

function telLink(raw: string): ContactLink | undefined {
  const compact = clean(raw).replace(/^tel:/i, '').replace(/[\s().-]/g, '');
  if (!E164.test(compact)) return undefined;
  return { href: `tel:${compact}`, label: `Call ${compact}`, external: false };
}

function webLink(raw: string, label: string): ContactLink | undefined {
  const href = safeWebURL(raw);
  return href ? { href, label, external: true } : undefined;
}

/** A value that names its own scheme. Only allowed schemes produce a link;
 * any other scheme is refused outright rather than guessed around. */
function linkFromURI(raw: string, label: string): ContactLink | undefined | null {
  const match = SCHEME.exec(raw);
  if (!match) return null;
  const scheme = (match[1] ?? '').toLowerCase();
  if (scheme === 'mailto') return emailLink(raw);
  if (scheme === 'tel') return telLink(raw);
  if (scheme === 'https' || scheme === 'http') return webLink(raw, label);
  return undefined;
}

const HOST_LIKE = /^(?:www\.)?[a-z0-9-]+(?:\.[a-z0-9-]+)+(?:[/?#].*)?$/i;

type Builder = (value: string) => string | undefined;

function pathHandle(pattern: RegExp, base: string): Builder {
  return (value) => {
    const handle = stripAt(value);
    return pattern.test(handle) ? `${base}${encodeURIComponent(handle)}` : undefined;
  };
}

function linkedinURL(value: string): string | undefined {
  const path = value
    .replace(/^https?:\/\//i, '')
    .replace(/^(?:[a-z]{2,3}\.|www\.)?linkedin\.com\//i, '')
    .replace(/\/+$/, '');
  const typed = /^(in|company|school|pub)\/([^/?#\s]+)$/i.exec(path);
  if (typed) return `https://www.linkedin.com/${(typed[1] ?? '').toLowerCase()}/${encodeURIComponent(safeDecode(typed[2] ?? ''))}`;
  if (/^[\p{L}\p{N}_.%-]{2,100}$/u.test(path)) return `https://www.linkedin.com/in/${encodeURIComponent(safeDecode(path))}`;
  return undefined;
}

function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function mastodonURL(value: string): string | undefined {
  const match = /^@?([A-Za-z0-9_]+(?:[.-][A-Za-z0-9_]+)*)@([A-Za-z0-9.-]+\.[A-Za-z]{2,})$/.exec(value);
  if (!match) return undefined;
  return `https://${(match[2] ?? '').toLowerCase()}/@${match[1] ?? ''}`;
}

function matrixURL(value: string): string | undefined {
  const id = value.startsWith('@') ? value : `@${value}`;
  if (!/^@[a-z0-9._=\-/+]+:[A-Za-z0-9.-]+(?::\d{1,5})?$/i.test(id)) return undefined;
  return `https://matrix.to/#/${encodeURIComponent(id)}`;
}

function youtubeURL(value: string): string | undefined {
  if (/^UC[A-Za-z0-9_-]{22}$/.test(value)) return `https://www.youtube.com/channel/${value}`;
  const handle = stripAt(value);
  return /^[A-Za-z0-9._-]{3,30}$/.test(handle) ? `https://www.youtube.com/@${encodeURIComponent(handle)}` : undefined;
}

/** Built-in profile URL builders for services whose handle form is well
 * known, used when the daemon reports no template for the service. */
const BUILTIN: Record<string, Builder> = {
  github: pathHandle(/^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/, 'https://github.com/'),
  mastodon: mastodonURL,
  linkedin: linkedinURL,
  x: pathHandle(/^[A-Za-z0-9_]{1,15}$/, 'https://x.com/'),
  twitter: pathHandle(/^[A-Za-z0-9_]{1,15}$/, 'https://x.com/'),
  bluesky: pathHandle(/^(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}$|^did:plc:[a-z2-7]{24}$/, 'https://bsky.app/profile/'),
  bsky: pathHandle(/^(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}$|^did:plc:[a-z2-7]{24}$/, 'https://bsky.app/profile/'),
  instagram: pathHandle(/^[A-Za-z0-9._]{1,30}$/, 'https://www.instagram.com/'),
  facebook: pathHandle(/^[A-Za-z0-9.]{1,50}$/, 'https://www.facebook.com/'),
  threads: pathHandle(/^[A-Za-z0-9._]{1,30}$/, 'https://www.threads.com/@'),
  youtube: youtubeURL,
  matrix: matrixURL,
  telegram: pathHandle(/^[A-Za-z][A-Za-z0-9_]{4,31}$/, 'https://t.me/')
};

const SERVICE_LABELS: Record<string, string> = {
  github: 'GitHub', mastodon: 'Mastodon', linkedin: 'LinkedIn', x: 'X', twitter: 'X',
  bluesky: 'Bluesky', bsky: 'Bluesky', instagram: 'Instagram', facebook: 'Facebook',
  threads: 'Threads', youtube: 'YouTube', matrix: 'Matrix', telegram: 'Telegram'
};

function profileLabel(service: string, value: string): string {
  const name = SERVICE_LABELS[service] ?? (service ? service.charAt(0).toUpperCase() + service.slice(1) : '');
  return name ? `Open ${name} profile ${value}` : `Open ${value}`;
}

function fromTemplate(template: string | undefined, handle: string): string | undefined {
  if (!template || !template.includes('{username}')) return undefined;
  if (!handle || !PLAIN_HANDLE.test(handle)) return undefined;
  return safeWebURL(template.replaceAll('{username}', encodeURIComponent(handle)));
}

function isEmailKind(kind: string): boolean {
  return kind === 'email';
}

function isPhoneKind(kind: string): boolean {
  return kind === 'phone' || kind === 'tel';
}

/**
 * Returns the link for a contact value, or undefined when the value should
 * stay plain text.
 */
export function contactLink(input: ContactLinkInput): ContactLink | undefined {
  const kind = clean(input.kind).toLowerCase();
  const service = clean(input.service).toLowerCase();
  const value = clean(input.value);
  const normalized = clean(input.normalized);
  if (!value && !normalized) return undefined;

  if (isEmailKind(kind)) return emailLink(normalized || value);
  if (isPhoneKind(kind)) return telLink(normalized) ?? telLink(value);

  const label = profileLabel(service, value);

  const uri = clean(input.uri);
  if (uri) {
    const fromURI = linkFromURI(uri, label);
    if (fromURI) return fromURI;
  }

  const direct = linkFromURI(value, label);
  if (direct !== null) return direct;
  if (kind === 'url' || kind === 'contact_uri') {
    return HOST_LIKE.test(value) ? webLink(`https://${value}`, `Open ${value}`) : undefined;
  }

  const handle = stripAt(normalized || value);
  const templated = fromTemplate(input.services?.[service]?.profile_url_template, handle);
  if (templated) return { href: templated, label, external: true };

  const built = BUILTIN[service]?.(value);
  if (built) return webLink(built, label);

  // A profile given without a scheme ("github.com/ada") is still a web address.
  if ((kind === 'social' || kind === 'handle') && HOST_LIKE.test(value) && value.includes('/')) {
    return webLink(`https://${value}`, label);
  }
  return undefined;
}

/** The address fields a map link can use. */
export interface MapLinkInput {
  geo_uri?: string;
  original_value?: string;
  free_text?: string;
  street_address?: string;
  locality?: string;
  region?: string;
  postal_code?: string;
  country_name?: string;
}

const GEO = /^geo:(-?\d{1,2}(?:\.\d+)?),(-?\d{1,3}(?:\.\d+)?)(?:,-?\d+(?:\.\d+)?)?(?:;.*)?$/i;

/** Formats an address for display and map search: the stored text when
 * present, otherwise its components joined in reading order. */
export function formattedAddress(address: MapLinkInput): string {
  const text = clean(address.original_value) || clean(address.free_text);
  if (text) return text.replace(/\s*\n\s*/g, ', ');
  return [address.street_address, address.locality, address.region, address.postal_code, address.country_name]
    .map(clean).filter(Boolean).join(', ');
}

/** A Google Maps link for a postal address: exact coordinates from a
 * `geo:` URI when stored, otherwise a search for the formatted address. */
export function mapLink(address: MapLinkInput): ContactLink | undefined {
  const geo = GEO.exec(clean(address.geo_uri));
  if (geo) {
    const lat = Number(geo[1]);
    const lon = Number(geo[2]);
    if (Math.abs(lat) <= 90 && Math.abs(lon) <= 180) {
      return {
        href: `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(`${lat},${lon}`)}`,
        label: 'Open location in Google Maps',
        external: true
      };
    }
  }
  const text = formattedAddress(address);
  if (!text) return undefined;
  return {
    href: `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(text)}`,
    label: `Open ${text} in Google Maps`,
    external: true
  };
}
