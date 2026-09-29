import { render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { describe, expect, it } from 'vitest';

import StatusNotice from './StatusNotice.svelte';

const content = createRawSnippet(() => ({ render: () => '<span>Retry failed</span>' }));

describe('StatusNotice', () => {
  it.each([
    ['error', 'alert'],
    ['warning', 'status'],
    ['info', 'status'],
  ] as const)('announces a %s notice as %s', (tone, role) => {
    render(StatusNotice, { props: { tone, children: content } });
    const notice = screen.getByRole(role);
    expect(notice.getAttribute('data-tone')).toBe(tone);
    expect(notice.textContent).toBe('Retry failed');
  });
});
