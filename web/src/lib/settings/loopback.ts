/** True when a configured endpoint URL points at this machine (localhost,
 * 127.0.0.0/8, or ::1), so requests to it keep data local. An empty or
 * unparseable value is not local. */
export function isLoopbackURL(value: string | undefined): boolean {
  if (!value?.trim()) return false;
  let host: string;
  try {
    host = new URL(value.trim()).hostname.toLowerCase();
  } catch {
    return false;
  }
  if (host === 'localhost' || host.endsWith('.localhost')) return true;
  if (host === '[::1]' || host === '::1') return true;
  return /^127(\.\d{1,3}){3}$/.test(host);
}
