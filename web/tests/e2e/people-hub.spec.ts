import AxeBuilder from '@axe-core/playwright';
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

/** Records marked as not a person in this fixture, by participant. */
const kinds = new Map<number, { kind: string; organization_name?: string }>();

function summary(id: number, label: string, profileID?: number) {
  const kind = kinds.get(id);
  return {
    ...(kind ? { correspondent_kind: { source: 'user', ...kind } } : {}),
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
  kinds.clear();
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
    if (path === '/api/v1/identity/correspondent-kinds') {
      return json({ records: [...kinds.entries()].map(([id, kind]) => ({
        canonical_id: id, member_ids: [id], source: 'user', addresses: [], classified_at: when,
        display_name: id === 31 ? 'Bo Example' : 'Ada Example', ...kind,
        ...(id === 21 ? { person: { id: 7, revision: 1, only_this_cluster: true } } : {}),
      })) });
    }
    const kindPath = /^\/api\/v1\/identity\/correspondent-kinds\/(\d+)$/.exec(path);
    if (kindPath) {
      const id = Number(kindPath[1]);
      const method = route.request().method();
      const body = method === 'PUT' ? route.request().postDataJSON() as { kind: string; organization_name?: string } : undefined;
      if (body && body.kind !== 'person') kinds.set(id, body);
      else kinds.delete(id);
      const kind = kinds.get(id);
      return json({
        record: {
          canonical_id: id, member_ids: [id], kind: kind?.kind ?? 'person', source: 'user', addresses: [],
          ...(kind?.organization_name ? { organization_name: kind.organization_name } : {}),
          ...(id === 21 ? { person: { id: 7, revision: 1, only_this_cluster: true, display_name: 'Ada Example' } } : {}),
        },
        organization_created: false, resolved_candidates: 0, restored_candidates: 0,
      });
    }
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

test('a contact marked as not a person is labelled, listed under Not people, and restored', async ({ page }) => {
  await page.goto('/people/contact-31');
  await page.getByRole('button', { name: 'More actions for Bo Example' }).click();
  await page.getByRole('menuitem', { name: 'Not a person…' }).click();
  const dialog = page.getByRole('dialog', { name: 'Not a person' });
  await expect(dialog).toContainText('A record you do not need as a contact');
  await dialog.getByRole('radio', { name: /Ignored/ }).check();
  const marked = page.waitForRequest((request) => request.method() === 'PUT' &&
    new URL(request.url()).pathname === '/api/v1/identity/correspondent-kinds/31');
  await dialog.getByRole('button', { name: 'Mark as ignored' }).click();
  expect((await marked).postDataJSON()).toEqual({ kind: 'ignored' });
  await expect(dialog).toHaveCount(0);
  const banner = page.getByRole('region', { name: 'Not a person' });
  await expect(banner).toContainText('Not a person · Ignored');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

  await page.goto('/people');
  await page.getByRole('button', { name: 'Not people' }).click();
  const records = page.getByRole('region', { name: 'Records that are not people' });
  await expect(records.getByRole('list', { name: 'Ignored' })).toContainText('Bo Example');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await records.getByRole('button', { name: 'Bo Example is a person' }).click();
  await expect(records.getByText('Bo Example is a person again.')).toBeVisible();
  await expect(records.getByRole('list', { name: 'Ignored' })).toHaveCount(0);
});

test('a saved person marked as an organization keeps the profile unless deleting it is confirmed', async ({ page }) => {
  await page.goto('/people/7');
  await expect(page.getByRole('heading', { name: 'Ada Example' })).toBeVisible();
  await page.getByRole('button', { name: 'More actions for Ada Example' }).click();
  await page.getByRole('menuitem', { name: 'Not a person…' }).click();
  const dialog = page.getByRole('dialog', { name: 'Not a person' });
  await dialog.getByRole('radio', { name: /Organization/ }).check();
  await dialog.getByRole('textbox', { name: 'Organization name' }).fill('Example Co');
  const marked = page.waitForRequest((request) => request.method() === 'PUT' &&
    new URL(request.url()).pathname === '/api/v1/identity/correspondent-kinds/21');
  await dialog.getByRole('button', { name: 'Mark as organization' }).click();
  expect((await marked).postDataJSON()).toEqual({ kind: 'organization', organization_name: 'Example Co' });

  await expect(dialog).toContainText('is a saved profile made only of this record');
  let deleted = false;
  page.on('request', (request) => { if (request.method() === 'DELETE') deleted = true; });
  await dialog.getByRole('button', { name: 'Keep profile' }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole('region', { name: 'Not a person' })).toContainText('Not a person · Organization · Example Co');
  expect(deleted).toBe(false);
});

// A real browser moves focus from the picker to the next control before the
// click lands. Pickers that treated that focusout as "clear the choice" left
// the confirm button doing nothing.
test('a person picked in Same person stays picked when the confirm button is clicked', async ({ page }) => {
  await page.route('**/api/v1/participants/search', (route) => route.fulfill({ json: {
    rows: [summary(21, 'Ada Example', 7)], total_count: 1, cache_revision: 'c', search_provenance: {},
  } }));
  await page.goto('/people/contact-31');
  await page.getByRole('button', { name: 'More actions for Bo Example' }).click();
  await page.getByRole('menuitem', { name: 'Same person…' }).click();
  const dialog = page.getByRole('dialog', { name: 'Link another identity for Bo Example' });
  await dialog.getByRole('button', { name: /^Search people to link/ }).click();
  await dialog.getByRole('combobox', { name: 'Search people to link' }).fill('Ada');
  await page.getByRole('option', { name: /Ada Example/ }).click();
  await expect(dialog.getByRole('button', { name: 'Search people to link: Ada Example' })).toBeVisible();

  const linked = page.waitForRequest((request) => request.method() === 'POST' &&
    new URL(request.url()).pathname === '/api/v1/identity/links');
  await dialog.getByRole('button', { name: 'These are the same person' }).click();
  expect((await linked).postDataJSON()).toEqual({ participant_a: 31, participant_b: 21 });
});

test('a new relationship keeps the counterpart and type picked before Create is clicked', async ({ page }) => {
  await page.route('**/api/v1/relationship-types', (route) => route.fulfill({ json: { relationship_types: [{
    id: 31, revision: 1, slug: 'mentor', forward_label: 'mentors', reverse_label: 'is mentored by', is_symmetric: false,
    is_canonical: false, is_deletable: true, ownership: 'user', universal_id: 'relationship-type-31', created_at: when, updated_at: when,
  }] } }));
  await page.route(/\/api\/v1\/people\/directory\?.*\bq=/, (route) => route.fulfill({ json: { people: [
    { id: 7, revision: 1, display_name: 'Ada Example', contact_state: 'active', categories: [], organizations: [] },
    { id: 9, revision: 1, display_name: 'Cy Example', contact_state: 'active', categories: [], organizations: [] },
  ] } }));
  await page.route('**/api/v1/person-relationships', (route) => route.fulfill({ status: 201, headers: { ETag: '"relationship-44-r1"' }, json: {
    id: 44, revision: 1, relationship_type_id: 31, type_slug: 'mentor', source_person_id: 7, target_person_id: 9,
    forward_label: 'mentors', reverse_label: 'is mentored by', is_symmetric: false, status: 'active', source: 'user',
    created_by: 'user', updated_by: 'user', vcard_identity: {}, created_at: when, updated_at: when,
  } }));
  await page.goto('/people/7/profile');
  const relationships = page.getByRole('region', { name: 'Relationships' });
  await relationships.getByRole('button', { name: 'Add relationship', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Add relationship' });
  await expect(dialog.getByRole('combobox', { name: 'Relationship type: Choose a type' })).toBeVisible();

  await dialog.getByRole('button', { name: /^Relationship counterpart/ }).click();
  await dialog.getByRole('combobox', { name: 'Relationship counterpart' }).fill('Cy');
  await page.getByRole('option', { name: /Cy Example/ }).click();
  await dialog.getByRole('combobox', { name: /^Relationship type:/ }).click();
  await page.getByRole('option', { name: 'mentors / is mentored by' }).click();
  await dialog.getByRole('textbox', { name: 'Relationship notes' }).click();
  await expect(dialog.getByRole('button', { name: 'Relationship counterpart: Cy Example' })).toBeVisible();

  const created = page.waitForRequest((request) => request.method() === 'POST' &&
    new URL(request.url()).pathname === '/api/v1/person-relationships');
  await dialog.getByRole('button', { name: 'Create relationship' }).click();
  expect((await created).postDataJSON()).toMatchObject({ source_person_id: 7, target_person_id: 9, relationship_type_slug: 'mentor' });
});
