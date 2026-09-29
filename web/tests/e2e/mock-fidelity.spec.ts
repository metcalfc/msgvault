import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Locator, type Page } from '@playwright/test';
import { setKitTheme } from '../kit-ui';

/** The design brief's person page and search mocks, rendered from
 * synthetic data shaped like the mock, checked element by element, and
 * captured in both themes under test-results/mock-fidelity/. */

const now = Date.now();
const daysAgo = (days: number) => new Date(now - days * 86_400_000).toISOString();
const envelope = (id: number, extra: Record<string, unknown> = {}) => ({
  id, ordinal: 0, source: 'user', created_at: daysAgo(900), updated_at: daysAgo(900), vcard: {}, ...extra,
});

const person = {
  id: 7, revision: 3, display_name: 'Avery Example', participant_ids: [21], vcard_uid: '',
  created_at: daysAgo(900), updated_at: daysAgo(1),
};

const employments = [
  { id: 1, person_id: 7, organization_id: 2, is_current: true, is_primary: true, source: 'user', revision: 1,
    created_at: daysAgo(900), updated_at: daysAgo(900), title: 'General Partner', location: 'Portland, Oregon',
    start_date: { year: 2018, month: 4 } },
  ...[3, 4, 5].map((id) => ({ id, person_id: 7, organization_id: id, is_current: false, is_primary: false, source: 'user',
    revision: 1, created_at: daysAgo(900), updated_at: daysAgo(900), title: 'Partner' })),
];

const timelineRows = [
  { key: 'message:901', kind: 'email', title: 'Re: Q3 portfolio update', preview: 'Sounds good, lets lock in Thursday',
    occurred_at: daysAgo(2), source_id: 1, message_count: 1, has_attachments: false, anchor_message_id: 901, conversation_id: 91 },
  { key: 'message:904', kind: 'email', title: 'Deck for Thursday', preview: '', from_me: true,
    occurred_at: daysAgo(3), source_id: 1, message_count: 1, has_attachments: true, anchor_message_id: 904, conversation_id: 94 },
  { key: 'message:902', kind: 'calendar_event', title: 'Example Ventures partners sync', preview: '6 attendees · 45 min',
    occurred_at: daysAgo(4), source_id: 1, message_count: 1, has_attachments: false, anchor_message_id: 902, conversation_id: 92 },
  { key: 'burst:903', kind: 'chat_burst', title: 'Avery Example', preview: 'can you send the deck when it is final?',
    occurred_at: daysAgo(6), source_id: 2, message_count: 4, has_attachments: false, anchor_message_id: 903, conversation_id: 93 },
];

function searchRow(key: string, kind: string, messageType: string, title: string, people: string[], excerpt: string, count: number, days: number) {
  return {
    key, kind, message_type: messageType, conversation_type: messageType === 'imessage' ? 'direct_chat' : 'email_thread',
    title, preview: excerpt, occurred_at: daysAgo(days), source_id: 1, source_identifier: 'archive@example.test',
    source_type: 'synthetic', participant_labels: people, participant_ids: [21], attachment_count: 0, attachment_size: 0,
    has_attachments: false, deleted_from_source: false, message_count: count, matched_sender_identities: [],
    matched_recipient_identities: [], match: { strongest_excerpt: excerpt },
  };
}

async function install(page: Page): Promise<void> {
  await page.route('**/api/session', (route) => route.fulfill({ json: { auth_mode: 'loopback', https: false, plain_http_warning: false } }));
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = (body: unknown) => route.fulfill({ json: body });
    if (path === '/api/v1/settings') return json({ settings: [], pending_restart: false });
    if (path === '/api/v1/people/7') return json(person);
    if (path === '/api/v1/people/7/profile') return json({
      person, names: [],
      contact_points: [
        { person_id: 7, address_kind: 'email', original_value: 'avery@example.test', normalized_value: 'avery@example.test',
          normalization: 'email', normalization_version: 1, envelope: envelope(11, { type_label: 'work' }) },
        { person_id: 7, address_kind: 'phone', original_value: '+1 (555) 555-0142', normalized_value: '+15555550142',
          normalization: 'phone', normalization_version: 1, service_slug: 'imessage', envelope: envelope(12, { type_label: 'cell' }) },
        { person_id: 7, address_kind: 'url', original_value: 'linkedin.com/in/avery-example', normalized_value: 'linkedin.com/in/avery-example',
          normalization: 'url', normalization_version: 1, service_slug: 'linkedin', envelope: envelope(13, { source: 'enrichment' }) },
      ],
      addresses: [{ person_id: 7, address_kind: 'work', original_value: 'Portland, Oregon, United States', locality: 'Portland',
        region: 'Oregon', country_name: 'United States', envelope: envelope(14, { source: 'enrichment' }) }],
      dates: [], categories: [], media: [],
    });
    if (path === '/api/v1/people/7/attributes') return json({ person_id: 7, attributes: [] });
    if (path === '/api/v1/people/7/contact-state') return json({
      person_id: 7, cadence_status: 'unknown', computed_at: daysAgo(0), interaction_count: 607, stale: false,
      last_contact_at: daysAgo(2), last_contact_channel: 'email', last_contact_ref: 'message:901',
      last_outbound_at: daysAgo(7), last_inbound_at: daysAgo(2), first_contact_at: '2023-03-01T00:00:00Z',
    });
    if (path === '/api/v1/people/7/days') return json({ person_id: 7, total_count: 0, days: [] });
    if (path === '/api/v1/people/7/employments') return json({
      employments, projection: { employment_id: 1, organization_id: 2, organization_name: 'Example Ventures', vcard: {} },
    });
    if (path === '/api/v1/people/7/relationships') return json({ relationships: [] });
    if (path === '/api/v1/participants/21') return json({
      id: 21, display_label: 'Avery Example', partial_label: false, identifiers: [], activity_count: 607, meeting_count: 12,
      file_count: 3, current_relationship_temperature: 0, peak_relationship_temperature: 0, peak_relationship_year: 2026,
      source_counts: [], first_at: '2023-03-01T00:00:00Z', last_at: daysAgo(2), cache_revision: 'c',
      cluster: { canonical_id: 21, member_ids: [21], edges: [] }, profile: { id: 7, revision: 3 },
    });
    if (path === '/api/v1/participants/31') return json({
      id: 31, display_label: 'Rowan Example', partial_label: false, activity_count: 48, meeting_count: 2,
      file_count: 5, current_relationship_temperature: 0, peak_relationship_temperature: 0, peak_relationship_year: 2026,
      source_counts: [], first_at: '2024-02-01T00:00:00Z', last_at: daysAgo(3), cache_revision: 'c',
      cluster: { canonical_id: 31, member_ids: [31], edges: [] },
      identifiers: [
        { participant_id: 31, type: 'email', value: 'rowan@example.test', is_primary: true, provenance: 'participant_identifiers' },
        { participant_id: 31, type: 'phone', value: '+15555550188', display_value: '+1 (555) 555-0188', is_primary: false,
          provenance: 'participant_identifiers', service_slug: 'imessage', service_label: 'iMessage' },
      ],
    });
    if (path === '/api/v1/relationships/31/timeline') {
      return json({ canonical_id: 31, identity_revision: 1, cache_revision: 'c', rows: timelineRows.slice(0, 2), total_count: 2 });
    }
    if (path === '/api/v1/relationships/31/calendar') return json({
      participant_id: 31, canonical_id: 31, year: 2026, timezone: 'UTC', days: [], annual: [],
      current: { temperature: 40, rank: 4, population: 20, raw_score: 3, signals: { sent_signal: 1, received_volume: 0, meeting_signal: 0, modalities: 1 } },
      peak_temperature: 40, peak_year: 2026, scoring_timezone: 'UTC', score_version: 1, effective_date: '2026-09-29',
      cache_revision: 'c', identity_revision: 1,
    });
    if (path === '/api/v1/relationships/21/timeline') {
      return json({ canonical_id: 21, identity_revision: 1, cache_revision: 'c', rows: timelineRows, total_count: 3 });
    }
    if (path === '/api/v1/explore') return json({
      rows: [
        searchRow('conversation:1', 'conversation', 'email', 'Fwd: Re: Board Deck July 2026', ['Tom Example'],
          'attached the latest deck ahead of Thursday', 3, 3),
        searchRow('message:2', 'message', 'calendar_event', 'Board prep', ['Tom Example', 'Dana Example'],
          'Jul 10, 2:00–3:00 · Zoom', 1, 5),
        searchRow('message:3', 'message', 'imessage', 'Tom Example', ['Tom Example'],
          'can you send the deck when it is final?', 1, 6),
      ], total_count: 3, cache_revision: 'c', search_provenance: {},
    });
    if (path === '/api/v1/sources/status') return json({ sources: [] });
    return route.fulfill({ status: 404, json: { error: 'not_found', message: 'Not in this fixture' } });
  });
}

async function contrast(locator: Locator): Promise<number> {
  return locator.evaluate((element) => {
    // Computed colors come back as rgb() or, for color-mix(), color(srgb 0-1).
    const parse = (value: string) => {
      const scale = value.startsWith('color(') ? 255 : 1;
      return (value.match(/[\d.]+/g) ?? []).slice(0, 3).map((part) => Number(part) * scale);
    };
    const luminance = ([r, g, b]: number[]) => {
      const channel = (c: number) => { const v = c / 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
      return 0.2126 * channel(r!) + 0.7152 * channel(g!) + 0.0722 * channel(b!);
    };
    const ink = parse(getComputedStyle(element).color);
    let node: Element | null = element;
    let background = 'rgba(0, 0, 0, 0)';
    while (node && /rgba\(0, 0, 0, 0\)|transparent/.test(background)) {
      background = getComputedStyle(node).backgroundColor;
      node = node.parentElement;
    }
    const [lighter, darker] = [luminance(ink), luminance(parse(background))].sort((a, b) => b - a);
    return (lighter! + 0.05) / (darker! + 0.05);
  });
}

async function noAxeViolations(page: Page, label: string): Promise<void> {
  const result = await new AxeBuilder({ page }).analyze();
  expect(result.violations, `${label}: ${result.violations.map((violation) => violation.id).join(', ')}`).toEqual([]);
}

test.beforeEach(async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await install(page);
});

for (const theme of ['light', 'dark'] as const) {
  test(`person Overview matches the brief's mock (${theme})`, async ({ page }, info) => {
    await page.goto('/people/7');
    await setKitTheme(page, theme);
    const main = page.getByRole('main', { name: 'Person' });

    // Header: rounded avatar with initials, name, subtitle, actions.
    const avatar = main.locator('.person-header .identity-avatar');
    await expect(avatar).toHaveText('AE');
    expect(await avatar.evaluate((element) => parseFloat(getComputedStyle(element).borderRadius))).toBeGreaterThan(8);
    const name = main.getByRole('heading', { name: 'Avery Example' });
    await expect(name).toBeVisible();
    expect(await name.evaluate((element) => parseFloat(getComputedStyle(element).fontSize))).toBeGreaterThanOrEqual(19);
    await expect(main.locator('.person-subtitle')).toHaveText('General Partner · Example Ventures · Portland, Oregon');
    await expect(main.getByRole('button', { name: 'Messages with Avery Example' })).toBeVisible();
    await expect(main.getByRole('button', { name: 'Edit Avery Example' })).toBeVisible();
    await expect(main.getByRole('button', { name: 'More actions for Avery Example' })).toBeVisible();

    // Underline tabs.
    await expect(main.getByRole('tablist', { name: 'Person detail sections' }).getByRole('tab'))
      .toHaveText(['Overview', 'Timeline', 'Files', 'Meetings', 'Profile', 'Maintenance']);

    // Labeled contact rows and the last-contact row.
    const rows = main.getByRole('list', { name: 'Contact methods' }).locator('[data-fact-row]');
    await expect(rows.locator('[data-fact-label]')).toHaveText(['Email', 'Mobile', 'LinkedIn', 'Last contact']);
    await expect(rows.nth(0).locator('[data-fact-meta]')).toHaveText(/work\s*·\s*copy/);
    await expect(rows.nth(1).locator('[data-fact-meta]')).toHaveText(/iMessage\s*·\s*copy/);
    await expect(rows.nth(2).locator('[data-fact-meta]')).toHaveText(/from enrichment\s*·\s*copy/);
    await expect(rows.nth(3).locator('[data-fact-meta]')).toHaveText(/you wrote .* · they wrote .* · 607 interactions since 2023/);
    expect(await rows.nth(0).locator('[data-fact-label]').evaluate((element) => getComputedStyle(element).textTransform)).toBe('uppercase');

    // Recent and Context.
    const recent = main.getByRole('region', { name: 'Recent' });
    await expect(recent.getByText('See all in Timeline')).toBeVisible();
    await expect(recent.locator('.recent-item')).toHaveCount(4);
    await expect(recent.locator('.recent-item .title')).toHaveText(['Re: Q3 portfolio update', 'Deck for Thursday', 'Example Ventures partners sync', 'Avery Example']);
    // Authored rows lead with who wrote them; dates keep a single space.
    await expect(recent.locator('.recent-item small')).toHaveText([
      'Avery · "Sounds good, lets lock in Thursday"', 'you · attachment', '"6 attendees · 45 min"',
      'Avery · 4 messages · "can you send the deck when it is final?"',
    ]);
    await expect(recent.locator('.recent-item time').first()).toHaveText(/^[A-Z][a-z]{2} \d{1,2}$/);
    const context = main.getByRole('region', { name: 'Context' });
    await expect(context.getByText('filled attributes only')).toBeVisible();
    await expect(context.locator('[data-fact-label]')).toHaveText(['Location', 'Employment']);
    await expect(context.getByText('Portland, Oregon, United States')).toBeVisible();
    await expect(context.getByText('Example Ventures, General Partner, since Apr 2018')).toBeVisible();
    await expect(context.getByRole('button', { name: 'History (4)' })).toBeVisible();
    // Profile editors and empty groups stay off Overview.
    await expect(main.getByRole('region', { name: 'Structured profile' })).toHaveCount(0);

    await noAxeViolations(page, `person Overview ${theme}`);
    await page.screenshot({ path: `test-results/mock-fidelity/person-overview-${theme}.png`, fullPage: false });
    info.annotations.push({ type: 'screenshot', description: `test-results/mock-fidelity/person-overview-${theme}.png` });
  });

  test(`unsaved contact page shares the person-page header (${theme})`, async ({ page }) => {
    await page.goto('/people/contact-31');
    await setKitTheme(page, theme);
    await expect(page.getByRole('heading', { name: 'Rowan Example' })).toBeVisible();
    await expect(page.locator('.person-title .identity-avatar')).toHaveText('RE');
    await expect(page.getByRole('button', { name: 'Messages with Rowan Example' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Save to Directory' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'More actions for Rowan Example' })).toBeVisible();
    await expect(page.getByRole('tablist', { name: 'Contact sections' })).toBeVisible();
    const labels = page.getByRole('list', { name: 'Contact methods' }).locator('[data-fact-label]');
    await expect(labels).toHaveText(['Email', 'Phone', 'Last contact']);
    await expect(page.getByRole('region', { name: 'Recent' }).locator('.recent-item')).toHaveCount(2);
    await noAxeViolations(page, `unsaved contact ${theme}`);
    await page.screenshot({ path: `test-results/mock-fidelity/contact-overview-${theme}.png`, fullPage: false });
  });

  test(`search results match the brief's mock rows (${theme})`, async ({ page }) => {
    await page.goto('/search?q=from%3Atom%40example.test%20deck&mode=full_text&since=all');
    await setKitTheme(page, theme);
    const grid = page.getByRole('grid', { name: 'Message results' });
    await expect(grid.locator('[data-row-key]')).toHaveCount(3);
    const first = grid.locator('[data-row-key="conversation:1"]');
    await expect(first.locator('.cell--people')).toHaveText(/Tom Example/);
    await expect(first.locator('.cell--title strong')).toHaveText('Fwd: Re: Board Deck July 2026');
    await expect(first.locator('.in-thread')).toHaveText('· 3 in thread');
    await expect(first.locator('.cell--excerpt mark').first()).toHaveText(/deck/i);
    // The chips row above the results carries the query's operators.
    await expect(page.getByRole('region', { name: 'Active analytical context' }).locator('.crumb--operator')).toContainText('From: tom@example.test');

    // One distinct, readable ink per modality.
    const inks: string[] = [];
    for (const [key, modality] of [['conversation:1', 'email'], ['message:2', 'event'], ['message:3', 'text']] as const) {
      const glyph = grid.locator(`[data-row-key="${key}"] .row-kind[data-modality="${modality}"] svg`);
      await expect(glyph).toBeVisible();
      expect(await contrast(glyph), `${modality} glyph contrast (${theme})`).toBeGreaterThanOrEqual(4.5);
      inks.push(await glyph.evaluate((element) => getComputedStyle(element).color));
    }
    expect(new Set(inks).size).toBe(3);

    await noAxeViolations(page, `search results ${theme}`);
    await page.screenshot({ path: `test-results/mock-fidelity/search-${theme}.png`, fullPage: false });
  });
}
