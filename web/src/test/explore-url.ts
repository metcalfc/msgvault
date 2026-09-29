import type { ExploreURLState } from '../lib/explore/models';
import { defaultExploreURLState, serializeExploreURLState } from '../lib/explore/state.svelte';

/** The URL the app itself writes for a view, for seeding browser history
 * in tests. Going through the serializer keeps the seed honest: an
 * Everything link carries the same bounds marker a real link would. */
export function exploreLink(state: Partial<ExploreURLState> = {}): string {
  return serializeExploreURLState({ ...defaultExploreURLState, ...state });
}
