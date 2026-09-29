import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import type { TimelineRow } from '../../api/generated/models';
import RecentActivity from './RecentActivity.svelte';

function row(key: string, patch: Partial<TimelineRow>): TimelineRow {
  return {
    key, kind: 'email', title: 'Subject', preview: '', occurred_at: `${new Date().getFullYear()}-09-27T12:00:00Z`, source_id: 1,
    message_count: 1, has_attachments: false, anchor_message_id: 1, ...patch,
  };
}

describe('RecentActivity', () => {
  it('leads each authored row with who wrote it and shows at most five items', async () => {
    const rows = [
      row('a', { title: 'Re: Q3 update', preview: 'Sounds good' }),
      row('b', { title: 'Deck', from_me: true, has_attachments: true }),
      row('c', { kind: 'event', title: 'Partners sync', preview: '6 attendees' }),
      row('d', { kind: 'chat_burst', title: 'Avery Example', preview: 'see you then', message_count: 4 }),
      row('e', { title: 'Fifth' }),
      row('f', { title: 'Sixth' }),
    ];
    const onOpen = vi.fn();
    render(RecentActivity, { rows, onOpen, onSeeAll: vi.fn(), counterpart: 'Avery Example' });

    const items = screen.getAllByRole('button').filter((button) => button.classList.contains('recent-item'));
    expect(items).toHaveLength(5);
    const lines = items.map((item) => item.querySelector('small')?.textContent ?? '');
    expect(lines.slice(0, 4)).toEqual([
      'Avery · "Sounds good"',
      'you · attachment',
      '"6 attendees"',
      'Avery · 4 messages · "see you then"',
    ]);
    // A single-space date, not the monospace double gap.
    expect(items[0]!.querySelector('time')?.textContent).toBe('Sep 27');

    await fireEvent.click(items[0]!);
    expect(onOpen).toHaveBeenCalledWith(rows[0]);
  });
});
