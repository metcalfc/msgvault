import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import ParticipantPill from './ParticipantPill.svelte';

const match = {
  cache_revision: 'c',
  rows: [{ participant_id: 55, display_label: 'Bob Example', kind: 'email', source: 'observed', value: 'bob@example.com' }]
};

async function choose(item: string): Promise<void> {
  await fireEvent.click(screen.getByRole('button', { name: 'bob@example.com: person actions' }));
  await fireEvent.click(await screen.findByRole('menuitem', { name: item }));
}

describe('ParticipantPill', () => {
  it('shares one pending lookup between quick repeat clicks', async () => {
    let release: (response: Response) => void = () => undefined;
    const fetchFn = vi.fn<typeof fetch>(() => new Promise<Response>((resolve) => { release = resolve; }));
    const onOpenPerson = vi.fn();
    const onFilterPerson = vi.fn();
    render(ParticipantPill, { props: { value: 'bob@example.com', client: createAPIClient(fetchFn), onOpenPerson, onFilterPerson } });

    await choose('Open person');
    await choose('Filter by person');
    expect(fetchFn).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(/No person found/)).toBeNull();
    release(Response.json(match));
    await waitFor(() => expect(onOpenPerson).toHaveBeenCalledWith(55));
    await waitFor(() => expect(onFilterPerson).toHaveBeenCalledWith(55, 'bob@example.com'));
  });

  it('retries a failed lookup instead of remembering the failure', async () => {
    let calls = 0;
    const fetchFn = vi.fn<typeof fetch>(async () => {
      calls += 1;
      if (calls === 1) return Response.json({ error: 'internal_error', message: 'boom' }, { status: 500 });
      return Response.json(match);
    });
    const onOpenPerson = vi.fn();
    render(ParticipantPill, { props: { value: 'bob@example.com', client: createAPIClient(fetchFn), onOpenPerson } });

    await choose('Open person');
    expect(await screen.findByText('Could not look up bob@example.com. Try again.')).toBeDefined();
    expect(onOpenPerson).not.toHaveBeenCalled();
    await choose('Open person');
    await waitFor(() => expect(onOpenPerson).toHaveBeenCalledWith(55));
    expect(fetchFn).toHaveBeenCalledTimes(2);
  });

  it('offers Email for an address and follows a mailto: link', async () => {
    const openLink = vi.fn();
    render(ParticipantPill, { props: { value: 'Bob Example <bob@example.com>', openLink } });
    await fireEvent.click(screen.getByRole('button', { name: 'Bob Example (bob@example.com): person actions' }));
    expect(screen.queryByRole('menuitem', { name: 'Call' })).toBeNull();
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Email' }));
    expect(openLink).toHaveBeenCalledWith(expect.objectContaining({ href: 'mailto:bob@example.com', external: false }));
  });

  it('offers Call for an E.164 number and follows a tel: link', async () => {
    const openLink = vi.fn();
    render(ParticipantPill, { props: { value: '+15550100001', openLink } });
    await fireEvent.click(screen.getByRole('button', { name: '+15550100001: person actions' }));
    expect(screen.queryByRole('menuitem', { name: 'Email' })).toBeNull();
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Call' }));
    expect(openLink).toHaveBeenCalledWith(expect.objectContaining({ href: 'tel:+15550100001', external: false }));
  });

  it('offers neither for a value it cannot link', async () => {
    render(ParticipantPill, { props: { value: 'local-number 5550100' } });
    await fireEvent.click(screen.getByRole('button', { name: 'local-number 5550100: person actions' }));
    expect(await screen.findByRole('menuitem', { name: 'Copy number' })).toBeDefined();
    expect(screen.queryByRole('menuitem', { name: 'Email' })).toBeNull();
    expect(screen.queryByRole('menuitem', { name: 'Call' })).toBeNull();
  });
});

