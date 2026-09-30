import { fireEvent, screen } from '@testing-library/svelte';

/** Opens Settings or Saved Views from the header gear menu. */
export async function openFromGear(label: 'Settings' | 'Saved Views'): Promise<void> {
  await fireEvent.click(await screen.findByRole('button', { name: 'Settings and saved views' }));
  await fireEvent.click(await screen.findByRole('menuitem', { name: new RegExp(`^${label}`) }));
}

/** Opens the command palette with its shortcut (P). */
export async function openCommandPalette(): Promise<HTMLElement> {
  await fireEvent.keyDown(window, { key: 'p' });
  return screen.findByRole('dialog', { name: 'Commands' });
}
