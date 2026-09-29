import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import SearchModeControl from './SearchModeControl.svelte';

describe('SearchModeControl', () => {
  it('labels all three modes and emits explicit choices', async () => {
    const onchange = vi.fn();
    render(SearchModeControl, { props: { requestedMode: 'full_text', onchange } });

    expect(screen.getByRole('radio', { name: 'Full text' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'Semantic' })).toBeDefined();
    expect(screen.getByRole('radio', { name: 'Hybrid' })).toBeDefined();
    await fireEvent.click(screen.getByRole('radio', { name: 'Semantic' }));
    expect(onchange).toHaveBeenCalledWith('semantic');
  });

  it.each(['disabled', 'initializing', 'stale', 'incomplete', 'unavailable', 'ready'] as const)(
    'preserves the requested mode through %s coverage',
    async (status) => {
      const { rerender } = render(SearchModeControl, { props: { requestedMode: 'hybrid', status } });

      expect(screen.getByRole('radio', { name: 'Hybrid' }).getAttribute('aria-checked')).toBe('true');
      await rerender({ requestedMode: 'hybrid', status });
      expect(screen.getByRole('radio', { name: 'Hybrid' }).getAttribute('aria-checked')).toBe('true');
    }
  );

  it('explains degraded semantic coverage on the semantic segments', () => {
    render(SearchModeControl, { props: { requestedMode: 'full_text', status: 'disabled' } });

    expect(screen.getByRole('radio', { name: 'Semantic' }).getAttribute('title')).toBe(
      'Semantic search is disabled in Settings'
    );
    expect(screen.getByRole('radio', { name: 'Hybrid' }).getAttribute('title')).toBe(
      'Semantic search is disabled in Settings'
    );
    expect(screen.getByRole('radio', { name: 'Full text' }).getAttribute('title')).toBe(
      'Match words and operators exactly'
    );
  });
});
