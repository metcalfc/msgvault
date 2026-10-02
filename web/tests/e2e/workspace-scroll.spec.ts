import { expect, test, type Locator, type Page } from '@playwright/test';
import { installDirectoryReviewArchive } from './fixtures/mixed-archive';
import { installOperations } from './fixtures/operations';

/**
 * The app shell fills the viewport and clips its own overflow, so every
 * workspace must scroll its content in a container of its own. Each case
 * opens a routed workspace at a small viewport with more content than fits,
 * points at the content the way a mouse would, and proves that the wheel
 * moves the container under the pointer.
 */

const SMALL = { width: 1024, height: 600 };
const NARROW = { width: 390, height: 600 };
const WHEN = '2026-01-03T12:00:00Z';

/** Longer lists than the shared fixture serves, so every list overflows. */
async function installLongLists(page: Page, recentRows: unknown[]): Promise<void> {
  await page.route('**/api/v1/relationships/12/timeline', (route) => route.fulfill({ json: {
    canonical_id: 12, identity_revision: 1, cache_revision: 'mixed-100k', rows: recentRows, total_count: recentRows.length,
  } }));
  const range = (count: number) => Array.from({ length: count }, (_, index) => index + 1);
  await page.route('**/api/v1/people/directory*', (route) => route.fulfill({ json: {
    people: range(60).map((index) => ({
      id: 1000 + index, revision: 1, display_name: `Synthetic Person ${index}`, primary_channel: 'email',
      contact_state: 'active', categories: [], organizations: [],
    })),
  } }));
  await page.route('**/api/v1/domains/search', (route) => route.fulfill({ json: {
    rows: range(60).map((index) => ({
      domain: `example-${index}.test`, activity_count: 3, person_count: 1, file_count: 0,
      source_counts: [{ source_type: 'synthetic', count: 3 }], first_at: WHEN, last_at: WHEN,
      cache_revision: 'mixed-100k',
    })),
    total_count: 60, cache_revision: 'mixed-100k', search_provenance: {},
  } }));
  await page.route('**/api/v1/files/search', (route) => route.fulfill({ json: {
    files: range(80).map((index) => ({
      id: 5000 + index, key: `file:${5000 + index}`, entry_key: 'message:100001', message_id: 100001,
      conversation_id: 100001, occurred_at: WHEN, source_id: 1, source_type: 'synthetic',
      source_identifier: 'archive@example.com', containing_title: 'Synthetic email',
      filename: `synthetic-${index}.txt`, mime_type: 'text/plain', mime_family: 'text', size_bytes: 2048,
      content_state: 'unsupported', content_available: true,
    })),
    total_count: 80, cache_revision: 'mixed-100k', search_provenance: {},
  } }));
  await page.route('**/api/v1/saved-views', (route) => route.fulfill({ json: {
    saved_views: range(30).map((index) => ({
      id: index, name: `Synthetic view ${index}`, description: 'Synthetic saved view', schema_version: 1,
      revision: 1, created_at: WHEN, updated_at: WHEN,
      canonical_state: { query: `view ${index}`, search_mode: 'full_text', filters: [], grouping: [],
        presentation: 'table', sort: [{ field: 'occurred_at', direction: 'desc' }],
        columns: ['kind', 'title'], inspector_pinned: false },
    })),
  } }));
  await page.route('**/api/v1/sources/status', (route) => route.fulfill({ json: {
    sources: range(30).map((index) => ({
      id: index, source_type: 'gmail', identifier: `archive-${index}@example.com`, display_name: `Archive ${index}`,
      last_sync_at: null, next_sync_at: null, updated_at: WHEN, active_sync: null, latest_sync: null,
      last_successful_sync: null, scheduled: false, can_sync: true,
    })),
  } }));
  await page.route('**/api/v1/relationships/12/calendar', (route) => route.fulfill({ json: {
    participant_id: 12, canonical_id: 12, year: 2026, timezone: 'UTC',
    days: [{ date: '2026-01-03', sent: 1, received: 1, email: 2, chat: 0, meetings: 0, total: 2,
      modality_mask: 1, level: 'FOURTH_QUARTILE' }],
    current: { temperature: 62, rank: 1, population: 1, raw_score: 3,
      signals: { sent_signal: 1, received_volume: 1, meeting_signal: 0, modalities: 1 } },
    annual: [], peak_temperature: 62, peak_year: 2026, scoring_timezone: 'UTC',
    score_version: 1, effective_date: '2026-01-03', cache_revision: 'mixed-100k', identity_revision: 1,
  } }));
  await page.route('**/api/v1/deletions', (route) => route.fulfill({ json: {
    manifests: range(30).map((index) => ({
      id: `batch-${index}`, status: 'pending', created_at: WHEN, created_by: 'api',
      description: `Synthetic reviewed selection ${index}`, message_count: 1,
    })),
  } }));
}

/**
 * Finds the scroll container under a point inside the workspace, asserts it
 * holds more content than it shows, and scrolls it with the mouse wheel.
 * Returns the container's selector for further checks.
 */
async function expectWheelScrolls(page: Page, label: string, content?: Locator): Promise<string> {
  const target = content ?? page.getByRole('main').first();
  await expect(target).toBeVisible();
  const box = await target.boundingBox();
  const viewport = page.viewportSize();
  if (!box || !viewport) throw new Error(`${label}: workspace has no layout box`);
  // The middle of the part of the content the reader can see.
  const x = box.x + box.width / 2;
  const y = (Math.max(box.y, 0) + Math.min(box.y + box.height, viewport.height)) / 2;
  const measured = await page.evaluate(({ x, y }) => {
    for (const marked of document.querySelectorAll('[data-scroll-probe]')) marked.removeAttribute('data-scroll-probe');
    for (let element = document.elementFromPoint(x, y) as HTMLElement | null; element; element = element.parentElement) {
      const overflow = getComputedStyle(element).overflowY;
      if ((overflow === 'auto' || overflow === 'scroll') && element.scrollHeight > element.clientHeight) {
        element.setAttribute('data-scroll-probe', '');
        return { scrollHeight: element.scrollHeight, clientHeight: element.clientHeight };
      }
    }
    return null;
  }, { x, y });
  expect(measured, `${label}: content under the pointer has no scroll container that overflows`).not.toBeNull();
  expect(measured!.scrollHeight, `${label}: scroll container holds more than it shows`).toBeGreaterThan(measured!.clientHeight);
  const container = page.locator('[data-scroll-probe]');
  const before = await container.evaluate((element) => element.scrollTop);
  await page.mouse.move(x, y);
  await page.mouse.wheel(0, 400);
  await expect.poll(() => container.evaluate((element) => element.scrollTop), {
    message: `${label}: the wheel scrolls the workspace`,
  }).toBeGreaterThan(before);
  return '[data-scroll-probe]';
}

const archiveWorkspaces: Array<{
  label: string;
  path: string;
  ready: (page: Page) => Promise<void>;
  /** The scrolling content, where the workspace is split into panes. */
  content?: (page: Page) => Locator;
}> = [
  { label: 'People', path: '/people', ready: (page) => expect(page.getByText('Synthetic Person 1', { exact: true })).toBeVisible() },
  { label: 'Person profile', path: '/people/42/profile', ready: (page) => expect(page.getByRole('heading', { name: 'Archive Person' })).toBeVisible() },
  { label: 'Person maintenance', path: '/people/42/maintenance', ready: (page) => expect(page.getByRole('region', { name: 'Profile maintenance' })).toBeVisible() },
  {
    label: 'Contact overview', path: '/people/contact-12',
    ready: (page) => expect(page.getByRole('region', { name: 'Relationship activity calendar' })).toBeVisible(),
  },
  {
    label: 'Domains', path: '/people/domains',
    ready: (page) => expect(page.getByText('example-1.test', { exact: true })).toBeVisible(),
    content: (page) => page.getByRole('grid', { name: 'Relationship results' }),
  },
  { label: 'Inbox', path: '/inbox', ready: (page) => expect(page.getByRole('grid', { name: 'Message results' }).locator('[data-row-key]').first()).toBeVisible() },
  { label: 'Files', path: '/files', ready: (page) => expect(page.getByText('synthetic-1.txt', { exact: true })).toBeVisible() },
  { label: 'Meetings', path: '/meetings', ready: (page) => expect(page.getByRole('region', { name: 'Meeting results' })).toBeVisible() },
  { label: 'Saved Views', path: '/saved-views', ready: (page) => expect(page.getByText('Synthetic view 1', { exact: true })).toBeVisible() },
  { label: 'Sources', path: '/activity/sources', ready: (page) => expect(page.getByText('Archive 1', { exact: true })).toBeVisible() },
  { label: 'Deletions', path: '/activity/deletions', ready: (page) => expect(page.getByText('Synthetic reviewed selection 1', { exact: true })).toBeVisible() },
  { label: 'Settings', path: '/settings', ready: (page) => expect(page.getByRole('main', { name: 'Settings' })).toBeVisible() },
];

test.describe('every routed workspace scrolls content taller than the viewport', () => {
  test.use({ viewport: SMALL });

  for (const workspace of archiveWorkspaces) {
    test(workspace.label, async ({ page }) => {
      const { archive } = await installDirectoryReviewArchive(page);
      await installLongLists(page, archive.logicalRows.slice(0, 12));
      await page.goto(workspace.path);
      await workspace.ready(page);
      await expectWheelScrolls(page, workspace.label, workspace.content?.(page));
    });
  }

  test('Operations', async ({ page }) => {
    // The fixture's page fits in 1024x600 with Linux CI fonts, so a shorter
    // viewport guarantees overflow regardless of font metrics.
    await page.setViewportSize({ width: SMALL.width, height: 420 });
    await installOperations(page);
    await page.goto('/activity/operations');
    await expect(page.getByRole('button', { name: 'Open Document extraction run' })).toBeVisible();
    await expectWheelScrolls(page, 'Operations');
  });

  test('Keyboard shortcuts dialog', async ({ page }) => {
    await installDirectoryReviewArchive(page);
    await page.goto('/people');
    await expect(page.getByRole('main', { name: 'People' })).toBeVisible();
    await page.keyboard.press('Shift+/');
    const dialog = page.getByRole('dialog', { name: 'Keyboard shortcuts' });
    await expectWheelScrolls(page, 'Keyboard shortcuts dialog', dialog);
  });

  for (const viewport of [SMALL, NARROW]) {
    test(`Reviews at ${viewport.width}x${viewport.height}`, async ({ page }) => {
      await page.setViewportSize(viewport);
      await installDirectoryReviewArchive(page);
      await page.goto('/reviews');
      const candidates = page.getByRole('article', { name: /^Identity match \d+$/ });
      await expect(candidates.first()).toBeVisible();
      const container = page.locator(await expectWheelScrolls(page, 'Reviews'));

      // The keyboard scrolls the same container once focus is in the page.
      await container.evaluate((element) => { element.scrollTop = 0; });
      await page.getByRole('heading', { level: 1, name: 'Reviews' }).click();
      await page.keyboard.press('PageDown');
      await expect.poll(() => container.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
      const afterPageDown = await container.evaluate((element) => element.scrollTop);
      await page.keyboard.press('Space');
      await expect.poll(() => container.evaluate((element) => element.scrollTop)).toBeGreaterThan(afterPageDown);

      // Every queued item is reachable.
      await container.evaluate((element) => { element.scrollTop = element.scrollHeight; });
      await expect(candidates.last()).toBeInViewport();
    });
  }
});
