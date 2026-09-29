/** Human labels for archive vocabularies that otherwise leak as raw tokens
 * ("imessage", "calendar_event") into list rows, crumbs, and summaries. */

const messageTypeLabels: Record<string, string> = {
  email: 'Email',
  imessage: 'Text (iMessage)',
  sms: 'Text (SMS)',
  mms: 'Text (MMS)',
  text: 'Text',
  chat: 'Chat',
  whatsapp: 'WhatsApp',
  signal: 'Signal',
  telegram: 'Telegram',
  slack: 'Slack',
  discord: 'Discord',
  matrix: 'Matrix',
  beeper: 'Beeper',
  calendar_event: 'Event',
  calendar: 'Event',
  event: 'Event',
  meeting_transcript: 'Meeting',
  meeting: 'Meeting',
  voicemail: 'Voicemail',
  file: 'File',
  conversation: 'Conversation'
};

/** True for archive message types with a curated label. */
export function isKnownMessageType(type: string | null | undefined): boolean {
  return Boolean(type) && (type!.trim().toLowerCase() in messageTypeLabels);
}

/** "imessage" → "Text (iMessage)", "calendar_event" → "Event"; unknown
 * values are title-cased with underscores turned into spaces so nothing
 * renders as a raw token. An empty value stays empty. */
export function messageTypeLabel(type: string | null | undefined): string {
  const normalized = type?.trim().toLowerCase() ?? '';
  if (!normalized) return '';
  const known = messageTypeLabels[normalized];
  if (known) return known;
  const words = normalized.replaceAll('_', ' ');
  return words.charAt(0).toUpperCase() + words.slice(1);
}

const channelLabels: Record<string, string> = {
  email: 'email', chat: 'chat', meeting: 'meeting', other: ''
};

/** Contact-state channels ("email", "chat", "meeting") as short prose words;
 * "other" and unknown channels contribute nothing to a summary line. */
export function channelLabel(channel: string | null | undefined): string {
  const normalized = channel?.trim().toLowerCase() ?? '';
  if (!normalized) return '';
  if (normalized in channelLabels) return channelLabels[normalized]!;
  return messageTypeLabel(normalized).toLowerCase();
}
