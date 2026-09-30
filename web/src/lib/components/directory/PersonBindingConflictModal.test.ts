import { appShortcuts } from '@kenn-io/kit-ui';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { withEntityLabels } from '../../../test/entity-labels';
import type { Person as GeneratedPerson } from '../../api/generated/models';
import type { PersonMergeSuccess, ValidatedPersonMergeRequired } from '../../directory/person-merge';
import PersonBindingConflictModal from './PersonBindingConflictModal.svelte';
import { focusAndClick } from '../../../test/kit-ui';

type Person = GeneratedPerson;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function person(id: number, revision: number, displayName: string): Person {
  return {
    id,
    revision,
    display_name: displayName,
    participant_ids: [id * 10],
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-02T00:00:00Z',
    vcard_uid: `synthetic-${id}`,
  };
}

function conflict(): ValidatedPersonMergeRequired {
  return {
    error: 'person_merge_required',
    message: 'Choose a survivor',
    profiles: [
      { person: person(7, 4, 'Synthetic One'), etag: '"person-7-r4"' },
      { person: person(9, 2, 'Synthetic Two'), etag: '"person-9-r2"' },
    ],
  };
}

function mergeResult(survivor: Person, cacheState: 'ready' | 'stale' = 'ready') {
  return {
    cache_state: cacheState,
    identity_revision: 8,
    merge: {
      id: 41,
      survivor_person_id: survivor.id,
      absorbed_person_id: survivor.id === 7 ? 9 : 7,
      current_person_id: survivor.id,
      survivor_vcard_uid: survivor.vcard_uid,
      absorbed_vcard_uid: survivor.id === 7 ? 'synthetic-9' : 'synthetic-7',
      survivor_revision_before: survivor.revision - 1,
      absorbed_revision_before: 2,
      survivor_revision_after: survivor.revision,
      actor: 'web',
      snapshot_version: 1,
      snapshot_sha256: 'synthetic-digest',
      created_at: '2026-08-03T00:00:00Z',
    },
    person: survivor,
    review_candidates: [],
  } as const;
}

function requestOf(input: RequestInfo | URL): Request {
  return input instanceof Request ? input : new Request(input);
}

function renderModal(
  fetchFn: typeof fetch,
  overrides: Partial<{
    onOpenProfile: (personID: number) => void;
    onSuccess: (success: PersonMergeSuccess) => void | Promise<void>;
    onClose: () => void;
  }> = {},
) {
  const onOpenProfile = overrides.onOpenProfile ?? vi.fn();
  const onSuccess = overrides.onSuccess ?? vi.fn();
  const onClose = overrides.onClose ?? vi.fn();
  const rendered = render(PersonBindingConflictModal, {
    client: createAPIClient(fetchFn),
    conflict: conflict(),
    onOpenProfile,
    onSuccess,
    onClose,
  });
  return { ...rendered, onOpenProfile, onSuccess, onClose };
}

async function selectSurvivor(label = 'Synthetic One'): Promise<void> {
  await fireEvent.click(screen.getByRole('radio', { name: label }));
}

function renderProfiles(first: Person, second: Person) {
  return render(PersonBindingConflictModal, {
    client: createAPIClient(vi.fn<typeof fetch>()),
    conflict: {
      error: 'person_merge_required',
      message: 'Choose a survivor',
      profiles: [
        { person: first, etag: `"person-${first.id}-r${first.revision}"` },
        { person: second, etag: `"person-${second.id}-r${second.revision}"` },
      ],
    },
    onOpenProfile: vi.fn(),
    onSuccess: vi.fn(),
    onClose: vi.fn(),
  });
}

describe('PersonBindingConflictModal', () => {
  it('needs no confirmation checkbox: a survivor is preselected and the merge is ready', () => {
    renderModal(vi.fn<typeof fetch>());

    expect(screen.queryByRole('checkbox')).toBeNull();
    expect(screen.getByRole('radio', { name: 'Synthetic One' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'Synthetic Two' }).getAttribute('aria-checked')).toBe('false');
    expect(screen.getByRole('button', { name: 'Merge into selected survivor' })).toHaveProperty('disabled', false);
    const hint = screen.getByText(/undo a merge later/).textContent ?? '';
    expect(hint).toContain('Maintenance tab');
    expect(hint).toContain('Split merged profile');
  });

  it.each([
    {
      name: 'the profile with more identities',
      first: person(7, 1, 'Synthetic One'),
      second: { ...person(9, 1, 'Synthetic Two'), participant_ids: [90, 91] },
      want: 'Synthetic Two',
    },
    {
      name: 'the older profile when identity counts tie',
      first: person(7, 1, 'Synthetic One'),
      second: { ...person(9, 1, 'Synthetic Two'), created_at: '2026-07-01T00:00:00Z' },
      want: 'Synthetic Two',
    },
    {
      name: 'the lower id when identities and age tie',
      first: person(9, 1, 'Synthetic Two'),
      second: person(7, 1, 'Synthetic One'),
      want: 'Synthetic One',
    },
  ])('preselects $name as the survivor', ({ first, second, want }) => {
    renderProfiles(first, second);

    const checked = screen.getAllByRole('radio').filter((radio) => radio.getAttribute('aria-checked') === 'true');
    expect(checked.map((radio) => radio.textContent?.trim())).toEqual([want]);
  });

  it('names an unnamed profile through the resolver and tells same-named profiles apart without IDs', async () => {
    const fetchFn = vi.fn<typeof fetch>(async () => Response.json({}));
    const unnamed = { ...person(7, 4, ''), created_at: '2026-07-01T00:00:00Z' };
    render(PersonBindingConflictModal, {
      client: createAPIClient(withEntityLabels(fetchFn, { person: { 7: 'Synthetic Two' } })),
      conflict: { ...conflict(), profiles: [{ person: unnamed, etag: '"person-7-r4"' }, conflict().profiles[1]] },
      onOpenProfile: vi.fn(),
      onSuccess: vi.fn(),
      onClose: vi.fn(),
    });

    const radios = await waitFor(() => {
      const found = screen.getAllByRole('radio').map((radio) => radio.getAttribute('aria-label') ?? radio.textContent ?? '');
      expect(found.every((label) => label.startsWith('Synthetic Two (created '))).toBe(true);
      return found;
    });
    expect(new Set(radios).size).toBe(2);
    expect(document.body.textContent).not.toMatch(/Person \d|\b(7|9)\b,/);
    expect(fetchFn).not.toHaveBeenCalled();
  });

  it('sends the exact survivor-first merge request and reports operation-result metadata once', async () => {
    const requests: Request[] = [];
    const merged = person(7, 5, 'Synthetic One');
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      return Response.json(mergeResult(merged, 'stale'), { headers: { ETag: '"person-7-r5"' } });
    });
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111');
    const { onSuccess, onClose } = renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce());
    expect(requests).toHaveLength(1);
    expect(new URL(requests[0]!.url).pathname).toBe('/api/v1/people/7/merge');
    expect(requests[0]!.headers.get('If-Match')).toBe('"person-7-r4", "person-9-r2"');
    expect(requests[0]!.headers.get('Idempotency-Key')).toBe('11111111-1111-4111-8111-111111111111');
    await expect(requests[0]!.clone().json()).resolves.toEqual({ absorbed_person_id: 9 });
    expect(onSuccess).toHaveBeenCalledWith({
      result: mergeResult(merged, 'stale'),
      survivor: merged,
      responseETag: '"person-7-r5"',
    });
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Merge into selected survivor' })).toBeNull();
  });

  it('reuses one UUID only for an explicit unchanged transport retry', async () => {
    const requests: Request[] = [];
    const merged = person(7, 5, 'Synthetic One');
    let attempts = 0;
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      requests.push(requestOf(input));
      attempts += 1;
      if (attempts === 1) throw new TypeError('connection reset');
      return Response.json(mergeResult(merged), { headers: { ETag: '"person-7-r5"' } });
    });
    const uuid = vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValueOnce('11111111-1111-4111-8111-111111111111');
    const { onSuccess } = renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));
    expect((await screen.findByRole('alert')).textContent).toContain('connection reset');
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce());
    expect(requests.map((request) => request.headers.get('Idempotency-Key'))).toEqual([
      '11111111-1111-4111-8111-111111111111',
      '11111111-1111-4111-8111-111111111111',
    ]);
    expect(uuid).toHaveBeenCalledOnce();
  });

  it('changing the survivor rotates the full-snapshot key', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      throw new TypeError('offline');
    });
    vi.spyOn(globalThis.crypto, 'randomUUID')
      .mockReturnValueOnce('11111111-1111-4111-8111-111111111111')
      .mockReturnValueOnce('22222222-2222-4222-8222-222222222222');
    renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));
    await screen.findByRole('alert');
    await fireEvent.click(screen.getByRole('radio', { name: 'Synthetic Two' }));
    expect(screen.getByRole('button', { name: 'Merge into selected survivor' })).toHaveProperty('disabled', false);
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    await waitFor(() => expect(requests).toHaveLength(2));
    expect(new URL(requests[1]!.url).pathname).toBe('/api/v1/people/9/merge');
    expect(requests[1]!.headers.get('If-Match')).toBe('"person-9-r2", "person-7-r4"');
    expect(requests.map((request) => request.headers.get('Idempotency-Key'))).toEqual([
      '11111111-1111-4111-8111-111111111111',
      '22222222-2222-4222-8222-222222222222',
    ]);
  });

  it('keeps an explicitly chosen survivor across a stale reload so the retry merges into it', async () => {
    const requests: Request[] = [];
    let mergeAttempts = 0;
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      const path = new URL(request.url).pathname;
      if (request.method === 'GET' && path === '/api/v1/people/7') {
        return Response.json(person(7, 5, 'Synthetic One Updated'), { headers: { ETag: '"person-7-r5"' } });
      }
      if (request.method === 'GET' && path === '/api/v1/people/9') {
        return Response.json(person(9, 3, 'Synthetic Two Updated'), { headers: { ETag: '"person-9-r3"' } });
      }
      mergeAttempts += 1;
      if (mergeAttempts === 1) {
        return Response.json({ error: 'person_merge_revision_conflict', message: 'Reload profiles' }, { status: 409 });
      }
      return Response.json(mergeResult(person(9, 4, 'Synthetic Two Updated')), { headers: { ETag: '"person-9-r4"' } });
    });
    const { onSuccess } = renderModal(fetchFn);

    // The default is Synthetic One; the user explicitly keeps Synthetic Two.
    await focusAndClick(screen.getByRole('radio', { name: 'Synthetic Two' }));
    await focusAndClick(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    expect((await screen.findByRole('alert')).textContent).toContain('Profiles changed — check the survivor');
    const chosen = await screen.findByRole('radio', { name: 'Synthetic Two Updated' });
    expect(chosen.getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'Synthetic One Updated' }).getAttribute('aria-checked')).toBe('false');
    const submit = screen.getByRole('button', { name: 'Merge into selected survivor' });
    await waitFor(() => expect(submit).toHaveProperty('disabled', false));
    await focusAndClick(submit);

    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce());
    const posts = requests.filter((request) => request.method === 'POST');
    expect(posts.map((request) => new URL(request.url).pathname)).toEqual([
      '/api/v1/people/9/merge', '/api/v1/people/9/merge'
    ]);
    expect(posts[1]!.headers.get('If-Match')).toBe('"person-9-r3", "person-7-r5"');
    await expect(posts[1]!.clone().json()).resolves.toEqual({ absorbed_person_id: 7 });
  });

  it('reloads both exact profiles atomically and needs another explicit merge click', async () => {
    const requests: Request[] = [];
    const refreshedSeven = person(7, 5, 'Synthetic One Updated');
    const refreshedNine = person(9, 3, 'Synthetic Two Updated');
    let mergeAttempts = 0;
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      const path = new URL(request.url).pathname;
      if (request.method === 'GET' && path === '/api/v1/people/7') {
        return Response.json(refreshedSeven, { headers: { ETag: '"person-7-r5"' } });
      }
      if (request.method === 'GET' && path === '/api/v1/people/9') {
        return Response.json(refreshedNine, { headers: { ETag: '"person-9-r3"' } });
      }
      mergeAttempts += 1;
      if (mergeAttempts === 1) {
        return Response.json({ error: 'person_merge_revision_conflict', message: 'Reload profiles' }, { status: 409 });
      }
      return Response.json(mergeResult(person(7, 6, 'Synthetic One Updated')), { headers: { ETag: '"person-7-r6"' } });
    });
    vi.spyOn(globalThis.crypto, 'randomUUID')
      .mockReturnValueOnce('11111111-1111-4111-8111-111111111111')
      .mockReturnValueOnce('22222222-2222-4222-8222-222222222222');
    const { onSuccess } = renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    expect(await screen.findByRole('radio', { name: 'Synthetic One Updated' })).toBeDefined();
    expect(screen.getByRole('radio', { name: 'Synthetic Two Updated' })).toBeDefined();
    expect(
      requests
        .filter((request) => request.method === 'GET')
        .map((request) => new URL(request.url).pathname)
        .sort(),
    ).toEqual(['/api/v1/people/7', '/api/v1/people/9']);
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);
    const refreshedSurvivor = screen.getByRole('radio', { name: 'Synthetic One Updated' });
    const refreshedAbsorbed = screen.getByRole('radio', { name: 'Synthetic Two Updated' });
    const submit = screen.getByRole('button', { name: 'Merge into selected survivor' });
    expect(refreshedSurvivor.getAttribute('aria-checked')).toBe('true');
    expect(refreshedAbsorbed.getAttribute('aria-checked')).toBe('false');
    expect((await screen.findByRole('alert')).textContent).toContain('Profiles changed');
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);

    await waitFor(() => expect(submit).toHaveProperty('disabled', false));
    await fireEvent.click(submit);
    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce());
    const posts = requests.filter((request) => request.method === 'POST');
    expect(posts).toHaveLength(2);
    expect(posts[1]!.headers.get('If-Match')).toBe('"person-7-r5", "person-9-r3"');
    expect(posts[1]!.headers.get('Idempotency-Key')).toBe('22222222-2222-4222-8222-222222222222');
  });

  it.each([
    { status: 409, error: 'person_merge_idempotency_conflict' },
    { status: 409, error: 'person_carddav_published' },
    { status: 412, error: 'precondition_failed' },
  ])('does not reload or reuse a key for application failure $status/$error', async ({ status, error }) => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      requests.push(requestOf(input));
      return Response.json({ error, message: 'Application failure' }, { status });
    });
    const uuid = vi
      .spyOn(globalThis.crypto, 'randomUUID')
      .mockReturnValueOnce('11111111-1111-4111-8111-111111111111')
      .mockReturnValueOnce('22222222-2222-4222-8222-222222222222');
    renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));
    expect((await screen.findByRole('alert')).textContent).toContain('Application failure');
    expect(requests.filter((request) => request.method === 'GET')).toHaveLength(0);
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests.map((request) => request.headers.get('Idempotency-Key'))).toEqual([
      '11111111-1111-4111-8111-111111111111',
      '22222222-2222-4222-8222-222222222222',
    ]);
    expect(uuid).toHaveBeenCalledTimes(2);
  });

  it.each([
    {
      name: 'a wrong self-consistent person ID',
      responses: {
        7: { body: person(8, 5, 'Wrong Person'), etag: '"person-8-r5"' },
        9: { body: person(9, 3, 'Synthetic Two Updated'), etag: '"person-9-r3"' },
      },
    },
    {
      name: 'swapped self-consistent person IDs',
      responses: {
        7: { body: person(9, 3, 'Synthetic Two Updated'), etag: '"person-9-r3"' },
        9: { body: person(7, 5, 'Synthetic One Updated'), etag: '"person-7-r5"' },
      },
    },
  ])('rejects the entire stale reload when it returns $name', async ({ responses }) => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      const path = new URL(request.url).pathname;
      if (request.method === 'POST') {
        return Response.json({ error: 'person_merge_revision_conflict', message: 'Reload profiles' }, { status: 409 });
      }
      const requestedID = Number(path.split('/').at(-1));
      const response = responses[requestedID as keyof typeof responses];
      return Response.json(response.body, { headers: { ETag: response.etag } });
    });
    renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    expect((await screen.findByRole('alert')).textContent).toContain('could not load both current profile revisions');
    expect(screen.getByRole('radio', { name: 'Synthetic One' })).toBeDefined();
    expect(screen.getByRole('radio', { name: 'Synthetic Two' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'Merge into selected survivor' })).toHaveProperty('disabled', true);
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);
  });

  it('retains both original snapshots when either stale reload is unusable', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      const path = new URL(request.url).pathname;
      if (request.method === 'GET' && path === '/api/v1/people/7') {
        return Response.json(person(7, 5, 'Synthetic One Updated'), { headers: { ETag: '"person-7-r5"' } });
      }
      if (request.method === 'GET') return Response.json(person(9, 3, 'Synthetic Two Updated'));
      return Response.json({ error: 'person_merge_revision_conflict', message: 'Reload profiles' }, { status: 409 });
    });
    renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    expect((await screen.findByRole('alert')).textContent).toContain('could not load both current profile revisions');
    expect(screen.getByRole('radio', { name: 'Synthetic One' })).toBeDefined();
    expect(screen.getByRole('radio', { name: 'Synthetic Two' })).toBeDefined();
    expect(screen.queryByRole('radio', { name: 'Synthetic One Updated' })).toBeNull();
  });

  it('blocks another merge POST after the stale profile reload fails', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      const path = new URL(request.url).pathname;
      if (request.method === 'POST') {
        return Response.json({ error: 'person_merge_revision_conflict', message: 'Reload profiles' }, { status: 409 });
      }
      if (path === '/api/v1/people/7') {
        return Response.json(person(7, 5, 'Synthetic One Updated'), { headers: { ETag: '"person-7-r5"' } });
      }
      return Response.json(person(9, 3, 'Synthetic Two Updated'));
    });
    renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    expect((await screen.findByRole('alert')).textContent).toContain('could not load both current profile revisions');
    const survivor = screen.getByRole('radio', { name: 'Synthetic One' });
    const submit = screen.getByRole('button', { name: 'Merge into selected survivor' });
    expect(survivor).toHaveProperty('disabled', true);
    expect(submit).toHaveProperty('disabled', true);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Retry profile reload' })).toHaveProperty('disabled', false),
    );

    await fireEvent.click(submit);
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);
  });

  it('keeps merge readiness invalid when an explicit profile reload retry also fails', async () => {
    const requests: Request[] = [];
    const retrySettlers: Array<(response: Response) => void> = [];
    let getCount = 0;
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      if (request.method === 'POST') {
        return Response.json({ error: 'person_merge_revision_conflict', message: 'Reload profiles' }, { status: 409 });
      }
      getCount += 1;
      if (getCount <= 2) return Response.json({ error: 'unavailable', message: 'Reload unavailable' }, { status: 503 });
      return new Promise<Response>((resolve) => {
        retrySettlers.push(resolve);
      });
    });
    const onClose = vi.fn();
    renderModal(fetchFn, { onClose });

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Retry profile reload' })).toHaveProperty('disabled', false),
    );

    await fireEvent.click(screen.getByRole('button', { name: 'Retry profile reload' }));
    await waitFor(() => expect(retrySettlers).toHaveLength(2));
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveProperty('disabled', true);
    expect(screen.queryByRole('button', { name: 'Close person merge' })).toBeNull();
    await fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).not.toHaveBeenCalled();

    for (const settle of retrySettlers) {
      settle(Response.json({ error: 'unavailable', message: 'Reload unavailable' }, { status: 503 }));
    }
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Retry profile reload' })).toHaveProperty('disabled', false),
    );
    expect((await screen.findByRole('alert')).textContent).toContain('could not load both current profile revisions');
    expect(screen.getByRole('radio', { name: 'Synthetic One' })).toHaveProperty('disabled', true);
    expect(screen.getByRole('button', { name: 'Merge into selected survivor' })).toHaveProperty('disabled', true);
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);
    expect(requests.filter((request) => request.method === 'GET')).toHaveLength(4);
  });

  it('uses only fresh tags after an explicit reload and another merge click', async () => {
    const requests: Request[] = [];
    const refreshedSeven = person(7, 5, 'Synthetic One Updated');
    const refreshedNine = person(9, 3, 'Synthetic Two Updated');
    const getCounts = new Map<number, number>();
    let mergeAttempts = 0;
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      const path = new URL(request.url).pathname;
      if (request.method === 'POST') {
        mergeAttempts += 1;
        if (mergeAttempts === 1) {
          return Response.json(
            { error: 'person_merge_revision_conflict', message: 'Reload profiles' },
            { status: 409 },
          );
        }
        return Response.json(mergeResult(person(7, 6, 'Synthetic One Updated')), {
          headers: { ETag: '"person-7-r6"' },
        });
      }
      const requestedID = Number(path.split('/').at(-1));
      const count = (getCounts.get(requestedID) ?? 0) + 1;
      getCounts.set(requestedID, count);
      if (count === 1) return Response.json({ error: 'unavailable', message: 'Reload unavailable' }, { status: 503 });
      const refreshed = requestedID === 7 ? refreshedSeven : refreshedNine;
      return Response.json(refreshed, { headers: { ETag: `"person-${requestedID}-r${refreshed.revision}"` } });
    });
    vi.spyOn(globalThis.crypto, 'randomUUID')
      .mockReturnValueOnce('11111111-1111-4111-8111-111111111111')
      .mockReturnValueOnce('22222222-2222-4222-8222-222222222222');
    const { onSuccess } = renderModal(fetchFn);

    await selectSurvivor();
    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Retry profile reload' })).toHaveProperty('disabled', false),
    );
    await fireEvent.click(screen.getByRole('button', { name: 'Retry profile reload' }));

    expect(await screen.findByRole('radio', { name: 'Synthetic One Updated' })).toBeDefined();
    expect(screen.getByRole('radio', { name: 'Synthetic Two Updated' })).toBeDefined();
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'Retry profile reload' })).toBeNull();
    const refreshedSurvivor = screen.getByRole('radio', { name: 'Synthetic One Updated' });
    const refreshedAbsorbed = screen.getByRole('radio', { name: 'Synthetic Two Updated' });
    await waitFor(() => expect(refreshedSurvivor).toHaveProperty('disabled', false));
    expect(refreshedSurvivor.getAttribute('aria-checked')).toBe('true');
    expect(refreshedAbsorbed.getAttribute('aria-checked')).toBe('false');
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);

    await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce());
    const posts = requests.filter((request) => request.method === 'POST');
    expect(posts).toHaveLength(2);
    expect(posts[1]!.headers.get('If-Match')).toBe('"person-7-r5", "person-9-r3"');
    expect(posts[1]!.headers.get('Idempotency-Key')).toBe('22222222-2222-4222-8222-222222222222');
  });

  it('offers one non-mutating inspection action per profile', async () => {
    const fetchFn = vi.fn<typeof fetch>();
    const onOpenProfile = vi.fn();
    renderModal(fetchFn, { onOpenProfile });

    await fireEvent.click(screen.getByRole('button', { name: 'Open Synthetic One profile' }));

    expect(onOpenProfile).toHaveBeenCalledOnce();
    expect(onOpenProfile).toHaveBeenCalledWith(7);
    expect(fetchFn).not.toHaveBeenCalled();
  });

  it('blocks dismissal and root shortcuts while merge work is pending', async () => {
    let resolveMerge: ((response: Response) => void) | undefined;
    const fetchFn = vi.fn<typeof fetch>(
      () =>
        new Promise<Response>((resolve) => {
          resolveMerge = resolve;
        }),
    );
    const onClose = vi.fn();
    const rootShortcut = vi.fn();
    const unregister = appShortcuts.register('x', rootShortcut);
    try {
      renderModal(fetchFn, { onClose });
      await waitFor(() => expect(appShortcuts.activeScope()).toBe('person-binding-conflict-modal'));
      await selectSurvivor();
      await fireEvent.click(screen.getByRole('button', { name: 'Merge into selected survivor' }));

      expect(screen.getByRole('button', { name: 'Cancel' })).toHaveProperty('disabled', true);
      expect(screen.queryByRole('button', { name: 'Close person merge' })).toBeNull();
      await fireEvent.keyDown(window, { key: 'Escape' });
      await fireEvent.pointerDown(document.querySelector('.kit-modal-overlay')!);
      appShortcuts.handleKeydown(new KeyboardEvent('keydown', { key: 'x', cancelable: true }));
      expect(onClose).not.toHaveBeenCalled();
      expect(rootShortcut).not.toHaveBeenCalled();

      resolveMerge?.(Response.json({ error: 'person_carddav_published', message: 'Unpublish first' }, { status: 409 }));
      expect((await screen.findByRole('alert')).textContent).toContain('Unpublish first');
      await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
      expect(onClose).toHaveBeenCalledOnce();
    } finally {
      unregister();
    }
  });
});
