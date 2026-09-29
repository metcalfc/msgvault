/** Explore entry keys for a message known only by id.
 *
 * Mirrors internal/query/entry_key.go and identityindex.IsChat: chat-classified
 * messages are keyed by their conversation, everything else by the source
 * message id (falling back to the internal id). Keeping the two in step lets
 * a Directory contact-state ref select its row in Everything without a
 * dedicated message route. `GET /api/v1/messages/{id}` does not return
 * `conversation_type`, so the fallback chat types ("", "chat", "text") can
 * only be classified when the caller supplies it. */

const TEXT_MESSAGE_TYPES = new Set([
  'google_chat', 'whatsapp', 'imessage', 'sms', 'mms', 'rcs',
  'google_voice_text', 'teams', 'discord', 'beeper', 'slack', 'fbmessenger'
]);
const CHAT_FALLBACK_MESSAGE_TYPES = new Set(['', 'chat', 'text']);
const CHAT_CONVERSATION_TYPES = new Set(['direct_chat', 'group_chat', 'channel', 'chat']);

export function isChatEntry(messageType: string | undefined, conversationType: string | undefined): boolean {
  const type = (messageType ?? '').toLowerCase();
  if (TEXT_MESSAGE_TYPES.has(type)) return true;
  return CHAT_FALLBACK_MESSAGE_TYPES.has(type) && CHAT_CONVERSATION_TYPES.has((conversationType ?? '').toLowerCase());
}

export interface MessageEntryKeyFacts {
  id: number;
  source_id?: number;
  source_message_id?: string;
  conversation_id?: number;
  message_type?: string;
  conversation_type?: string;
}

/** The explore row key for a message, or undefined when the facts cannot
 * name a row (no source, or a chat message with no conversation). */
export function messageEntryKey(facts: MessageEntryKeyFacts): string | undefined {
  if (!Number.isSafeInteger(facts.source_id) || facts.source_id! < 1) return undefined;
  if (isChatEntry(facts.message_type, facts.conversation_type)) {
    if (!Number.isSafeInteger(facts.conversation_id) || facts.conversation_id! < 1) return undefined;
    return `source:${facts.source_id}:conversation:${facts.conversation_id}`;
  }
  return `source:${facts.source_id}:message:${facts.source_message_id || facts.id}`;
}

import type { EntryRow, ExploreFilter, ExplorePredicate } from './models';
import { dayWindowFilters } from './date-range';

export interface MessageRowFacts extends MessageEntryKeyFacts {
  sent_at: string;
}

/** The narrowest predicate that still lists the message's row: one day
 * either side of when it was sent, within its source. */
export function messageRowFilters(message: MessageRowFacts): ExploreFilter[] {
  return [
    ...dayWindowFilters(message.sent_at),
    ...(Number.isSafeInteger(message.source_id) && message.source_id! > 0
      ? [{ dimension: 'source' as const, values: [String(message.source_id)] }]
      : [])
  ];
}

/** Finds the explore row that shows this message by asking the explore
 * query itself, so chat messages the server groups under their
 * conversation (including the fallback types whose conversation_type the
 * message endpoint does not return) are addressed correctly. The row
 * anchored on the message wins; otherwise the conversation row containing
 * it; otherwise the key is derived locally as a best guess. */
export async function resolveMessageRowKey(
  message: MessageRowFacts,
  explore: (predicate: ExplorePredicate) => Promise<{ rows: EntryRow[] }>
): Promise<string | undefined> {
  try {
    const { rows } = await explore({ filters: messageRowFilters(message), presentation: 'table' });
    const anchored = rows.find((row) => row.anchor_message_id === message.id);
    if (anchored) return anchored.key;
    const conversation = Number.isSafeInteger(message.conversation_id) && message.conversation_id! > 0
      ? rows.find((row) => row.conversation_id === message.conversation_id && row.key.includes(':conversation:'))
      : undefined;
    if (conversation) return conversation.key;
  } catch {
    // The local derivation below is the fallback.
  }
  return messageEntryKey(message);
}
