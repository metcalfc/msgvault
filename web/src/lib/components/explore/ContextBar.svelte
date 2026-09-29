<script lang="ts">
  import XIcon from '@lucide/svelte/icons/x';
  import { untrack } from 'svelte';
  import {
    Button, Checkbox, DateRangePicker, IconButton, SegmentedControl, SelectDropdown, type RangeSelection
  } from '@kenn-io/kit-ui';

  import type { APIClient } from '../../api/client';
  import type {
    ExploreColumn, ExploreFilter, ExploreGroupDimension, ExploreSearchMode, ExploreURLState
  } from '../../explore/models';
  import { EXPLORE_COLUMN_LABELS } from '../../explore/models';
  import {
    DATE_RANGE_PRESETS, activeDateRangePreset, dateBound, dateInputValue, isDateDimension,
    withDateRange, type DateRangePreset
  } from '../../explore/date-range';
  import { withRangeSelection } from '../../explore/date-range-selection';
  import {
    groupingDimensionLabel,
    groupingOptions,
    isGroupingDimension
  } from '../../grouping/catalog';
  import { shortDate } from '../../util/dates';
  import { messageTypeLabel } from '../../util/labels';
  import { FilterLabels } from '../../explore/filter-labels.svelte';
  import { withPersonFilter } from '../../explore/group-context';
  import {
    effectiveSearchMode, queryHasAttachmentOperator, queryOperatorChips, withAttachmentOperator, withoutQueryToken
  } from '../../search/query';
  import IdentityFilter from './IdentityFilter.svelte';
  import PersonFilterPicker from './PersonFilterPicker.svelte';
  import SaveViewSheet from '../saved-views/SaveViewSheet.svelte';

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
    onColumnsChange = undefined,
    onQueryChange = undefined,
    sourceLabelHint = undefined,
    saveState = undefined,
    onSaved = undefined
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
    /** Commits a rewritten query: removing an operator chip, or the
     * has:attachment toggle, which has no filter dimension. */
    onQueryChange?: (query: string) => void;
    /** An account name already on screen for a source ID, if any. */
    sourceLabelHint?: (sourceID: string) => string | undefined;
    /** The full view state; when given, a Save view button saves it. */
    saveState?: ExploreURLState;
    onSaved?: (name: string) => void;
  } = $props();

  let saveOpen = $state(false);

  /** A filter as the chips above show it: one label per person or account. */
  function filterChips(filter: ExploreFilter): string[] {
    return perValue(filter) ? filter.values.map((value) => valueChipText(filter, value)) : [crumbText(filter)];
  }

  let filtersOpen = $state(false);
  const MULTIPLE = '\u0000multiple';
  const labels =new FilterLabels(untrack(() => client));
  $effect(() => labels.ensure(filters));
  $effect(() => {
    if (filtersOpen) labels.loadSources();
  });
  const operatorChips = $derived(queryOperatorChips(query));
  const relevanceOrdered = $derived(query.trim() !== '' && effectiveSearchMode(query, searchMode) !== 'full_text');
  const hasAttachment = $derived(queryHasAttachmentOperator(query));
  const sourceValues = $derived(filters.find((filter) => filter.dimension === 'source')?.values ?? []);
  const sourceValue = $derived(sourceValues.length === 1 ? sourceValues[0]! : sourceValues.length > 1 ? MULTIPLE : '');
  const sourceOptions = $derived([
    { value: '', label: 'Any account' },
    ...(sourceValues.length > 1 ? [{ value: MULTIPLE, label: 'Multiple', disabled: true }] : []),
    ...(sourceValues.length === 1 && !labels.sourceOptions.some((source) => String(source.id) === sourceValues[0])
      ? [{ value: sourceValues[0]!, label: labels.sourceLabel(sourceValues[0]!, sourceLabelHint?.(sourceValues[0]!)) }]
      : []),
    ...labels.sourceOptions.map((source) => ({
      value: String(source.id),
      label: source.display_name?.trim() || source.identifier
    }))
  ]);

  function selectSource(value: string): void {
    if (value === MULTIPLE || value === sourceValue) return;
    const rest = filters.filter((filter) => filter.dimension !== 'source');
    onFiltersChange(value ? [...rest, { dimension: 'source', values: [value] }] : rest);
  }

  function addPerson(participantID: string, label: string): void {
    labels.rememberParticipant(participantID, label);
    const next = withPersonFilter(filters, participantID);
    if (next.length !== filters.length) onFiltersChange(next);
  }

  /** A multi-valued participant filter shows one chip per person; removing
   * one keeps the others. */
  function removeFilterValue(index: number, value: string): void {
    const filter = filters[index];
    if (!filter) return;
    const values = filter.values.filter((item) => item !== value);
    onFiltersChange(values.length > 0
      ? filters.map((item, position) => position === index ? { ...item, values } : item)
      : filters.filter((_, position) => position !== index));
  }
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
  const OFFERED_MESSAGE_TYPES = ['email', 'chat', 'imessage', 'sms', 'calendar_event', 'meeting_transcript'];
  const messageTypeValues = $derived(filters.find((filter) => filter.dimension === 'message_type')?.values ?? []);
  const messageType = $derived(messageTypeValues.length > 1 ? MULTIPLE_MESSAGE_TYPES : (messageTypeValues[0] ?? ''));
  const messageTypeOptions = $derived([
    { value: '', label: 'Any type' },
    ...(messageTypeValues.length > 1 ? [{ value: MULTIPLE_MESSAGE_TYPES, label: 'Multiple', disabled: true }] : []),
    // A single non-empty value outside the offered types (from a URL or a
    // drilled group) is named by its own label rather than read back as
    // "Any type"; an empty value is "Any type" itself.
    ...(messageTypeValues.length === 1 && messageType !== '' && !OFFERED_MESSAGE_TYPES.includes(messageType)
      ? [{ value: messageType, label: messageTypeLabel(messageType) || messageType }]
      : []),
    ...OFFERED_MESSAGE_TYPES.map((value) => ({ value, label: messageTypeLabel(value) }))
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

  /** A custom range rewrites only the bound the user changed; a preset
   * means whole days for both bounds. */
  function selectDateBounds(selection: RangeSelection): void {
    onFiltersChange(withRangeSelection(filters, selection));
  }

  function selectMessageType(value: string): void {
    if (value === MULTIPLE_MESSAGE_TYPES || value === messageType) return;
    const rest = filters.filter((filter) => filter.dimension !== 'message_type');
    onFiltersChange(value ? [...rest, { dimension: 'message_type', values: [value] }] : rest);
  }

  /** "After Sep 22", "Type: Text (iMessage)", "List: …", or the dimension
   * and values for the rest. People and accounts get one chip per value
   * (see chipValues). */
  function crumbText(filter: ExploreFilter): string {
    if (isDateDimension(filter.dimension)) {
      return `${filter.dimension === 'after' ? 'After' : 'Before'} ${shortDate(filter.values[0] ?? '')}`;
    }
    if (filter.dimension === 'message_type') return `Type: ${filter.values.map(messageTypeLabel).join(', ')}`;
    if (filter.dimension === 'mailing_list') return `List: ${filter.values.join(', ')}`;
    if (filter.dimension === 'domain') return `Domain: ${filter.values.join(', ')}`;
    if (filter.dimension === 'identity') return `Identity: ${filter.values[1] ?? ''}`;
    if (filter.dimension === 'deletion') return `Deleted: ${filter.values.join(', ')}`;
    return `Filter ${filter.dimension}: ${filter.values.join(', ')}`;
  }

  function valueChipText(filter: ExploreFilter, value: string): string {
    return filter.dimension === 'participant'
      ? `Person: ${labels.participantLabel(value)}`
      : `Account: ${labels.sourceLabel(value, sourceLabelHint?.(value))}`;
  }

  function perValue(filter: ExploreFilter): boolean {
    return filter.dimension === 'participant' || filter.dimension === 'source';
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
    <!-- Semantic and hybrid results are ranked by relevance; the control
         says so instead of claiming a date order it cannot apply. -->
    <Button
      size="sm"
      surface="soft"
      label={relevanceOrdered ? 'Relevance' : 'Newest first'}
      ariaLabel={relevanceOrdered ? 'Sort: relevance' : 'Sort: newest first'}
      onclick={() => onSort?.()}
    />
  </div>

  <div class="context-crumbs">
    {#each filters as filter, index (`${filter.dimension}:${filter.values.join('\u0000')}`)}
      {#if perValue(filter)}
        {#each filter.values as value (value)}
          <span class="crumb crumb--filter">
            {valueChipText(filter, value)}
            <IconButton
              size="sm"
              ariaLabel={`Remove ${valueChipText(filter, value)}`}
              onclick={() => removeFilterValue(index, value)}
            ><XIcon size="12" aria-hidden="true" /></IconButton>
          </span>
        {/each}
      {:else}
        <span class="crumb crumb--filter" class:crumb--date={isDateDimension(filter.dimension)}>
          {crumbText(filter)}
          <IconButton
            size="sm"
            ariaLabel={`Remove ${crumbText(filter)}`}
            onclick={() => removeFilter(index)}
          ><XIcon size="12" aria-hidden="true" /></IconButton>
        </span>
      {/if}
    {/each}
    <!-- Operators with no filter dimension stay in the query text; each
         still reads as its own chip and can be removed on its own. -->
    {#each operatorChips as chip (`${chip.index}:${chip.token}`)}
      <span class="crumb crumb--operator">
        {chip.label}
        {#if onQueryChange}
          <IconButton
            size="sm"
            ariaLabel={`Remove ${chip.label}`}
            onclick={() => onQueryChange?.(withoutQueryToken(query, chip.index))}
          ><XIcon size="12" aria-hidden="true" /></IconButton>
        {/if}
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
    {#if !query.trim() && filters.length === 0 && groupingChain.length === 0}
      <span class="empty-context">All archive entries</span>
    {/if}
  </div>

  {#if totalCount !== undefined}
    <span class="context-count" data-mono>{`${totalCount.toLocaleString()} results`}</span>
  {/if}

  {#if saveState}
    <Button size="sm" surface="outline" label="Save view" onclick={() => (saveOpen = true)} />
  {/if}

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

  {#if saveOpen && saveState}
    <SaveViewSheet
      {client}
      view={saveState}
      describeFilter={filterChips}
      onclose={() => (saveOpen = false)}
      onsaved={onSaved}
    />
  {/if}

  {#if filtersOpen}
    <div class="filter-panel">
      <div class="filter-summary">
        {#if filters.length === 0}
          <span>No active filters</span>
        {:else}
          <span>{filters.length} active {filters.length === 1 ? 'filter' : 'filters'}</span>
          <Button size="sm" surface="soft" label="Clear filters" onclick={onClearFilters} />
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
        <SelectDropdown
          title="Account"
          value={sourceValue}
          options={sourceOptions}
          onchange={selectSource}
        />
        {#if onQueryChange}
          <!-- No filter dimension carries attachments, so this toggles the
               has:attachment operator in the query text. -->
          <Checkbox
            checked={hasAttachment}
            label="Has attachment"
            onchange={() => onQueryChange?.(withAttachmentOperator(query, !hasAttachment))}
          />
        {/if}
      </div>
      <div class="filter-field">
        <span>Person</span>
        <PersonFilterPicker {client} onpick={addPerson} />
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

  .crumb--operator {
    border: 1px solid var(--border-muted);
    font-family: var(--font-mono);
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
