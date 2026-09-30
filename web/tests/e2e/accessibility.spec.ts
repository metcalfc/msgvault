import AxeBuilder from '@axe-core/playwright';
import { exploreLink } from '../../src/test/explore-url';
import { expect, test, type Page } from '@playwright/test';
import { expectKitTheme, openActivity, openFromGear, openPersonFromPeople, selectKitOption, selectKitTopBarTab, setKitTheme, setDensity } from '../kit-ui';
import { assertCardDAVForbiddenMarkersAbsent, installCardDAV } from './fixtures/carddav';
import { installDirectoryReviewArchive, installMixedArchive } from './fixtures/mixed-archive';
import { installOperations, OPERATION_REFERENCES } from './fixtures/operations';

/** The person Overview keeps its maintenance cards (tracking, CardDAV
 * publication, brief, merge history) behind a closed disclosure. */
async function openMaintenance(page: Page) {
  const tab = page.getByRole('tab', { name: 'Maintenance' });
  await expect(tab).toBeVisible();
  await tab.click();
}

async function assertNoViolations(page: Page, label: string) {
  const result = await new AxeBuilder({ page }).analyze();
  expect(result.violations, `${label}: ${result.violations.map((v) => `${v.id}: ${v.help}`).join('; ')}`)
    .toEqual([]);
}

test('Operations workspace, detail, failure, and narrow states have no axe violations', async ({ page }) => {
  test.slow();
  const fixture = await installOperations(page);
  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({ workspace: 'operations' }))}`);
  await expect(page.getByRole('main', { name: 'Operations' })).toBeVisible();
  await assertNoViolations(page, 'Operations workspace');

  await page.getByRole('button', { name: 'Open Document extraction run' }).click();
  await expect(page.getByRole('region', { name: 'Operation run detail' })).toContainText('Operation archive input changed.');
  await assertNoViolations(page, 'Operations detail with fixed failure');

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
    workspace: 'operations', operationRunID: OPERATION_REFERENCES.document
  }))}`);
  await expect(page.getByRole('region', { name: 'Operation detail focused content' })).toBeVisible();
  await assertNoViolations(page, 'Operations narrow detail');

  await page.getByRole('button', { name: 'Back to operation history' }).click();
  fixture.failNextHistory();
  await page.getByRole('button', { name: 'Refresh operations' }).click();
  await expect(page.getByRole('alert', { name: 'Operation history failure' })).toBeVisible();
  await assertNoViolations(page, 'Operations history failure');
});

for (const theme of ['light', 'dark'] as const) {
  for (const density of ['compact', 'comfortable'] as const) {
    test(`${theme}/${density} primary workspaces and representative states have no axe violations`, async ({ page }) => {
      test.slow();
      await installMixedArchive(page);
      await page.unroute('**/api/v1/participants/12/files/search');
      await page.route('**/api/v1/participants/12/files/search', async (route) => {
        const body = route.request().postDataJSON() as { mime_families?: string[] };
        const media = body.mime_families?.includes('image');
        return route.fulfill({ json: {
          files: [{
            id: media ? 8 : 7, key: media ? 'file:8' : 'file:7', entry_key: 'message:100001',
            message_id: 100001, conversation_id: 100001, occurred_at: '2026-01-03T12:00:00Z',
            source_id: 1, source_type: 'synthetic', source_identifier: 'archive@example.com',
            containing_title: 'Synthetic email', filename: media ? 'archive-photo.png' : 'archive-notes.pdf',
            mime_type: media ? 'image/png' : 'application/pdf', mime_family: media ? 'image' : 'pdf',
            size_bytes: 2048, content_state: 'metadata_only', content_available: false,
            person_provenance: {
              participant_ids: [12], roles: ['from', 'conversation_member'],
              directions: ['from_person', 'group']
            }
          }],
          total_count: 1, cache_revision: 'mixed-100k', search_provenance: {}
        } });
      });
      await page.goto('/');
      await setKitTheme(page, theme);
      await setDensity(page, density);

      // People is the default landing; walk an archive contact's page tab
      // by tab so each layout gets its own axe pass.
      await expect(page.getByRole('main', { name: 'People' })).toBeVisible();
      const people = page.getByRole('region', { name: 'People results' });
      await expect(people.getByText('Archive Person').first()).toBeVisible();
      await assertNoViolations(page, `People list ${theme}/${density}`);
      await page.getByRole('button', { name: 'Not saved' }).click();
      await openPersonFromPeople(page, 'Archive Person');
      await expect(page.getByRole('heading', { name: 'Archive Person' })).toBeVisible();
      await assertNoViolations(page, `Contact overview ${theme}/${density}`);
      const contactTabs = page.getByRole('tablist', { name: 'Contact sections' });
      await contactTabs.getByRole('tab', { name: 'Timeline' }).click();
      const relationshipTimeline = page.getByRole('grid', { name: 'Relationship activity' });
      await expect(relationshipTimeline.locator('[data-row-key]').first()).toBeVisible();
      await assertNoViolations(page, `Contact timeline ${theme}/${density}`);
      await contactTabs.getByRole('tab', { name: 'Files' }).click();
      await expect(page.getByRole('grid', { name: 'Files results' }).getByText('archive-notes.pdf')).toBeVisible();
      await assertNoViolations(page, `Person files ${theme}/${density}`);
      await page.getByRole('radio', { name: 'Media' }).click();
      await expect(page.getByRole('button', { name: 'Open archive-photo.png' })).toBeVisible();
      await assertNoViolations(page, `Person media ${theme}/${density}`);
      await contactTabs.getByRole('tab', { name: 'Timeline' }).click();
      await expect(relationshipTimeline).toBeVisible();
      await relationshipTimeline.focus();
      await page.keyboard.press('Enter');
      await expect(page.getByRole('complementary', { name: /Reading pane/ })).toBeVisible();
      await assertNoViolations(page, `Relationships reading pane ${theme}/${density}`);
      await page.keyboard.press('Escape');

      await selectKitTopBarTab(page, 'Inbox');
      const grid = page.getByRole('grid', { name: 'Message results' });
      await expect(grid.locator('[data-row-key]').first()).toBeVisible();
      await assertNoViolations(page, `Everything ${theme}/${density}`);
      await grid.focus();
      await page.keyboard.press('Enter');
      await expect(page.getByRole('complementary', { name: /Reading pane/ })).toBeVisible();
      await assertNoViolations(page, `reading pane ${theme}/${density}`);
      await page.keyboard.press('Escape');

      await page.keyboard.press('Shift+/');
      const keyboardHelp = page.getByRole('dialog', { name: 'Keyboard shortcuts' });
      await expect(keyboardHelp).toBeVisible();
      await assertNoViolations(page, `modal ${theme}/${density}`);
      await keyboardHelp.getByRole('button', { name: 'Close' }).click();

      const destinations: Array<[string, () => Promise<void>]> = [
        ['People', () => selectKitTopBarTab(page, 'People')],
        ['Files', () => selectKitTopBarTab(page, 'Files')],
        ['Reviews', () => selectKitTopBarTab(page, 'Reviews')],
        ['Saved Views', () => openFromGear(page, 'Saved Views')],
        ['Sources', () => openActivity(page, 'Sources')],
        ['Deletions', () => openActivity(page, 'Deletions')],
        ['Settings', () => openFromGear(page, 'Settings')],
      ];
      for (const [workspace, open] of destinations) {
        await open();
        await expect(page.getByRole('main', { name: workspace, exact: true })).toBeVisible();
        await assertNoViolations(page, `${workspace} ${theme}/${density}`);
        if (workspace === 'Files') {
          const files = page.getByRole('grid', { name: 'Files results' });
          await files.focus();
          await page.keyboard.press('Enter');
          const viewer = page.getByRole('dialog', { name: 'View synthetic.txt' });
          await expect(viewer).toBeVisible();
          await assertNoViolations(page, `viewer ${theme}/${density}`);
          await viewer.getByRole('button', { name: 'Close file viewer' }).click();
        }
      }
    });
  }
}

test('Directory network list and visualization have no axe violations', async ({ page }) => {
  await installMixedArchive(page);
  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({ workspace: 'directory', directoryPersonID: 42 }))}`);
  await expect(page.getByRole('heading', { name: 'Archive Person' })).toBeVisible();
  await page.getByRole('tab', { name: 'Profile' }).click();
  await expect(page.getByRole('list', { name: 'Directory network connections' })).toContainText('Curated Peer');
  await assertNoViolations(page, 'Directory network');
});

test('Directory profile maintenance is accessible at desktop and narrow widths', async ({ page }) => {
  await installMixedArchive(page);
  for (const viewport of [
    { label: 'desktop', width: 1280, height: 900 },
    { label: 'narrow', width: 390, height: 844 }
  ]) {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
      workspace: 'directory', directoryPersonID: 42
    }))}`);
    await openMaintenance(page);
    const maintenance = page.getByRole('region', { name: 'Profile maintenance' });
    await expect(maintenance.getByRole('switch', {
      name: 'Track this person for profile maintenance'
    })).toBeEnabled();
    // The eligible-fields catalogue sits behind its own disclosure.
    await maintenance.getByText('What can be maintained?').click();
    const reveal = maintenance.getByRole('button', { name: 'Show sensitive eligible fields' });
    await reveal.focus();
    await page.keyboard.press('Enter');
    await expect(maintenance.getByText('Private note')).toBeVisible();
    await expect(maintenance.getByText('Sensitive', { exact: true })).toBeVisible();
    await assertNoViolations(page, `Directory profile maintenance ${viewport.label}`);
  }
});

test('primary navigation reaches Reviews without overflowing the header at desktop and narrow widths', async ({ page }) => {
  await installDirectoryReviewArchive(page);
  for (const viewport of [
    { label: 'desktop', width: 1280, height: 900 },
    { label: 'narrow', width: 390, height: 844 }
  ]) {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await page.goto('/');
    await expect(page.getByRole('main', { name: 'People' })).toBeVisible();
    await selectKitTopBarTab(page, 'Reviews');
    await expect(page).toHaveURL(/\/reviews/);
    await expect(page.getByRole('main', { name: 'Reviews' })).toBeVisible();

    const nav = page.getByRole('navigation', { name: 'Primary' });
    if (viewport.label === 'narrow') {
      // Six places do not fit beside the search field, so they collapse
      // into one menu that names the current place.
      await expect(nav.getByRole('combobox', { name: /^Primary: Reviews/ })).toBeVisible();
    } else {
      // Open matches light the dot, which names itself inside the tab.
      const tab = nav.getByRole('button', { name: 'Reviews Items waiting' });
      await expect(tab).toHaveAttribute('aria-current', 'page');
      for (const theme of ['light', 'dark'] as const) {
        await setKitTheme(page, theme);
        const dot = await tab.getByRole('img', { name: 'Items waiting' }).evaluate((element) => {
          const channel = (value: number) => {
            const c = value / 255;
            return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
          };
          const luminance = (color: string) => {
            const [r, g, b] = (color.match(/[\d.]+/g) ?? []).map(Number);
            return 0.2126 * channel(r ?? 0) + 0.7152 * channel(g ?? 0) + 0.0722 * channel(b ?? 0);
          };
          let surface: Element | null = element.parentElement;
          let background = 'rgba(0, 0, 0, 0)';
          while (surface && /rgba\(.*, 0\)|transparent/.test(background)) {
            background = getComputedStyle(surface).backgroundColor;
            surface = surface.parentElement;
          }
          const [light, dark] = [luminance(getComputedStyle(element).backgroundColor), luminance(background)]
            .sort((a, b) => b - a);
          const tabRect = element.parentElement!.getBoundingClientRect();
          const rect = element.getBoundingClientRect();
          return {
            contrast: (light! + 0.05) / (dark! + 0.05),
            position: getComputedStyle(element).position,
            inside: rect.left >= tabRect.left && rect.right <= tabRect.right && rect.top >= tabRect.top && rect.bottom <= tabRect.bottom,
          };
        });
        // The dot overlays the tab's corner, so it never widens the tab.
        expect(dot.position).toBe('absolute');
        expect(dot.inside).toBe(true);
        expect(dot.contrast, `${theme} dot contrast`).toBeGreaterThanOrEqual(3);
      }
      await setKitTheme(page, 'light');
    }
    // Visible header controls stay inside the viewport. (kit-ui's hidden
    // tab-measurement probe is not a visible control, so the bar's
    // scrollWidth is not the measure here.)
    const widths = await page.evaluate(() => {
      const right = (selector: string) =>
        document.querySelector<HTMLElement>(selector)?.getBoundingClientRect().right ?? Number.NaN;
      return {
        page: document.documentElement.scrollWidth,
        viewport: window.innerWidth,
        nav: document.querySelector('.kit-top-bar__nav-select') ? right('.kit-top-bar__nav-select') : right('.kit-top-bar__tabs'),
        actions: right('.kit-top-bar__right'),
      };
    });
    expect(widths.page, `${viewport.label}: page scrolls sideways`).toBeLessThanOrEqual(widths.viewport);
    expect(widths.nav, `${viewport.label}: navigation leaves the viewport`).toBeLessThanOrEqual(widths.viewport);
    expect(widths.actions, `${viewport.label}: header actions leave the viewport`).toBeLessThanOrEqual(widths.viewport);
    await assertNoViolations(page, `Primary navigation to Reviews ${viewport.label}`);
  }
});

test('Directory review, merge, split, and honest Fact gate have no axe violations', async ({ page }) => {
  test.slow();
  await installDirectoryReviewArchive(page);
  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
    workspace: 'directory_review', reviewKind: 'identity', identityState: 'candidate'
  }))}`);

  await expect(page.getByRole('article', { name: 'Identity match 17' })).toBeVisible();
  await assertNoViolations(page, 'Directory identity review');

  await page.getByRole('radio', { name: 'Enrichment identities' }).click();
  await expect(page.getByRole('article', { name: 'Synthetic One' })).toBeVisible();
  await assertNoViolations(page, 'Directory enrichment identity review');
  await page.getByRole('radio', { name: 'Unclear correspondents' }).click();
  await expect(page.getByRole('article', { name: 'Front Desk' })).toBeVisible();
  await assertNoViolations(page, 'Directory unclear correspondent review');
  await page.getByRole('radio', { name: 'Identity matches' }).click();
  await expect(page.getByRole('article', { name: 'Identity match 17' })).toBeVisible();

  await page.getByRole('article', { name: 'Identity match 17' })
    .getByRole('button', { name: 'Keep separate' }).click();
  const decision = page.getByRole('dialog', { name: 'Keep separate' });
  await expect(decision).toBeVisible();
  await assertNoViolations(page, 'Directory identity decision');
  await page.keyboard.press('Escape');

  await page.getByRole('radio', { name: 'Contacts that match your archive' }).click();
  const shared = page.getByRole('article', { name: 'Identity match 26' });
  await expect(shared.getByRole('note')).toContainText('Looks like a shared mailbox');
  await assertNoViolations(page, 'Directory shared mailbox card');
  await shared.getByRole('button', { name: 'Not a person: identity match 26' }).click();
  await page.getByRole('menuitem', { name: 'Organization' }).click();
  // The menu choice applies at once, named after the identity; Undo stays.
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(shared).toBeHidden();
  const undo = page.getByRole('button', { name: 'Undo: Shop Support is a person' });
  await expect(undo).toBeVisible();
  await assertNoViolations(page, 'Directory not-a-person status with undo');
  await undo.click();
  await expect(shared).toBeVisible();
  await page.getByRole('radio', { name: 'All matches' }).click();
  await expect(page.getByRole('article', { name: 'Identity match 17' })).toBeVisible();

  // Fail the first automatic merge so the choice UI is audited too.
  const failMerge = /\/api\/v1\/people\/\d+\/merge$/;
  await page.route(failMerge, (route) => route.fulfill({
    status: 409, json: { error: 'person_carddav_published', message: 'Synthetic profile is published to CardDAV.' }
  }));
  await page.getByRole('article', { name: 'Identity match 19' })
    .getByRole('button', { name: 'Link identities' }).click();
  const linkDecision = page.getByRole('dialog', { name: 'Link identities' });
  await linkDecision.getByRole('button', { name: 'Link identities' }).click();
  // Accepting hands straight to the merge, which runs on its own; a failed
  // one falls back to the choice.
  const merge = page.getByRole('dialog', { name: 'Resolve person merge' });
  await expect(merge).toBeVisible();
  await assertNoViolations(page, 'Directory person merge choice');
  await expect(merge.getByRole('radio', { name: 'Synthetic One' })).toBeChecked();
  await page.unroute(failMerge);
  await merge.getByRole('button', { name: 'Merge into selected survivor' }).click();
  // Merging from Reviews stays in the queue and moves on to the next match.
  await expect(merge).toHaveCount(0);
  await expect(page).toHaveURL(/\/reviews/);
  await expect(page.getByRole('article', { name: 'Identity match 25' })).toBeFocused();
  await expect(page.getByRole('button', { name: 'Open Synthetic One profile' })).toBeVisible();
  await assertNoViolations(page, 'Directory review after merge');

  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
    workspace: 'directory_review', reviewKind: 'fact', identityState: 'candidate', directoryPersonID: 42
  }))}`);
  await expect(page.getByRole('region', { name: 'Fact review' })).toBeVisible();
  await expect(page.getByLabel('Fact evidence')).toBeVisible();
  await assertNoViolations(page, 'Directory Fact ledger desktop');
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole('radiogroup', { name: 'Fact ledger section' })).toBeVisible();
  await assertNoViolations(page, 'Directory Fact ledger narrow');
  await page.setViewportSize({ width: 1280, height: 720 });

  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
    workspace: 'directory', directoryPersonID: 7
  }))}`);
  await openMaintenance(page);
  const history = page.getByRole('table', { name: 'Person merge history' });
  await expect(history).toBeVisible();
  await history.getByRole('button', { name: 'Inspect merge 41' }).click();
  await expect(page.getByRole('heading', { name: 'Merge 41 detail' })).toBeVisible();
  await assertNoViolations(page, 'Directory merge history detail');
  await page.getByRole('button', { name: 'Split merged profile' }).click();
  const split = page.getByRole('dialog', { name: 'Split merged profile' });
  await expect(split).toBeVisible();
  const splitCancel = split.getByRole('button', { name: 'Cancel' });
  await expect(splitCancel).toBeEnabled();
  await expect(splitCancel).toHaveCSS('opacity', '1');
  await assertNoViolations(page, 'Directory split modal');
});

test('Imported relationship reviews have no axe violations at desktop and narrow widths', async ({ page }) => {
  await installDirectoryReviewArchive(page);
  for (const viewport of [
    { label: 'desktop', width: 1280, height: 900 },
    { label: 'narrow', width: 390, height: 844 }
  ]) {
    await page.setViewportSize(viewport);
    await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
      workspace: 'directory_review', reviewKind: 'relationship', relationshipReviewState: 'pending'
    }))}`);
    await expect(page.getByRole('article', { name: 'Imported relationship review 41' })).toBeVisible();
    await assertNoViolations(page, `Imported relationship reviews ${viewport.label}`);
  }
});

for (const mode of ['failed', 'malformed'] as const) {
  test(`Directory Fact claims fail closed with an accessible ${mode} target catalog`, async ({ page }) => {
    const fixture = await installDirectoryReviewArchive(page);
    if (mode === 'failed') fixture.failFactCatalog();
    else fixture.malformFactCatalog();
    await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
      workspace: 'directory_review', reviewKind: 'fact', identityState: 'candidate', directoryPersonID: 42
    }))}`);

    const fact = page.getByRole('region', { name: 'Fact review' });
    await fact.getByRole('radio', { name: 'Claims' }).click();
    await expect(fact.getByRole('status', {
      name: 'Fact value hidden until target sensitivity is verified.'
    })).toBeVisible();
    await expect(fact).not.toContainText('Allowed synthetic sensitive value.');
    await expect(fact.getByRole('button', { name: 'Reveal sensitive fact value' })).toHaveCount(0);
    await assertNoViolations(page, `Directory Fact ledger ${mode} catalog`);
  });
}

test('Directory Fact claims stay private and accessible while the target catalog is deferred', async ({ page }) => {
  const fixture = await installDirectoryReviewArchive(page);
  const releaseCatalog = fixture.deferFactCatalog();
  await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
    workspace: 'directory_review', reviewKind: 'fact', identityState: 'candidate', directoryPersonID: 42
  }))}`);

  const fact = page.getByRole('region', { name: 'Fact review' });
  await fact.getByRole('radio', { name: 'Claims' }).click();
  await expect(fact.getByRole('status', {
    name: 'Fact value hidden until target sensitivity is verified.'
  })).toBeVisible();
  await expect(fact).not.toContainText('Allowed synthetic sensitive value.');
  await assertNoViolations(page, 'Directory Fact ledger deferred catalog');

  releaseCatalog();
  await expect(fact.getByRole('button', { name: 'Reveal sensitive fact value' })).toBeVisible();
  await expect(fact).not.toContainText('Allowed synthetic sensitive value.');
});

test('CardDAV account, operations, conflicts, modal, and publication are accessible at desktop and narrow widths', async ({ page }) => {
  test.slow();
  await installMixedArchive(page);
  await installCardDAV(page, { configured: true, staleConflictOnce: false });

  for (const viewport of [
    { label: 'desktop', width: 1280, height: 900 },
    { label: 'narrow', width: 390, height: 844 }
  ]) {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({ workspace: 'settings' }))}`);
    const cardDAVCategory = page.getByRole('button', { name: /^CardDAV account/ });
    await cardDAVCategory.focus();
    await page.keyboard.press('Enter');

    await expect(page.getByLabel('CardDAV status summary')).toContainText('Configured');
    await expect(page.getByLabel('CardDAV status summary')).toContainText('Runtime available');
    await expect(page.getByLabel('CardDAV status summary')).toContainText('Credential ready');
    await expect(page.getByText('Scheduled sync enabled')).toBeVisible();
    await expect(page.getByRole('table', { name: 'CardDAV sync history' })).toContainText('Succeeded');
    await assertNoViolations(page, `CardDAV account and operations ${viewport.label}`);
    await assertCardDAVForbiddenMarkersAbsent(page);

    const conflictRow = page.getByRole('button', { name: 'Review conflict 41 in Synthetic contacts' });
    await conflictRow.focus();
    await page.keyboard.press('Enter');
    const comparison = page.getByRole('region', { name: 'CardDAV conflict 41 comparison' });
    await expect(comparison).toContainText('Present');
    await expect(comparison).toContainText('Deleted. This side is a deletion tombstone.');
    await expect(comparison).toContainText('Unavailable. No safe comparison summary is available.');
    await expect(page.getByText('Only display name, email addresses, and phone numbers are shown. Your choice applies to the whole card.')).toBeVisible();
    const baseBox = await page.getByLabel('Base card summary').boundingBox();
    const localBox = await page.getByLabel('Local card summary').boundingBox();
    expect(baseBox).not.toBeNull();
    expect(localBox).not.toBeNull();
    if (viewport.label === 'desktop') expect(Math.abs(baseBox!.y - localBox!.y)).toBeLessThan(2);
    else expect(localBox!.y).toBeGreaterThan(baseBox!.y + baseBox!.height - 2);
    await assertNoViolations(page, `CardDAV conflict detail ${viewport.label}`);
    await assertCardDAVForbiddenMarkersAbsent(page);

    const keepLocal = page.getByRole('button', { name: 'Keep local card' }).first();
    await keepLocal.focus();
    await page.keyboard.press('Enter');
    const modal = page.getByRole('dialog', { name: 'Keep local CardDAV card' });
    await expect(modal).toContainText('Choosing it deletes the whole card');
    await assertNoViolations(page, `CardDAV conflict modal ${viewport.label}`);
    await assertCardDAVForbiddenMarkersAbsent(page);
    await page.keyboard.press('Escape');
    await expect(keepLocal).toBeFocused();

    await page.goto(`/?explore=${encodeURIComponent(JSON.stringify({
      workspace: 'directory', directoryPersonID: 42
    }))}`);
    await openMaintenance(page);
    const publication = page.getByRole('region', { name: 'CardDAV publication' });
    // The publication state renders as label / value detail rows.
    await expect(publication).toContainText('Not published');
    await expect(publication).toContainText(/Desired publication\s+Unpublished/);
    await expect(publication).toContainText(/Address book\s+Synthetic contacts/);
    await expect(publication.getByRole('switch', { name: 'Publish person to CardDAV' })).toBeEnabled();
    await assertNoViolations(page, `CardDAV publication ${viewport.label}`);
    await assertCardDAVForbiddenMarkersAbsent(page);
  }
});

for (const theme of ['light', 'dark'] as const) {
  for (const density of ['compact', 'comfortable'] as const) {
    test(`${theme}/${density} login, loading, empty, error, and degraded states have no axe violations`, async ({ page }) => {
      let sessionRequired = true;
      let exploreMode: 'loading' | 'empty' | 'error' | 'degraded' = 'loading';
      await page.addInitScript(({ theme, density }) => {
        sessionStorage.setItem('msgvault.appearance.override', JSON.stringify({ theme, density }));
      }, { theme, density });
      await page.route('**/api/session', (route) => route.fulfill({ json: sessionRequired
        ? { auth_mode: 'required', https: true, plain_http_warning: false }
        : { auth_mode: 'loopback', https: false, plain_http_warning: false } }));
      await page.route('**/api/v1/settings', (route) => route.fulfill({ json: {
        settings: [
          { key: 'web.theme', value: { string: 'light' } },
          { key: 'web.density', value: { string: 'compact' } }
        ], pending_restart: false
      } }));
      await page.route('**/api/v1/explore', async (route) => {
        if (exploreMode === 'loading') return new Promise(() => {});
        if (exploreMode === 'error') return route.fulfill({
          status: 500, json: { error: 'internal_error', message: 'Synthetic request failure.' }
        });
        if (exploreMode === 'degraded') return route.fulfill({ status: 503, json: {
          error: 'analytical_cache_unavailable', message: 'Synthetic cache unavailable.',
          readiness: 'absent', recovery_action: 'msgvault build-cache'
        } });
        return route.fulfill({ json: {
          rows: [], total_count: 0, cache_revision: 'empty', search_provenance: {}
        } });
      });

      // This test exercises Everything's own loading/empty/error/degraded
      // states, not the Relationships hub, so it lands there explicitly
      // rather than relying on whatever the default landing workspace is.
      await page.goto(exploreLink({ workspace: 'everything' }));
      await expectKitTheme(page, theme);
      await expect(page.locator('html')).toHaveAttribute('data-density', density);
      await expect(page.getByRole('main', { name: 'Authentication' })).toBeVisible();
      await expect(page.getByLabel('API key')).toBeVisible();
      await assertNoViolations(page, `login ${theme}/${density}`);

      sessionRequired = false;
      await page.reload();
      const grid = page.getByRole('grid', { name: 'Message results' });
      await expect(grid).toHaveAttribute('aria-busy', 'true');
      await expect(page.getByTestId('everything-skeleton').first()).toBeVisible();
      await assertNoViolations(page, `loading ${theme}/${density}`);

      exploreMode = 'empty';
      await page.reload();
      await expect(page.getByText('No items match this view')).toBeVisible();
      await assertNoViolations(page, `empty ${theme}/${density}`);

      exploreMode = 'error';
      await page.reload();
      await expect(page.getByRole('alert')).toContainText('Synthetic request failure.');
      await assertNoViolations(page, `error ${theme}/${density}`);

      exploreMode = 'degraded';
      await page.reload();
      const degraded = page.getByRole('alert');
      await expect(degraded).toContainText('Analytical cache unavailable');
      await expect(degraded).toContainText('Synthetic cache unavailable.');
      await expect(degraded).toContainText('msgvault build-cache');
      await assertNoViolations(page, `degraded ${theme}/${density}`);
    });
  }
}
