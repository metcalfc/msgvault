import { expect, test } from '@playwright/test';

import { exploreLink } from '../src/test/explore-url';

const baseState = {
  schemaVersion: 1,
  workspace: 'everything',
  query: 'alpha',
  searchMode: 'semantic',
  filters: [],
  groupingChain: [],
  presentation: 'table',
  sort: [{ field: 'occurred_at', direction: 'desc' }],
  columns: ['kind', 'people', 'title', 'excerpt', 'time', 'attachments'],
  columnWidths: {},
  activeRow: null,
  selectedRow: null,
  inspectorPinned: false,
  conversationAnchor: null,
  scrollAnchor: null
};

test('URL mode overrides the remembered default and remains selected through a named semantic failure', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('msgvault-search-mode', 'hybrid'));
  await page.route('**/api/session', (route) =>
    route.fulfill({ json: { auth_mode: 'loopback', https: false, plain_http_warning: false } })
  );
  await page.route('**/api/v1/search/coverage', (route) => route.fulfill({ json: {
    status: 'incomplete', eligible_count: 2, embedded_count: 1, percentage: 50,
    vector_generation: 7, cache_revision: 'cache-1', actions: []
  } }));
  await page.route('**/api/v1/explore', (route) => route.fulfill({
    status: 503,
    json: { error: 'vector_initializing', message: 'Vector search is still building.' }
  }));

  // An app link stamps an explicit mode; drop it so the remembered one applies.
  const withoutMode = new URL(exploreLink({ workspace: 'everything' }), 'http://localhost');
  withoutMode.searchParams.delete('mode');
  await page.goto(`${withoutMode.pathname}${withoutMode.search}`);
  await expect(page.getByRole('radio', { name: 'Hybrid' })).toHaveAttribute('aria-checked', 'true');

  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify(baseState))}`);
  await expect(page.getByRole('radio', { name: 'Full text' })).toBeVisible();
  await expect(page.getByRole('radio', { name: 'Semantic' })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('radio', { name: 'Hybrid' })).toBeVisible();
  // Coverage folds into the one-line result status; the full sentence sits behind Details.
  await expect(page.getByRole('status', { name: 'Result status' })).toContainText('semantic index 50%');
  await page.getByText('Details', { exact: true }).click();
  await expect(page.getByText(/Semantic index: 50% of 2 items/)).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('Vector search is still building.');
  await expect(page.getByRole('radio', { name: 'Semantic' })).toHaveAttribute('aria-checked', 'true');
});
