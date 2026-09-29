import { describe, expect, it } from 'vitest';

import { isChatEntry, messageEntryKey, messageRowFilters, resolveMessageRowKey } from './entry-key';
import type { EntryRow } from './models';

describe('messageEntryKey', () => {
  it('keys mail by the source message id, falling back to the internal id', () => {
    expect(messageEntryKey({ id: 42, source_id: 3, source_message_id: 'source-42', message_type: 'email' })).toBe('source:3:message:source-42');
    expect(messageEntryKey({ id: 42, source_id: 3, source_message_id: '', message_type: 'email' })).toBe('source:3:message:42');
    expect(messageEntryKey({ id: 42, source_id: 3, message_type: 'calendar_event' })).toBe('source:3:message:42');
  });

  it('keys chat by its conversation and refuses when it cannot name a row', () => {
    expect(messageEntryKey({ id: 42, source_id: 3, conversation_id: 71, message_type: 'imessage' })).toBe('source:3:conversation:71');
    expect(messageEntryKey({ id: 42, source_id: 3, conversation_id: 71, message_type: 'chat', conversation_type: 'group_chat' })).toBe('source:3:conversation:71');
    expect(messageEntryKey({ id: 42, source_id: 3, conversation_id: 71, message_type: 'chat' })).toBe('source:3:message:42');
    expect(messageEntryKey({ id: 42, source_id: 3, message_type: 'imessage' })).toBeUndefined();
    expect(messageEntryKey({ id: 42, message_type: 'email' })).toBeUndefined();
  });

  it('classifies chat like the server', () => {
    expect(isChatEntry('SMS', undefined)).toBe(true);
    expect(isChatEntry('', 'direct_chat')).toBe(true);
    expect(isChatEntry('', 'thread')).toBe(false);
    expect(isChatEntry('email', 'chat')).toBe(false);
  });
});

function row(key: string, extra: Partial<EntryRow> = {}): EntryRow {
  return {
    key, kind: 'message', message_type: 'email', conversation_type: 'email', title: key, preview: '',
    occurred_at: '2026-08-01T12:00:00Z', source_id: 3, source_identifier: 's', source_type: 'gmail',
    attachment_count: 0, attachment_size: 0, has_attachments: false, deleted_from_source: false, message_count: 1,
    matched_sender_identities: [], matched_recipient_identities: [], match: {}, ...extra
  };
}

describe('resolveMessageRowKey', () => {
  const message = { id: 42, source_id: 3, source_message_id: '', conversation_id: 71, message_type: '', sent_at: '2026-08-01T12:00:00Z' };

  it('bounds the lookup to the day and source of the message', () => {
    expect(messageRowFilters(message).map((filter) => filter.dimension)).toEqual(['after', 'before', 'source']);
    expect(messageRowFilters({ ...message, source_id: undefined }).map((filter) => filter.dimension)).toEqual(['after', 'before']);
  });

  it('prefers the row anchored on the message, then the conversation row that contains it', async () => {
    const rows = [row('source:3:message:other', { anchor_message_id: 40 }), row('source:3:conversation:71', { conversation_id: 71, anchor_message_id: 40 })];
    expect(await resolveMessageRowKey(message, async () => ({ rows }))).toBe('source:3:conversation:71');
    const anchored = [...rows, row('source:3:message:42', { anchor_message_id: 42 })];
    expect(await resolveMessageRowKey(message, async () => ({ rows: anchored }))).toBe('source:3:message:42');
  });

  it('falls back to the locally derived key when the query fails or finds nothing', async () => {
    expect(await resolveMessageRowKey(message, async () => ({ rows: [] }))).toBe('source:3:message:42');
    expect(await resolveMessageRowKey(message, async () => { throw new Error('down'); })).toBe('source:3:message:42');
  });
});
