import { expect, test, type Page } from '@playwright/test';

const message = {
  id: 42001, source_id: 3, source_message_id: 'source-message', conversation_id: 71,
  subject: 'Routed message', message_type: 'email', from: 'sender@example.com',
  to: ['reader@example.com'], sent_at: '2020-01-01T12:00:00Z', snippet: 'Routed',
  labels: [], has_attachments: false, size_bytes: 20, body: 'The routed message body', attachments: [],
};

/** A daemon that answers the session, settings, and one message; every
 * other archive read is empty, which is all routing needs. */
async function installDaemon(page: Page): Promise<void> {
  await page.route('**/api/session', (route) => route.fulfill({ json: {
    auth_mode: 'loopback', https: false, plain_http_warning: false,
  } }));
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/v1/settings') {
      return route.fulfill({ json: {
        settings: [
          { key: 'web.theme', group: 'browser', label: 'Theme', kind: 'enum', options: ['system', 'light', 'dark'], restart_required: false, value: { string: 'light' } },
          { key: 'vector.enabled', group: 'search', label: 'Semantic search', kind: 'boolean', restart_required: false, value: { boolean: false } },
        ],
        groups: [
          { id: 'browser', label: 'Appearance', description: 'How the web app looks.' },
          { id: 'search', label: 'Search', description: 'Search settings.' },
        ],
        pending_restart: false,
      } });
    }
    if (url.pathname === '/api/v1/messages/42001') return route.fulfill({ json: message });
    if (url.pathname === '/api/v1/conversations/71') {
      return route.fulfill({ json: { id: 71, anchor_id: 42001, messages: [message], has_before: false, has_after: false, total: 1 } });
    }
    if (url.pathname === '/api/v1/people/directory') return route.fulfill({ json: { people: [] } });
    return route.fulfill({ status: 404, json: { error: 'not_found', message: 'Not in this fixture' } });
  });
}

test.beforeEach(async ({ page }) => installDaemon(page));

test('legacy workspace links open the same view at its readable path', async ({ page }) => {
  const payload = encodeURIComponent(JSON.stringify({ schemaVersion: 2, operationLane: 'contacts' }));
  await page.goto(`/?workspace=operations&mode=full_text&explore=${payload}`);
  await expect(page).toHaveURL(/\/activity\/operations\?explore=/);
  expect(JSON.parse(new URL(page.url()).searchParams.get('explore')!)).toMatchObject({ operationLane: 'contacts' });

  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({ workspace: 'directory_review', reviewKind: 'relationship' }))}`);
  await expect(page).toHaveURL(/\/reviews\?explore=/);

  await page.goto('/?workspace=everything&mode=full_text');
  await expect(page).toHaveURL(/\/inbox\?since=7d$/);
  await expect(page).toHaveTitle(/^Inbox · msgvault$/);
});

test('Back and Forward walk readable paths and restore each view', async ({ page }) => {
  await page.goto('/settings/search');
  await expect(page.getByRole('heading', { name: 'Search', level: 2 })).toBeVisible();
  await expect(page).toHaveTitle('Settings · msgvault');
  await page.goto('/files');
  await expect(page).toHaveTitle('Files · msgvault');
  await page.goBack();
  await expect(page).toHaveURL(/\/settings\/search$/);
  await expect(page.getByRole('heading', { name: 'Search', level: 2 })).toBeVisible();
  await page.goForward();
  await expect(page).toHaveURL(/\/files$/);
});

test('a message link opens inside the app shell and Back leaves for the Inbox', async ({ page }) => {
  await page.goto('/messages/42001');
  await expect(page.getByRole('article', { name: 'Message 42001' })).toContainText('The routed message body');
  await expect(page.getByRole('banner')).toBeVisible();
  await expect(page).toHaveTitle('Routed message · msgvault');
  await page.getByRole('button', { name: 'Back' }).click();
  await expect(page).toHaveURL(/\/inbox/);
});
