import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import SemanticIndexControl from './SemanticIndexControl.svelte';

function pathOf(input: RequestInfo | URL): string {
  return new URL(input instanceof Request ? input.url : String(input)).pathname;
}

describe('SemanticIndexControl', () => {
  it('loads nothing until asked, then names the index status', async () => {
    const fetchFn = vi.fn<typeof fetch>(async () => Response.json({
      status: 'incomplete', eligible_count: 2, embedded_count: 1, percentage: 50,
      cache_revision: 'cache-1', actions: []
    }));
    render(SemanticIndexControl, { client: createAPIClient(fetchFn) });

    expect(fetchFn).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole('button', { name: 'Check index status' }));
    expect(await screen.findByText('Semantic index: 50% of 2 items.')).toBeDefined();
  });

  it('runs a confirmed full rebuild and refreshes the status after completion', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      requests.push(request);
      if (pathOf(request.url).endsWith('/cli/run')) {
        return new Response(`${JSON.stringify({ type: 'complete' })}\n`, {
          headers: { 'Content-Type': 'application/x-ndjson' }
        });
      }
      return Response.json({
        status: 'ready', eligible_count: 2, embedded_count: 2, percentage: 100,
        vector_generation: 8, cache_revision: 'cache-1', actions: []
      });
    });
    render(SemanticIndexControl, { client: createAPIClient(fetchFn) });

    await fireEvent.click(screen.getByRole('button', { name: 'Rebuild semantic index' }));
    expect(screen.getByText('Start a full rebuild of the semantic index?')).toBeDefined();
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(requests).toHaveLength(0);

    await fireEvent.click(screen.getByRole('button', { name: 'Rebuild semantic index' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm full rebuild' }));
    expect(await screen.findByText('Semantic index: 100% of 2 items.')).toBeDefined();

    const cliRequest = requests.find((request) => pathOf(request.url).endsWith('/cli/run'));
    await expect(cliRequest?.clone().json()).resolves.toEqual({
      args: ['embeddings', 'build', '--full-rebuild', '--yes']
    });
  });

  it('surfaces a streamed build failure', async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      if (pathOf(input instanceof Request ? input.url : String(input)).endsWith('/cli/run')) {
        return new Response([
          JSON.stringify({ type: 'stdout', data: 'starting\n' }),
          JSON.stringify({ type: 'error', error: 'embedding endpoint failed' })
        ].join('\n') + '\n', { headers: { 'Content-Type': 'application/x-ndjson' } });
      }
      return Response.json({});
    });
    render(SemanticIndexControl, { client: createAPIClient(fetchFn) });

    await fireEvent.click(screen.getByRole('button', { name: 'Rebuild semantic index' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm full rebuild' }));
    expect((await screen.findByRole('alert')).textContent).toBe('embedding endpoint failed');
  });
});
