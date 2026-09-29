import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';

import { installMixedArchive } from './fixtures/mixed-archive';
import { openPersonFromPeople } from '../kit-ui';

function directoryURL(personID?: number, tab?: string): string {
  return personID === undefined ? '/people' : `/people/${personID}${tab ? `/${tab}` : ''}`;
}

async function expectNoAxeViolations(page: Page, label: string): Promise<void> {
  const result = await new AxeBuilder({ page }).analyze();
  expect(result.violations, `${label}: ${result.violations.map((violation) => violation.id).join(', ')}`)
    .toEqual([]);
}

/** A person's maintenance cards live on the Maintenance tab, and the
 * tracking card keeps its eligible-fields catalogue behind "What can be
 * maintained?". */
async function openProfileMaintenance(root: Page | ReturnType<Page['getByRole']>) {
  await root.getByRole('tab', { name: 'Maintenance' }).click();
  const maintenance = root.getByRole('region', { name: 'Profile maintenance' });
  await expect(maintenance).toBeVisible();
  await maintenance.getByText('What can be maintained?').click();
  return maintenance;
}

async function installTallDirectory(page: Page): Promise<void> {
  await page.unroute('**/api/v1/people/directory*');
  await page.route('**/api/v1/people/directory*', (route) => route.fulfill({
    json: {
      people: Array.from({ length: 40 }, (_, index) => ({
        id: index === 0 ? 42 : 100 + index,
        revision: 1,
        display_name: index === 0 ? 'Archive Person' : `Synthetic Person ${index}`,
        primary_channel: 'email',
        contact_state: 'active',
        categories: [],
        organizations: []
      }))
    }
  }));
}

test('People lists durable people, opens one person page, and scopes Files to the durable person', async ({ page }) => {
  const requests: string[] = [];
  page.on('request', (request) => requests.push(new URL(request.url()).pathname));
  await installMixedArchive(page);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(directoryURL());

  await expect(page.getByRole('main', { name: 'People' })).toBeVisible();
  const row = page.getByRole('region', { name: 'People results' }).getByRole('link', { name: /Archive Person/ }).first();
  await expect(row).toBeVisible();
  await expectNoAxeViolations(page, 'People list');

  await row.click();
  await expect(page).toHaveURL(/\/people\/(\d+|contact-\d+)$/);
  await expect(page.getByRole('heading', { name: 'Archive Person' })).toBeVisible();

  await page.goto(directoryURL(42));
  await expect(page.getByRole('main', { name: 'Person' })).toBeVisible();
  const overview = page.getByRole('tab', { name: 'Overview' });
  const files = page.getByRole('tab', { name: 'Files' });
  await overview.focus();
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('ArrowRight');
  await expect(files).toBeFocused();
  await expect(files).toHaveAttribute('aria-selected', 'true');
  await expect(page).toHaveURL(/\/people\/42\/files$/);
  await expect(page.getByRole('tabpanel', { name: 'Files' })).toBeVisible();
  await expect(page.getByRole('grid', { name: 'Files results' }).getByText('durable-person.pdf')).toBeVisible();
  expect(requests).toContain('/api/v1/people/42/files/search');
  expect(requests).not.toContain('/api/v1/participants/42/files/search');
  expect(requests).not.toContain('/api/v1/files/search');
  await expectNoAxeViolations(page, 'Person files');
});

test('A person page scrolls on its own below a fixed header', async ({ page }) => {
  await installMixedArchive(page);
  await installTallDirectory(page);
  await page.setViewportSize({ width: 1280, height: 600 });
  await page.goto(directoryURL(42, 'maintenance'));
  const person = page.getByRole('main', { name: 'Person' });
  await expect(person.getByRole('heading', { name: 'Archive Person' })).toBeVisible();
  await expect(person.getByRole('region', { name: 'Person merge history' }).or(person.getByRole('heading', { name: 'Merge history' }))).toBeVisible();
  const metrics = await person.evaluate((el) => ({
    scroll: el.scrollHeight,
    client: el.clientHeight,
    overflow: getComputedStyle(el).overflowY
  }));
  expect(metrics.scroll).toBeGreaterThan(metrics.client);
  expect(metrics.overflow).toBe('auto');
  const header = page.getByRole('banner');
  const before = await header.boundingBox();
  await person.evaluate((el) => el.scrollTo(0, el.scrollHeight));
  expect(await person.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
  expect((await header.boundingBox())?.y).toBe(before?.y);
});

test('A narrow person page fits the viewport and returns to People', async ({ page }) => {
  await installMixedArchive(page);
  await page.setViewportSize({ width: 640, height: 900 });
  await page.goto(directoryURL(42));

  const person = page.getByRole('main', { name: 'Person' });
  await expect(person.getByRole('tabpanel', { name: 'Overview' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await expectNoAxeViolations(page, 'Narrow person page');
  await page.getByRole('button', { name: 'Back to People' }).click();
  await expect(page.getByRole('main', { name: 'People' })).toBeVisible();
});

test('Directory profile maintenance uses exact safe requests and GET-only ambiguity recovery', async ({ page }) => {
  const archive = await installMixedArchive(page);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(directoryURL(42));

  const maintenance = await openProfileMaintenance(page);
  await expect(maintenance).toContainText('Time zone');
  await expectNoAxeViolations(page, 'Directory profile maintenance desktop');
  const forbidden = /forbidden-/i;
  expect(await page.locator('body').innerHTML()).not.toMatch(forbidden);

  const toggle = maintenance.getByRole('switch', { name: 'Track this person for profile maintenance' });
  await toggle.focus();
  await page.keyboard.press('Space');
  await expect(toggle).toBeChecked();
  await expect(toggle).toBeFocused();
  await expect(page.getByRole('status', { name: 'Operation status' })).toContainText('Profile maintenance tracking enabled.');

  archive.failNextTrackingMutation();
  archive.failNextTrackingRead();
  await page.keyboard.press('Space');
  const retry = maintenance.getByRole('button', { name: 'Retry profile maintenance state' });
  await expect(retry).toBeVisible();
  await retry.focus();
  await page.keyboard.press('Enter');
  await expect(toggle).not.toBeChecked();
  await expect(toggle).toBeEnabled();
  await expect(toggle).toBeFocused();

  const reveal = maintenance.getByRole('button', { name: 'Show sensitive eligible fields' });
  await reveal.focus();
  await page.keyboard.press('Enter');
  await expect(maintenance.getByText('Private note')).toBeVisible();
  await expect(maintenance.getByText('Sensitive', { exact: true })).toBeVisible();
  expect(await page.locator('body').innerHTML()).not.toMatch(forbidden);

  expect(archive.trackingRequests).toEqual([
    { method: 'GET', path: '/api/v1/people/42/tracking' },
    { method: 'GET', path: '/api/v1/person-fact-targets', includeSensitive: false },
    { method: 'PUT', path: '/api/v1/people/42/tracking', tracked: true },
    { method: 'PUT', path: '/api/v1/people/42/tracking', tracked: false },
    { method: 'GET', path: '/api/v1/people/42/tracking' },
    { method: 'GET', path: '/api/v1/people/42/tracking' },
    { method: 'GET', path: '/api/v1/person-fact-targets', includeSensitive: true }
  ]);

  await page.setViewportSize({ width: 390, height: 844 });
  // The same page reflows at phone width; the revealed fields stay revealed.
  await expect(maintenance.getByText('Time zone')).toBeVisible();
  await expect(maintenance.getByText('Private note')).toBeVisible();
  const targetCards = maintenance.locator('li');
  const first = await targetCards.nth(0).boundingBox();
  const second = await targetCards.nth(1).boundingBox();
  expect(first).not.toBeNull();
  expect(second).not.toBeNull();
  expect(second!.y).toBeGreaterThanOrEqual(first!.y + first!.height - 1);
  await expectNoAxeViolations(page, 'Directory profile maintenance narrow');
});

test('Directory retains rows after an invalid cursor and reloads page one', async ({ page }) => {
  await installMixedArchive(page);
  await page.unroute('**/api/v1/people/directory*');
  let requests = 0;
  await page.route('**/api/v1/people/directory*', (route) => {
    requests += 1;
    const cursor = new URL(route.request().url()).searchParams.get('cursor');
    if (cursor) return route.fulfill({ status: 400, json: {
      error: 'invalid_cursor', message: 'Synthetic Directory changed while paging.'
    } });
    return route.fulfill({ json: requests === 1 ? {
      people: [{
        id: 42, revision: 1, display_name: 'Retained Person', primary_channel: 'email',
        contact_state: 'active', categories: [], organizations: []
      }], next_cursor: 'invalidated'
    } : {
      people: [{
        id: 43, revision: 1, display_name: 'Reloaded Person', primary_channel: 'email',
        contact_state: 'active', categories: [], organizations: []
      }]
    } });
  });
  await page.goto(directoryURL());

  await expect(page.getByText('Retained Person')).toBeVisible();
  await page.getByRole('button', { name: 'Load more people' }).click();
  await expect(page.getByRole('alert')).toContainText('Synthetic Directory changed while paging.');
  await expect(page.getByText('Retained Person')).toBeVisible();
  await page.getByRole('button', { name: 'Reload people' }).click();
  await expect(page.getByText('Reloaded Person')).toBeVisible();
  await expect(page.getByText('Retained Person')).toBeHidden();
});

test('Relationships promotes its selected participant and opens the returned person in Directory', async ({ page }) => {
  await installMixedArchive(page);
  await page.goto('/');

  await page.getByRole('button', { name: 'Not saved' }).click();
  await openPersonFromPeople(page, 'Archive Person');
  await expect(page.getByRole('heading', { name: 'Archive Person' })).toBeVisible();
  await expect(page.getByRole('button', { name: /^Open contact record for / })).toHaveCount(0);

  const promotionRequest = page.waitForRequest((request) =>
    new URL(request.url()).pathname === '/api/v1/people' && request.method() === 'POST'
  );
  await page.getByRole('button', { name: 'Save to Directory' }).click();
  expect((await promotionRequest).postDataJSON()).toEqual({ participant_id: 12 });
  await expect(page).toHaveURL(/\/people\/\d+(\?|$)/);
  await expect(page.getByRole('main', { name: 'Person' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Archive Person' })).toBeVisible();
});

test('Relationships keeps a promotion conflict beside the person instead of opening Directory', async ({ page }) => {
  await installMixedArchive(page);
  await page.goto('/');
  await page.getByRole('button', { name: 'Not saved' }).click();
  await openPersonFromPeople(page, 'Archive Person');
  await expect(page.getByRole('heading', { name: 'Archive Person' })).toBeVisible();

  await page.route('**/api/v1/people', (route) => route.fulfill({
    status: 409,
    json: { error: 'person_binding_conflict', message: 'Synthetic promotion conflict.' }
  }));
  await page.getByRole('button', { name: 'Save to Directory' }).click();
  await expect(page.getByRole('alert').filter({ hasText: 'Synthetic promotion conflict.' })).toBeVisible();
  await expect(page.getByRole('main', { name: 'Person' })).toHaveCount(0);
  await expect(page).not.toHaveURL(/\/people\/\d+(\?|$)/);
});
