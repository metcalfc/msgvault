import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import MeetingPage from './MeetingPage.svelte';
import MeetingsWorkspace from './MeetingsWorkspace.svelte';

const when = '2026-09-20T15:00:00Z';

function meetingRow(id: number, type: 'calendar_event' | 'meeting_transcript', title: string) {
  return {
    key: `message:${id}`, kind: 'message', message_type: type, conversation_type: 'meeting', title, preview: '',
    occurred_at: when, source_id: 3, source_identifier: 'archive@example.test', source_type: 'synthetic',
    participant_labels: ['Ada Example', 'Bo Example'], participant_ids: [21, 31], attachment_count: 0,
    attachment_size: 0, has_attachments: false, deleted_from_source: false, message_count: 1,
    anchor_message_id: id, match: {}, matched_sender_identities: [], matched_recipient_identities: [],
  };
}

describe('Meetings workspace', () => {
  it('lists events and transcripts newest first within the window and the chosen person and account', async () => {
    const bodies: Array<{ filters: Array<{ dimension: string; values: string[] }> }> = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/explore') {
        bodies.push(await request.clone().json());
        return Response.json({
          rows: [meetingRow(77, 'meeting_transcript', 'Planning review'), meetingRow(76, 'calendar_event', 'Weekly sync')],
          total_count: 2, cache_revision: 'c', search_provenance: {},
        });
      }
      if (path === '/api/v1/sources/status') return Response.json({ sources: [] });
      return Response.json({}, { status: 404 });
    });
    const onOpenMeeting = vi.fn();
    const onFiltersChange = vi.fn();
    const view = render(MeetingsWorkspace, {
      client: createAPIClient(fetchFn), person: '21', source: '3', since: '30d', onFiltersChange, onOpenMeeting,
    });

    const results = await screen.findByRole('region', { name: 'Meeting results' });
    await waitFor(() => expect(within(results).getAllByRole('link')).toHaveLength(2));
    const [transcript, event] = within(results).getAllByRole('link');
    expect(transcript!.textContent).toContain('Transcript');
    expect(event!.textContent).toContain('Event');
    expect(transcript!.getAttribute('href')).toBe('/meetings/77');

    const filters = bodies[0]!.filters;
    expect(filters).toContainEqual({ dimension: 'message_type', values: ['calendar_event', 'meeting_transcript'] });
    expect(filters).toContainEqual({ dimension: 'participant', values: ['21'] });
    expect(filters).toContainEqual({ dimension: 'source', values: ['3'] });
    const after = Date.parse(filters.find((filter) => filter.dimension === 'after')!.values[0]!);
    expect(Math.round((Date.now() - after) / 86_400_000)).toBe(30);

    await fireEvent.click(transcript!);
    expect(onOpenMeeting).toHaveBeenCalledWith(77);

    await view.rerender({ client: createAPIClient(fetchFn), person: '', source: '', since: 'all', onFiltersChange, onOpenMeeting });
    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1]!.filters.some((filter) => filter.dimension === 'after')).toBe(false);
  });

  it('shows a transcript with its action items', async () => {
    const actionRequests: unknown[] = [];
    const transcript = {
      id: 77, source_id: 3, source_message_id: 'source-77', conversation_id: 90, subject: 'Planning review',
      message_type: 'meeting_transcript', from: 'Ada Example <ada@example.test>', to: ['bo@example.test'],
      sent_at: when, snippet: '', labels: [], has_attachments: false, size_bytes: 10,
      body: 'Ada: we ship Friday.', attachments: [],
    };
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/messages/77') return Response.json(transcript);
      if (path === '/api/v1/conversations/90') {
        return Response.json({ id: 90, anchor_id: 77, messages: [transcript], has_before: false, has_after: false, total: 1 });
      }
      if (path === '/api/v1/meetings/actions') {
        actionRequests.push(await request.clone().json());
        return Response.json({
          archive_uid: 'a', schema_version: 1, total_count: 0, rows: [],
          scope: { kind: 'direct' }, coverage: { meeting_count: 1, available: 1, partial: 0, unsupported: 0, unavailable: 0 },
        });
      }
      return Response.json({}, { status: 404 });
    });
    const onTitle = vi.fn();
    render(MeetingPage, { client: createAPIClient(fetchFn), meetingID: 77, onBack: vi.fn(), onTitle });

    expect(await screen.findByRole('heading', { level: 1, name: 'Planning review' })).toBeDefined();
    expect(screen.getByText(/^Transcript/)).toBeDefined();
    expect(await screen.findByRole('region', { name: 'Action items' })).toBeDefined();
    await waitFor(() => expect(actionRequests[0]).toMatchObject({ scope: { message_ids: [77] } }));
    expect(onTitle).toHaveBeenCalledWith('Planning review');
  });
});
