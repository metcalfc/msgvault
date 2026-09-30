import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';

import { exploreLink } from '../../src/test/explore-url';
import { installMixedArchive } from './fixtures/mixed-archive';

type ExploreBody = { query?: string; search_mode?: string; filters?: { dimension: string; values: string[] }[] };

const TYPED = 'invoices from Ana Example last week';
const QUESTION = 'what did the landlord say about the deposit';

/** Serves query understanding from a table and records every Explore
 * request the page makes. */
async function installSuggestions(page: Page): Promise<{ explore: ExploreBody[]; understood: string[] }> {
  const fixture = await installMixedArchive(page);
  const explore: ExploreBody[] = [];
  const understood: string[] = [];
  await page.route('**/api/v1/explore', (route) => {
    const body = route.request().postDataJSON() as ExploreBody;
    explore.push(body);
    if (body.query === QUESTION) {
      return route.fulfill({ json: { rows: [], total_count: 0, cache_revision: 'mixed-100k', search_provenance: {} } });
    }
    return route.fulfill({ json: fixture.firstPage });
  });
  await page.route('**/api/v1/explore/query-understanding', (route) => {
    const body = route.request().postDataJSON() as { query: string; timezone?: string };
    understood.push(body.query);
    if (body.query === QUESTION) {
      return route.fulfill({ json: {
        status: 'judged', suggestions: [], natural_language: 0.91, offer_hybrid: true, elapsed_ms: 240,
      } });
    }
    return route.fulfill({ json: {
      status: 'judged', model: 'jev-1.13.0', offer_hybrid: false, elapsed_ms: 310, natural_language: 0.2,
      suggestions: [
        {
          kind: 'time_window', label: 'Past 7 days (Sep 24 to Sep 30, 2026)', span: 'last week', probability: 0.88,
          filters: [
            { dimension: 'after', values: ['2026-09-24T00:00:00Z'] },
            { dimension: 'before', values: ['2026-09-30T23:59:59.999Z'] },
          ],
          query_operators: [],
        },
        {
          kind: 'person', label: 'With Ana Example', span: 'from Ana Example', probability: 0.93,
          filters: [{ dimension: 'participant', values: ['12'] }], query_operators: [],
        },
      ],
    } });
  });
  return { explore, understood };
}

async function assertNoViolations(page: Page, label: string) {
  const result = await new AxeBuilder({ page }).analyze();
  expect(result.violations, `${label}: ${result.violations.map((v) => `${v.id}: ${v.help}`).join('; ')}`)
    .toEqual([]);
}

async function search(page: Page, query: string) {
  const searchbox = page.getByRole('searchbox', { name: 'Search messages', exact: true });
  await page.getByRole('radio', { name: 'Full text' }).click();
  await searchbox.fill(query);
  await searchbox.press('Enter');
}

test('a typed query offers suggested filters that apply and remove their text', async ({ page }) => {
  const captured = await installSuggestions(page);
  await page.goto(exploreLink({ workspace: 'everything' }));
  await expect(page.getByRole('main', { name: /^(Inbox|Search)$/ })).toBeVisible();
  expect(captured.understood, 'a restored or browsed view asks nothing').toEqual([]);

  await search(page, TYPED);
  const group = page.getByRole('group', { name: 'Suggested filters' });
  await expect(group).toBeVisible();
  await expect(page.getByRole('status').filter({ hasText: '2 suggested filters for this search' })).toHaveCount(1);
  expect(captured.understood).toEqual([TYPED]);
  await assertNoViolations(page, 'Suggested filters');

  await group.getByRole('button', {
    name: 'Apply suggested time period filter: Past 7 days (Sep 24 to Sep 30, 2026), replacing “last week” in the search',
  }).click();
  await expect.poll(() => captured.explore.at(-1)?.query).toBe('invoices from Ana Example');
  const applied = captured.explore.at(-1)!;
  expect(applied.filters?.filter((filter) => filter.dimension === 'after' || filter.dimension === 'before')).toEqual([
    { dimension: 'after', values: ['2026-09-24T00:00:00Z'] },
    { dimension: 'before', values: ['2026-09-30T23:59:59.999Z'] },
  ]);
  await expect(page.getByRole('searchbox', { name: 'Search messages', exact: true })).toHaveValue('invoices from Ana Example');
  // The other suggestion stays offered for the rewritten query.
  await expect(group.getByRole('button', { name: /With Ana Example/ })).toBeVisible();
  expect(captured.understood, 'applying a suggestion is not a typed query').toEqual([TYPED]);

  await group.getByRole('button', { name: /With Ana Example/ }).click();
  await expect.poll(() => captured.explore.at(-1)?.query).toBe('invoices');
  expect(captured.explore.at(-1)!.filters).toContainEqual({ dimension: 'participant', values: ['12'] });
  await expect(page.getByRole('group', { name: 'Suggested filters' })).toHaveCount(0);
});

test('an empty full-text search for a question offers hybrid search', async ({ page }) => {
  await installSuggestions(page);
  await page.goto(exploreLink({ workspace: 'everything' }));
  await search(page, QUESTION);
  const offer = page.getByRole('button', { name: 'Search by meaning with hybrid search' });
  await expect(offer).toBeVisible();
  await assertNoViolations(page, 'Hybrid offer');
  await offer.click();
  await expect(page.getByRole('radio', { name: 'Hybrid' })).toHaveAttribute('aria-checked', 'true');
});
