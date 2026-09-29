import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import type { MessageDetail } from '../../api/generated/models';
import ChatTranscript, { chatRuns, speakerNames } from './ChatTranscript.svelte';

function chat(id: number, overrides: Partial<MessageDetail> = {}): MessageDetail {
  return {
    id,
    conversation_id: 9,
    subject: '',
    message_type: 'imessage',
    from: '+15555550101',
    from_phone: '+15555550101',
    to: [],
    sent_at: `2026-03-0${id < 3 ? 1 : 2}T1${id}:00:00`,
    snippet: `Line ${id}`,
    body: `Line ${id}`,
    labels: [],
    has_attachments: false,
    size_bytes: 1,
    attachments: [],
    ...overrides
  };
}

describe('ChatTranscript', () => {
  it('names an unnamed one-to-one partner from the row counterpart and groups runs by day and speaker', () => {
    const messages = [chat(1), chat(2), chat(3, { is_from_me: true, from: '', from_phone: '' }), chat(4)];
    const speaker = speakerNames(messages, 'Avery Example');
    const runs = chatRuns(messages, speaker);

    expect(runs.map((run) => [run.speaker, run.messages.map((message) => message.id)])).toEqual([
      ['Avery Example', [1, 2]],
      ['You', [3]],
      ['Avery Example', [4]]
    ]);
    expect(new Set(runs.map((run) => run.day)).size).toBe(2);
  });

  it('prefers a name the same number carried elsewhere and keeps unknown numbers in group chats', () => {
    const messages = [
      chat(1, { from_name: 'Blake Example' }),
      chat(2),
      chat(3, { from: '+15555550199', from_phone: '+15555550199' })
    ];
    const speaker = speakerNames(messages, 'Group label');
    expect(messages.map(speaker)).toEqual(['Blake Example', 'Blake Example', '+15555550199']);
  });

  it('renders day dividers, one speaker label per run, and own messages on the right', async () => {
    const onSelect = vi.fn();
    render(ChatTranscript, {
      props: {
        messages: [chat(1), chat(2), chat(3, { is_from_me: true, from: '', from_phone: '' })],
        anchorId: 2,
        conversationId: 9,
        counterpartLabel: 'Avery Example',
        onSelect
      }
    });

    expect(screen.getAllByText('Avery Example')).toHaveLength(1);
    const runs = screen.getAllByRole('listitem').filter((item) => item.classList.contains('run'));
    expect(runs.map((run) => run.classList.contains('run--mine'))).toEqual([false, true]);
    expect(document.querySelectorAll('.day-divider')).toHaveLength(2);
    expect(document.querySelector('[data-message-id="2"]')?.getAttribute('aria-current')).toBe('true');
    await fireEvent.click(screen.getByText('Line 1'));
    expect(onSelect).toHaveBeenCalledWith(1);
  });
});
