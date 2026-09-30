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
  it('renders one labeled row per method with the value and an observed note for archive values', () => {
    render(PersonReachBlock, { entries: entries() });

    const rows = screen.getAllByRole('listitem');
    expect(rows).toHaveLength(3);
    expect(rows.map((row) => row.querySelector('[data-fact-label]')?.textContent)).toEqual(['Email', 'Phone', 'Chat']);
    expect(rows[0]?.querySelector('[data-fact-value]')?.textContent).toBe('person@example.test');
    expect(rows[0]?.textContent).not.toContain('observed');
    expect(rows[1]?.textContent).toContain('observed');
    expect(rows[1]?.textContent).toContain('Person');
    // Opaque keys stay out of the text and live in the tooltip.
    expect(rows[2]?.getAttribute('title')).toBe('beeper:opaque-key');
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

  it('labels a mobile number and trails the address book type and enrichment source', () => {
    render(PersonReachBlock, { entries: [
      { key: 'phone:1', kind: 'phone', value: '+1 555 010 0142', display: '+1 555 010 0142', label: '+1 555 010 0142',
        observed: false, typeLabel: 'cell', participantIDs: [] },
      { key: 'email:work', kind: 'email', value: 'ada@example.test', display: 'ada@example.test', label: 'ada@example.test',
        observed: false, typeLabel: 'work', participantIDs: [] },
      { key: 'url:linkedin', kind: 'url', value: 'linkedin.com/in/ada-example', display: 'linkedin.com/in/ada-example',
        label: 'linkedin.com/in/ada-example', service: 'LinkedIn', observed: false, source: 'enrichment', participantIDs: [] },
    ] });
    const rows = screen.getAllByRole('listitem');
    expect(rows.map((row) => row.querySelector('[data-fact-label]')?.textContent)).toEqual(['Mobile', 'Email', 'LinkedIn']);
    expect(rows[1]?.querySelector('[data-fact-meta]')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('work · copy');
    expect(rows[2]?.querySelector('[data-fact-meta]')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('from enrichment · copy');
  });

  it('renders nothing when there are no entries', () => {
    render(PersonReachBlock, { entries: [] });
    expect(screen.queryByRole('list')).toBeNull();
  });

  it('links emails, E.164 phones, and profiles, and leaves opaque keys unlinked', () => {
    render(PersonReachBlock, { entries: [
      ...entries(),
      { key: 'handle:github:example', kind: 'handle', value: 'example-person', display: 'example-person', label: 'example-person',
        service: 'GitHub', serviceSlug: 'github', profileURLTemplate: 'https://github.com/{username}', observed: false, participantIDs: [] }
    ] });
    const links = screen.getAllByRole('link');
    expect(links.map((link) => link.getAttribute('href'))).toEqual([
      'mailto:person@example.test', 'tel:+15550100001', 'https://github.com/example-person'
    ]);
    expect(links[2]?.getAttribute('rel')).toBe('noopener noreferrer');
    expect(links[2]?.getAttribute('target')).toBe('_blank');
  });
});

