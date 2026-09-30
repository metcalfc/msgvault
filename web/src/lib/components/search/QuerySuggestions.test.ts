import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import type { QuerySuggestion } from '../../search/suggestions';
import QuerySuggestions from './QuerySuggestions.svelte';

const texts: QuerySuggestion = {
  kind: 'message_type', label: 'Texts', span: 'texts', spanStart: 0, spanEnd: 5, probability: 0.93,
  filters: [{ dimension: 'message_type', values: ['sms'] }], queryOperators: [],
};
const person: QuerySuggestion = {
  kind: 'person', label: 'With Ana Example', span: 'with Ana', probability: 0.9,
  filters: [{ dimension: 'participant', values: ['7'] }], queryOperators: [],
};

describe('QuerySuggestions', () => {
  it('offers each suggestion as a labelled button in a named group', async () => {
    const onapply = vi.fn();
    render(QuerySuggestions, { props: { suggestions: [texts, person], onapply } });

    expect(screen.getByRole('group', { name: 'Suggested filters' })).toBeDefined();
    expect(screen.getByRole('status').textContent).toBe('2 suggested filters for this search');
    const chip = screen.getByRole('button', {
      name: 'Apply suggested person filter: With Ana Example, replacing “with Ana” in the search',
    });
    expect(chip.textContent).toContain('With Ana Example');
    await fireEvent.click(chip);
    expect(onapply).toHaveBeenCalledWith(person);
  });

  it('renders nothing but an empty status without suggestions', () => {
    render(QuerySuggestions, { props: { suggestions: [], onapply: vi.fn() } });
    expect(screen.queryByRole('group')).toBeNull();
    expect(screen.getByRole('status').textContent).toBe('');
  });
});
