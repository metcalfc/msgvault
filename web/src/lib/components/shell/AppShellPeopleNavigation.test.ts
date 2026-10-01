import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { ExploreState } from '../../explore/state.svelte';
import AppShell from './AppShell.svelte';

function renderPeople() {
  window.history.replaceState(null, '', '/people');
  const state = new ExploreState(window);
  const client = createAPIClient(vi.fn<typeof fetch>(async () => Response.json({
    people: [], rows: [], total_count: 0, cache_revision: 'test', identity_revision: 1,
    pending: false, kinds: [],
  })));
  const rendered = render(AppShell, { client, state, enabled: false });
  return { state, destroy: () => { rendered.unmount(); state.destroy(); } };
}

afterEach(() => vi.useRealTimers());

describe('People filter navigation', () => {
  it.each([
    ['Search people', 'directoryQuery'],
    ['Category filter', 'directoryCategory'],
    ['Organization filter', 'directoryOrganization'],
  ] as const)('discards pending %s edits when history restores the list', async (label, field) => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { state, destroy } = renderPeople();
    try {
      state.commitNavigation({ [field]: 'earlier' });
      state.commitNavigation({ [field]: 'current' });
      const input = await screen.findByLabelText(label) as HTMLInputElement;
      await fireEvent.input(input, { target: { value: 'pending' } });
      window.history.back();
      await new Promise((resolve) => window.addEventListener('popstate', resolve, { once: true }));
      expect(state.current[field]).toBe('earlier');
      await vi.advanceTimersByTimeAsync(250);
      expect(state.current[field]).toBe('earlier');
      expect(input.value).toBe('earlier');
    } finally {
      destroy();
    }
  });

  it('commits pending typing to the People history entry before leaving the list', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { state, destroy } = renderPeople();
    try {
      const input = await screen.findByLabelText('Search people');
      await fireEvent.input(input, { target: { value: 'Example' } });
      expect(state.current.directoryQuery).toBe('');
      await fireEvent.click(within(screen.getByRole('navigation', { name: 'Primary' })).getByRole('button', { name: 'Inbox' }));
      expect(state.current.workspace).toBe('everything');
      window.history.back();
      await new Promise((resolve) => window.addEventListener('popstate', resolve, { once: true }));
      expect(state.current.workspace).toBe('directory');
      expect(state.current.directoryQuery).toBe('Example');
      await vi.advanceTimersByTimeAsync(250);
      expect(state.current.directoryQuery).toBe('Example');
    } finally {
      destroy();
    }
  });
});
