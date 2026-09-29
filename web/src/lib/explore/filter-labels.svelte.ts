import { SvelteMap } from 'svelte/reactivity';

import { listSourceStatus } from '../api/generated/api/api';
import type { APIClient } from '../api/client';
import type { SourceStatus } from '../api/generated/models';
import { UNKNOWN_LABELS, entityNames } from '../names/entity-names.svelte';
import type { ExploreFilter } from './models';

/**
 * Human names for the IDs that participant and source filters carry, so a
 * chip reads "Person: Avery Example" instead of "participant: 42". Participant
 * names come from the shared entity-name resolver; a label never shows the ID.
 */
export class FilterLabels {
  readonly participants = new SvelteMap<string, string>();
  readonly sources = new SvelteMap<string, string>();
  sourceOptions = $state<SourceStatus[]>([]);
  private sourcesRequested = false;

  constructor(private readonly client: APIClient) {}

  /** Remembers a label the caller already knows (a typeahead pick). */
  rememberParticipant(id: string, label: string): void {
    const name = label.trim();
    if (!name) return;
    this.participants.set(id, name);
  }

  participantLabel(id: string): string {
    const remembered = this.participants.get(id);
    if (remembered) return remembered;
    return /^\d+$/.test(id) ? entityNames(this.client).label('participant', Number(id)) : UNKNOWN_LABELS.participant;
  }

  /** `hint` is a name the caller already has on screen (a loaded row's
   * account), so a source chip needs no request of its own. */
  sourceLabel(id: string, hint: string | undefined = undefined): string {
    return this.sources.get(id) ?? hint ?? `Account #${id}`;
  }

  /** Resolves participant chips. Account names load with the Filters
   * panel's account list (loadSources), not per chip. */
  ensure(filters: readonly ExploreFilter[]): void {
    for (const filter of filters) {
      if (filter.dimension !== 'participant') continue;
      const ids = filter.values.filter((id) => /^\d+$/.test(id) && !this.participants.has(id)).map(Number);
      entityNames(this.client).load('participant', ids).catch(() => {
        // The chip shows the resolver's "unavailable" label until a later render retries.
      });
    }
  }

  loadSources(): void {
    if (this.sourcesRequested) return;
    this.sourcesRequested = true;
    void listSourceStatus(undefined, this.client)
      .then(({ data }) => {
        if (!Array.isArray(data?.sources)) return;
        this.sourceOptions = data.sources;
        for (const source of data.sources) {
          this.sources.set(String(source.id), source.display_name?.trim() || source.identifier);
        }
      })
      .catch(() => {
        this.sourcesRequested = false;
      });
  }
}
