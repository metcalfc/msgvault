import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { withEntityLabels } from '../../../test/entity-labels';
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

  it('lists records marked as not a person by kind and restores one', async () => {
    let records = [
      { canonical_id: 11, member_ids: [11], kind: 'organization', source: 'user', addresses: ['orders@shop.example.test'],
        display_name: 'Example Shop', organization_id: 5, organization_name: 'Example Shop', classified_at: '2026-09-02T00:00:00Z' },
      { canonical_id: 12, member_ids: [12], kind: 'shared_mailbox', source: 'user', addresses: ['desk@example.test'],
        display_name: 'Example Desk', classified_at: '2026-09-01T00:00:00Z',
        person: { id: 7, revision: 2, only_this_cluster: false } },
      { canonical_id: 13, member_ids: [13], kind: 'ignored', source: 'user', addresses: [],
        classified_at: '2026-08-31T00:00:00Z' },
    ];
    const cleared: string[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/people/directory') return Response.json({ people: [] });
      if (path === '/api/v1/identity/correspondent-kinds' && request.method === 'GET') return Response.json({ records });
      if (path === '/api/v1/identity/correspondent-kinds/11' && request.method === 'DELETE') {
        cleared.push(path);
        records = records.filter((record) => record.canonical_id !== 11);
        return Response.json({ record: { canonical_id: 11, member_ids: [11], kind: 'person', addresses: [] },
          organization_created: false, resolved_candidates: 0, restored_candidates: 0 });
      }
      return Response.json({}, { status: 404 });
    });
    const client = createAPIClient(withEntityLabels(fetchFn, { participant: { 13: 'unnamed@example.test' } }));
    const directory = new DirectoryController(client);
    const hub = new PeopleHub(client, directory);
    const filters: PeopleFilters = { query: '', saved: 'not_people', hasName: false, category: '', organization: '' };
    hub.apply(filters);
    const onFiltersChange = vi.fn();
    render(PeopleWorkspace, { hub, filters, onFiltersChange, onOpen: vi.fn() });

    expect(screen.getByRole('button', { name: 'Not people' }).getAttribute('aria-pressed')).toBe('true');
    const list = screen.getByRole('region', { name: 'Records that are not people' });
    const organizations = await within(list).findByRole('list', { name: 'Organization' });
    expect(within(organizations).getByRole('link', { name: /Example Shop/ }).getAttribute('href')).toBe('/people/contact-11');
    expect(within(organizations).getByText('Organization · Example Shop')).toBeDefined();
    const shared = within(list).getByRole('list', { name: 'Shared mailbox' });
    // A record with a saved profile opens the person page.
    expect(within(shared).getByRole('link', { name: /Example Desk/ }).getAttribute('href')).toBe('/people/7');
    // A record with neither a name nor an address is named by the label lookup, never its ID.
    const ignored = within(list).getByRole('list', { name: 'Ignored' });
    expect(await within(ignored).findByRole('link', { name: /unnamed@example\.test/ })).toBeDefined();
    expect(ignored.textContent).not.toContain('13');

    await fireEvent.click(within(organizations).getByRole('button', { name: 'Example Shop is a person' }));
    await waitFor(() => expect(within(list).queryByRole('list', { name: 'Organization' })).toBeNull());
    expect(cleared).toEqual(['/api/v1/identity/correspondent-kinds/11']);
    expect(await screen.findByText('Example Shop is a person again.')).toBeDefined();

    await fireEvent.click(screen.getByRole('button', { name: 'Not people' }));
    expect(onFiltersChange).toHaveBeenCalledWith({ saved: '' }, 'push');
    hub.destroy();
    directory.destroy();
  });
});
