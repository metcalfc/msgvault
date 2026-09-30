import { fireEvent, screen } from '@testing-library/svelte';
import { tick } from 'svelte';

// A browser moves focus to a control before its click lands, so whichever
// picker held focus first sees focusout with the control as relatedTarget.
// fireEvent.click alone skips that step and hides pickers that drop a choice
// when focus leaves them.
export async function focusAndClick(control: HTMLElement): Promise<void> {
  control.focus();
  await tick();
  await fireEvent.click(control);
}

export async function chooseSelectOption(trigger: HTMLElement, optionName: string): Promise<void> {
  await focusAndClick(trigger);
  await fireEvent.click(await screen.findByRole('option', { name: optionName }));
}

// The closed trigger is named "<placeholder>: <selected label>" (or the
// placeholder alone with nothing to show), so match on the placeholder prefix.
export async function openTypeahead(triggerName: string): Promise<HTMLInputElement> {
  const escaped = triggerName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  await focusAndClick(screen.getByRole('button', { name: new RegExp(`^${escaped}(: |$)`) }));
  return screen.getByRole('combobox', { name: triggerName }) as HTMLInputElement;
}
