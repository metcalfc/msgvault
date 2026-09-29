import { expect, test, type Page } from '@playwright/test';
import { openPersonFromPeople } from '../kit-ui';

const when = '2026-07-19T10:00:00Z';

const message = {
  id: 501, source_id: 3, source_message_id: 'source-501', conversation_id: 71,
  subject: 'Planning notes', message_type: 'email', conversation_type: 'email_thread',
  from: 'Ada Example <ada@example.test>', to: ['reader@example.test'], sent_at: when,
  snippet: 'Planning', labels: [], has_attachments: false, size_bytes: 20,
  body: 'Notes for the planning session', attachments: [],
};

function summary(id: number, label: string, profileID?: number) {
  return {
    id, display_label: label, partial_label: false, identifiers: [], activity_count: 3,
    meeting_count: 0, file_count: 0, current_relationship_temperature: 0, peak_relationship_temperature: 0,
    peak_relationship_year: 2026, source_counts: [], first_at: when, last_at: when, cache_revision: 'c',
    cluster: { canonical_id: id, member_ids: [id], edges: [] },
    ...(profileID ? { profile: { id: profileID, revision: 1, display_name: label } } : {}),
  };
}

/** Ada is saved (person 7, participant 21); Bo is an archive contact that
 * has not been saved (participant 31). */
async function installPeople(page: Page): Promise<void> {
  await page.route('**/api/session', (route) => route.fulfill({ json: {
    auth_mode: 'loopback', https: false, plain_http_warning: false,
  } }));
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const json = (body: unknown) => route.fulfill({ json: body });
    if (path === '/api/v1/settings') return json({ settings: [], pending_restart: false });
    if (path === '/api/v1/people/directory') return json({ people: [{
      id: 7, revision: 1, display_name: 'Ada Example', contact_state: 'active', categories: [], organizations: [],
      last_contact_at: '2026-07-18T10:00:00Z', primary_identifier: { kind: 'email', value: 'ada@example.test' },
    }] });
    if (path === '/api/v1/relationships') return json({
      rows: [{
        canonical_id: 31, display_label: 'Bo Example', last_at: when, member_ids: [31], score: 1,
        primary_identifier: { kind: 'phone', value: '+15555550131' },
        signals: { last_interaction_at: when, meeting_count: 0, meetings_together: 0, modalities: 1,
          received_from_them: 1, sent_count: 1, sent_to_them: 1 },
      }], total_count: 1, cache_revision: 'c', identity_revision: 1,
    });
    if (path === '/api/v1/participants/completions') return json({
      rows: [{ participant_id: 21, value: 'ada@example.test', label: 'Ada Example' }],
    });
    if (path === '/api/v1/participants/21') return json(summary(21, 'Ada Example', 7));
    if (path === '/api/v1/participants/31') return json(summary(31, 'Bo Example'));
    if (/^\/api\/v1\/relationships\/\d+\/timeline$/.test(path)) {
      return json({ canonical_id: Number(path.split('/')[4]), identity_revision: 1, cache_revision: 'c', rows: [], total_count: 0 });
    }
    if (path === '/api/v1/people/7') return json({
      id: 7, revision: 1, display_name: 'Ada Example', participant_ids: [21], vcard_uid: '', created_at: when, updated_at: when,
    });
    if (path === '/api/v1/people/7/profile') return json({
      person: { id: 7, revision: 1, display_name: 'Ada Example' },
      names: [], contact_points: [], addresses: [], dates: [], categories: [], media: [],
    });
    if (path === '/api/v1/people/7/attributes') return json({ person_id: 7, attributes: [] });
    if (path === '/api/v1/people/7/contact-state') return json({
      person_id: 7, cadence_status: 'unknown', interaction_count: 3, computed_at: when, stale: false,
    });
    if (path === '/api/v1/people/7/days') return json({ person_id: 7, total_count: 0, days: [] });
    if (path === '/api/v1/people/7/employments') return json({ employments: [] });
    if (path === '/api/v1/people/7/relationships') return json({ relationships: [] });
    if (path.endsWith('/files/search')) return json({ files: [], total_count: 0, cache_revision: 'c', search_provenance: {} });
    if (path === '/api/v1/explore') return json({
      rows: [{
        key: 'message:501', kind: 'message', message_type: 'email', conversation_type: 'email_thread',
        title: 'Planning notes', preview: 'Planning', occurred_at: when, source_id: 3,
        source_identifier: 'archive@example.test', source_type: 'synthetic', participant_labels: ['Ada Example'],
        participant_ids: [21], attachment_count: 0, attachment_size: 0, has_attachments: false,
        deleted_from_source: false, message_count: 1, conversation_id: 71, anchor_message_id: 501, match: {},
      }], total_count: 1, cache_revision: 'c', search_provenance: {},
    });
    if (path === '/api/v1/messages/501') return json(message);
    if (path === '/api/v1/conversations/71') {
      return json({ id: 71, anchor_id: 501, messages: [message], has_before: false, has_after: false, total: 1 });
    }
    return route.fulfill({ status: 404, json: { error: 'not_found', message: 'Not in this fixture' } });
  });
}

async function openPersonFromPill(page: Page, scope: Page | ReturnType<Page['getByRole']>): Promise<void> {
  await scope.getByRole('button', { name: /^Ada Example \(ada@example\.test\): person actions$/ }).first().click();
  await page.getByRole('menuitem', { name: 'Open person' }).click();
}

test.beforeEach(async ({ page }) => installPeople(page));

test('People lists saved people and archive contacts together and opens either as one page', async ({ page }) => {
  await page.goto('/people');
  const results = page.getByRole('region', { name: 'People results' });
  await expect(results.getByRole('link')).toHaveText([/Bo Example\s*Not saved/, /Ada Example/]);

  await openPersonFromPeople(page, 'Bo Example');
  await expect(page).toHaveURL(/\/people\/contact-31$/);
  await expect(page.getByRole('button', { name: 'Save to Directory' })).toBeVisible();
  await page.goBack();

  await openPersonFromPeople(page, 'Ada Example');
  await expect(page).toHaveURL(/\/people\/7$/);
  await expect(page.getByRole('heading', { name: 'Ada Example' })).toBeVisible();
  await expect(page).toHaveTitle('People · msgvault');
});

test('a message pill opens the saved person, not a second page for the same human', async ({ page }) => {
  await page.goto('/messages/501');
  await expect(page.getByRole('article', { name: 'Message 501' })).toContainText('Notes for the planning session');
  await openPersonFromPill(page, page.getByRole('article', { name: 'Message 501' }));
  // The pill resolves to the archive contact, which is saved as person 7.
  await expect(page).toHaveURL(/\/people\/7$/);
  await expect(page.getByRole('heading', { name: 'Ada Example' })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/messages\/501$/);
});

test('search from the header opens results, and a result opens the person', async ({ page }) => {
  await page.goto('/people');
  const field = page.getByRole('searchbox', { name: 'Search the archive' });
  await page.keyboard.press(process.platform === 'darwin' ? 'Meta+K' : 'Control+K');
  await expect(field).toBeFocused();
  await field.fill('planning');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/search\?q=planning/);
  await expect(page.getByRole('main', { name: 'Search' })).toBeVisible();

  await page.getByRole('grid', { name: 'Message results' }).getByText('Planning notes').click();
  const reading = page.getByRole('complementary', { name: /Reading pane: Planning notes/ });
  await expect(reading.getByText('Notes for the planning session')).toBeVisible();
  await openPersonFromPill(page, reading);
  await expect(page).toHaveURL(/\/people\/7$/);

  await page.goBack();
  await expect(page).toHaveURL(/\/search\?q=planning/);
  await page.goForward();
  await expect(page.getByRole('heading', { name: 'Ada Example' })).toBeVisible();
});
