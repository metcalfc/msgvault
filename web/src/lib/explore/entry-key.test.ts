import { describe, expect, it } from 'vitest';

import { isChatEntry, messageEntryKey } from './entry-key';

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
