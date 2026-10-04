import { expect, test, type Page } from '@playwright/test';

import { FACT_LEDGER_FORBIDDEN_MARKERS, installDirectoryReviewArchive } from './e2e/fixtures/mixed-archive';
import { parseExploreURLState } from '../src/lib/explore/state.svelte';

// Browser boundary: these journeys exercise the built UI over Playwright-
// intercepted HTTP transport. Generated-client unit tests and API/store tests
// remain the proof for wire generation and real backend persistence semantics.
function reviewURL(
  reviewKind: 'identity' | 'fact' | 'relationship' = 'identity',
  identityState = 'candidate',
  directoryPersonID?: number
): string {
  return `/?explore=${encodeURIComponent(JSON.stringify({
    workspace: 'directory_review', reviewKind, identityState,
    relationshipReviewState: 'pending',
    ...(directoryPersonID === undefined ? {} : { directoryPersonID })
  }))}`;
}

/** Accepts identity match 19, which needs the two profiles merged first. */
async function startMerge(page: Page) {
  const card = page.getByRole('article', { name: 'Identity match 19' });
  await card.getByRole('button', { name: 'Link identities' }).focus();
  await page.keyboard.press('Enter');
  // Accepting applies at once and hands straight to the merge.
  await expect(page.getByRole('dialog', { name: 'Link identities' })).toHaveCount(0);
}

/** Makes the automatic merge fail so the choice between both profiles shows. */
async function openMergeChoice(page: Page) {
  const attempts: string[] = [];
  await page.route(/\/api\/v1\/people\/\d+\/merge$/, (route) => {
    attempts.push(new URL(route.request().url()).pathname);
    return route.fulfill({
      status: 409,
      json: { error: 'person_carddav_published', message: 'Synthetic profile is published to CardDAV.' },
    });
  });
  await startMerge(page);
  const merge = page.getByRole('dialog', { name: 'Resolve person merge' });
  await expect(merge).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await expect(merge.getByRole('alert')).toContainText('Synthetic profile is published to CardDAV.');
  await expect(merge).toContainText('revision 4');
  await expect(merge).toContainText('revision 2');
  await expect(merge).not.toContainText(/Person \d/);
  expect(attempts).toEqual(['/api/v1/people/7/merge']);
  return merge;
}

test('review cards expose the complete server-supplied synthetic evidence', async ({ page }) => {
  await installDirectoryReviewArchive(page);
  await page.goto(reviewURL());

  const card = page.getByRole('article', { name: 'Identity match 17' });
  await expect(card).toBeVisible();
  await expect(card).toContainText('Avery Candidate');
  await expect(card).toContainText('avery@example.com');
  await expect(card.getByRole('region', { name: 'Candidate endpoints for identity match 17' })).not.toContainText(/170|171/);
  await expect(card).toContainText('stable_provider_id');
  await expect(card).toContainText('synthetic-chat');
  await expect(card).toContainText('workspace / example-space');
  await expect(card).toContainText('0.82');
  await expect(card.getByRole('list', { name: 'Evidence for identity match 17' }))
    .toContainText('Both synthetic endpoints expose the same provider identifier.');
});

test('contacts that match the archive are named, explained, and linked from their own filter', async ({ page }) => {
  await installDirectoryReviewArchive(page);
  await page.goto(reviewURL());
  await expect(page.getByRole('article', { name: 'Identity match 17' })).toBeVisible();

  const listRequest = page.waitForRequest((request) =>
    request.method() === 'GET' &&
    new URL(request.url()).pathname.endsWith('/identity/match-candidates') &&
    new URL(request.url()).searchParams.get('origin') === 'contact_match');
  await page.getByRole('radio', { name: 'Contacts that match your archive' }).click();
  await listRequest;
  await expect(page).toHaveURL(/identityOrigin%22%3A%22contact_match/);
  await expect(page.getByRole('article', { name: 'Identity match 17' })).toHaveCount(0);

  const card = page.getByRole('article', { name: 'Identity match 25' });
  const endpoints = card.getByRole('region', { name: 'Candidate endpoints for identity match 25' });
  await expect(endpoints).toContainText('Archive identity');
  await expect(endpoints).toContainText('Ada Sender');
  await expect(endpoints).toContainText('Person profile');
  await expect(endpoints).toContainText('Ada Contact');
  await expect(endpoints).toContainText('+15550100100');
  await expect(card).toContainText('Accepting links this archive identity to the profile.');

  const accepted = page.waitForRequest((request) =>
    request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/25/accept'));
  await card.getByRole('button', { name: 'Link identities' }).click();
  await accepted;
  await expect(card).toBeHidden();
});

test('a shared mailbox is marked as not a person straight from its card menu and can be undone', async ({ page }) => {
  await installDirectoryReviewArchive(page);
  await page.goto(reviewURL());
  await page.getByRole('radio', { name: 'Contacts that match your archive' }).click();

  const card = page.getByRole('article', { name: 'Identity match 26' });
  const hint = card.getByRole('note');
  await expect(hint).toContainText('Looks like a shared mailbox');
  await expect(hint).toContainText('support@shop.example.test is a role address');
  await expect(hint).toContainText('Avery Stone, Blake Rivera');
  await expect(card.getByRole('button', { name: 'Link identities' })).toBeDisabled();

  await card.getByRole('button', { name: 'Not a person: identity match 26' }).click();
  const marked = page.waitForRequest((request) =>
    request.method() === 'PUT' && new URL(request.url()).pathname.endsWith('/identity/correspondent-kinds/260'));
  await page.getByRole('menuitem', { name: 'Shared mailbox' }).click();
  // The menu choice is the decision: no second dialog asks again.
  expect((await marked).postDataJSON()).toEqual({ kind: 'shared_mailbox' });
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(card).toBeHidden();
  await expect(page).toHaveURL(/\/reviews/);
  await expect(page.getByRole('article', { name: 'Identity match 25' })).toBeFocused();
  await expect(page.getByRole('status').filter({ hasText: 'Marked Shop Support as shared mailbox.' })).toBeVisible();

  const cleared = page.waitForRequest((request) =>
    request.method() === 'DELETE' && new URL(request.url()).pathname.endsWith('/identity/correspondent-kinds/260'));
  await page.getByRole('button', { name: 'Undo: Shop Support is a person' }).click();
  await cleared;
  await expect(page.getByRole('status').filter({ hasText: 'Undone: Shop Support is a person again.' })).toBeVisible();
  await expect(card).toBeVisible();
});

test('enrichment identities are shown with what the provider returned and decided explicitly', async ({ page }) => {
  await installDirectoryReviewArchive(page);
  await page.goto(reviewURL());
  await page.getByRole('radio', { name: 'Enrichment identities' }).click();
  await expect(page).toHaveURL(/reviewKind%22%3A%22enrichment/);

  const first = page.getByRole('article', { name: 'Synthetic One' });
  const returned = first.getByRole('region', { name: 'Returned identity for attempt 61' });
  await expect(returned).toContainText('S. One');
  await expect(returned).toContainText('Engineer at Example Labs');
  await expect(returned).toContainText('profiles.example.test');
  await expect(first).toContainText('70%');
  await expect(first).toContainText('99%');

  const confirmed = page.waitForRequest((request) =>
    request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/61/confirm'));
  await first.getByRole('button', { name: 'Confirm identity' }).click();
  await confirmed;
  await expect(page.getByRole('region', { name: 'Enrichment identities to confirm' }).getByRole('status'))
    .toContainText('Identity confirmed for Synthetic One');
  await expect(first).toBeHidden();

  const second = page.getByRole('article', { name: 'Synthetic Two' });
  await expect(second).toContainText('profiles.example.test');
  const rejected = page.waitForRequest((request) =>
    request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/62/reject'));
  await second.getByRole('button', { name: 'Not this person' }).click();
  await rejected;
  await expect(page.getByText('No enrichment identities to confirm.')).toBeVisible();
});

test('ordinary accept and reject keep keyboard focus connected as rows leave the queue', async ({ page }) => {
  await installDirectoryReviewArchive(page);
  await page.goto(reviewURL());

  const acceptCard = page.getByRole('article', { name: 'Identity match 17' });
  await expect(acceptCard.getByLabel('Decision notes')).toHaveCount(0);
  await acceptCard.getByRole('button', { name: 'Add a note' }).click();
  await expect(acceptCard.getByLabel('Decision notes')).toBeFocused();
  await acceptCard.getByLabel('Decision notes').fill('Synthetic provider IDs match.');
  const acceptedRequest = page.waitForRequest((request) =>
    request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/17/accept'));
  const acceptTrigger = acceptCard.getByRole('button', { name: 'Link identities' });
  await acceptTrigger.focus();
  await expect(acceptTrigger).toBeFocused();
  // One keypress decides; there is no confirmation dialog.
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect((await acceptedRequest).postDataJSON()).toEqual({ notes: 'Synthetic provider IDs match.' });
  await expect(acceptCard).toBeHidden();
  const rejectCard = page.getByRole('article', { name: 'Identity match 18' });
  await expect(rejectCard).toBeFocused();
  await rejectCard.getByRole('button', { name: 'Add a note' }).click();
  await rejectCard.getByLabel('Decision notes').fill('Synthetic endpoints belong to different people.');
  const rejectedRequest = page.waitForRequest((request) =>
    request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/18/reject'));
  const rejectTrigger = rejectCard.getByRole('button', { name: 'Keep separate' });
  await rejectTrigger.focus();
  await expect(rejectTrigger).toBeFocused();
  await page.keyboard.press('Enter');
  expect((await rejectedRequest).postDataJSON()).toEqual({ notes: 'Synthetic endpoints belong to different people.' });
  await expect(rejectCard).toBeHidden();
  await expect(page.getByRole('article', { name: 'Identity match 19' })).toBeFocused();
  await expect(page).toHaveURL(/\/reviews/);

  const waitingFilter = page.getByRole('radio', { name: 'Waiting' });
  await waitingFilter.focus();
  await expect(waitingFilter).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('radio', { name: 'Accepted' })).toBeFocused();
  await expect(page.getByRole('radio', { name: 'Accepted' })).toBeChecked();
  const acceptedCard = page.getByRole('article', { name: 'Identity match 17' });
  await expect(acceptedCard).toContainText('accepted');
  await expect(acceptedCard.getByRole('button', { name: /Link identities|Keep separate/ })).toHaveCount(0);
  await expect(page).toHaveURL(/identityState%22%3A%22accepted/);

  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('radio', { name: 'Rejected' })).toBeFocused();
  await expect(page.getByRole('radio', { name: 'Rejected' })).toBeChecked();
  const rejectedCard = page.getByRole('article', { name: 'Identity match 18' });
  await expect(rejectedCard).toContainText('rejected');
  await expect(rejectedCard).toContainText('Synthetic endpoints belong to different people.');
  await expect(page).toHaveURL(/identityState%22%3A%22rejected/);
});

test('a pending decision disables its card, while failure retains an explicit retry', async ({ page }) => {
  const fixture = await installDirectoryReviewArchive(page);
  const releaseAccept = fixture.holdNextDecision(17);
  await page.goto(reviewURL());

  const acceptCard = page.getByRole('article', { name: 'Identity match 17' });
  const acceptTrigger = acceptCard.getByRole('button', { name: 'Link identities' });
  await acceptTrigger.focus();
  await expect(acceptTrigger).toBeFocused();
  await page.keyboard.press('Enter');
  await expect.poll(() => fixture.requests.filter((request) =>
    request.method === 'POST' && request.path.endsWith('/17/accept')).length).toBe(1);
  await expect(acceptCard).toContainText('Decision pending…');
  await expect(acceptTrigger).toBeDisabled();
  await expect(acceptCard.getByRole('button', { name: 'Keep separate' })).toBeDisabled();
  releaseAccept();
  await expect(acceptCard).toBeHidden();
  await expect(page.getByRole('article', { name: 'Identity match 18' })).toBeFocused();

  fixture.failNextDecision(18, 'Synthetic decision service unavailable.');
  const rejectCard = page.getByRole('article', { name: 'Identity match 18' });
  await rejectCard.getByRole('button', { name: 'Add a note' }).click();
  const notes = rejectCard.getByLabel('Decision notes');
  await notes.fill('Retain this synthetic review note.');
  const rejectTrigger = rejectCard.getByRole('button', { name: 'Keep separate' });
  await rejectTrigger.focus();
  await expect(rejectTrigger).toBeFocused();
  await page.keyboard.press('Enter');

  await expect(page.getByRole('alert')).toContainText('Synthetic decision service unavailable.');
  await expect(notes).toHaveValue('Retain this synthetic review note.');
  expect(fixture.requests.filter((request) =>
    request.method === 'POST' && request.path.endsWith('/18/reject'))).toHaveLength(1);

  await rejectTrigger.focus();
  await expect(rejectTrigger).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(rejectCard).toBeHidden();
  expect(fixture.requests.filter((request) =>
    request.method === 'POST' && request.path.endsWith('/18/reject'))).toHaveLength(2);
});

for (const profile of [
  { id: 7, name: 'Synthetic One' },
  { id: 9, name: 'Synthetic Two' }
]) {
  test(`a failed automatic merge lets the user inspect ${profile.name} separately`, async ({ page }) => {
    const fixture = await installDirectoryReviewArchive(page);
    await page.goto(reviewURL());
    const merge = await openMergeChoice(page);

    await merge.getByRole('button', { name: `Open ${profile.name} profile` }).focus();
    await page.keyboard.press('Enter');

    await expect(page).toHaveURL(new RegExp(`/people/${profile.id}$`));
    await expect(page.getByRole('heading', { name: profile.name })).toBeVisible();
    // Merge history lives on the Maintenance tab.
    await page.getByRole('tab', { name: 'Maintenance' }).click();
    await expect(page.getByText('No merge history on this page.')).toBeVisible();
    await expect(page.getByRole('table', { name: 'Person merge history' })).toHaveCount(0);
    expect(fixture.requests.filter((request) => request.path.endsWith('/merge'))).toHaveLength(0);
    expect(fixture.requests.filter((request) => request.path.endsWith('/19/accept'))).toHaveLength(1);
  });
}

for (const completionTarget of [
  { id: 7, label: 'Open source profile Synthetic One', heading: 'Synthetic One' },
  { id: 19, label: 'Open restored profile Synthetic Restored', heading: 'Synthetic Restored' }
]) test(`merge keeps the queue, then partial split opens ${completionTarget.heading} with fresh server state`, async ({ page }) => {
  const fixture = await installDirectoryReviewArchive(page);
  await page.setViewportSize({ width: 1280, height: 1000 });
  await page.goto(reviewURL());
  // Same person merges on its own into the profile with more identities.
  await startMerge(page);

  // The queue stays open: the merged match leaves and focus moves on.
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page).toHaveURL(/\/reviews/);
  await expect(page.getByRole('article', { name: 'Identity match 19' })).toBeHidden();
  await expect(page.getByRole('article', { name: 'Identity match 25' })).toBeFocused();
  await expect(page.getByRole('status', { name: 'Operation status' }))
    .toContainText("People merged into Synthetic One. Undo it from Synthetic One's merge history.");
  expect(fixture.requests.filter((request) =>
    request.method === 'GET' && request.path === '/api/v1/people/7')).toHaveLength(0);

  // The kept profile opens only from its explicit link.
  await page.getByRole('button', { name: 'Open Synthetic One profile' }).click();
  await expect(page.getByRole('heading', { name: 'Synthetic One' })).toBeVisible();
  await expect.poll(() => fixture.requests.filter((request) =>
    request.method === 'GET' && request.path === '/api/v1/people/7').length).toBe(1);
  const mergeRequests = fixture.requests.filter((request) => request.path === '/api/v1/people/7/merge');
  expect(mergeRequests).toHaveLength(1);
  expect(mergeRequests[0]!.body).toEqual({ absorbed_person_id: 9 });
  expect(mergeRequests[0]!.headers['if-match']).toBe('"person-7-r4", "person-9-r2"');
  expect(mergeRequests[0]!.headers['idempotency-key'])
    .toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  expect(fixture.requests.filter((request) => request.path.endsWith('/19/accept'))).toHaveLength(1);

  await page.getByRole('tab', { name: 'Maintenance' }).click();
  const history = page.getByRole('table', { name: 'Person merge history' });
  await expect(history).toBeVisible();
  expect(fixture.requests.filter((request) => request.path.endsWith('/snapshot'))).toHaveLength(0);
  await history.getByRole('button', { name: 'Inspect merge 41' }).focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Merge 41 detail' })).toBeVisible();
  await expect(page.getByRole('table', { name: 'Merge participants' })).toContainText('absorbed');
  expect(fixture.requests.filter((request) => request.path.endsWith('/snapshot'))).toHaveLength(0);
  if (completionTarget.id === 7) {
    await page.getByRole('button', { name: 'View verified snapshot' }).focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('region', { name: 'Verified merge snapshot content' }))
      .toContainText('synthetic snapshot');
    expect(fixture.requests.filter((request) => request.path.endsWith('/snapshot'))).toHaveLength(1);
  }

  await page.getByRole('button', { name: 'Split merged profile' }).focus();
  await page.keyboard.press('Enter');
  const split = page.getByRole('dialog', { name: 'Split merged profile' });
  await expect(split).toContainText('The merge currently belongs to Synthetic One.');
  const create = split.getByRole('button', { name: 'Create restored person' });
  await expect(create).toBeDisabled();
  await split.getByRole('checkbox', { name: 'lineage@example.com' }).focus();
  await page.keyboard.press('Space');
  await split.getByRole('checkbox', { name: /I confirm splitting lineage@example.com from Synthetic One/ }).focus();
  await page.keyboard.press('Space');
  await create.focus();
  await page.keyboard.press('Enter');

  await expect(split.getByRole('heading', { name: 'Split completed' })).toBeVisible();
  await expect(split).toContainText('Partial restoration.');
  await expect(split.getByRole('button', { name: 'Open source profile Synthetic One' })).toBeVisible();
  await expect(split.getByRole('button', { name: 'Open restored profile Synthetic Restored' })).toBeVisible();
  await expect(split).toContainText('Ambiguous rows');
  const splitRequests = fixture.requests.filter((request) => request.path === '/api/v1/people/7/split');
  expect(splitRequests).toHaveLength(1);
  expect(splitRequests[0]!.body).toEqual({ merge_id: 41, participant_ids: [901] });
  expect(splitRequests[0]!.headers['if-match']).toBe('"person-7-r5"');
  expect(splitRequests[0]!.headers['idempotency-key'])
    .toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);

  const completionAction = split.getByRole('button', { name: completionTarget.label });
  const personGETsBefore = fixture.requests.filter((request) =>
    request.method === 'GET' && request.path === `/api/v1/people/${completionTarget.id}`).length;
  await completionAction.focus();
  await expect(completionAction).toBeFocused();
  await page.keyboard.press('Enter');
  await expect.poll(() => fixture.requests.filter((request) =>
    request.method === 'GET' && request.path === `/api/v1/people/${completionTarget.id}`).length)
    .toBe(personGETsBefore + 1);
  await expect(page.getByRole('heading', { name: completionTarget.heading })).toBeVisible();

  if (completionTarget.id === 7) {
    // The completion handoff opens the person on Overview.
    await page.getByRole('tab', { name: 'Maintenance' }).click();
    const updatedHistory = page.getByRole('table', { name: 'Person merge history' });
    await expect(updatedHistory).toBeVisible();
    await updatedHistory.getByRole('button', { name: 'Inspect merge 41' }).focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('table', { name: 'Prior splits' })).toContainText('55');
  }

  // The People list reflects the merge and the split.
  await page.getByRole('button', { name: 'Back to People' }).click();
  const people = page.getByRole('region', { name: 'People results' });
  await expect(people).toContainText('Synthetic One');
  await expect(people).toContainText('Synthetic Restored');
  await expect(people).not.toContainText('Synthetic Two');
});

test('Fact review is a keyboard-readable private ledger with honest unavailable gates', async ({ page }) => {
  const fixture = await installDirectoryReviewArchive(page);
  await page.goto(reviewURL());
  await expect(page.getByRole('article', { name: 'Identity match 17' })).toBeVisible();

  const identityReview = page.getByRole('radio', { name: 'Identity matches' });
  await identityReview.focus();
  await expect(identityReview).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('radio', { name: 'Fact review' })).toBeFocused();
  await expect(page.getByRole('radio', { name: 'Fact review' })).toBeChecked();
  const fact = page.getByRole('region', { name: 'Fact review' });
  await expect(fact).toContainText('Choose a person in Directory to inspect their fact ledger');
  expect(fixture.requests.filter((request) => request.path.includes('fact-'))).toHaveLength(0);

  await page.goto(reviewURL('fact', 'candidate', 42));
  await expect(fact).toContainText('Archive Person');
  await expect(fact).toContainText('Fact candidate decisions are unavailable until a generated candidate contract is installed.');
  await expect(fact).toContainText('A dated last-time-we-talked brief is unavailable until the server exposes a generated brief contract.');
  for (const action of ['Accept', 'Reject', 'Unsure', 'Run']) {
    await expect(fact.getByRole('button', { name: new RegExp(action, 'i') })).toHaveCount(0);
  }
  await expect.poll(() => fixture.requests.filter((request) => request.path.includes('fact')).length).toBe(5);
  const initial = fixture.requests.filter((request) => request.path.includes('fact'));
  expect(initial.map((request) => request.path).sort()).toEqual([
    '/api/v1/people/42/fact-claims', '/api/v1/people/42/fact-decisions',
    '/api/v1/people/42/fact-evidence', '/api/v1/people/42/fact-pins', '/api/v1/person-fact-targets'
  ]);
  expect(initial.find((request) => request.path.endsWith('/person-fact-targets'))?.query).toEqual({ include_sensitive: 'true' });

  const targetPicker = fact.getByRole('combobox', { name: /^Fact target:/ });
  await targetPicker.focus();
  await page.keyboard.press('Enter');
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');
  await expect(targetPicker).toHaveAccessibleName(/Private note/);
  await expect.poll(() => fixture.requests.filter((request) => request.path.endsWith('/fact-evidence')).length).toBe(2);
  expect(fixture.requests.filter((request) => request.path.endsWith('/fact-evidence')).at(-1)?.query).toEqual({
    target: `attribute:forbidden-target-key:${`sha256:${'d'.repeat(64)}`}`,
    limit: '50', offset: '0'
  });

  const evidence = fact.getByLabel('Fact evidence');
  await expect(evidence).toContainText('First party');
  await expect(evidence).not.toContainText('Allowed synthetic evidence excerpt.');
  const revealExcerpt = evidence.getByRole('button', { name: 'Reveal evidence excerpt' });
  await revealExcerpt.focus();
  await page.keyboard.press('Enter');
  await expect(evidence).toContainText('Allowed synthetic evidence excerpt.');

  const historyTrigger = evidence.getByRole('button', { name: 'View support history' });
  await historyTrigger.focus();
  await page.keyboard.press('Enter');
  const history = page.getByRole('dialog', { name: 'Evidence support history' });
  await expect(history).toContainText('Unsupported');
  await expect(history).toContainText('Source deleted');
  expect(fixture.requests.filter((request) => request.path.endsWith('/fact-evidence-status-events'))).toHaveLength(1);
  await page.keyboard.press('Escape');
  await expect(history).toHaveCount(0);
  await expect(historyTrigger).toBeFocused();

  const evidenceSection = fact.getByRole('radio', { name: 'Evidence' });
  await evidenceSection.focus();
  await page.keyboard.press('ArrowRight');
  await expect(fact.getByRole('radio', { name: 'Claims' })).toBeFocused();
  await expect(fact).not.toContainText('Allowed synthetic sensitive value.');
  const revealValue = fact.getByRole('button', { name: 'Reveal sensitive fact value' });
  await revealValue.focus();
  await page.keyboard.press('Enter');
  await expect(fact).toContainText('Allowed synthetic sensitive value.');
  await page.keyboard.press('Shift+Tab');
  await fact.getByRole('radio', { name: 'Claims' }).focus();
  await page.keyboard.press('ArrowRight');
  await expect(fact).toContainText('Total 21');
  await page.keyboard.press('ArrowRight');
  await expect(fact).toContainText('Pinned');

  const html = await page.locator('body').evaluate((body) => body.innerHTML);
  for (const marker of FACT_LEDGER_FORBIDDEN_MARKERS) expect(html).not.toContain(marker);
  expect(fixture.requests.filter((request) => request.method !== 'GET' && request.path.includes('fact'))).toHaveLength(0);
  expect(fixture.requests.filter((request) => request.path.includes('brief'))).toHaveLength(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.setViewportSize({ width: 1280, height: 720 });

  await fact.getByRole('button', { name: 'Open person profile' }).focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Archive Person' })).toBeVisible();
  const factReadsBeforeBack = fixture.requests.filter((request) => request.path.includes('fact')).length;
  await page.goBack();
  await expect(fact).toContainText('Archive Person');
  await expect.poll(() => fixture.requests.filter((request) => request.path.includes('fact')).length)
    .toBe(factReadsBeforeBack + 5);
});

test('imported relationship reviews are safe, read-only, keyboard navigable, and URL-restorable', async ({ page }) => {
  const fixture = await installDirectoryReviewArchive(page);
  await page.goto(reviewURL('relationship'));

  const heading = page.getByRole('heading', { name: 'Imported relationships' });
  await expect(heading).toBeFocused();
  const card = page.getByRole('article', { name: 'Imported relationship review 41' });
  await expect(card).toContainText('urn:uuid:synthetic-related-person');
  await expect(card).toContainText('friend');
  await expect(card).toContainText('uri');
  await expect(card).toContainText('pending');
  await expect(card).toContainText('vcard_import');
  await expect(card.locator('a')).toHaveCount(0);
  for (const action of ['Accept', 'Reject', 'Unsure']) {
    await expect(card.getByRole('button', { name: new RegExp(action, 'i') })).toHaveCount(0);
  }
  expect(fixture.relationshipRequests).toEqual([
    { method: 'GET', path: '/api/v1/person-relationship-reviews', status: 'pending' }
  ]);
  const rendered = await page.content();
  for (const marker of [
    'forbidden-source-ref-marker', 'forbidden-resource-uid-marker', 'forbidden-creator-marker',
    'forbidden-reviewer-marker', 'forbidden-vcard-marker', 'forbidden.invalid',
    'forbidden-credential-marker'
  ]) expect(rendered).not.toContain(marker);

  const pending = page.getByRole('radio', { name: 'Pending' });
  await pending.focus();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('radio', { name: 'Accepted' })).toBeChecked();
  await expect(heading).toBeFocused();
  await expect(page.getByText('No imported relationship reviews in Accepted.')).toBeVisible();
  expect(fixture.relationshipRequests.at(-1)).toEqual({
    method: 'GET', path: '/api/v1/person-relationship-reviews', status: 'accepted'
  });

  fixture.failNextRelationshipRead();
  await page.getByRole('radio', { name: 'Accepted' }).focus();
  await page.keyboard.press('ArrowRight');
  const retry = page.getByRole('button', { name: 'Retry imported relationship reviews' });
  await expect(retry).toBeVisible();
  await expect(page.getByRole('alert')).not.toContainText('forbidden-remote-error-marker');
  await retry.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByText('No imported relationship reviews in Rejected.')).toBeVisible();
  await expect(heading).toBeFocused();
  expect(fixture.relationshipRequests.filter((request) => request.status === 'rejected')).toHaveLength(2);

  await page.goBack();
  await expect(page.getByRole('radio', { name: 'Accepted' })).toBeChecked();
  await expect(heading).toBeFocused();
  await page.goBack();
  await expect(card).toBeVisible();
  await expect(heading).toBeFocused();

  const owner = card.getByRole('button', { name: 'Open owner profile' });
  await owner.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Synthetic One' })).toBeVisible();
  expect(parseExploreURLState(new URL(page.url()).search, new URL(page.url()).pathname).directoryPersonID).toBe(7);
});
