<script lang="ts">
  import XIcon from '@lucide/svelte/icons/x';
  import {
    Button, Checkbox, DateRangePicker, IconButton, SegmentedControl, SelectDropdown, resolveRange, type RangeSelection
  } from '@kenn-io/kit-ui';

  import type { APIClient } from '../../api/client';
  import type {
    ExploreColumn, ExploreFilter, ExploreGroupDimension, ExploreSearchMode, ExploreURLState
  } from '../../explore/models';
  import { EXPLORE_COLUMN_LABELS } from '../../explore/models';
  import {
    DATE_RANGE_PRESETS, activeDateRangePreset, dateBound, dateInputBound, dateInputValue, isDateDimension,
    withDateBound, withDateRange, withoutDateRange, type DateRangePreset
  } from '../../explore/date-range';
  import {
    groupingDimensionLabel,
    groupingOptions,
    isGroupingDimension
  } from '../../grouping/catalog';
  import { shortDate } from '../../util/dates';
  import { messageTypeLabel } from '../../util/labels';
  import IdentityFilter from './IdentityFilter.svelte';

  let {
    client,
    query,
    searchMode,
    filters,
    groupingChain,
    totalCount = undefined,
    presentation = 'table',
    onAddGroup,
    onRemoveGroup,
    onClearFilters,
    onFiltersChange,
    onSort = undefined,
    onPresentationChange = undefined,
    columns = undefined,
    onColumnsChange = undefined
  }: {
    client: APIClient;
    query: string;
    searchMode: ExploreSearchMode;
    filters: ExploreFilter[];
    groupingChain: ExploreGroupDimension[];
    totalCount?: number;
    presentation?: ExploreURLState['presentation'];
    onAddGroup: (dimension: ExploreGroupDimension) => void;
    onRemoveGroup: (index: number) => void;
    onClearFilters: () => void;
    onFiltersChange: (filters: ExploreFilter[]) => void;
    onSort?: () => void;
    onPresentationChange?: (presentation: ExploreURLState['presentation']) => void;
    /** The table's visible columns; the Columns picker renders only when
     * both this and onColumnsChange are supplied (the table presentation). */
    columns?: ExploreColumn[];
    onColumnsChange?: (columns: ExploreColumn[]) => void;
  } = $props();

  let filtersOpen = $state(false);
  const ALL_COLUMNS = Object.keys(EXPLORE_COLUMN_LABELS) as ExploreColumn[];

  /** Hiding the last visible column would leave the grid empty, so the
   * title column stays when everything else is turned off. */
  function toggleColumn(column: ExploreColumn): void {
    const visible = columns ?? [];
    const next = visible.includes(column)
      ? visible.filter((item) => item !== column)
      : ALL_COLUMNS.filter((id) => visible.includes(id) || id === column);
    onColumnsChange?.(next.length > 0 ? next : ['title']);
  }
  const options = $derived(groupingOptions({ excluded: groupingChain, includeUnavailable: true }));
  const firstRequestable = $derived(options.find((option) => !option.disabled)?.value ?? '');
  const presentationOptions = [
    { value: 'table', label: 'Table' },
    { value: 'timeline', label: 'Timeline' },
    { value: 'files', label: 'Files' }
  ];
  // A multi-valued message_type filter (from a URL or a drilled group) is
  // shown as "Multiple" and left exactly as it is until the user picks a
  // value; the control never narrows a filter merely by rendering it.
  const MULTIPLE_MESSAGE_TYPES = '\u0000multiple';
  const messageTypeValues = $derived(filters.find((filter) => filter.dimension === 'message_type')?.values ?? []);
  const messageType = $derived(messageTypeValues.length > 1 ? MULTIPLE_MESSAGE_TYPES : (messageTypeValues[0] ?? ''));
  const messageTypeOptions = $derived([
    { value: '', label: 'Any type' },
    ...(messageTypeValues.length > 1 ? [{ value: MULTIPLE_MESSAGE_TYPES, label: 'Multiple', disabled: true }] : []),
    ...['email', 'chat', 'imessage', 'sms', 'calendar_event', 'meeting_transcript']
      .map((value) => ({ value, label: messageTypeLabel(value) }))
  ]);

  const datePreset = $derived(activeDateRangePreset(filters));
  const dateRangeOptions = $derived([
    ...DATE_RANGE_PRESETS,
    ...(datePreset === 'custom' ? [{ value: 'custom', label: 'Custom range', disabled: true }] : [])
  ]);
  // The popover's picker is controlled from the after/before filters: no
  // bounds read as "all time", a single bound as an incomplete custom range
  // the picker reopens armed to complete.
  const dateSelection = $derived.by((): RangeSelection => {
    const after = dateInputValue(dateBound(filters, 'after'));
    const before = dateInputValue(dateBound(filters, 'before'));
    if (!after && !before) return { mode: 'relative', days: 0 };
    return { mode: 'custom', from: after, to: before };
  });

  function selectGrouping(value: string): void {
    if (isGroupingDimension(value)) onAddGroup(value);
  }

  function selectDateRange(value: string): void {
    if (value === 'custom') return;
    onFiltersChange(withDateRange(filters, value as DateRangePreset));
  }

  function removeFilter(index: number): void {
    onFiltersChange(filters.filter((_, position) => position !== index));
  }

  function selectDateBounds(selection: RangeSelection): void {
    if (selection.mode === 'relative' && selection.days <= 0) {
      onFiltersChange(withoutDateRange(filters));
      return;
    }
    const range = resolveRange(selection);
    const bounded = withDateBound(filters, 'after', dateInputBound(range.from, 'after'));
    onFiltersChange(withDateBound(bounded, 'before', dateInputBound(range.to, 'before')));
  }

  function selectMessageType(value: string): void {
    if (value === MULTIPLE_MESSAGE_TYPES || value === messageType) return;
    const rest = filters.filter((filter) => filter.dimension !== 'message_type');
    onFiltersChange(value ? [...rest, { dimension: 'message_type', values: [value] }] : rest);
  }

  /** "After Sep 22", "Type: Text (iMessage)", or the raw dimension for the rest. */
  function crumbText(filter: ExploreFilter): string {
    if (isDateDimension(filter.dimension)) {
      return `${filter.dimension === 'after' ? 'After' : 'Before'} ${shortDate(filter.values[0] ?? '')}`;
    }
    if (filter.dimension === 'message_type') return `Type: ${filter.values.map(messageTypeLabel).join(', ')}`;
    return `Filter ${filter.dimension}: ${filter.values.join(', ')}`;
  }
</script>

<section class="context-bar" aria-label="Active analytical context">
  <div class="context-controls">
    <Button
      size="sm"
      surface={filtersOpen || filters.length > 0 ? 'soft' : 'outline'}
      label="Filters"
      ariaLabel="Filters"
      ariaExpanded={filtersOpen}
      onclick={() => { filtersOpen = !filtersOpen; }}
    />
    <SegmentedControl
      ariaLabel="Date range"
      value={datePreset}
      options={dateRangeOptions}
      onchange={selectDateRange}
    />
    <SelectDropdown
      title="Show as"
      value={presentation}
      options={presentationOptions}
      onchange={(value) => onPresentationChange?.(value as ExploreURLState['presentation'])}
    />
    <div class="group-picker" data-group-picker>
      <SelectDropdown
        value={firstRequestable}
        {options}
        title="Group by"
        disabled={!firstRequestable}
        onchange={selectGrouping}
      />
    </div>
    <Button
      size="sm"
      surface="outline"
      label="Newest first"
      ariaLabel="Sort: newest first"
      onclick={() => onSort?.()}
    />
  </div>

  <div class="context-crumbs">
    {#if query}
      <span class="crumb crumb--query">{searchMode}: “{query}”</span>
    {/if}
    {#each filters as filter, index (`${filter.dimension}:${filter.values.join('\u0000')}`)}
      <span class="crumb crumb--filter" class:crumb--date={isDateDimension(filter.dimension)}>
        {crumbText(filter)}
        <IconButton
          size="sm"
          ariaLabel={`Remove ${crumbText(filter)}`}
          onclick={() => removeFilter(index)}
        ><XIcon size="12" aria-hidden="true" /></IconButton>
      </span>
    {/each}
    {#each groupingChain as dimension, index (`${dimension}:${index}`)}
      <span class="crumb crumb--group">
        Group {groupingDimensionLabel(dimension)}
        <IconButton
          size="sm"
          ariaLabel={`Remove ${groupingDimensionLabel(dimension)} grouping`}
          onclick={() => onRemoveGroup(index)}
        ><XIcon size="12" aria-hidden="true" /></IconButton>
      </span>
    {/each}
    {#if !query && filters.length === 0 && groupingChain.length === 0}
      <span class="empty-context">All archive entries</span>
    {/if}
  </div>

  <span class="context-count" data-mono>{totalCount === undefined ? 'Count pending' : `${totalCount.toLocaleString()} results`}</span>

  {#if columns && onColumnsChange}
    <details class="column-picker-disclosure">
      <summary>Columns</summary>
      <div class="column-picker kit-popover-card">
        {#each ALL_COLUMNS as column (column)}
          <Checkbox
            checked={columns.includes(column)}
            label={EXPLORE_COLUMN_LABELS[column]}
            onchange={() => toggleColumn(column)}
          />
        {/each}
      </div>
    </details>
  {/if}

  {#if filtersOpen}
    <div class="filter-panel">
      <div class="filter-summary">
        {#if filters.length === 0}
          <span>No active filters</span>
        {:else}
          <span>{filters.length} active {filters.length === 1 ? 'filter' : 'filters'}</span>
          <Button size="sm" surface="outline" label="Clear filters" onclick={onClearFilters} />
        {/if}
      </div>
      <div class="filter-fields">
        <div class="filter-field">
          <span>Dates</span>
          <DateRangePicker selection={dateSelection} onSelect={selectDateBounds} dialogLabel="Select date bounds" />
        </div>
        <SelectDropdown
          title="Message type"
          value={messageType}
          options={messageTypeOptions}
          onchange={selectMessageType}
        />
      </div>
      <IdentityFilter {client} {filters} onChange={onFiltersChange} />
    </div>
  {/if}
</section>

<style>
  /* A borderless 32px toolbar row on the canvas; spacing, not a box,
   * separates it from the search bar above and the table below. */
  .context-bar {
    position: relative;
    display: flex;
    min-width: 0;
    min-height: 32px;
    align-items: center;
    gap: var(--space-4);
    font-size: var(--font-size-xs);
  }

  .column-picker-disclosure {
    position: relative;
    flex: none;
    color: var(--text-secondary);
  }

  .column-picker-disclosure summary {
    cursor: pointer;
  }

  .column-picker {
    position: absolute;
    z-index: var(--z-popover);
    top: 24px;
    right: 0;
    display: grid;
    width: 176px;
    gap: var(--space-3);
    padding: var(--space-4);
  }

  .column-picker :global(.kit-checkbox) {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    white-space: nowrap;
  }

  .context-controls,
  .context-crumbs {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--space-2);
  }

  .context-crumbs {
    flex: 1;
    overflow-x: auto;
  }

  .group-picker {
    width: 172px;
  }

  .crumb {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-1) var(--space-2);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .crumb--filter {
    border: 1px solid color-mix(in srgb, var(--accent-amber) 35%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-amber) 8%, var(--bg-surface));
  }

  .crumb--date {
    border-color: color-mix(in srgb, var(--accent-blue) 35%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-blue) 8%, var(--bg-surface));
  }

  .crumb--group {
    border: 1px solid color-mix(in srgb, var(--accent-teal) 35%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-teal) 8%, var(--bg-surface));
  }

  .empty-context,
  .context-count,
  .filter-panel {
    color: var(--text-muted);
  }

  .context-count {
    flex: none;
    font-variant-numeric: tabular-nums;
  }

  .filter-panel {
    position: absolute;
    z-index: var(--z-popover);
    top: calc(100% + var(--space-2));
    left: 0;
    display: flex;
    min-width: 320px;
    align-items: stretch;
    flex-direction: column;
    gap: var(--space-4);
    padding: var(--space-4);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    box-shadow: var(--shadow-md);
  }

  .filter-summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
  }

  .filter-fields {
    display: flex;
    flex-wrap: wrap;
    align-items: end;
    gap: var(--space-3);
  }

  .filter-field {
    display: grid;
    gap: var(--space-1);
    color: var(--text-secondary);
  }

</style>
