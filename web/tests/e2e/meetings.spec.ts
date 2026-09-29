import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';
import { selectKitTopBarTab } from '../kit-ui';

const when = new Date(Date.now() - 3 * 86_400_000).toISOString();

const event = {
  id: 76, source_id: 3, source_message_id: 'source-76', conversation_id: 89, subject: 'Weekly sync',
  message_type: 'calendar_event', from: 'Ada Example <ada@example.test>', to: ['Bo Example <bo@example.test>'],
  sent_at: when, snippet: '', labels: [], has_attachments: false, size_bytes: 10,
  body: `When: ${when}\nWhere: Room 4`, attachments: [],
};

function row(message: typeof event) {
  return {
    key: `message:${message.id}`, kind: 'message', message_type: message.message_type, conversation_type: 'meeting',
    title: message.subject, preview: '', occurred_at: message.sent_at, source_id: 3,
    source_identifier: 'archive@example.test', source_type: 'synthetic', participant_labels: ['Ada Example', 'Bo Example'],
    participant_ids: [21, 31], attachment_count: 0, attachment_size: 0, has_attachments: false,
    deleted_from_source: false, message_count: 1, anchor_message_id: message.id, match: {},
  };
}

async function install(page: Page): Promise<void> {
  await page.route('**/api/session', (route) => route.fulfill({ json: {
    auth_mode: 'loopback', https: false, plain_http_warning: false,
  } }));
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = (body: unknown) => route.fulfill({ json: body });
    if (path === '/api/v1/settings') return json({ settings: [], pending_restart: false });
    if (path === '/api/v1/explore') return json({ rows: [row(event)], total_count: 1, cache_revision: 'c', search_provenance: {} });
    if (path === '/api/v1/sources/status') return json({ sources: [] });
    if (path === '/api/v1/messages/76') return json(event);
    if (path === '/api/v1/conversations/89') {
      return json({ id: 89, anchor_id: 76, messages: [event], has_before: false, has_after: false, total: 1 });
    }
    return route.fulfill({ status: 404, json: { error: 'not_found', message: 'Not in this fixture' } });
  });
}

test('Meetings lists recent meetings and opens one on its own page', async ({ page }) => {
  await install(page);
  await page.goto('/people');
  await selectKitTopBarTab(page, 'Meetings');
  await expect(page).toHaveURL(/\/meetings$/);
  await expect(page).toHaveTitle('Meetings · msgvault');
  const results = page.getByRole('region', { name: 'Meeting results' });
  await expect(results.getByRole('link', { name: /Weekly sync/ })).toBeVisible();
  const axe = await new AxeBuilder({ page }).analyze();
  expect(axe.violations.map((violation) => violation.id)).toEqual([]);

  await results.getByRole('link', { name: /Weekly sync/ }).click();
  await expect(page).toHaveURL(/\/meetings\/76$/);
  await expect(page.getByRole('heading', { level: 1, name: 'Weekly sync' })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Event details' })).toBeVisible();
  await expect(page).toHaveTitle('Weekly sync · msgvault');

  await page.getByRole('button', { name: 'Back to Meetings' }).click();
  await expect(page).toHaveURL(/\/meetings$/);
  await page.goForward();
  await expect(page.getByRole('heading', { level: 1, name: 'Weekly sync' })).toBeVisible();
});
