import { SvelteMap } from 'svelte/reactivity';

import { getParticipant, listSourceStatus } from '../api/generated/api/api';
import type { APIClient } from '../api/client';
import type { SourceStatus } from '../api/generated/models';
import type { ExploreFilter } from './models';

/**
 * Human names for the IDs that participant and source filters carry, so a
 * chip reads "Person: Avery Example" instead of "participant: 42". Lookups
 * are lazy and remembered; an unresolved ID keeps a neutral fallback.
 */
export class FilterLabels {
  readonly participants = new SvelteMap<string, string>();
  readonly sources = new SvelteMap<string, string>();
  sourceOptions = $state<SourceStatus[]>([]);
  private requestedParticipants = new Set<string>();
  private sourcesRequested = false;

  constructor(private readonly client: APIClient) {}

  /** Remembers a label the caller already knows (a typeahead pick). */
  rememberParticipant(id: string, label: string): void {
    this.participants.set(id, label);
    this.requestedParticipants.add(id);
  }

  participantLabel(id: string): string {
    return this.participants.get(id) ?? `Person #${id}`;
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
      if (filter.dimension === 'participant') filter.values.forEach((id) => this.loadParticipant(id));
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

  private loadParticipant(id: string): void {
    if (this.requestedParticipants.has(id) || !/^\d+$/.test(id)) return;
    this.requestedParticipants.add(id);
    void getParticipant({ id: Number(id) }, this.client)
      .then(({ data }) => {
        const label = data?.display_name?.trim() || data?.display_label?.trim();
        if (label) this.participants.set(id, label);
      })
      .catch(() => {
        // The chip keeps its neutral fallback label.
      });
  }
}
