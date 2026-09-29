import { render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { DirectoryController } from '../../directory/controller.svelte';
import { PeopleHub, type PeopleFilters } from '../../people/hub.svelte';
import PeopleWorkspace from './PeopleWorkspace.svelte';

function contact(id: number, label: string, lastAt: string) {
  return {
    canonical_id: id, display_label: label, last_at: lastAt, member_ids: [id], score: 1,
    primary_identifier: { kind: 'email', value: `${label.toLowerCase().replace(/\s+/g, '.')}@example.test` },
    signals: { last_interaction_at: lastAt, meeting_count: 0, meetings_together: 0, modalities: 1,
      received_from_them: 1, sent_count: 1, sent_to_them: 1 },
  };
}

describe('PeopleWorkspace', () => {
  it('keeps loading archive contacts when "Has name" hides a whole page, and never hides saved people', async () => {
    const relationshipCursors: Array<string | undefined> = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/people/directory') return Response.json({ people: [{
        id: 7, revision: 1, display_name: 'Saved Person', contact_state: 'active', categories: [], organizations: [],
        last_contact_at: '2026-07-01T00:00:00Z',
      }] });
      if (path === '/api/v1/relationships') {
        const body = await request.clone().json() as { cursor?: string };
        relationshipCursors.push(body.cursor);
        return Response.json(body.cursor
          ? { rows: [contact(2, 'Named Contact', '2026-07-05T00:00:00Z')], total_count: 2, cache_revision: 'c', identity_revision: 1 }
          : { rows: [contact(1, 'unnamed@example.test', '2026-07-20T00:00:00Z')], total_count: 2, cache_revision: 'c', identity_revision: 1, next_cursor: 'page-2' });
      }
      return Response.json({}, { status: 404 });
    });
    const client = createAPIClient(fetchFn);
    const directory = new DirectoryController(client);
    const hub = new PeopleHub(client, directory);
    const filters: PeopleFilters = { query: '', saved: '', hasName: true, category: '', organization: '' };
    directory.applyURLState({
      directoryQuery: '', directoryContactState: '', directoryCategory: '', directoryOrganization: '',
      directoryPrimaryChannel: '', directoryLastContactAfter: '', directoryLastContactBefore: '',
      directorySort: 'last_contact_desc', directoryHasName: true, directoryPersonID: null,
    });
    hub.apply(filters);
    render(PeopleWorkspace, { hub, filters, onFiltersChange: vi.fn(), onOpen: vi.fn() });

    const results = screen.getByRole('region', { name: 'People results' });
    await waitFor(() => expect(within(results).getAllByRole('link').map((link) => link.textContent?.includes('Named Contact')))
      .toEqual([true, false]));
    expect(within(results).getByRole('link', { name: /Saved Person/ })).toBeDefined();
    expect(relationshipCursors).toEqual([undefined, 'page-2']);
    expect(screen.queryByText('unnamed@example.test')).toBeNull();
    hub.destroy();
    directory.destroy();
  });
});
