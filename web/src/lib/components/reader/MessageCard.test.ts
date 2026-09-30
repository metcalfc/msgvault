import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import type { ArchiveMessageDetail } from '../../archive/types';
import MessageCard from './MessageCard.svelte';

function detail(overrides: Partial<ArchiveMessageDetail> = {}): ArchiveMessageDetail {
  return {
    id: 42,
    conversationId: 7,
    subject: 'Quarterly plan',
    sender: 'Alice Example <alice@example.com>',
    recipients: ['bob@example.com'],
    sentAt: '2026-01-01T12:00:00Z',
    snippet: 'Plan preview',
    body: 'Plain text fallback.',
    attachments: [],
    ...overrides
  };
}

describe('MessageCard', () => {
  it('renders a calendar event description that carries markup through the sanitized frame, not a <pre>', async () => {
    const { container } = render(MessageCard, {
      props: {
        message: detail({
          messageType: 'calendar_event',
          body: '<p>Agenda: <b>planning</b></p><script>alert(1)</script><a href="https://example.test/join">Join</a>'
        }),
        expanded: true
      }
    });

    expect(container.querySelector('pre')).toBeNull();
    const frame = await waitFor(() => {
      const element = container.querySelector<HTMLIFrameElement>('iframe[title="Event description"]');
      expect(element).not.toBeNull();
      return element!;
    });
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts');
    expect(frame.getAttribute('srcdoc')).toContain('planning');
    expect(frame.getAttribute('srcdoc')).not.toContain('alert(1)');
    expect(container.textContent).not.toContain('<p>');
  });

  it('keeps plain calendar descriptions as text and plain-text view mode in a <pre>', () => {
    const plain = render(MessageCard, {
      props: { message: detail({ messageType: 'calendar_event', body: 'Bring the deck' }), expanded: true }
    });
    expect(plain.container.querySelector('.event-description')?.textContent).toBe('Bring the deck');
    plain.unmount();

    // Angle-bracketed addresses and entities in prose are not markup: the
    // newlines and the bracketed text must survive.
    const prose = render(MessageCard, {
      props: { message: detail({ messageType: 'calendar_event', body: 'Ask <alice@example.test>\nabout R&amp;D' }), expanded: true }
    });
    expect(prose.container.querySelector('iframe')).toBeNull();
    expect(prose.container.querySelector('.event-description')?.textContent).toBe('Ask <alice@example.test>\nabout R&amp;D');
    prose.unmount();

    const text = render(MessageCard, {
      props: { message: detail({ messageType: 'calendar_event', body: '<p>Agenda</p>' }), expanded: true, viewMode: 'text' }
    });
    expect(text.container.querySelector('pre')?.textContent).toBe('<p>Agenda</p>');
  });

  it('shows a calendar event as when, where, organizer, and attendees above its description', () => {
    render(MessageCard, {
      props: {
        message: detail({
          messageType: 'calendar_event',
          subject: 'Planning review',
          // The event's start instant; the When line gives only its length.
          sentAt: new Date(2026, 6, 18, 10, 0).toISOString(),
          from: 'Alice Example <alice@example.com>',
          recipients: ['Bob Example <bob@example.com>', 'casey@example.com'],
          body: 'Planning review\nWhen: 2026-07-18 10:00 - 2026-07-18 10:30\nLocation: Room 4 https://meet.example.com/abc\nBring the deck\nAttendees: Bob Example'
        }),
        expanded: true
      }
    });

    const card = screen.getByRole('region', { name: 'Event details' });
    const facts = [...card.querySelectorAll('dt')].map((term) => term.textContent);
    expect(facts).toEqual(['When', 'Where', 'Organizer', 'Attendees']);
    expect(card.querySelector('dd')?.textContent).toMatch(/Jul 18, 2026 · .*10:00.*–.*10:30/);
    expect(screen.getByRole('link', { name: 'https://meet.example.com/abc' }).getAttribute('rel')).toBe('noopener noreferrer');
    expect(screen.getByRole('button', { name: 'Alice Example (alice@example.com): person actions' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'Bob Example (bob@example.com): person actions' })).toBeDefined();
    expect(card.querySelector('.event-description')?.textContent).toBe('Bring the deck');
  });

  it('offers Join meeting and Open in Calendar for allowlisted event links', () => {
    render(MessageCard, {
      props: {
        message: detail({
          messageType: 'calendar_event', body: 'Planning review',
          eventLinks: {
            joinURL: 'https://meet.google.com/abc-defg-hij',
            calendarURL: 'https://www.google.com/calendar/event?eid=abc'
          }
        }),
        expanded: true
      }
    });

    const join = screen.getByRole('link', { name: 'Join meeting (opens in new tab)' });
    expect(join.getAttribute('href')).toBe('https://meet.google.com/abc-defg-hij');
    expect(join.getAttribute('target')).toBe('_blank');
    expect(join.getAttribute('rel')).toBe('noopener noreferrer');
    const calendar = screen.getByRole('link', { name: 'Open in Calendar (opens in new tab)' });
    expect(calendar.getAttribute('href')).toBe('https://www.google.com/calendar/event?eid=abc');
    expect(calendar.getAttribute('rel')).toBe('noopener noreferrer');
  });

  it('hides event links to hosts outside the allowlist', () => {
    render(MessageCard, {
      props: {
        message: detail({
          messageType: 'calendar_event', body: 'Planning review',
          eventLinks: { joinURL: 'https://meet.evil.example/abc', calendarURL: 'https://calendar.evil.example/e' }
        }),
        expanded: true
      }
    });

    expect(screen.queryByRole('link', { name: /Join meeting/ })).toBeNull();
    expect(screen.queryByRole('link', { name: /Open in Calendar/ })).toBeNull();
  });

  it('collapses to one line of sender, snippet, and date that expands on click', async () => {
    const onToggle = vi.fn();
    render(MessageCard, {
      props: { message: detail(), expanded: false, onToggle }
    });

    const collapsed = screen.getByRole('button', {
      name: 'Expand message 42 from Alice Example <alice@example.com>'
    });
    expect(collapsed.textContent).toContain('Alice Example');
    expect(collapsed.textContent).toContain('Plan preview');
    expect(collapsed.getAttribute('aria-expanded')).toBe('false');

    await fireEvent.click(collapsed);
    expect(onToggle).toHaveBeenCalledWith(42);
  });

  it('never offers remote images for a message the daemon marks as junk or trash', async () => {
    const html = '<img src="https://images.example/pixel.png" alt="Pixel">';
    for (const message of [
      detail({ bodyHtml: html, remoteImagesBlocked: true, labels: ['Corbeille'] }),
      detail({ bodyHtml: html, labels: ['Junk Email'] }),
    ]) {
      const { unmount } = render(MessageCard, { props: { message, expanded: true } });
      expect(await screen.findByText(/Remote images never load for spam or trash\./)).toBeDefined();
      expect(screen.queryByRole('button', { name: /remote image/ })).toBeNull();
      unmount();
    }
  });

  it('renders the expanded header directly: sender, recipients, date, subject, then the body', async () => {
    const { container } = render(MessageCard, {
      props: { message: detail({ bodyHtml: '<p>Formatted body</p>' }), expanded: true, anchor: true }
    });

    const card = screen.getByRole('article', { name: 'Message 42' });
    expect(card.getAttribute('aria-current')).toBe('true');
    expect(card.textContent).toContain('Alice Example <alice@example.com>');
    expect(card.textContent).toContain('to bob@example.com');
    expect(card.textContent).toContain('Quarterly plan');
    await waitFor(() => expect(container.querySelector('iframe')).not.toBeNull());
    // Body renders without any frame chrome to click through.
    expect(screen.queryByRole('button', { name: /Enter archived content/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /HTML/ })).toBeNull();
  });

  it('collapses again from the expanded header', async () => {
    const onToggle = vi.fn();
    render(MessageCard, {
      props: { message: detail(), expanded: true, onToggle }
    });

    await fireEvent.click(screen.getByRole('button', {
      name: 'Collapse message 42 from Alice Example <alice@example.com>'
    }));
    expect(onToggle).toHaveBeenCalledWith(42);
  });

  it('renders plain text on the theme surface when text mode is selected', () => {
    const { container } = render(MessageCard, {
      props: { message: detail({ bodyHtml: '<p>Formatted body</p>' }), expanded: true, viewMode: 'text' }
    });

    expect(container.querySelector('pre')?.textContent).toBe('Plain text fallback.');
    expect(container.querySelector('iframe')).toBeNull();
  });

  it('offers plain text through a small overflow control, defaulting to HTML', async () => {
    const onViewModeChange = vi.fn();
    render(MessageCard, {
      props: {
        message: detail({ bodyHtml: '<p>Formatted body</p>' }),
        expanded: true,
        onViewModeChange
      }
    });

    await fireEvent.click(screen.getByText('⋯'));
    await fireEvent.click(screen.getByRole('button', { name: 'Show plain text' }));
    expect(onViewModeChange).toHaveBeenCalledWith(42, 'text');
  });

  it('switches the overflow control back to HTML from text mode', async () => {
    const onViewModeChange = vi.fn();
    render(MessageCard, {
      props: {
        message: detail({ bodyHtml: '<p>Formatted body</p>' }),
        expanded: true,
        viewMode: 'text',
        onViewModeChange
      }
    });

    await fireEvent.click(screen.getByText('⋯'));
    await fireEvent.click(screen.getByRole('button', { name: 'Show formatted HTML' }));
    expect(onViewModeChange).toHaveBeenCalledWith(42, 'html');
  });

  it('sanitizes framed HTML and blocks sender-controlled network requests', async () => {
    const { container } = render(MessageCard, {
      props: {
        message: detail({
          bodyHtml:
            '<script>parent.postMessage("stolen", "*")</script>' +
            '<img src="https://tracking.example/pixel.png">' +
            '<a href="//tracking.example/click">Open</a>' +
            '<p style="background:url(https://tracking.example/bg.png)">Safe text</p>'
        }),
        expanded: true
      }
    });

    await waitFor(() => expect(container.querySelector('iframe')?.getAttribute('srcdoc')).toContain('Safe text'));
    const srcdoc = container.querySelector('iframe')?.getAttribute('srcdoc') ?? '';
    expect(srcdoc).toContain('Content-Security-Policy');
    expect(srcdoc).toContain("default-src 'none'");
    // The only script is the same-origin static bridge; no inline script or
    // style survives into the archived document.
    expect(srcdoc.match(/<script\b/g)).toHaveLength(1);
    expect(srcdoc).toContain('src="http://localhost:3000/archived-frame.js"');
    expect(srcdoc).not.toMatch(/<script>|<style>/);
    expect(srcdoc).not.toContain('stolen');
    expect(srcdoc).not.toContain('tracking.example');
  });

  it('removes SVG URL attributes that can bypass HTML URL filtering', async () => {
    const { container } = render(MessageCard, {
      props: {
        message: detail({
          bodyHtml:
            '<svg><a xlink:href="https://tracking.example/click"><text>Open</text></a></svg>'
        }),
        expanded: true
      }
    });

    await waitFor(() => expect(container.querySelector('iframe')?.getAttribute('srcdoc')).toContain('Open'));
    const srcdoc = container.querySelector('iframe')?.getAttribute('srcdoc') ?? '';
    expect(srcdoc).toContain('Open');
    expect(srcdoc).not.toContain('tracking.example');
  });

  it('falls back to plain text when HTML contains only whitespace', () => {
    const { container } = render(MessageCard, {
      props: { message: detail({ bodyHtml: ' \n\t ' }), expanded: true }
    });

    expect(container.querySelector('pre')?.textContent).toBe('Plain text fallback.');
    expect(container.querySelector('iframe')).toBeNull();
    expect(screen.queryByText('⋯')).toBeNull();
  });

  it('shows a loading state instead of the body while an omitted body is fetched', () => {
    const { container } = render(MessageCard, {
      props: { message: detail({ body: '' }), expanded: true, bodyPending: true }
    });

    expect(screen.getByRole('status').textContent).toContain('Loading message');
    expect(container.querySelector('pre')).toBeNull();
    expect(container.querySelector('iframe')).toBeNull();
  });

  it('shows the body fetch error in place of the body', () => {
    const { container } = render(MessageCard, {
      props: { message: detail({ body: '' }), expanded: true, bodyError: 'Could not load message body' }
    });

    expect(screen.getByRole('alert').textContent).toContain('Could not load message body');
    expect(container.querySelector('pre')).toBeNull();
  });

  it('falls back to plain text when HTML is rejected by sanitization', () => {
    const { container } = render(MessageCard, {
      props: {
        message: detail({ bodyHtml: '<p>Unsafe body</p>' }),
        expanded: true,
        sanitizationFailed: true
      }
    });

    expect(screen.getByRole('alert').textContent).toMatch(/could not render HTML/i);
    expect(container.querySelector('pre')).not.toBeNull();
    expect(container.querySelector('iframe')).toBeNull();
    expect(screen.queryByText('⋯')).toBeNull();
  });
});

describe('MessageCard reader chrome', () => {
  it('lists attachments under the body and opens one in the file viewer', async () => {
    const onOpenAttachment = vi.fn();
    render(MessageCard, {
      props: {
        message: detail({ attachments: [{ id: 91, filename: 'plan.pdf', mimeType: 'application/pdf', sizeBytes: 2048 }] }),
        expanded: true,
        onOpenAttachment
      }
    });

    const button = screen.getByRole('button', { name: /plan\.pdf/ });
    expect(button.textContent).toContain('2 KB');
    await fireEvent.click(button);
    expect(onOpenAttachment).toHaveBeenCalledWith({
      id: 91, message_id: 42, conversation_id: 7, filename: 'plan.pdf', mime_type: 'application/pdf', size_bytes: 2048,
      sender: 'Alice Example <alice@example.com>', containing_title: 'Quarterly plan', occurred_at: '2026-01-01T12:00:00Z'
    });
  });

  it('shows participants as pills and links a Gmail message to Gmail', async () => {
    render(MessageCard, {
      props: {
        message: detail({
          from: 'Alice Example <alice@example.com>', cc: ['Casey Example <casey@example.com>'],
          sourceMessageId: '18c2f0a1b2c3d4e5'
        }),
        expanded: true,
        sourceType: 'gmail',
        sourceIdentifier: 'archive+work@example.com'
      }
    });

    expect(screen.getByRole('button', { name: 'Alice Example (alice@example.com): person actions' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'bob@example.com: person actions' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'Casey Example (casey@example.com): person actions' })).toBeDefined();
    expect(screen.getByRole('link', { name: 'Open in Gmail' }).getAttribute('href'))
      .toBe('https://mail.google.com/mail/?authuser=archive%2Bwork%40example.com#all/18c2f0a1b2c3d4e5');
  });

  it('offers no Gmail link for other sources', () => {
    render(MessageCard, { props: { message: detail({ sourceMessageId: '18c2f0a1b2c3d4e5' }), expanded: true, sourceType: 'imap' } });
    expect(screen.queryByRole('link', { name: 'Open in Gmail' })).toBeNull();
  });
});
