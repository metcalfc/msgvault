import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { appShortcuts } from '@kenn-io/kit-ui';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { withEntityLabels } from '../../../test/entity-labels';
import {
  DirectoryReviewController,
  type IdentityMatchCandidate,
  type PersonMergeRequiredError
} from '../../directory/review-controller.svelte';
import IdentityDecisionModal from './IdentityDecisionModal.svelte';
import { focusAndClick } from '../../../test/kit-ui';


const syntheticNames = { person: { 42: 'Avery Example', 170: 'Avery Example' }, participant: { 171: 'blair@example.org' } };
afterEach(() => cleanup());

function candidate(state = 'candidate'): IdentityMatchCandidate {
  return {
    id: 17,
    left_id: 170,
    left_kind: 'person',
    right_id: 171,
    right_kind: 'participant',
    basis: 'stable_provider_id',
    source: 'synthetic',
    state,
    evidence: [],
    created_at: '2026-08-01T10:00:00Z',
    updated_at: '2026-08-02T11:00:00Z'
  };
}

function page(rows: IdentityMatchCandidate[]): Response {
  return Response.json({ candidates: rows, limit: 100, offset: 0 });
}

function deferredResponse() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((next) => { resolve = next; });
  return { promise, resolve };
}

function requestOf(input: RequestInfo | URL): Request {
  return input instanceof Request ? input : new Request(input);
}

describe('IdentityDecisionModal', () => {
  it.each([
    { decision: 'accept' as const, action: 'Link identities', path: '/api/v1/identity/match-candidates/17/accept', state: 'accepted' },
    { decision: 'reject' as const, action: 'Keep separate', path: '/api/v1/identity/match-candidates/17/reject', state: 'rejected' }
  ])('submits one generated $decision request with only trimmed notes', async ({ decision, action, path, state }) => {
    const requests: Request[] = [];
    const decided = candidate(state);
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      if (request.method === 'POST') {
        return Response.json({ candidate: decided, identity_revision: 4, cache_state: 'ready' });
      }
      return page([decided]);
    });
    const controller = new DirectoryReviewController(createAPIClient(withEntityLabels(fetchFn, syntheticNames)));
    const onClose = vi.fn();
    render(IdentityDecisionModal, {
      controller,
      candidate: candidate(),
      decision,
      reviewContext: controller.reviewContextSnapshot(),
      onClose,
      onContextInvalidated: vi.fn(),
      onResolveMerge: vi.fn()
    });

    expect(screen.queryByText(/merge people/i)).toBeNull();
    expect(screen.queryByRole('textbox', { name: 'Decision notes' })).toBeNull();
    await focusAndClick(screen.getByRole('button', { name: 'Add a note' }));
    expect(document.activeElement).toBe(screen.getByRole('textbox', { name: 'Decision notes' }));
    await fireEvent.input(screen.getByRole('textbox', { name: 'Decision notes' }), {
      target: { value: '  Synthetic review note  ' }
    });
    await fireEvent.click(screen.getByRole('button', { name: action }));

    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    const posts = requests.filter((request) => request.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(new URL(posts[0]!.url).pathname).toBe(path);
    await expect(posts[0]!.clone().json()).resolves.toEqual({ notes: 'Synthetic review note' });
    expect(posts[0]!.headers.has('If-Match')).toBe(false);
    expect(posts[0]!.headers.has('Idempotency-Key')).toBe(false);
  });

  it('folds the optional notes away and sends no notes when none were added', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      if (request.method === 'POST') {
        return Response.json({ candidate: candidate('accepted'), identity_revision: 4, cache_state: 'ready' });
      }
      return page([]);
    });
    const controller = new DirectoryReviewController(createAPIClient(withEntityLabels(fetchFn, syntheticNames)));
    const onClose = vi.fn();
    const onDecided = vi.fn();
    render(IdentityDecisionModal, {
      controller,
      candidate: candidate(),
      decision: 'accept',
      reviewContext: controller.reviewContextSnapshot(),
      onClose,
      onDecided,
      onContextInvalidated: vi.fn(),
      onResolveMerge: vi.fn()
    });

    const disclosure = screen.getByRole('button', { name: 'Add a note' });
    expect(disclosure.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByRole('textbox')).toBeNull();
    await focusAndClick(screen.getByRole('button', { name: 'Link identities' }));

    await waitFor(() => expect(onDecided).toHaveBeenCalledOnce());
    expect(onClose).not.toHaveBeenCalled();
    const posts = requests.filter((request) => request.method === 'POST');
    expect(posts).toHaveLength(1);
    await expect(posts[0]!.clone().json()).resolves.toEqual({});
  });

  it('shows the notes field already open when the candidate has a saved draft', () => {
    const controller = new DirectoryReviewController(createAPIClient(vi.fn<typeof fetch>()));
    controller.setDecisionDraft(17, 'Earlier synthetic note');
    render(IdentityDecisionModal, {
      controller,
      candidate: candidate(),
      decision: 'reject',
      reviewContext: controller.reviewContextSnapshot(),
      onClose: vi.fn(),
      onContextInvalidated: vi.fn(),
      onResolveMerge: vi.fn()
    });

    expect(screen.queryByRole('button', { name: 'Add a note' })).toBeNull();
    expect((screen.getByRole('textbox', { name: 'Decision notes' }) as HTMLTextAreaElement).value).toBe('Earlier synthetic note');
  });

  it('keeps the failed row and its own draft visible without automatically retrying', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      return Response.json({ error: 'unavailable', message: 'Decision unavailable' }, { status: 503 });
    });
    const controller = new DirectoryReviewController(createAPIClient(withEntityLabels(fetchFn, syntheticNames)));
    controller.rows = [candidate(), { ...candidate(), id: 18, left_id: 180, right_id: 181 }];
    controller.setDecisionDraft(18, 'Other row note');
    const onClose = vi.fn();
    render(IdentityDecisionModal, {
      controller,
      candidate: candidate(),
      decision: 'reject',
      reviewContext: controller.reviewContextSnapshot(),
      onClose,
      onContextInvalidated: vi.fn(),
      onResolveMerge: vi.fn()
    });

    await focusAndClick(screen.getByRole('button', { name: 'Add a note' }));
    const notes = screen.getByRole('textbox', { name: 'Decision notes' });
    await fireEvent.input(notes, { target: { value: 'Keep row 17 note' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Keep separate' }));

    expect((await screen.findByRole('alert')).textContent).toContain('Decision unavailable');
    expect(screen.getByRole('dialog', { name: 'Keep separate' })).toBeDefined();
    expect((notes as HTMLTextAreaElement).value).toBe('Keep row 17 note');
    expect(controller.getDecisionDraft(17)).toBe('Keep row 17 note');
    expect(controller.getDecisionDraft(18)).toBe('Other row note');
    expect(controller.rows).toHaveLength(2);
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);
    expect(onClose).not.toHaveBeenCalled();
  });

  it('refuses a submit from an invalidated opening context', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      return page([candidate()]);
    });
    const controller = new DirectoryReviewController(createAPIClient(withEntityLabels(fetchFn, syntheticNames)));
    const onContextInvalidated = vi.fn();
    render(IdentityDecisionModal, {
      controller,
      candidate: candidate(),
      decision: 'accept',
      reviewContext: controller.reviewContextSnapshot(),
      onClose: vi.fn(),
      onContextInvalidated,
      onResolveMerge: vi.fn()
    });

    controller.applyURLState({ reviewKind: 'identity', identityState: 'candidate' }, true);
    await fireEvent.click(screen.getByRole('button', { name: 'Link identities' }));

    expect(onContextInvalidated).toHaveBeenCalledOnce();
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(0);
  });

  it('refuses to retry a failed decision after its opening context is invalidated', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      if (request.method === 'POST') {
        return Response.json({ error: 'unavailable', message: 'Decision unavailable' }, { status: 503 });
      }
      return page([candidate()]);
    });
    const controller = new DirectoryReviewController(createAPIClient(withEntityLabels(fetchFn, syntheticNames)));
    const onContextInvalidated = vi.fn();
    render(IdentityDecisionModal, {
      controller,
      candidate: candidate(),
      decision: 'reject',
      reviewContext: controller.reviewContextSnapshot(),
      onClose: vi.fn(),
      onContextInvalidated,
      onResolveMerge: vi.fn()
    });

    await fireEvent.click(screen.getByRole('button', { name: 'Keep separate' }));
    expect((await screen.findByRole('alert')).textContent).toContain('Decision unavailable');
    controller.applyURLState({ reviewKind: 'identity', identityState: 'candidate' }, true);
    await fireEvent.click(screen.getByRole('button', { name: 'Keep separate' }));

    expect(onContextInvalidated).toHaveBeenCalledOnce();
    expect(requests.filter((request) => request.method === 'POST')).toHaveLength(1);
  });

  it('blocks every close path and the root shortcut scope while a decision is pending', async () => {
    const pending = deferredResponse();
    const fetchFn = vi.fn<typeof fetch>(async () => pending.promise);
    const controller = new DirectoryReviewController(createAPIClient(withEntityLabels(fetchFn, syntheticNames)));
    const onClose = vi.fn();
    const rootShortcut = vi.fn();
    const unregister = appShortcuts.register('x', rootShortcut);
    try {
      render(IdentityDecisionModal, {
        controller,
        candidate: candidate(),
        decision: 'accept',
        reviewContext: controller.reviewContextSnapshot(),
        onClose,
        onContextInvalidated: vi.fn(),
        onResolveMerge: vi.fn()
      });
      await waitFor(() => expect(appShortcuts.activeScope()).toBe('identity-decision-modal'));
      await fireEvent.click(screen.getByRole('button', { name: 'Link identities' }));
      await waitFor(() => expect(controller.isDecisionPending(17)).toBe(true));

      expect(screen.getByRole('button', { name: 'Cancel' })).toHaveProperty('disabled', true);
      expect(screen.queryByRole('button', { name: 'Close identity decision' })).toBeNull();
      await fireEvent.keyDown(window, { key: 'Escape' });
      await fireEvent.pointerDown(document.querySelector('.kit-modal-overlay')!);
      appShortcuts.handleKeydown(new KeyboardEvent('keydown', { key: 'x', cancelable: true }));
      expect(onClose).not.toHaveBeenCalled();
      expect(rootShortcut).not.toHaveBeenCalled();

      pending.resolve(Response.json({ error: 'unavailable', message: 'Decision unavailable' }, { status: 503 }));
      expect((await screen.findByRole('alert')).textContent).toContain('Decision unavailable');
      await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
      expect(onClose).toHaveBeenCalledOnce();
    } finally {
      unregister();
    }
  });

  it('hands an accept that needs a merge straight to the merge, with no extra step or click', async () => {
    const conflict: PersonMergeRequiredError = {
      error: 'person_merge_required',
      message: 'Choose a survivor',
      profiles: [
        {
          etag: '"person-7-r4"',
          person: {
            id: 7, revision: 4, display_name: 'Synthetic One', participant_ids: [170],
            created_at: '2026-07-01T00:00:00Z', updated_at: '2026-08-01T00:00:00Z', vcard_uid: 'synthetic-one'
          }
        },
        {
          etag: '"person-9-r2"',
          person: {
            id: 9, revision: 2, display_name: 'Synthetic Two', participant_ids: [171],
            created_at: '2026-07-02T00:00:00Z', updated_at: '2026-08-02T00:00:00Z', vcard_uid: 'synthetic-two'
          }
        }
      ]
    };
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = requestOf(input);
      requests.push(request);
      return Response.json(conflict, { status: 409 });
    });
    const controller = new DirectoryReviewController(createAPIClient(withEntityLabels(fetchFn, syntheticNames)));
    const onResolveMerge = vi.fn();
    const onClose = vi.fn();
    const onDecided = vi.fn();
    render(IdentityDecisionModal, {
      controller,
      candidate: candidate(),
      decision: 'accept',
      reviewContext: controller.reviewContextSnapshot(),
      onClose,
      onDecided,
      onContextInvalidated: vi.fn(),
      onResolveMerge
    });

    await fireEvent.click(screen.getByRole('button', { name: 'Link identities' }));

    await waitFor(() => expect(onResolveMerge).toHaveBeenCalledOnce());
    expect(onResolveMerge).toHaveBeenCalledWith(conflict);
    expect(onClose).not.toHaveBeenCalled();
    expect(onDecided).not.toHaveBeenCalled();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Resolve merge' })).toBeNull();
    expect(document.body.textContent).not.toContain('person-7-r4');
    expect(document.body.textContent).not.toMatch(/ETag|explicit merge/i);
    // The accept is sent once and not retried; the merge modal issues the merge.
    expect(requests).toHaveLength(1);
    expect(new URL(requests[0]!.url).pathname).toBe('/api/v1/identity/match-candidates/17/accept');
  });
});
