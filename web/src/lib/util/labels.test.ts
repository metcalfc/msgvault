import { describe, expect, it } from 'vitest';

import { channelLabel, isKnownMessageType, messageTypeLabel } from './labels';

describe('messageTypeLabel', () => {
  it('humanizes archive message types', () => {
    expect(messageTypeLabel('imessage')).toBe('Text (iMessage)');
    expect(messageTypeLabel('sms')).toBe('Text (SMS)');
    expect(messageTypeLabel('calendar_event')).toBe('Event');
    expect(messageTypeLabel('meeting_transcript')).toBe('Meeting');
    expect(messageTypeLabel('email')).toBe('Email');
  });

  it('ignores Object.prototype keys so "constructor" is just an unknown type', () => {
    expect(isKnownMessageType('constructor')).toBe(false);
    expect(messageTypeLabel('constructor')).toBe('Constructor');
    expect(messageTypeLabel('toString')).toBe('Tostring');
    expect(channelLabel('constructor')).toBe('constructor');
  });

  it('title-cases unknown types instead of leaking raw tokens', () => {
    expect(messageTypeLabel('voice_note')).toBe('Voice note');
    expect(messageTypeLabel(' WhatsApp ')).toBe('WhatsApp');
    expect(messageTypeLabel('')).toBe('');
    expect(messageTypeLabel(undefined)).toBe('');
  });
});

describe('channelLabel', () => {
  it('turns contact-state channels into prose words and drops "other"', () => {
    expect(channelLabel('email')).toBe('email');
    expect(channelLabel('meeting')).toBe('meeting');
    expect(channelLabel('other')).toBe('');
    expect(channelLabel(undefined)).toBe('');
    expect(channelLabel('imessage')).toBe('text (imessage)');
  });
});
