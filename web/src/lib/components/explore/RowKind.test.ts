import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';

import RowKind from './RowKind.svelte';

describe('RowKind', () => {
  it.each([
    ['conversation', 'sms', 'Text (SMS)'],
    ['conversation', 'imessage', 'Text (iMessage)'],
    ['event', 'calendar_event', 'Event'],
    ['meeting', 'meeting_transcript', 'Meeting'],
    ['email', 'unknown', 'Email'],
    ['conversation', 'unknown', 'Conversation'],
    ['unknown', 'unknown', 'Item']
  ])('labels server kind %s with the humanized message type %s', (kind, messageType, label) => {
    render(RowKind, { kind, messageType });
    expect(screen.getByLabelText(label)).toBeDefined();
    expect(screen.getByLabelText(label).textContent).toContain(label);
    expect(document.body.textContent).not.toContain(messageType === 'unknown' ? 'never' : messageType);
  });

  it.each([
    ['conversation', 'imessage', 'text'],
    ['conversation', 'whatsapp', 'chat'],
    ['email', 'email', 'email'],
    ['event', 'calendar_event', 'event'],
    ['meeting', 'meeting_transcript', 'meeting'],
  ])('gives %s/%s the %s glyph ink', (kind, messageType, modality) => {
    const { container } = render(RowKind, { kind, messageType });
    const glyph = container.querySelector('.row-kind')!;
    expect(glyph.getAttribute('data-modality')).toBe(modality);
    expect(glyph.querySelector('svg')?.getAttribute('width')).toBe('16');
  });
});
