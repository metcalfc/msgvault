import { afterEach, describe, expect, it } from 'vitest';

import { openContactLink } from './open-link';

describe('openContactLink', () => {
  const seen: Array<{ href: string; target: string; rel: string }> = [];
  const capture = (event: MouseEvent): void => {
    const anchor = event.target as HTMLAnchorElement;
    seen.push({ href: anchor.getAttribute('href') ?? '', target: anchor.target, rel: anchor.rel });
    // jsdom cannot navigate; the click itself is what the browser follows.
    event.preventDefault();
  };

  afterEach(() => {
    document.removeEventListener('click', capture, true);
    seen.length = 0;
  });

  it('activates a same-tab anchor for mailto: and removes it', () => {
    document.addEventListener('click', capture, true);
    openContactLink({ href: 'mailto:person@example.test', label: 'Email', external: false });
    expect(seen).toEqual([{ href: 'mailto:person@example.test', target: '', rel: '' }]);
    expect(document.querySelectorAll('a')).toHaveLength(0);
  });

  it('opens a web link in a new tab without opener or referrer', () => {
    document.addEventListener('click', capture, true);
    openContactLink({ href: 'https://example.com/', label: 'Open', external: true });
    expect(seen).toEqual([{ href: 'https://example.com/', target: '_blank', rel: 'noopener noreferrer' }]);
  });
});
