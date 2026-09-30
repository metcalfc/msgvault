<script lang="ts">
  import { Button } from '@kenn-io/kit-ui';

  import { NOT_A_PERSON_CHOICES, assignmentLabel, type NotAPersonKind } from '../../people/correspondent-kind';

  interface Props {
    kind: NotAPersonKind;
    organizationName?: string;
    pending?: boolean;
    error?: string | null;
    onChange: () => void;
    onRestore: () => void;
  }

  let { kind, organizationName = undefined, pending = false, error = null, onChange, onRestore }: Props = $props();
  const explanation = $derived(NOT_A_PERSON_CHOICES.find((choice) => choice.kind === kind)?.explanation ?? '');
</script>

<section class="kind-banner" aria-label="Not a person">
  <div class="text">
    <strong>Not a person · {assignmentLabel({ kind, organization_name: organizationName })}</strong>
    <span>{explanation}</span>
    {#if error}<span class="error" role="alert">{error}</span>{/if}
  </div>
  <div class="actions">
    <Button size="sm" surface="soft" label="Change" disabled={pending} onclick={onChange} />
    <Button size="sm" surface="outline" label="This is a person" disabled={pending} onclick={onRestore} />
  </div>
</section>

<style>
  .kind-banner {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-3);
    border-left: 2px solid var(--accent-amber);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
  }
  .text { display: grid; gap: var(--space-1); min-width: 0; }
  strong { color: var(--text-primary); }
  .text span { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .text .error { color: var(--text-danger); }
  .actions { display: flex; gap: var(--space-2); }
</style>
