import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import type { DirectoryReadBundle } from '../../directory/models';
import { DirectoryProfileController } from '../../directory/profile-controller.svelte';
import StructuredProfileSection from './StructuredProfileSection.svelte';

const when = '2026-08-01T00:00:00Z';

function profile() {
  return {
    person: { id: 7, revision: 3, display_name: 'Test User', participant_ids: [], vcard_uid: 'person-7', created_at: when, updated_at: when },
    names: [{
      person_id: 7, name_kind: 'formatted', original_value: 'Test User', formatted: 'Test User', is_derived: false,
      envelope: { id: 11, ordinal: 0, source: 'user', active_from: when, created_at: when, updated_at: when, vcard: {} }
    }],
    contact_points: [], addresses: [], dates: [], categories: [], media: []
  };
}

function renderSection(
  fetchFn: typeof fetch,
  profileETag: string | null = '"person-7-r3"',
  structuredProfile: NonNullable<DirectoryReadBundle['structuredProfile']> = profile()
) {
  const bundle = {
    person: structuredProfile.person,
    structuredProfile,
    etags: { person: '"person-7-r3"', structuredProfile: profileETag ?? undefined }, errors: {}
  } satisfies DirectoryReadBundle;
  const client = createAPIClient(fetchFn);
  const controller = new DirectoryProfileController(client, 7, bundle);
  render(StructuredProfileSection, { client, controller, personID: 7 });
  return controller;
}

describe('StructuredProfileSection', () => {
  it('shows current source and validity time returned by the generated profile read', () => {
    renderSection(vi.fn());

    expect(screen.getByText('Test User')).toBeDefined();
    expect(screen.getByText(/Source: user/)).toBeDefined();
    // Stamps render through kit's formatTimestamp in the runner's zone, so
    // 2026-08-01T00:00:00Z reads as Aug 1 or the evening of Jul 31.
    expect(screen.getByText(/Valid from: (Aug 1|Jul 31)/)).toBeDefined();
    expect(screen.getByText(/Updated: (Aug 1|Jul 31)/)).toBeDefined();
    expect(screen.queryByText(/2026-08-01T00:00:00Z/)).toBeNull();
  });

  it('renders labeled records as detail rows whose actions reveal on hover or focus', () => {
    renderSection(vi.fn());

    const edit = screen.getByRole('button', { name: 'Edit name Test User' });
    const row = edit.closest('[data-detail-row]');
    expect(row).not.toBeNull();
    // A labeled row keeps the three-column layout (the attribute is present
    // and empty), and its actions sit in the hover-revealed cluster.
    expect(row?.getAttribute('data-detail-row')).toBe('');
    expect(row?.querySelector('[data-detail-label]')?.textContent).toBe('Formatted');
    expect(edit.closest('[data-detail-actions="hover"]')).not.toBeNull();
  });

  it('requires explicit confirmation before closing a current fact', async () => {
    const requests: Request[] = [];
    renderSection(vi.fn<typeof fetch>(async (input) => {
      requests.push(input instanceof Request ? input : new Request(input));
      return new Response(JSON.stringify({ ...profile(), names: [] }), {
        headers: { 'Content-Type': 'application/json', ETag: '"person-7-r4"' }
      });
    }));

    await fireEvent.click(screen.getByRole('button', { name: 'Close name Test User' }));
    expect(requests).toHaveLength(0);
    expect(screen.getByRole('group', { name: 'Confirm closing name Test User' })).toBeDefined();

    await fireEvent.click(screen.getByRole('button', { name: 'Confirm close name' }));
    await waitFor(() => expect(requests).toHaveLength(1));
    await expect(requests[0]!.clone().json()).resolves.toEqual({ names: { supersede: [11] } });
  });

  it('replaces an open editor draft when the user selects a different current row', async () => {
    const controller = renderSection(vi.fn());
    controller.structuredProfile = {
      ...profile(),
      names: [
        ...profile().names,
        {
          person_id: 7, name_kind: 'formatted', original_value: 'Second User', formatted: 'Second User', is_derived: false,
          envelope: { id: 12, ordinal: 1, source: 'user', active_from: when, created_at: when, updated_at: when, vcard: {} }
        }
      ]
    };

    await fireEvent.click(screen.getByRole('button', { name: 'Edit name Test User' }));
    expect(screen.getByLabelText('Formatted name')).toHaveProperty('value', 'Test User');

    await fireEvent.click(screen.getByRole('button', { name: 'Edit name Second User' }));
    expect(screen.getByLabelText('Formatted name')).toHaveProperty('value', 'Second User');
  });

  it('blocks writes without a profile ETag and offers an explicit reload', () => {
    renderSection(vi.fn(), null);

    expect(screen.getByRole('button', { name: 'Add name' })).toHaveProperty('disabled', true);
    expect(screen.getByRole('button', { name: 'Reload profile' })).toBeDefined();
    expect(screen.getByRole('status').textContent).toContain('Profile revision unavailable');
  });

  it('cancels a local-only structured edit without creating a controller draft', async () => {
    const requests: Request[] = [];
    const controller = renderSection(vi.fn<typeof fetch>(async (input) => {
      requests.push(input instanceof Request ? input : new Request(input));
      return Response.json(profile());
    }));

    await fireEvent.click(screen.getByRole('button', { name: 'Edit name Test User' }));
    await fireEvent.input(screen.getByLabelText('Formatted name'), { target: { value: 'Unsaved local name' } });
    expect(controller.draft).toBeNull();
    expect(controller.conflict).toBeNull();

    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

    expect(requests).toHaveLength(0);
    expect(screen.queryByLabelText('Formatted name')).toBeNull();
    expect(controller.draft).toBeNull();
    expect(controller.conflict).toBeNull();
  });

  it('treats Cancel on a conflicted structured editor as an explicit discard', async () => {
    const requests: Request[] = [];
    const controller = renderSection(vi.fn<typeof fetch>(async (input) => {
      requests.push(input instanceof Request ? input : new Request(input));
      return Response.json({ error: 'person_revision_conflict', message: 'changed elsewhere' }, { status: 409 });
    }));

    await fireEvent.click(screen.getByRole('button', { name: 'Edit name Test User' }));
    await fireEvent.input(screen.getByLabelText('Formatted name'), { target: { value: 'Local retained name' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    expect(await screen.findByRole('alert')).toBeDefined();
    expect(controller.draft).not.toBeNull();

    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

    expect(requests).toHaveLength(1);
    expect(controller.draft).toBeNull();
    expect(controller.conflict).toBeNull();
    expect(screen.getByRole('button', { name: 'Add name' })).toHaveProperty('disabled', false);
  });

  it('keeps a conflicted editor visible when Cancel cannot discard during reload', async () => {
    let resolveProfileReload!: (response: Response) => void;
    const profileReload = new Promise<Response>((resolve) => { resolveProfileReload = resolve; });
    const controller = renderSection(vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      const path = new URL(request.url).pathname;
      if (request.method === 'PATCH') {
        return Response.json({ error: 'person_revision_conflict', message: 'changed elsewhere' }, { status: 409 });
      }
      if (path.endsWith('/profile')) return profileReload;
      if (path.endsWith('/attributes')) return Response.json({ person_id: 7, attributes: [] });
      if (path.endsWith('/attribute-definitions')) return Response.json({ definitions: [] });
      return Response.json(profile().person, { headers: { ETag: '"person-7-r4"' } });
    }));

    await fireEvent.click(screen.getByRole('button', { name: 'Edit name Test User' }));
    await fireEvent.input(screen.getByLabelText('Formatted name'), { target: { value: 'Local retained name' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    expect(await screen.findByRole('alert')).toBeDefined();

    await fireEvent.click(screen.getByRole('button', { name: 'Reload profile' }));
    expect(controller.reloadPending).toBe(true);
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

    expect(screen.getByLabelText('Formatted name')).toHaveProperty('value', 'Local retained name');
    expect(controller.draft).not.toBeNull();
    expect(controller.conflict?.code).toBe('person_revision_conflict');

    resolveProfileReload(Response.json(
      { error: 'person_revision_conflict', message: 'still changed elsewhere' },
      { status: 409 }
    ));
    await waitFor(() => expect(controller.reloadPending).toBe(false));

    expect(screen.getByLabelText('Formatted name')).toHaveProperty('value', 'Local retained name');
    expect(controller.draft).not.toBeNull();
    expect(controller.conflict?.code).toBe('person_revision_conflict');

    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(controller.draft).toBeNull();
    expect(controller.conflict).toBeNull();
    expect(screen.queryByLabelText('Formatted name')).toBeNull();
    expect(screen.getByRole('button', { name: 'Add name' })).toHaveProperty('disabled', false);
  });

  it('labels duplicate provider handles with full service scope in their actions', async () => {
    const controller = renderSection(vi.fn());
    controller.structuredProfile = {
      ...profile(),
      contact_points: ['workspace-a', 'workspace-b'].map((scope, index) => ({
        person_id: 7, address_kind: 'username', original_value: 'alice', normalized_value: 'alice', normalization: 'casefold', normalization_version: 1,
        service_slug: 'slack', scope_kind: 'workspace', scope_value: scope,
        envelope: { id: 50 + index, ordinal: index, source: 'user', created_at: when, updated_at: when, vcard: {} }
      }))
    };

    expect(await screen.findByText('Service: slack · Scope: workspace / workspace-a')).toBeDefined();
    expect(screen.getByText('Service: slack · Scope: workspace / workspace-b')).toBeDefined();
    expect(screen.getByRole('button', { name: 'Edit contact point alice — slack — workspace / workspace-a' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'Edit contact point alice — slack — workspace / workspace-b' })).toBeDefined();
  });

  it('links contact points through the service template and addresses to Google Maps', async () => {
    const envelope = (id: number) => ({ id, ordinal: 0, source: 'user', created_at: when, updated_at: when, vcard: {} });
    renderSection(vi.fn<typeof fetch>(async () => Response.json({ observations: [] })), '"person-7-r3"', {
      ...profile(),
      contact_points: [
        { person_id: 7, address_kind: 'email', original_value: 'person@example.test', normalized_value: 'person@example.test',
          normalization: 'email', normalization_version: 1, envelope: envelope(60) },
        { person_id: 7, address_kind: 'username', original_value: '@example_person', normalized_value: 'example_person',
          normalization: 'strip_at_lower', normalization_version: 1, service_slug: 'telegram',
          profile_url_template: 'https://t.me/{username}', envelope: envelope(61) },
        { person_id: 7, address_kind: 'url', original_value: 'javascript:alert(1)', normalized_value: 'javascript:alert(1)',
          normalization: 'none', normalization_version: 1, envelope: envelope(62) }
      ],
      addresses: [{ person_id: 7, address_kind: 'home', original_value: '1 Example Road, Exampletown',
        geo_uri: 'geo:40.5,-73.25', envelope: envelope(63) }]
    });

    expect(screen.getByRole('link', { name: 'person@example.test' }).getAttribute('href')).toBe('mailto:person@example.test');
    const profileLink = screen.getByRole('link', { name: '@example_person (opens in new tab)' });
    expect(profileLink.getAttribute('href')).toBe('https://t.me/example_person');
    expect(profileLink.getAttribute('rel')).toBe('noopener noreferrer');
    expect(screen.getByText('javascript:alert(1)').closest('a')).toBeNull();
    expect(screen.getByRole('link', { name: '1 Example Road, Exampletown (opens in new tab)' }).getAttribute('href'))
      .toBe('https://www.google.com/maps/search/?api=1&query=40.5%2C-73.25');
  });

  it('groups contact points by service and shows the latest matching archive observation', async () => {
    const structuredProfile = {
      ...profile(),
      contact_points: [{
        person_id: 7, address_kind: 'username', original_value: 'alice', normalized_value: 'alice', normalization: 'casefold', normalization_version: 1,
        service_slug: 'slack', scope_kind: 'workspace', scope_value: 'workspace-a',
        envelope: { id: 50, ordinal: 0, source: 'archive_observation', created_at: when, updated_at: when, vcard: {} }
      }]
    };
    renderSection(vi.fn<typeof fetch>(async () => Response.json({
      person: profile().person,
      names: [], addresses: [], dates: [], categories: [], media: [], contact_points: [],
      observations: [{
        participant_id: 17, address_kind: 'username', original_value: 'alice', normalized_value: 'alice',
        normalization: 'casefold', normalization_version: 1, service_slug: 'slack',
        scope_kind: 'workspace', scope_value: 'workspace-a', observed_at: '2026-08-03T12:00:00Z', source_id: 4,
        envelope: { id: 70, ordinal: 0, source: 'archive_observation', created_at: when, updated_at: when, vcard: {} }
      }]
    })), '"person-7-r3"', structuredProfile);

    expect(await screen.findByRole('heading', { name: 'Slack' })).toBeDefined();
    expect(await screen.findByText(/^Observed Aug 3, .* · Source 4$/)).toBeDefined();
  });

  it('loads observations when a contact point is added after the section mounts', async () => {
    const contactPoint = {
      person_id: 7, address_kind: 'username', original_value: 'alice', normalized_value: 'alice',
      normalization: 'casefold', normalization_version: 1, service_slug: 'slack',
      scope_kind: 'workspace', scope_value: 'workspace-a', uri: 'slack:alice',
      envelope: { id: 41, ordinal: 0, source: 'user', created_at: when, updated_at: when, vcard: {} }
    };
    const fetchFn = vi.fn<typeof fetch>(async () => Response.json({
      person: profile().person,
      names: [], addresses: [], dates: [], categories: [], media: [], contact_points: [],
      observations: [{
        participant_id: 17, address_kind: 'username', original_value: 'alice', normalized_value: 'alice',
        normalization: 'casefold', normalization_version: 1, service_slug: 'slack',
        scope_kind: 'workspace', scope_value: 'workspace-a', observed_at: '2026-08-03T12:00:00Z', source_id: 4,
        envelope: { id: 70, ordinal: 0, source: 'archive_observation', created_at: when, updated_at: when, vcard: {} }
      }]
    }));
    const controller = renderSection(fetchFn);

    controller.structuredProfile = { ...profile(), contact_points: [contactPoint] };

    expect(await screen.findByText(/^Observed Aug 3, .* · Source 4$/)).toBeDefined();
    expect(fetchFn).toHaveBeenCalledOnce();
  });

  it('renames the selected person with the current strong ETag', async () => {
    const requests: Request[] = [];
    renderSection(vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      requests.push(request);
      return new Response(JSON.stringify({ ...profile().person, revision: 4, display_name: 'Renamed User' }), {
        headers: { 'Content-Type': 'application/json', ETag: '"person-7-r4"' }
      });
    }));

    await fireEvent.click(screen.getByRole('button', { name: 'Rename person' }));
    await fireEvent.input(screen.getByRole('textbox', { name: 'Display name' }), { target: { value: 'Renamed User' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save display name' }));

    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]!.method).toBe('PATCH');
    expect(requests[0]!.headers.get('If-Match')).toBe('"person-7-r3"');
    await expect(requests[0]!.clone().json()).resolves.toEqual({ display_name: 'Renamed User' });
  });

  it('locks rename controls while the non-abortable write is pending', async () => {
    let resolveRename!: (response: Response) => void;
    const pendingRename = new Promise<Response>((resolve) => { resolveRename = resolve; });
    const controller = renderSection(vi.fn<typeof fetch>(async () => pendingRename));

    await fireEvent.click(screen.getByRole('button', { name: 'Rename person' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Save display name' }));

    await waitFor(() => expect(controller.mutationPending).toBe(true));
    expect(screen.getByRole('button', { name: 'Cancel rename' })).toHaveProperty('disabled', true);
    expect(screen.getByRole('button', { name: 'Renaming…' })).toHaveProperty('disabled', true);

    resolveRename(new Response(JSON.stringify({ ...profile().person, revision: 4 }), {
      headers: { 'Content-Type': 'application/json', ETag: '"person-7-r4"' }
    }));
    await waitFor(() => expect(controller.mutationPending).toBe(false));
  });

  it('requires confirmation and the current strong ETag before deleting a person', async () => {
    const requests: Request[] = [];
    renderSection(vi.fn<typeof fetch>(async (input) => {
      requests.push(input instanceof Request ? input : new Request(input));
      return new Response(null, { status: 204 });
    }));

    await fireEvent.click(screen.getByRole('button', { name: 'Delete person' }));
    expect(requests).toHaveLength(0);
    expect(screen.getByRole('group', { name: 'Confirm deleting person' })).toBeDefined();
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete person' }));

    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]!.method).toBe('DELETE');
    expect(requests[0]!.headers.get('If-Match')).toBe('"person-7-r3"');
  });

  it('locks delete confirmation while the non-abortable delete is pending', async () => {
    let resolveDelete!: (response: Response) => void;
    const pendingDelete = new Promise<Response>((resolve) => { resolveDelete = resolve; });
    const controller = renderSection(vi.fn<typeof fetch>(async () => pendingDelete));

    await fireEvent.click(screen.getByRole('button', { name: 'Delete person' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete person' }));

    await waitFor(() => expect(controller.mutationPending).toBe(true));
    expect(screen.getByRole('button', { name: 'Cancel delete' })).toHaveProperty('disabled', true);
    expect(screen.getByRole('button', { name: 'Deleting…' })).toHaveProperty('disabled', true);

    resolveDelete(new Response(null, { status: 204 }));
    await waitFor(() => expect(controller.mutationPending).toBe(false));
  });

  it('does not offer an unsafe URI replacement for inline media content', async () => {
    const controller = renderSection(vi.fn());
    controller.structuredProfile = {
      ...profile(),
      media: [{
        person_id: 7, media_kind: 'photo', original_value: 'Inline portrait', has_data: true, media_type: 'image/png', byte_size: 128, content_hash: 'sha256:synthetic',
        envelope: { id: 60, ordinal: 0, source: 'user', created_at: when, updated_at: when, vcard: {} }
      }]
    };

    expect(await screen.findByText('Inline content is read-only here because metadata editing cannot preserve its stored bytes.')).toBeDefined();
    expect(screen.queryByRole('button', { name: /Edit media metadata Inline portrait/ })).toBeNull();
  });

  it('does not offer metadata editing when inline media also has a URI', async () => {
    const controller = renderSection(vi.fn());
    controller.structuredProfile = {
      ...profile(),
      media: [{
        person_id: 7, media_kind: 'photo', original_value: 'Stored portrait', has_data: true,
        uri: 'https://example.test/portrait.png', media_type: 'image/png', byte_size: 128, content_hash: 'sha256:synthetic',
        envelope: { id: 61, ordinal: 0, source: 'user', created_at: when, updated_at: when, vcard: {} }
      }]
    };

    expect(await screen.findByText('Inline content is read-only here because metadata editing cannot preserve its stored bytes.')).toBeDefined();
    expect(screen.queryByRole('button', { name: /Edit media metadata Stored portrait/ })).toBeNull();
  });

  it('closes mixed inline and URI media without creating a lossy replacement', async () => {
    const requests: Request[] = [];
    const controller = renderSection(vi.fn<typeof fetch>(async (input) => {
      requests.push(input instanceof Request ? input : new Request(input));
      return new Response(JSON.stringify({ ...profile(), media: [] }), {
        headers: { 'Content-Type': 'application/json', ETag: '"person-7-r4"' }
      });
    }));
    controller.structuredProfile = {
      ...profile(),
      media: [{
        person_id: 7, media_kind: 'photo', original_value: 'Stored portrait', has_data: true,
        uri: 'https://example.test/portrait.png', media_type: 'image/png', byte_size: 128, content_hash: 'sha256:synthetic',
        envelope: { id: 61, ordinal: 0, source: 'user', created_at: when, updated_at: when, vcard: {} }
      }]
    };

    await fireEvent.click(await screen.findByRole('button', { name: 'Close media metadata Stored portrait' }));
    expect(requests).toHaveLength(0);
    expect(screen.getByRole('group', { name: 'Confirm closing media metadata Stored portrait' })).toBeDefined();

    await fireEvent.click(screen.getByRole('button', { name: 'Confirm close media metadata' }));
    await waitFor(() => expect(requests).toHaveLength(1));
    await expect(requests[0]!.clone().json()).resolves.toEqual({ media: { supersede: [61] } });
  });

  it('opens history without replacing the Directory overview and returns focus to its trigger', async () => {
    renderSection(vi.fn<typeof fetch>(async () => Response.json({
      person: profile().person,
      names: [], contact_points: [], addresses: [], dates: [], categories: [], media: [], observations: []
    })));
    const trigger = screen.getByRole('button', { name: 'View profile history' });
    trigger.focus();

    await fireEvent.click(trigger);
    expect(await screen.findByRole('dialog', { name: 'Profile history' })).toBeDefined();
    await fireEvent.click(screen.getByRole('button', { name: 'Close profile history' }));

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Profile history' })).toBeNull());
    expect(document.activeElement).toBe(trigger);
    expect(screen.getByRole('heading', { name: 'Names' })).toBeDefined();
  });
});
