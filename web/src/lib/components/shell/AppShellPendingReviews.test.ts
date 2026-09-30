import { render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { PendingReviewsMonitor } from '../../directory/pending-reviews.svelte';
import { ExploreState } from '../../explore/state.svelte';
import { focusAndClick } from '../../../test/kit-ui';
import AppShell from './AppShell.svelte';

function candidate(id: number, state = 'candidate') {
  return {
    id, left_id: id * 10, left_kind: 'person', right_id: id * 10 + 1, right_kind: 'participant',
    basis: 'stable_provider_id', source: 'synthetic', state, evidence: [],
    created_at: '2026-08-01T00:00:00Z', updated_at: '2026-08-01T00:00:00Z'
  };
}

beforeEach(() => {
  PendingReviewsMonitor.autoStart = true;
});

afterEach(() => {
  PendingReviewsMonitor.autoStart = false;
});

describe('AppShell pending reviews dot', () => {
  it('shows the Reviews dot while anything waits and clears it after the last decision', async () => {
    window.history.replaceState(null, '', `/?explore=${encodeURIComponent(JSON.stringify({
      workspace: 'directory_review', reviewKind: 'identity', identityState: 'candidate'
    }))}`);
    let decided = false;
    const pendingChecks: string[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/reviews/pending') {
        pendingChecks.push(path);
        return Response.json(decided ? { pending: false, kinds: [] } : { pending: true, kinds: ['identity'] });
      }
      if (path === '/api/v1/identity/match-candidates') {
        return Response.json({ candidates: decided ? [] : [candidate(17)], limit: 100, offset: 0 });
      }
      if (path.endsWith('/17/reject')) {
        decided = true;
        return Response.json({ candidate: candidate(17, 'rejected'), identity_revision: 2, cache_state: 'ready' });
      }
      return Response.json({});
    });
    const state = new ExploreState(window);
    const rendered = render(AppShell, { client: createAPIClient(fetchFn), state, enabled: false });
    const nav = screen.getByRole('navigation', { name: 'Primary' });

    const reviews = await within(nav).findByRole('button', { name: 'Reviews Items waiting' });
    expect(within(reviews).getByRole('img', { name: 'Items waiting' })).toBeDefined();
    expect(pendingChecks).toHaveLength(1);

    await focusAndClick(await screen.findByRole('button', { name: 'Keep separate' }));
    await focusAndClick(screen.getByRole('dialog', { name: 'Keep separate' }).querySelector('button.kit-button--solid')!);

    // The decision forces a check at once, inside the one-minute floor.
    await waitFor(() => expect(within(nav).getByRole('button', { name: 'Reviews' })).toBeDefined());
    expect(within(nav).queryByRole('img', { name: 'Items waiting' })).toBeNull();
    expect(pendingChecks).toHaveLength(2);

    rendered.unmount();
    state.destroy();
  });
});

describe('PendingReviewsMonitor', () => {
  beforeEach(() => {
    PendingReviewsMonitor.autoStart = true;
  });

  it('checks at most once a minute unless a decision forces it', async () => {
    let now = 0;
    const fetchFn = vi.fn<typeof fetch>(async () => Response.json({ pending: true, kinds: ['organization'] }));
    const monitor = new PendingReviewsMonitor(createAPIClient(fetchFn), () => now);

    await monitor.refresh();
    expect(monitor.waiting).toBe(true);
    now = 59_000;
    await monitor.refresh();
    expect(fetchFn).toHaveBeenCalledOnce();
    await monitor.refresh(true);
    expect(fetchFn).toHaveBeenCalledTimes(2);
    now = 120_000;
    await monitor.refresh();
    expect(fetchFn).toHaveBeenCalledTimes(3);
  });

  it('keeps the last answer when a check fails', async () => {
    let fail = false;
    const fetchFn = vi.fn<typeof fetch>(async () => {
      if (fail) throw new TypeError('offline');
      return Response.json({ pending: true, kinds: ['enrichment'] });
    });
    const monitor = new PendingReviewsMonitor(createAPIClient(fetchFn));
    await monitor.refresh(true);
    fail = true;
    await monitor.refresh(true);
    expect(monitor.waiting).toBe(true);
  });
});
