<script lang="ts">
  import { Button, EmptyState, Notice, SelectDropdown, Typeahead, type TypeaheadOption } from '@kenn-io/kit-ui';
  import { onDestroy, untrack } from 'svelte';

  import { completeParticipants, listSourceStatus } from '../../api/generated/api/api';
  import type { APIClient } from '../../api/client';
  import type { EntryRow, ExploreFilter } from '../../explore/models';
  import { createExploreAPI } from '../../explore/api';
  import { entityNames } from '../../names/entity-names.svelte';
  import type { MeetingWindow } from '../../routing/routes';
  import { humanizeDate } from '../../util/dates';

  interface Props {
    client: APIClient;
    person: string;
    source: string;
    since: MeetingWindow;
    onFiltersChange: (patch: { meetingPerson?: string; meetingSource?: string; meetingSince?: MeetingWindow }) => void;
    onOpenMeeting: (messageID: number) => void;
  }

  let { client, person, source, since, onFiltersChange, onOpenMeeting }: Props = $props();

  const PAGE_LIMIT = 100;
  const DAY_MS = 86_400_000;
  const api = createExploreAPI(untrack(() => client));
  const names = $derived(entityNames(client));

  let rows = $state<EntryRow[]>([]);
  let cursor = $state<string>();
  let loading = $state(false);
  let loadingMore = $state(false);
  let error = $state('');
  let unavailable = $state(false);
  let controller: AbortController | undefined;
  onDestroy(() => controller?.abort());

  /** The filters behind the loaded first page. A cursor is bound to its
   * first page's exact request, so later pages reuse these bounds rather
   * than recomputing "now". */
  let pageFilters: ExploreFilter[] = [];

  /** Calendar events and meeting transcripts, newest first, within the
   * window, and narrowed to one person or account when chosen. */
  function filters(): ExploreFilter[] {
    const result: ExploreFilter[] = [{ dimension: 'message_type', values: ['calendar_event', 'meeting_transcript'] }];
    if (since !== 'all') {
      const days = since === '90d' ? 90 : 30;
      result.push({ dimension: 'after', values: [new Date(Date.now() - days * DAY_MS).toISOString()] });
    }
    // Upcoming events stay out of a list of meetings that happened.
    result.push({ dimension: 'before', values: [new Date().toISOString()] });
    if (person) result.push({ dimension: 'participant', values: [person] });
    if (source) result.push({ dimension: 'source', values: [source] });
    return result;
  }

  async function load(next: string | undefined): Promise<void> {
    if (!next) {
      controller?.abort();
      controller = new AbortController();
      loading = true;
      error = '';
      unavailable = false;
      pageFilters = filters();
    } else loadingMore = true;
    const signal = controller!.signal;
    try {
      const loaded = await api.explore({
        filters: pageFilters, presentation: 'table', grouping: [],
        sort: [{ field: 'occurred_at', direction: 'desc' }], limit: PAGE_LIMIT,
        ...(next ? { cursor: next } : {}),
      }, signal);
      if (signal.aborted) return;
      if (loaded.status !== 'ready') {
        unavailable = true;
        rows = [];
        cursor = undefined;
        return;
      }
      rows = next ? [...rows, ...loaded.result.rows.filter((row) => !rows.some((known) => known.key === row.key))] : loaded.result.rows;
      cursor = loaded.result.nextCursor;
    } catch (cause) {
      if (!signal.aborted) error = cause instanceof Error ? cause.message : 'Could not load meetings.';
    } finally {
      if (!signal.aborted) {
        loading = false;
        loadingMore = false;
      }
    }
  }

  $effect(() => {
    void person;
    void source;
    void since;
    untrack(() => void load(undefined));
  });

  // Person choices come from the participant index as the reader types.
  let personOptions = $state<TypeaheadOption[]>([]);
  let personLoading = $state(false);
  let personLabel = $state('');
  let personQueryGeneration = 0;
  async function searchPeople(query: string): Promise<void> {
    const generation = ++personQueryGeneration;
    if (!query.trim()) {
      personOptions = [];
      return;
    }
    personLoading = true;
    try {
      const { data } = await completeParticipants({ query: query.trim(), limit: 20 }, client);
      if (generation !== personQueryGeneration) return;
      const seen = new Set<number>();
      personOptions = (data?.rows ?? []).filter((row) => !seen.has(row.participant_id) && seen.add(row.participant_id))
        .map((row) => ({ name: String(row.participant_id), label: row.display_label || row.value, meta: row.value }));
    } catch {
      if (generation === personQueryGeneration) personOptions = [];
    } finally {
      if (generation === personQueryGeneration) personLoading = false;
    }
  }

  let accounts = $state<Array<{ value: string; label: string }>>([]);
  $effect(() => {
    const controller = new AbortController();
    void listSourceStatus(undefined, { ...client, signal: controller.signal }).then(({ data }) => {
      accounts = (data?.sources ?? []).map((account) => ({
        value: String(account.id), label: account.display_name?.trim() || account.identifier,
      }));
    }).catch(() => undefined);
    return () => controller.abort();
  });
  // SelectDropdown shows its first option for an unmatched value. Until the
  // account list loads, or for an account it no longer lists, the chosen
  // account keeps its own row so the trigger never reads "All accounts" while
  // the results are filtered to one.
  const accountOptions = $derived([
    { value: '', label: 'All accounts' },
    ...(source && !accounts.some((account) => account.value === source)
      ? [{ value: source, label: `Account #${source}` }]
      : []),
    ...accounts,
  ]);
  const windowOptions: Array<{ value: MeetingWindow; label: string }> = [
    { value: '30d', label: 'Last 30 days' },
    { value: '90d', label: 'Last 90 days' },
    { value: 'all', label: 'All time' },
  ];

  function kindLabel(row: EntryRow): string {
    return row.message_type === 'meeting_transcript' ? 'Transcript' : 'Event';
  }

  function open(event: MouseEvent, row: EntryRow): void {
    if (row.anchor_message_id === undefined) return;
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onOpenMeeting(row.anchor_message_id);
  }
</script>

<main class="meetings-workspace" aria-label="Meetings">
  <header class="meetings-header">
    <div>
      <h1 data-page-title>Meetings</h1>
      <p>Calendar events and meeting transcripts, most recent first.</p>
    </div>
  </header>

  <div class="meetings-filters" role="group" aria-label="Meeting filters">
    <SelectDropdown title="Window" value={since} options={windowOptions}
      onchange={(value) => onFiltersChange({ meetingSince: value as MeetingWindow })} />
    <Typeahead
      options={personOptions}
      value={person}
      fallbackLabel={person ? personLabel || names.label('participant', Number(person)) : 'Anyone'}
      placeholder="Find a person…"
      title="Person"
      triggerPrefix="Person:"
      remote
      allowClear
      clearLabel="Anyone"
      loading={personLoading}
      onquery={(query) => void searchPeople(query)}
      onselect={(value) => {
        personLabel = personOptions.find((option) => option.name === value)?.label ?? '';
        onFiltersChange({ meetingPerson: value });
      }}
    />
    <SelectDropdown title="Account" value={source} options={accountOptions}
      onchange={(value) => onFiltersChange({ meetingSource: value })} />
  </div>

  {#if error}<Notice tone="error" message={error} />{/if}
  {#if unavailable}<Notice tone="warning" message="Meetings need the analytical cache, which is not ready yet." />{/if}

  <section class="meetings-list" aria-label="Meeting results" aria-busy={loading || loadingMore}>
    {#if rows.length === 0}
      {#if loading}
        <p class="state" role="status">Loading meetings…</p>
      {:else if !unavailable && !error}
        <EmptyState title="No meetings" description="No calendar events or transcripts match this window and filter." />
      {/if}
    {:else}
      <ul>
        {#each rows as row (row.key)}
          <li>
            <a class="meeting-row" data-list-row href={row.anchor_message_id !== undefined ? `/meetings/${row.anchor_message_id}` : undefined}
              onclick={(event) => open(event, row)}>
              <span class="row-main">
                <span class="title" data-row-title>{row.title || 'Untitled meeting'}</span>
                <span class="kind">{kindLabel(row)}</span>
                <span class="when" data-mono><time datetime={row.occurred_at}>{humanizeDate(row.occurred_at)}</time></span>
              </span>
              <span class="row-meta" data-meta>{(row.participant_labels ?? []).join(', ') || row.source_identifier}</span>
            </a>
          </li>
        {/each}
      </ul>
      {#if cursor}
        <div class="more">
          <Button surface="soft" label={loadingMore ? 'Loading more…' : 'Load more meetings'} disabled={loadingMore}
            onclick={() => void load(cursor)} />
        </div>
      {/if}
    {/if}
  </section>
</main>

<style>
  .meetings-workspace {
    display: flex;
    flex: 1;
    min-height: 0;
    flex-direction: column;
    gap: var(--space-4);
    width: 100%;
    max-width: 1080px;
    margin-inline: auto;
    padding: var(--space-6) var(--space-7) var(--space-4);
    overflow: auto;
  }

  h1, p { margin: 0; }
  .meetings-header p { color: var(--text-muted); font-size: var(--font-size-sm); }
  .meetings-filters { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
  ul { margin: 0; padding: 0; list-style: none; }

  .meeting-row {
    display: grid;
    gap: 2px;
    padding: var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--hairline);
    color: var(--text-primary);
    text-decoration: none;
  }

  .meeting-row:hover { background: var(--surface-well); }
  .meeting-row:focus-visible { outline: var(--focus-ring); outline-offset: -2px; }
  .row-main { display: flex; align-items: baseline; gap: var(--space-3); }
  .title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
  .kind {
    flex: none;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    padding: 0 var(--space-1);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }
  .when { flex: none; margin-left: auto; color: var(--text-muted); font-size: var(--font-size-xs); }
  .row-meta { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-secondary); font-size: var(--font-size-sm); }
  .state { color: var(--text-muted); font-size: var(--font-size-sm); }
  .more { display: flex; justify-content: center; padding: var(--space-3); }

  @media (max-width: 760px) {
    .meetings-workspace { padding: var(--space-4) var(--space-3); }
  }
</style>
