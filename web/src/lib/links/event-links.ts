/**
 * Links for a calendar event's stored provider URLs. Only https links to
 * known meeting and calendar hosts are offered; anything else stays hidden.
 */
import { safeWebURL, type ContactLink } from './contact-links';

/** Video-meeting hosts a "Join meeting" link may open. A leading dot allows
 * any subdomain (Zoom's per-company hosts such as example.zoom.us). */
const JOIN_HOSTS = ['meet.google.com', 'zoom.us', '.zoom.us', 'teams.microsoft.com', 'teams.live.com'];

/** Calendar hosts an "Open in Calendar" link may open. */
const CALENDAR_HOSTS = ['calendar.google.com', 'www.google.com'];

function allowed(raw: string | undefined, hosts: readonly string[]): string | undefined {
  if (!raw) return undefined;
  const href = safeWebURL(raw);
  if (!href) return undefined;
  const host = new URL(href).hostname.toLowerCase();
  const ok = hosts.some((entry) => entry.startsWith('.') ? host.endsWith(entry) && host.length > entry.length : host === entry);
  return ok ? href : undefined;
}

export function eventJoinLink(raw: string | undefined): ContactLink | undefined {
  const href = allowed(raw, JOIN_HOSTS);
  return href ? { href, label: 'Join meeting', external: true } : undefined;
}

export function eventCalendarLink(raw: string | undefined): ContactLink | undefined {
  const href = allowed(raw, CALENDAR_HOSTS);
  if (!href) return undefined;
  // www.google.com serves Calendar only under /calendar.
  const url = new URL(href);
  if (url.hostname === 'www.google.com' && !url.pathname.startsWith('/calendar/')) return undefined;
  return { href, label: 'Open in Calendar', external: true };
}
