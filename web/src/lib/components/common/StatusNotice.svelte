<script lang="ts">
  import type { Snippet } from 'svelte';

  /**
   * A toned row with a 2px leading bar: a failure or named state that
   * carries its own action, typically a Retry button that must disable
   * while its request runs. Kit's Notice covers states whose action never
   * disables; this is the one app definition for the rest.
   */
  interface Props {
    tone?: 'error' | 'warning' | 'info';
    /** Defaults to alert for errors and status otherwise. */
    role?: 'alert' | 'status';
    /** Stack the content (a heading line above its explanation). */
    stack?: boolean;
    children: Snippet;
  }

  let { tone = 'error', role = undefined, stack = false, children }: Props = $props();
</script>

<div class="status-notice" class:status-notice--stack={stack} data-tone={tone} role={role ?? (tone === 'error' ? 'alert' : 'status')}>
  {@render children()}
</div>

<style>
  .status-notice {
    --status-notice-ink: var(--status-error-ink);
    --status-notice-bg: var(--status-error-bg);

    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    border-left: 2px solid var(--status-notice-ink);
    background: var(--status-notice-bg);
    color: var(--status-notice-ink);
    font-size: var(--font-size-sm);
  }

  .status-notice[data-tone='warning'] {
    --status-notice-ink: var(--status-warning-ink);
    --status-notice-bg: var(--status-warning-bg);
  }

  .status-notice[data-tone='info'] {
    --status-notice-ink: var(--active-ink);
    --status-notice-bg: var(--selected-bg);
  }

  .status-notice--stack {
    align-items: flex-start;
    flex-direction: column;
    gap: var(--space-2);
  }

  @media (max-width: 760px) {
    .status-notice {
      align-items: stretch;
      flex-direction: column;
    }
  }
</style>
