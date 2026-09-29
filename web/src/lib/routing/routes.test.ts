import { describe, expect, it } from 'vitest';

import { dateRangeFilters } from '../explore/date-range';
import type { ExploreFilter, ExploreURLState } from '../explore/models';
import { defaultExploreURLState, parseExploreURLState, serializeExploreURLState } from '../explore/state.svelte';
import { routeTitle } from './routes';

function view(patch: Partial<ExploreURLState>): ExploreURLState {
  return { ...defaultExploreURLState, ...patch };
}

function address(state: Partial<ExploreURLState>): URL {
  return new URL(serializeExploreURLState(view(state)), 'http://msgvault.invalid');
}

describe('readable routes', () => {
  it.each<[string, Partial<ExploreURLState>]>([
    ['/people', { workspace: 'directory' }],
    ['/people/42', { workspace: 'directory', directoryPersonID: 42 }],
    ['/people/42/timeline', { workspace: 'directory', directoryPersonID: 42, personTab: 'timeline' }],
    ['/people/42/profile', { workspace: 'directory', directoryPersonID: 42, personTab: 'profile' }],
    ['/people/contact-7', { workspace: 'relationships', relationshipTarget: 'cluster:7' }],
    ['/people/contact-7/files', { workspace: 'relationships', relationshipTarget: 'cluster:7', personTab: 'files' }],
    ['/people/domains', { workspace: 'relationships', relationshipFacet: 'domains' }],
    ['/reviews', { workspace: 'directory_review' }],
    ['/files', { workspace: 'files' }],
    ['/saved-views', { workspace: 'saved_views' }],
    ['/activity/sources', { workspace: 'sources' }],
    ['/activity/operations', { workspace: 'operations' }],
    ['/activity/deletions', { workspace: 'deletions' }],
    ['/settings', { workspace: 'settings' }],
    ['/settings/search', { workspace: 'settings', settingsSection: 'search' }],
    ['/messages/42001', { workspace: 'message', messageID: 42001 }],
  ])('names %s by its path and reads it back', (pathname, state) => {
    const url = address(state);
    expect(url.pathname).toBe(pathname);
    expect(url.searchParams.has('workspace')).toBe(false);
    expect(parseExploreURLState(url.search, url.pathname)).toMatchObject(state);
  });

  it('opens the old contacts list as the People list filtered to Not saved', () => {
    expect(parseExploreURLState('', '/people/contacts')).toMatchObject({ workspace: 'directory', peopleSaved: 'unsaved' });
    expect(parseExploreURLState('?workspace=relationships', '/')).toMatchObject({ workspace: 'directory', peopleSaved: 'unsaved' });
    expect(address({ workspace: 'directory', peopleSaved: 'unsaved' }).pathname).toBe('/people');
  });

  it('names a domain with a parameter so the daemon serves the app for it', () => {
    const url = address({ workspace: 'relationships', relationshipFacet: 'domains', relationshipTarget: 'domain:example.com' });
    expect(`${url.pathname}${url.search}`).toBe('/people/domains?domain=example.com');
    expect(parseExploreURLState(url.search, url.pathname)).toMatchObject({ relationshipFacet: 'domains', relationshipTarget: 'domain:example.com' });
  });

  it('names the Inbox and Search with readable parameters', () => {
    const inbox = address({ workspace: 'everything', filters: dateRangeFilters('week') });
    expect(`${inbox.pathname}${inbox.search}`).toBe('/inbox?since=7d');

    const search = address({ workspace: 'everything', query: 'quarterly plan', searchMode: 'hybrid', filters: [] });
    expect(search.pathname).toBe('/search');
    expect([...search.searchParams.entries()]).toEqual([['q', 'quarterly plan'], ['mode', 'hybrid'], ['since', 'all']]);
    expect(parseExploreURLState(search.search, search.pathname)).toMatchObject({
      workspace: 'everything', query: 'quarterly plan', searchMode: 'hybrid', filters: [], dateBoundsChosen: true,
    });
  });

  it('keeps exact bounds as after/before and other filters in the explore payload', () => {
    const filters: ExploreFilter[] = [
      { dimension: 'source', values: ['3'] },
      { dimension: 'after', values: ['2020-01-01T00:00:00.000Z'] },
      { dimension: 'before', values: ['2020-02-01T00:00:00.000Z'] },
    ];
    const url = address({ workspace: 'everything', filters, groupingChain: ['domain'] });
    expect(url.pathname).toBe('/inbox');
    expect(url.searchParams.get('after')).toBe('2020-01-01T00:00:00.000Z');
    expect(url.searchParams.get('before')).toBe('2020-02-01T00:00:00.000Z');
    const payload = JSON.parse(url.searchParams.get('explore')!);
    expect(payload.filters).toEqual([{ dimension: 'source', values: ['3'] }]);
    expect(payload.groupingChain).toEqual(['domain']);
    expect(parseExploreURLState(url.search, url.pathname).filters).toEqual(filters);
  });

  it('opens a bare Inbox without bounds so the seven-day default applies', () => {
    expect(parseExploreURLState('', '/inbox')).toMatchObject({ workspace: 'everything', filters: [], dateBoundsChosen: false });
    const rolling = parseExploreURLState('?since=30d', '/inbox');
    expect(rolling.filters.map((filter) => filter.dimension)).toEqual(['after', 'before']);
  });

  it('reads legacy workspace links on the root path', () => {
    const legacy = `?workspace=directory&mode=full_text&explore=${encodeURIComponent(JSON.stringify({ schemaVersion: 2, directoryPersonID: 7 }))}`;
    expect(parseExploreURLState(legacy, '/')).toMatchObject({ workspace: 'directory', directoryPersonID: 7 });
    const older = `?explore=${encodeURIComponent(JSON.stringify({ workspace: 'people', analysisTarget: 'person:9' }))}`;
    expect(parseExploreURLState(older, '/')).toMatchObject({ workspace: 'relationships', relationshipTarget: 'cluster:9' });
  });

  it('keeps parameters the router does not own', () => {
    const url = new URL(serializeExploreURLState(view({ workspace: 'files' }), '?feature=preview&workspace=everything'), 'http://msgvault.invalid');
    expect(`${url.pathname}${url.search}`).toBe('/files?feature=preview');
  });

  it('keeps Fact review person context in the payload rather than the path', () => {
    const url = address({ workspace: 'directory_review', reviewKind: 'fact', directoryPersonID: 7 });
    expect(url.pathname).toBe('/reviews');
    expect(parseExploreURLState(url.search, url.pathname)).toMatchObject({ reviewKind: 'fact', directoryPersonID: 7 });
  });

  it('titles each surface', () => {
    expect(routeTitle(view({ workspace: 'everything' }))).toBe('Inbox');
    expect(routeTitle(view({ workspace: 'everything', query: 'invoice' }))).toBe('Search: invoice');
    expect(routeTitle(view({ workspace: 'operations' }))).toBe('Operations · Activity');
    expect(routeTitle(view({ workspace: 'directory', directoryPersonID: 4 }))).toBe('People');
  });
});

describe('legacy address redirects', () => {
  it('rewrites a legacy link to its path in place and keeps its state', async () => {
    const { ExploreState } = await import('../explore/state.svelte');
    const payload = encodeURIComponent(JSON.stringify({ schemaVersion: 2, operationLane: 'contacts' }));
    window.history.replaceState(null, '', `/?workspace=operations&mode=full_text&explore=${payload}&feature=preview`);
    const length = window.history.length;
    const state = new ExploreState(window);
    expect(window.location.pathname).toBe('/activity/operations');
    expect(new URLSearchParams(window.location.search).get('feature')).toBe('preview');
    expect(new URLSearchParams(window.location.search).has('workspace')).toBe(false);
    expect(state.current).toMatchObject({ workspace: 'operations', operationLane: 'contacts' });
    expect(window.history.length).toBe(length);
    expect(state.arrivedAtDefault).toBe(false);
    state.destroy();
  });

  it('marks a bare root as the default landing and names its path', async () => {
    const { ExploreState } = await import('../explore/state.svelte');
    window.history.replaceState(null, '', '/');
    const state = new ExploreState(window);
    expect(state.arrivedAtDefault).toBe(true);
    expect(window.location.pathname).toBe('/people');
    state.destroy();
  });

  it('tells a Back button whether Back stays in the app', async () => {
    const { ExploreState } = await import('../explore/state.svelte');
    window.history.replaceState(null, '', '/messages/5');
    const state = new ExploreState(window);
    expect(state.canGoBack()).toBe(false);
    state.commitWorkspace('files');
    expect(state.canGoBack()).toBe(true);
    window.history.back();
    await new Promise((resolve) => window.addEventListener('popstate', resolve, { once: true }));
    expect(state.current).toMatchObject({ workspace: 'message', messageID: 5 });
    expect(state.canGoBack()).toBe(false);
    state.destroy();
  });
});
