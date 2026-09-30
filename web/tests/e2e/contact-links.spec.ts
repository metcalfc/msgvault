import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';

import { installMixedArchive } from './fixtures/mixed-archive';

const FIXTURE_TIME = '2026-01-01T00:00:00Z';

async function expectNoAxeViolations(page: Page, label: string, include?: string): Promise<void> {
  const builder = new AxeBuilder({ page });
  const result = await (include ? builder.include(include) : builder).analyze();
  expect(result.violations, `${label}: ${result.violations.map((violation) => violation.id).join(', ')}`)
    .toEqual([]);
}

function envelope(id: number) {
  return { id, ordinal: 0, source: 'user', created_at: FIXTURE_TIME, updated_at: FIXTURE_TIME, vcard: {} };
}

/** Records every anchor activation and cancels it, so mailto:/tel: never
 * reach an external handler and the page never navigates away. */
async function captureAnchorActivations(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const seen: Array<{ href: string; target: string; rel: string }> = [];
    (window as unknown as { __activatedLinks: typeof seen }).__activatedLinks = seen;
    document.addEventListener('click', (event) => {
      const anchor = (event.target as Element | null)?.closest?.('a');
      if (!anchor) return;
      seen.push({ href: anchor.getAttribute('href') ?? '', target: anchor.target, rel: anchor.rel });
      event.preventDefault();
    }, true);
  });
}

async function activatedLinks(page: Page): Promise<Array<{ href: string; target: string; rel: string }>> {
  return page.evaluate(() => (window as unknown as { __activatedLinks: Array<{ href: string; target: string; rel: string }> }).__activatedLinks);
}

test('a person page links email, E.164 phone, and profile contact methods', async ({ page }) => {
  await installMixedArchive(page);
  await page.unroute('**/api/v1/people/42/profile');
  await page.route('**/api/v1/people/42/profile', (route) => route.fulfill({
    json: {
      person: {
        id: 42, revision: 1, display_name: 'Archive Person', participant_ids: [12],
        vcard_uid: 'urn:uuid:archive-person', created_at: FIXTURE_TIME, updated_at: FIXTURE_TIME
      },
      names: [],
      contact_points: [
        { person_id: 42, address_kind: 'phone', original_value: '+1 555 555 0123', normalized_value: '+15555550123',
          normalization: 'phone_e164', normalization_version: 1, envelope: envelope(501) },
        { person_id: 42, address_kind: 'phone', original_value: '555-0199', normalized_value: '555-0199',
          normalization: 'none', normalization_version: 1, envelope: envelope(502) },
        { person_id: 42, address_kind: 'username', original_value: '@Example-Person', normalized_value: 'example-person',
          normalization: 'strip_at_lower', normalization_version: 1, service_slug: 'github',
          profile_url_template: 'https://github.com/{username}', envelope: envelope(503) },
        { person_id: 42, address_kind: 'url', original_value: 'javascript:alert(1)', normalized_value: 'javascript:alert(1)',
          normalization: 'none', normalization_version: 1, envelope: envelope(504) }
      ],
      addresses: [], dates: [], categories: [], media: []
    }
  }));
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto('/people/42');

  const methods = page.getByRole('list', { name: 'Contact methods' });
  await expect(methods.getByRole('link', { name: 'person@example.com' })).toHaveAttribute('href', 'mailto:person@example.com');
  const phone = methods.getByRole('link', { name: '+1 555 555 0123' });
  await expect(phone).toHaveAttribute('href', 'tel:+15555550123');
  await expect(phone).not.toHaveAttribute('target', /.+/);
  await expect(methods.getByText('555-0199')).toBeVisible();
  await expect(methods.getByRole('link', { name: '555-0199' })).toHaveCount(0);

  const profile = methods.getByRole('link', { name: '@Example-Person (opens in new tab)' });
  await expect(profile).toHaveAttribute('href', 'https://github.com/example-person');
  await expect(profile).toHaveAttribute('target', '_blank');
  await expect(profile).toHaveAttribute('rel', 'noopener noreferrer');

  await expect(methods.getByText('javascript:alert(1)')).toBeVisible();
  await expect(page.locator('a[href^="javascript:"]')).toHaveCount(0);
  await expectNoAxeViolations(page, 'Person contact links');
});

test('the reader offers Email and Call on participants and meeting links on events', async ({ page }) => {
  await captureAnchorActivations(page);
  const message = {
    id: 42002, source_id: 3, source_message_id: 'event-source', conversation_id: 72,
    subject: 'Planning review', message_type: 'calendar_event',
    from: 'Organizer Example <organizer@example.com>', to: ['+15555550123', 'guest@example.com'],
    sent_at: '2026-07-18T10:00:00Z', snippet: 'Planning review', labels: [], has_attachments: false,
    size_bytes: 20, body: 'Planning review\nBring the deck', attachments: [],
    event_links: {
      join_url: 'https://meet.google.com/abc-defg-hij',
      calendar_url: 'https://www.google.com/calendar/event?eid=abc'
    }
  };
  await page.route('**/api/session', (route) => route.fulfill({ json: {
    auth_mode: 'loopback', https: false, plain_http_warning: false
  } }));
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/v1/settings') return route.fulfill({ json: { settings: [], pending_restart: false } });
    if (url.pathname === '/api/v1/messages/42002') return route.fulfill({ json: message });
    if (url.pathname === '/api/v1/conversations/72') {
      return route.fulfill({ json: { id: 72, anchor_id: 42002, messages: [message], has_before: false, has_after: false, total: 1 } });
    }
    return route.fulfill({ status: 404, json: { message: 'Unexpected archive request' } });
  });
  await page.goto('/messages/42002');

  const card = page.getByRole('region', { name: 'Event details' });
  const join = card.getByRole('link', { name: 'Join meeting (opens in new tab)' });
  await expect(join).toHaveAttribute('href', 'https://meet.google.com/abc-defg-hij');
  await expect(join).toHaveAttribute('target', '_blank');
  await expect(join).toHaveAttribute('rel', 'noopener noreferrer');
  const calendar = card.getByRole('link', { name: 'Open in Calendar (opens in new tab)' });
  await expect(calendar).toHaveAttribute('href', 'https://www.google.com/calendar/event?eid=abc');
  await expect(calendar).toHaveAttribute('rel', 'noopener noreferrer');
  // Scoped to the event card: the reader header's sender avatar is outside
  // this change.
  await expectNoAxeViolations(page, 'Event links', '[aria-label="Event details"]');

  await card.getByRole('button', { name: 'Organizer Example (organizer@example.com): person actions' }).click();
  await expect(page.getByRole('menuitem', { name: 'Call' })).toHaveCount(0);
  await page.getByRole('menuitem', { name: 'Email' }).click();

  await card.getByRole('button', { name: '+15555550123: person actions' }).click();
  await expect(page.getByRole('menuitem', { name: 'Email' })).toHaveCount(0);
  await page.getByRole('menuitem', { name: 'Call' }).click();

  expect(await activatedLinks(page)).toEqual([
    { href: 'mailto:organizer@example.com', target: '', rel: '' },
    { href: 'tel:+15555550123', target: '', rel: '' }
  ]);
});
