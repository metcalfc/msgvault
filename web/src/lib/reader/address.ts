/** One archived address as the reader shows it. The daemon formats
 * participants as "Name <address>" when it knows a name, else the bare
 * address or phone number. */
export interface ReaderAddress {
  /** What the pill shows: the name when present, else the address. */
  label: string;
  /** The email address or phone number, used to copy and to find the person. */
  address: string;
  name: string;
}

export function parseAddress(value: string): ReaderAddress {
  const trimmed = value.trim();
  const match = /^(.*?)\s*<([^<>]+)>$/.exec(trimmed);
  if (match) {
    const name = match[1]!.replace(/^"(.*)"$/, '$1').trim();
    const address = match[2]!.trim();
    return { label: name || address, address, name };
  }
  return { label: trimmed, address: trimmed, name: '' };
}

/** A Gmail web link for a Gmail message's source ID (its hex message ID),
 * or undefined when the ID is not in that form. */
export function gmailMessageURL(sourceMessageID: string | undefined): string | undefined {
  const id = sourceMessageID?.trim() ?? '';
  return /^[0-9a-f]{6,}$/i.test(id) ? `https://mail.google.com/mail/u/0/#all/${id}` : undefined;
}
