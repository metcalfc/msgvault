<script lang="ts">
  import { Typeahead, debounce, type TypeaheadOption } from '@kenn-io/kit-ui';
  import { onDestroy } from 'svelte';

  import { completeParticipants } from '../../api/generated/api/api';
  import type { APIClient } from '../../api/client';

  interface Props {
    client: APIClient;
    /** Called with the participant ID and the name the completion showed. */
    onpick: (participantID: string, label: string) => void;
  }

  let { client, onpick }: Props = $props();

  let options = $state<TypeaheadOption[]>([]);
  let labels = new Map<string, string>();
  let loading = $state(false);
  let error = $state('');
  let generation = 0;
  let controller: AbortController | undefined;

  const search = debounce((query: string) => {
    const trimmed = query.trim();
    generation += 1;
    controller?.abort();
    if (trimmed.length < 2) {
      options = [];
      loading = false;
      return;
    }
    const requestGeneration = generation;
    const requestController = new AbortController();
    controller = requestController;
    loading = true;
    error = '';
    void completeParticipants({ query: trimmed, limit: 10 }, { ...client, signal: requestController.signal })
      .then(({ data }) => {
        if (requestGeneration !== generation) return;
        const seen = new Set<string>();
        const next: TypeaheadOption[] = [];
        labels = new Map();
        for (const row of data?.rows ?? []) {
          const id = String(row.participant_id);
          if (seen.has(id)) continue;
          seen.add(id);
          labels.set(id, row.display_label);
          next.push({ name: id, label: row.display_label, meta: row.value !== row.display_label ? row.value : undefined });
        }
        options = next;
      })
      .catch((cause: unknown) => {
        if (requestGeneration !== generation || requestController.signal.aborted) return;
        error = cause instanceof Error && cause.message ? cause.message : 'People could not be searched.';
      })
      .finally(() => {
        if (requestGeneration === generation) loading = false;
      });
  }, 150);

  onDestroy(() => {
    search.cancel();
    generation += 1;
    controller?.abort();
  });

  function select(value: string): void {
    if (!value) return;
    // An unknown pick passes no name; the filter chip then asks the resolver.
    onpick(value, labels.get(value) ?? '');
  }
</script>

<Typeahead
  title="Person"
  value=""
  fallbackLabel="Add a person"
  placeholder="Name, email, or phone"
  emptyLabel="Type two letters to find people"
  remote
  {loading}
  {error}
  {options}
  onquery={(query) => search(query)}
  onselect={select}
/>
