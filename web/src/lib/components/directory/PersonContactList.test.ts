import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import type { PersonContactPoint, PersonIdentifier } from '../../api/generated/models';
import { DirectoryProfileController } from '../../directory/profile-controller.svelte';
import type { DirectoryReadBundle } from '../../directory/models';
import { mergeReachEntries, reachEntriesFromContactPoints, reachEntriesFromIdentifiers } from '../../people/reach';
import PersonContactList from './PersonContactList.svelte';

const when = '2026-01-01T00:00:00Z';

function point(id: number, kind: 'email' | 'phone', value: string, extra: Partial<PersonContactPoint['envelope']> = {}): PersonContactPoint {
  return {
    person_id: 7, address_kind: kind, original_value: value,
    normalized_value: kind === 'phone' ? value.replace(/\D+/g, '') : value.toLowerCase(),
    normalization: kind, normalization_version: 1,
    envelope: { id, ordinal: 0, source: 'carddav_import', created_at: when, updated_at: when, vcard: {}, ...extra }
  };
}

function identifier(participantID: number, type: string, value: string): PersonIdentifier {
  return { participant_id: participantID, type, value, is_primary: true, provenance: 'participant_identifiers' };
}

const person = (revision: number) => ({
  id: 7, revision, display_name: 'Ana Example', participant_ids: [5, 6], vcard_uid: 'urn:uuid:ana', created_at: when, updated_at: when
});

interface Recorded { method: string; path: string; ifMatch: string | null; body: unknown }

function setup(options: {
  points?: PersonContactPoint[];
  identifiers?: PersonIdentifier[];
  history?: PersonContactPoint[];
  withController?: boolean;
} = {}) {
  const points = options.points ?? [];
  const requests: Recorded[] = [];
  let revision = 2;
  const fetcher = vi.fn<typeof fetch>(async (input) => {
    const request = input instanceof Request ? input : new Request(input);
    const path = new URL(request.url).pathname;
    const text = request.method === 'GET' ? '' : await request.text();
    requests.push({ method: request.method, path, ifMatch: request.headers.get('If-Match'), body: text ? JSON.parse(text) : undefined });
    if (path === '/api/v1/people/7/profile/history') {
      return Response.json({ person: person(revision), names: [], contact_points: options.history ?? [], addresses: [], dates: [], categories: [], media: [], observations: [] });
    }
    if (path === '/api/v1/people/7/profile' && request.method === 'PATCH') {
      revision += 1;
      return Response.json({ person: person(revision), names: [], contact_points: [], addresses: [], dates: [], categories: [], media: [] },
        { headers: { ETag: `"person-7-r${revision}"` } });
    }
    if (path === '/api/v1/people/7/participants/detach' || path === '/api/v1/people/7/participants/reattach') {
      revision += 1;
      return Response.json({
        detachment: { id: 11, person_id: 7, participant_ids: [5], actor: 'user', created_at: when },
        person: person(revision), identity_revision: 40 + revision, cache_state: 'ready'
      }, { headers: { ETag: `"person-7-r${revision}"` } });
    }
    return Response.json({});
  });
  const client = createAPIClient(fetcher);
  const bundle = {
    person: person(2),
    structuredProfile: { person: person(2), names: [], contact_points: points, addresses: [], dates: [], categories: [], media: [] },
    etags: { person: '"person-7-r2"', structuredProfile: '"person-7-r2"' },
    errors: {}
  } satisfies DirectoryReadBundle;
  const controller = options.withController === false ? null : new DirectoryProfileController(client, 7, bundle);
  const entries = mergeReachEntries(
    reachEntriesFromContactPoints(points),
    reachEntriesFromIdentifiers({ identifiers: options.identifiers ?? [] })
  );
  const onAnnounce = vi.fn();
  render(PersonContactList, {
    client, personID: 7, displayName: 'Ana Example', entries, contactPoints: points, revision: 2,
    profileController: controller, onAnnounce
  });
  const writes = () => requests.filter((request) => request.method !== 'GET');
  return { requests, writes, onAnnounce };
}

describe('PersonContactList', () => {
  it('offers a hover-revealed retire on address-book rows and detach on archive-only rows', () => {
    setup({
      points: [point(2, 'email', 'ana@example.com')],
      identifiers: [identifier(5, 'email', 'alerts@example.com'), identifier(6, 'email', 'ana@example.com')]
    });
    const retire = screen.getByRole('button', { name: 'Retire ana@example.com — stops syncing, keeps history' });
    const detach = screen.getByRole('button', { name: 'Not Ana Example — detach alerts@example.com' });
    for (const button of [retire, detach]) {
      expect(button.getAttribute('title')).toBe(button.getAttribute('aria-label'));
      const reveal = button.closest('[data-detail-actions="hover"]');
      expect(reveal).not.toBeNull();
      expect(reveal?.closest('[data-fact-row]')).not.toBeNull();
    }
    // A row with both a contact point and an archive identity is retired, never detached.
    expect(screen.queryByRole('button', { name: 'Not Ana Example — detach ana@example.com' })).toBeNull();
  });

  it('offers no detach for a linked identity the person does not hold', () => {
    setup({ identifiers: [identifier(5, 'email', 'alerts@example.com'), identifier(8, 'email', 'other@example.com')] });
    expect(screen.getByRole('button', { name: 'Not Ana Example — detach alerts@example.com' })).toBeDefined();
    expect(screen.getByText('other@example.com')).toBeDefined();
    expect(screen.queryByRole('button', { name: 'Not Ana Example — detach other@example.com' })).toBeNull();
  });

  it('retires a contact point in one click and undo adds it back with its kind, label, and type', async () => {
    const retired = point(2, 'phone', '+1 555 010 0100', { type_label: 'cell', type_tokens: ['cell', 'voice'], pref: 1 });
    const { writes, onAnnounce } = setup({ points: [retired] });

    await fireEvent.click(screen.getByRole('button', { name: 'Retire +1 555 010 0100 — stops syncing, keeps history' }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0]).toEqual({
      method: 'PATCH', path: '/api/v1/people/7/profile', ifMatch: '"person-7-r2"',
      body: { contact_points: { supersede: [2] } }
    });
    expect(await screen.findByRole('status')).toHaveProperty('textContent',
      'Retired +1 555 010 0100. It no longer syncs and stays under Former.');
    expect(onAnnounce).toHaveBeenCalledWith('Retired +1 555 010 0100. It no longer syncs and stays under Former.');

    await fireEvent.click(screen.getByRole('button', { name: 'Undo: restore +1 555 010 0100' }));
    await waitFor(() => expect(writes()).toHaveLength(2));
    expect(writes()[1]).toEqual({
      method: 'PATCH', path: '/api/v1/people/7/profile', ifMatch: '"person-7-r3"',
      body: { contact_points: { add: [{
        address_kind: 'phone', original_value: '+1 555 010 0100',
        envelope: { source: 'carddav_import', pref: 1, type_label: 'cell', type_tokens: ['cell', 'voice'] }
      }] } }
    });
    expect(await screen.findByText('Restored +1 555 010 0100.')).toBeDefined();
    expect(screen.queryByRole('button', { name: /^Undo/ })).toBeNull();
  });

  it('detaches an archive-only identity in one click and undo reattaches it', async () => {
    const { writes } = setup({ identifiers: [identifier(5, 'email', 'alerts@example.com')] });

    await fireEvent.click(screen.getByRole('button', { name: 'Not Ana Example — detach alerts@example.com' }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0]).toEqual({
      method: 'POST', path: '/api/v1/people/7/participants/detach', ifMatch: '"person-7-r2"',
      body: { participant_ids: [5] }
    });
    expect(await screen.findByText('Detached alerts@example.com from Ana Example.')).toBeDefined();

    await fireEvent.click(screen.getByRole('button', { name: 'Undo: restore alerts@example.com' }));
    await waitFor(() => expect(writes()).toHaveLength(2));
    expect(writes()[1]).toEqual({
      method: 'POST', path: '/api/v1/people/7/participants/reattach', ifMatch: '"person-7-r3"',
      body: { detachment_id: 11 }
    });
    expect(await screen.findByText('Restored alerts@example.com.')).toBeDefined();
  });

  it('lists retired values under a collapsed Former disclosure, with matching archive identities', async () => {
    setup({
      points: [point(2, 'email', 'ana@example.com')],
      identifiers: [identifier(5, 'phone', '+1 555 010 0199'), identifier(6, 'email', 'ana@example.com')],
      history: [
        point(2, 'email', 'ana@example.com'),
        point(9, 'phone', '+1 555 010 0199', { superseded_at: '2026-02-03T12:00:00Z' }),
        point(10, 'email', 'ana@example.com', { superseded_at: '2026-01-15T12:00:00Z' })
      ]
    });

    const summary = await screen.findByText('Former (1)');
    const disclosure = summary.closest('details')!;
    expect(disclosure.open).toBe(false);
    const former = within(disclosure).getByRole('list', { name: 'Former contact methods' });
    const rows = within(former).getAllByRole('listitem');
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain('+1 555 010 0199');
    expect(rows[0]?.textContent).toContain('retired');
    expect(within(former).queryByRole('button', { name: /Retire|detach/ })).toBeNull();

    // The archive phone matching the retired number is no longer current;
    // the re-added email stays current.
    const current = screen.getByRole('list', { name: 'Contact methods' });
    expect(within(current).queryByText('+1 555 010 0199')).toBeNull();
    expect(within(current).getByText('ana@example.com')).toBeDefined();
  });

  it('shows no remove control where the page cannot write the profile', () => {
    setup({
      points: [point(2, 'email', 'ana@example.com')],
      identifiers: [identifier(5, 'email', 'alerts@example.com')],
      withController: false
    });
    expect(screen.getAllByRole('listitem')).toHaveLength(2);
    expect(screen.queryByRole('button', { name: /Retire|detach/ })).toBeNull();
  });
});
