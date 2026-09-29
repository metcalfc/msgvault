import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import type { ExploreFilter } from '../../explore/models';
import { chooseSelectOption } from '../../../test/kit-ui';
import ContextBar from './ContextBar.svelte';

function baseProps(overrides: Record<string, unknown> = {}) {
  return {
    client: createAPIClient(vi.fn<typeof fetch>()),
    query: '', searchMode: 'full_text' as const, filters: [] as ExploreFilter[], groupingChain: [],
    presentation: 'table' as const,
    onAddGroup: vi.fn(), onRemoveGroup: vi.fn(), onClearFilters: vi.fn(),
    onFiltersChange: vi.fn(), onPresentationChange: vi.fn(),
    ...overrides
  };
}

describe('ContextBar presentation control', () => {
  it('exposes Table, Timeline, and Files as one keyboard-operable Show-as control', async () => {
    const onPresentationChange = vi.fn();
    render(ContextBar, baseProps({ query: 'pasta', searchMode: 'hybrid', onPresentationChange }));

    const control = screen.getByRole('combobox', { name: 'Show as: Table' });
    await fireEvent.click(control);
    expect(screen.getAllByRole('option').map((option) => option.textContent?.trim()))
      .toEqual(['Table', 'Timeline', 'Files']);
    await fireEvent.click(screen.getByRole('option', { name: 'Timeline' }));
    expect(onPresentationChange).toHaveBeenCalledWith('timeline');
  });
});

describe('ContextBar message type', () => {
  it('names a single value outside the offered types instead of reading it back as Any type', async () => {
    const onFiltersChange = vi.fn();
    render(ContextBar, baseProps({ filters: [{ dimension: 'message_type', values: ['voice_note'] }], onFiltersChange }));

    await fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    const control = screen.getByRole('combobox', { name: 'Message type: Voice note' });
    await fireEvent.click(control);
    expect(screen.getAllByRole('option').map((option) => option.textContent?.trim()))
      .toEqual(['Any type', 'Voice note', 'Email', 'Chat', 'Text (iMessage)', 'Text (SMS)', 'Event', 'Meeting']);
    // Re-picking the current value is not a change.
    await fireEvent.click(screen.getByRole('option', { name: 'Voice note' }));
    expect(onFiltersChange).not.toHaveBeenCalled();
  });

  it('reads an empty single value as Any type without adding a blank option', async () => {
    render(ContextBar, baseProps({ filters: [{ dimension: 'message_type', values: [''] }] }));

    await fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    await fireEvent.click(screen.getByRole('combobox', { name: 'Message type: Any type' }));
    expect(screen.getAllByRole('option').map((option) => option.textContent?.trim()))
      .toEqual(['Any type', 'Email', 'Chat', 'Text (iMessage)', 'Text (SMS)', 'Event', 'Meeting']);
  });
});

describe('ContextBar column picker', () => {
  it('renders only for the table presentation and never empties the column set', async () => {
    const onColumnsChange = vi.fn();
    const rendered = render(ContextBar, baseProps({ columns: ['title'], onColumnsChange }));

    await fireEvent.click(screen.getByText('Columns'));
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Size' }));
    expect(onColumnsChange).toHaveBeenLastCalledWith(['title', 'size']);

    await fireEvent.click(screen.getByRole('checkbox', { name: 'Subject / title' }));
    expect(onColumnsChange).toHaveBeenLastCalledWith(['title']);

    // A presentation without columns (timeline, files) passes neither prop.
    await rendered.rerender(baseProps({ presentation: 'timeline', columns: undefined, onColumnsChange: undefined }));
    expect(screen.queryByText('Columns')).toBeNull();
  });
});

describe('ContextBar date range', () => {
  it('writes presets as after/before filter dimensions and marks the active one', async () => {
    const onFiltersChange = vi.fn();
    render(ContextBar, baseProps({ filters: [{ dimension: 'source', values: ['2'] }], onFiltersChange }));

    const group = screen.getByRole('radiogroup', { name: 'Date range' });
    expect(screen.getByRole('radio', { name: 'All time' }).getAttribute('aria-checked')).toBe('true');
    expect(group.textContent).not.toContain('Custom range');
    await fireEvent.click(screen.getByRole('radio', { name: 'Last 7 days' }));

    const filters = onFiltersChange.mock.calls[0]![0] as ExploreFilter[];
    expect(filters.map((filter) => filter.dimension)).toEqual(['source', 'after', 'before']);
    // Rolling window counting today: starts at local midnight six days ago.
    const after = new Date(filters[1]!.values[0]!).getTime();
    expect(Date.now() - after).toBeGreaterThan(6 * 86_400_000);
    expect(Date.now() - after).toBeLessThan(7 * 86_400_000);
    expect(filters[1]!.values[0]).toMatch(/^\d{4}-\d{2}-\d{2}T/);
  });

  it('renders after/before as date crumbs with a remove control, never as a query crumb', async () => {
    const onFiltersChange = vi.fn();
    render(ContextBar, baseProps({
      filters: [
        { dimension: 'after', values: ['2020-03-05T00:00:00Z'] },
        { dimension: 'message_type', values: ['imessage'] }
      ],
      onFiltersChange
    }));

    expect(screen.getByText(/^After Mar [456], 2020$/)).toBeDefined();
    expect(screen.getByText('Type: Text (iMessage)')).toBeDefined();
    expect(document.querySelector('.crumb--query')).toBeNull();
    expect(screen.getByRole('radio', { name: 'Custom range' }).getAttribute('aria-checked')).toBe('true');

    await fireEvent.click(screen.getByRole('button', { name: /^Remove After Mar/ }));
    expect(onFiltersChange).toHaveBeenCalledWith([{ dimension: 'message_type', values: ['imessage'] }]);
    await fireEvent.click(screen.getByRole('button', { name: 'Remove Type: Text (iMessage)' }));
    expect(onFiltersChange).toHaveBeenLastCalledWith([{ dimension: 'after', values: ['2020-03-05T00:00:00Z'] }]);
  });

  it('shows a multi-valued message-type filter as Multiple and never narrows it on render', async () => {
    const onFiltersChange = vi.fn();
    render(ContextBar, baseProps({
      filters: [{ dimension: 'message_type', values: ['imessage', 'sms'] }],
      onFiltersChange
    }));

    await fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    expect(screen.getByRole('combobox', { name: 'Message type: Multiple' })).toBeDefined();
    expect(screen.getByText('Type: Text (iMessage), Text (SMS)')).toBeDefined();
    expect(onFiltersChange).not.toHaveBeenCalled();

    // Only an explicit pick replaces the filter.
    await chooseSelectOption(screen.getByRole('combobox', { name: 'Message type: Multiple' }), 'Email');
    expect(onFiltersChange).toHaveBeenCalledTimes(1);
    expect(onFiltersChange).toHaveBeenCalledWith([{ dimension: 'message_type', values: ['email'] }]);
  });

  it('offers date and message-type inputs in the Filters popover', async () => {
    const onFiltersChange = vi.fn();
    render(ContextBar, baseProps({ onFiltersChange }));

    await fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    expect(screen.getByText('No active filters')).toBeDefined();
    expect(screen.queryByText(/Filtering controls will expand/)).toBeNull();

    // The date picker reads "All time" with no bounds and writes both bounds
    // as instants when a window is picked.
    await fireEvent.click(screen.getByRole('button', { name: 'All time', expanded: false }));
    const dialog = screen.getByRole('dialog', { name: 'Select date bounds' });
    await fireEvent.click(within(dialog).getByRole('button', { name: '30d' }));
    const [rangeFilters] = onFiltersChange.mock.calls.at(-1) as [ExploreFilter[]];
    expect(rangeFilters.map((filter) => filter.dimension)).toEqual(['after', 'before']);
    const after = new Date(rangeFilters[0]!.values[0]!).getTime();
    expect(Date.now() - after).toBeGreaterThan(28 * 86_400_000);
    expect(Date.now() - after).toBeLessThan(31 * 86_400_000);

    await chooseSelectOption(screen.getByRole('combobox', { name: 'Message type: Any type' }), 'Event');
    expect(onFiltersChange).toHaveBeenLastCalledWith([{ dimension: 'message_type', values: ['calendar_event'] }]);
  });
});
