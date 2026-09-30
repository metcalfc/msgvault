import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import LinkedValue from './LinkedValue.svelte';

describe('LinkedValue', () => {
  it('links an email in the same tab', () => {
    render(LinkedValue, { input: { kind: 'email', value: 'person@example.test' } });
    const link = screen.getByRole('link', { name: 'person@example.test' });
    expect(link.getAttribute('href')).toBe('mailto:person@example.test');
    expect(link.getAttribute('target')).toBeNull();
    expect(link.getAttribute('rel')).toBeNull();
  });

  it('links an E.164 phone with tel:', () => {
    render(LinkedValue, { input: { kind: 'phone', value: '+1 555 010 0001' } });
    expect(screen.getByRole('link', { name: '+1 555 010 0001' }).getAttribute('href')).toBe('tel:+15550100001');
  });

  it('opens a profile in a new tab without a referrer and says so', () => {
    render(LinkedValue, { input: { kind: 'handle', service: 'github', value: 'example-person' } });
    const link = screen.getByRole('link', { name: 'example-person (opens in new tab)' });
    expect(link.getAttribute('href')).toBe('https://github.com/example-person');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toBe('noopener noreferrer');
  });

  it('renders text for a value it cannot link safely', () => {
    const { container } = render(LinkedValue, { input: { kind: 'url', value: 'javascript:alert(1)' } });
    expect(screen.queryByRole('link')).toBeNull();
    expect(container.textContent).toContain('javascript:alert(1)');
  });

  it('renders text and no controls inside a clickable row', () => {
    render(LinkedValue, { input: { kind: 'email', value: 'person@example.test' }, interactive: false });
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.queryByRole('button')).toBeNull();
    expect(screen.getByText('person@example.test')).toBeDefined();
  });

  it('copies the value with the copy button', async () => {
    const writeText = vi.fn(async () => undefined);
    const original = navigator.clipboard;
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    try {
      render(LinkedValue, { input: { kind: 'email', value: 'person@example.test' } });
      await fireEvent.click(screen.getByRole('button', { name: 'Copy person@example.test' }));
      await waitFor(() => expect(writeText).toHaveBeenCalledWith('person@example.test'));
    } finally {
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: original });
    }
  });

  it('hides the copy button when asked', () => {
    render(LinkedValue, { input: { kind: 'email', value: 'person@example.test' }, copy: false });
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('uses a precomputed link', () => {
    render(LinkedValue, {
      link: { href: 'https://www.google.com/maps/search/?api=1&query=Springfield', label: 'Open Springfield in Google Maps', external: true },
      text: 'Springfield', copy: false
    });
    expect(screen.getByRole('link', { name: 'Springfield (opens in new tab)' }).getAttribute('href'))
      .toBe('https://www.google.com/maps/search/?api=1&query=Springfield');
  });
});
