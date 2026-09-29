import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import type { ReachEntry } from '../../people/reach';
import PersonReachBlock from './PersonReachBlock.svelte';

function entries(): ReachEntry[] {
  return [
    { key: 'email:person@example.test', kind: 'email', value: 'person@example.test', display: 'person@example.test', label: 'person@example.test', observed: false, participantIDs: [] },
    { key: 'phone:15550100001', kind: 'phone', value: '+1 555 010 0001', display: '+1 555 010 0001', label: '+1 555 010 0001', observed: true, name: 'Person', participantIDs: [3] },
    { key: 'chat:whatsapp:key', kind: 'chat', value: 'beeper:opaque-key', display: 'WhatsApp', label: 'WhatsApp identifier for profile 3', observed: true, opaque: true, title: 'beeper:opaque-key', participantIDs: [3] }
  ];
}

describe('PersonReachBlock', () => {
  it('renders one row per method with a kind icon, the value, and an observed badge for archive values', () => {
    render(PersonReachBlock, { entries: entries() });

    const rows = screen.getAllByRole('listitem');
    expect(rows).toHaveLength(3);
    expect(screen.getByRole('img', { name: 'Email' })).toBeDefined();
    expect(screen.getByRole('img', { name: 'Phone' })).toBeDefined();
    expect(screen.getByRole('img', { name: 'Chat' })).toBeDefined();
    expect(rows[0]?.textContent).not.toContain('observed');
    expect(rows[1]?.textContent).toContain('observed');
    expect(rows[1]?.textContent).toContain('Person');
    // Opaque keys stay out of the text and live in the tooltip.
    expect(rows[2]?.textContent).toContain('WhatsApp');
    expect(rows[2]?.textContent).not.toContain('opaque-key');
    expect(rows[2]?.getAttribute('title')).toBe('beeper:opaque-key');
  });

  it('copies the full value and announces it', async () => {
    const onAnnounce = vi.fn();
    const writeText = vi.fn(async () => undefined);
    const original = navigator.clipboard;
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    try {
      render(PersonReachBlock, { entries: entries(), onAnnounce });
      await fireEvent.click(screen.getByRole('button', { name: 'Copy WhatsApp identifier for profile 3' }));
      await waitFor(() => expect(writeText).toHaveBeenCalledWith('beeper:opaque-key'));
      expect(onAnnounce).toHaveBeenCalledWith('Contact method copied');
      expect(await screen.findByRole('button', { name: 'Copied WhatsApp identifier for profile 3' })).toBeDefined();
    } finally {
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: original });
    }
  });

  it('renders nothing when there are no entries', () => {
    render(PersonReachBlock, { entries: [] });
    expect(screen.queryByRole('list')).toBeNull();
  });
});
