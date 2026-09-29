import { expect, type Page } from '@playwright/test';

export async function selectKitOption(
  page: Page,
  title: string,
  option: string
): Promise<void> {
  await page.getByRole('combobox', { name: new RegExp(`^${title}:`) }).click();
  await page.getByRole('option', { name: option, exact: true }).click();
}

export async function selectKitTopBarTab(page: Page, tab: string): Promise<void> {
  const navigation = page.getByRole('navigation', { name: 'Primary' });
  const button = navigation.getByRole('button', { name: tab, exact: true });
  const collapsed = navigation.getByRole('combobox', { name: /^Primary:/ });
  await expect(button.or(collapsed)).toBeVisible();
  if (await button.isVisible()) {
    await button.click();
    return;
  }
  await selectKitOption(page, 'Primary', tab);
}

/** Theme and density live in Settings; the command palette applies them
 * in place so a test can change them without leaving the current view. */
export async function openCommandPalette(page: Page): Promise<void> {
  await page.keyboard.press(process.platform === 'darwin' ? 'Meta+Shift+P' : 'Control+Shift+P');
  await expect(page.getByRole('dialog', { name: 'Commands' })).toBeVisible();
}

export async function runPaletteCommand(page: Page, label: string): Promise<void> {
  // Start from the page body so an editable control's suspended shortcuts
  // cannot swallow the palette chord.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await openCommandPalette(page);
  const palette = page.getByRole('dialog', { name: 'Commands' });
  await palette.getByRole('combobox').fill(label);
  await palette.getByRole('option', { name: label, exact: true }).click();
  await expect(palette).toHaveCount(0);
}

export async function setKitTheme(page: Page, theme: 'light' | 'dark'): Promise<void> {
  await runPaletteCommand(page, theme === 'light' ? 'Theme: Light' : 'Theme: Dark');
  await expectKitTheme(page, theme);
}

export async function setDensity(page: Page, density: 'compact' | 'comfortable'): Promise<void> {
  await runPaletteCommand(page, density === 'compact' ? 'Density: Compact' : 'Density: Comfortable');
  await expect(page.locator('html')).toHaveAttribute('data-density', density);
}

/** Opens Settings, Reviews, or Saved Views from the header gear menu. */
export async function openFromGear(page: Page, label: 'Settings' | 'Reviews' | 'Saved Views'): Promise<void> {
  await page.getByRole('button', { name: /^Settings and reviews/ }).click();
  await page.getByRole('menuitem', { name: new RegExp(`^${label}`) }).click();
}

/** Opens an Activity section: the Activity tab, then its sub-tab. */
export async function openActivity(page: Page, section: 'Sources' | 'Operations' | 'Deletions'): Promise<void> {
  await selectKitTopBarTab(page, 'Activity');
  await page.getByRole('tablist', { name: 'Activity sections' }).getByRole('tab', { name: section }).click();
}

export async function expectKitTheme(page: Page, theme: 'light' | 'dark'): Promise<void> {
  const root = page.locator('html');
  if (theme === 'dark') await expect(root).toHaveClass(/\bdark\b/);
  else await expect(root).not.toHaveClass(/\bdark\b/);
}
