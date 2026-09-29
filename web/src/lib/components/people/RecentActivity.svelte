<script lang="ts">
  import type { TimelineRow } from '../../api/generated/models';
  import { shortDate } from '../../util/dates';
  import RowKind from '../explore/RowKind.svelte';

  interface Props {
    rows: TimelineRow[];
    loading?: boolean;
    error?: string | null;
    /** Opens one item: a message, a text burst, or a meeting. */
    onOpen: (row: TimelineRow) => void;
    onSeeAll: () => void;
    /** The person's name; rows they wrote lead with its first word. */
    counterpart?: string;
  }

  let { rows, loading = false, error = null, onOpen, onSeeAll, counterpart = '' }: Props = $props();

  /** "Avery" for "Avery Example"; an address or handle stays whole. */
  const counterpartShort = $derived.by(() => {
    const name = counterpart.trim();
    return /[@+\d]/.test(name) ? name : name.split(/\s+/)[0] ?? '';
  });
  const RECENT_LIMIT = 5;
  const recent = $derived(rows.slice(0, RECENT_LIMIT));

  /** Timeline kinds name the modality; message types refine texts. */
  function kindOf(row: TimelineRow): { kind: string; type: string } {
    const messageType = typeof row.message_type === 'string' ? row.message_type : '';
    if (row.kind === 'chat_burst') return { kind: 'conversation', type: messageType || 'chat' };
    return { kind: row.kind, type: messageType || row.kind };
  }

  /** Who wrote it, then what: "Avery · "Sounds good…"", "you · attachment".
   * Calendar entries have no author to lead with. */
  function secondary(row: TimelineRow): string {
    const authored = !['event', 'calendar_event', 'meeting', 'meeting_transcript'].includes(row.kind);
    const who = !authored ? '' : row.from_me ? 'you' : counterpartShort;
    const count = row.kind === 'chat_burst' && row.message_count > 1 ? `${row.message_count} messages` : '';
    const preview = row.preview?.trim() ? `"${row.preview.trim()}"` : '';
    const attachment = row.has_attachments ? 'attachment' : '';
    return [who, count, preview, attachment].filter(Boolean).join(' · ');
  }
</script>

<section class="recent" aria-label="Recent">
  <div data-section-line>
    <h3 data-section-title>Recent</h3>
    <span>email, texts, and meetings together · <button type="button" onclick={onSeeAll}>See all in Timeline</button></span>
  </div>
  {#if error}
    <p class="state" role="status">{error}</p>
  {:else if recent.length === 0}
    <p class="state" role="status">{loading ? 'Loading recent activity…' : 'No recent activity.'}</p>
  {:else}
    <ul class="recent-list">
      {#each recent as row (row.key)}
        {@const kind = kindOf(row)}
        <li>
          <button type="button" class="recent-item" onclick={() => onOpen(row)}>
            <span class="glyph"><RowKind kind={kind.kind} messageType={kind.type} compact /></span>
            <span class="text">
              <span class="title" data-row-title>{row.title || '(untitled)'}</span>
              {#if secondary(row)}<small>{secondary(row)}</small>{/if}
            </span>
            <time class="date" datetime={row.occurred_at}>{shortDate(row.occurred_at)}</time>
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .recent { display: grid; min-width: 0; }
  .recent-list { margin: 0; padding: 0; list-style: none; }

  .recent-item {
    display: grid;
    width: 100%;
    grid-template-columns: 20px minmax(0, 1fr) auto;
    align-items: baseline;
    gap: var(--space-5);
    padding: 8px 0;
    border: 0;
    border-bottom: 1px solid var(--hairline);
    background: none;
    color: var(--text-primary);
    font: inherit;
    font-size: 13px;
    text-align: left;
    cursor: pointer;
  }

  .recent-item:hover .title { text-decoration: underline; }
  .recent-item:focus-visible { outline: var(--focus-ring); outline-offset: -2px; }
  .glyph { align-self: center; }
  .text { display: grid; min-width: 0; }
  .title, small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title { font-size: 13px; }
  small { color: var(--text-muted); font-size: 12px; }
  /* Sans with tabular figures: a monospace space reads as a double gap in "Sep 27". */
  .date { color: var(--text-muted); font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .state { margin: 0; padding: 8px 0; color: var(--text-muted); font-size: var(--font-size-sm); }
</style>
