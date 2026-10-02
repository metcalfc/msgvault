import { test, expect, loginToMeetingArchive } from './fixtures/meeting-daemon';

test.use({ seedReviews: true });

test('live identity acceptance and person merge survive refresh and profile navigation', async ({ page, daemon }) => {
  const reviews = daemon.reviews!;
  await loginToMeetingArchive(page, daemon);
  await page.goto(`${daemon.origin}/reviews`);
  const linked = page.getByRole('article', { name: `Identity match ${reviews.link_candidate_id}`, exact: true });
  const merged = page.getByRole('article', { name: `Identity match ${reviews.merge_candidate_id}`, exact: true });
  await expect(linked).toContainText('Avery Review');
  await expect(merged).toContainText('Morgan Survivor');
  await linked.getByRole('button', { name: 'Link identities', exact: true }).click();
  await expect(linked).toHaveCount(0);
  await expect(page).toHaveURL(/\/reviews/);
  await page.reload();
  await expect(linked).toHaveCount(0);
  await expect(merged).toBeVisible();

  const mergeResponse = page.waitForResponse(response => response.request().method() === 'POST' && /\/people\/\d+\/merge$/.test(new URL(response.url()).pathname));
  await merged.getByRole('button', { name: 'Add a note' }).click();
  await merged.getByRole('textbox', { name: 'Decision notes' }).fill('Reviewed the synthetic identity evidence');
  await merged.getByRole('button', { name: 'Link identities', exact: true }).click();
  const response = await mergeResponse;
  expect(response.status(), await response.text()).toBe(200);
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(merged).toHaveCount(0);
  const accepted = await page.request.get(`${daemon.origin}/api/v1/identity/match-candidates?state=accepted`);
  expect(accepted.ok()).toBe(true);
  expect((await accepted.json()).candidates).toEqual(expect.arrayContaining([
    expect.objectContaining({ id: reviews.link_candidate_id, state: 'accepted' }),
    expect.objectContaining({ id: reviews.merge_candidate_id, state: 'accepted', notes: 'Reviewed the synthetic identity evidence' }),
  ]));
  expect((await response.json()).person).toMatchObject({ id: reviews.survivor_id });
  await expect(page).toHaveURL(/\/reviews/);
  await page.getByRole('button', { name: 'Open Morgan Survivor profile', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/people/${reviews.survivor_id}(?:[/?]|$)`));
  await page.getByRole('tab', { name: 'Maintenance', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Merge history', exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Merge history', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: /Inspect merge/ })).toHaveCount(1);

  await page.goto(`${daemon.origin}/reviews`);
  await expect(page.getByText('No identity matches in this queue.', { exact: true })).toBeVisible();
});
