import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import type { SetCorrespondentKindResult } from '../../api/generated/models';
import { entityNames } from '../../names/entity-names.svelte';
import { entityLabelsResponse } from '../../../test/entity-labels';
import NotAPersonDialog from './NotAPersonDialog.svelte';

function result(kind: string, person?: SetCorrespondentKindResult['record']['person']): SetCorrespondentKindResult {
  return {
    record: {
      canonical_id: 41, member_ids: [41], kind: kind as SetCorrespondentKindResult['record']['kind'], source: 'user',
      addresses: ['orders@shop.example.test'], display_name: 'Example Shop', classified_at: '2026-09-01T00:00:00Z',
      ...(kind === 'organization' ? { organization_id: 5, organization_name: 'Example Shop' } : {}),
      ...(person ? { person } : {})
    },
    organization_created: kind === 'organization', organization_removed: false, resolved_candidates: 1,
    restored_candidates: 0
  };
}

function setup(responses: { kind?: string; person?: SetCorrespondentKindResult['record']['person'] } = {}) {
  const requests: Array<{ method: string; path: string; body?: unknown; ifMatch?: string | null }> = [];
  const fetchFn = vi.fn<typeof fetch>(async (input) => {
    const request = input instanceof Request ? input : new Request(input);
    const path = new URL(request.url, document.baseURI).pathname;
    const body = request.method === 'PUT' ? await request.clone().json() : undefined;
    requests.push({ method: request.method, path, body, ifMatch: request.headers.get('If-Match') });
    if (request.method === 'PUT' && path === '/api/v1/identity/correspondent-kinds/41') {
      return Response.json(result((body as { kind: string }).kind, responses.person));
    }
    if (request.method === 'GET' && path === '/api/v1/people/9') {
      return Response.json({ id: 9, revision: 3, participant_ids: [41] }, { headers: { ETag: '"person-9-r3"' } });
    }
    if (request.method === 'DELETE' && path === '/api/v1/people/9') return new Response(null, { status: 204 });
    const labels = entityLabelsResponse(request, { participant: { 41: 'Example Shop' }, person: { 9: 'Example Shop' } });
    if (labels) return labels;
    throw new Error(`unexpected ${request.method} ${path}`);
  });
  const onDone = vi.fn();
  const onClose = vi.fn();
  const client = createAPIClient(fetchFn);
  render(NotAPersonDialog, {
    client, participantIDs: [41], label: 'Example Shop',
    suggestedOrganization: 'Example Shop', onDone, onClose
  });
  return { requests, onDone, onClose, client };
}

describe('NotAPersonDialog', () => {
  it('explains each choice and marks an organization under the prefilled name', async () => {
    const { requests, onDone } = setup();
    expect(screen.getByText(/A business or institution/)).toBeDefined();
    expect(screen.getByText(/several people write from/)).toBeDefined();
    expect(screen.getByText(/do not need as a contact/)).toBeDefined();
    const confirm = screen.getByRole('button', { name: 'Mark as not a person' }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);

    await fireEvent.click(screen.getByRole('radio', { name: /Organization/ }));
    const name = screen.getByRole('textbox', { name: 'Organization name' }) as HTMLInputElement;
    expect(name.value).toBe('Example Shop');
    await fireEvent.click(screen.getByRole('button', { name: 'Mark as organization' }));

    await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    expect(requests).toEqual([{
      method: 'PUT', path: '/api/v1/identity/correspondent-kinds/41',
      body: { kind: 'organization', organization_name: 'Example Shop' }, ifMatch: null
    }]);
    expect(onDone.mock.calls[0]![1]).toBeUndefined();
  });

  it('keeps a profile made only of this record unless deleting it is confirmed separately', async () => {
    const { requests, onDone } = setup({ person: { id: 9, revision: 3, only_this_cluster: true, display_name: 'Example Shop' } });
    await fireEvent.click(screen.getByRole('radio', { name: /Ignored/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Mark as ignored' }));

    await screen.findByText(/is a saved profile made only of this record/);
    expect(onDone).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole('button', { name: 'Delete profile…' }));
    expect(screen.getByText(/removed permanently/)).toBeDefined();
    expect(requests.filter((request) => request.method === 'DELETE')).toEqual([]);

    await fireEvent.click(screen.getByRole('button', { name: 'Delete profile' }));
    await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    expect(onDone.mock.calls[0]![1]).toBe(9);
    expect(requests.find((request) => request.method === 'DELETE')).toEqual({
      method: 'DELETE', path: '/api/v1/people/9', ifMatch: '"person-9-r3"'
    });
  });

  it('reports the record as marked when the profile is kept', async () => {
    const { requests, onDone } = setup({ person: { id: 9, revision: 3, only_this_cluster: true } });
    await fireEvent.click(screen.getByRole('radio', { name: /Organization/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Mark as organization' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Keep profile' }));
    expect(onDone).toHaveBeenCalledOnce();
    expect(requests.some((request) => request.method === 'DELETE')).toBe(false);
  });

  it('asks for a participant\'s name again after marking it, since it no longer takes its person\'s name', async () => {
    const { requests, onDone, client } = setup();
    const names = entityNames(client);
    await names.load('participant', [41]);
    const lookups = () => requests.filter((request) => request.path === '/api/v1/entity-labels').length;
    expect(lookups()).toBe(1);

    await fireEvent.click(screen.getByRole('radio', { name: /Shared mailbox/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Mark as shared mailbox' }));
    await waitFor(() => expect(onDone).toHaveBeenCalledOnce());

    names.label('participant', 41);
    await waitFor(() => expect(lookups()).toBe(2));
  });
});
