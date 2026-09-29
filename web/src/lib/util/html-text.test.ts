import { describe, expect, it } from 'vitest';

import { decodeHTMLEntities, looksLikeHTML } from './html-text';

describe('decodeHTMLEntities', () => {
  it('decodes named, decimal, and hex references without creating elements', () => {
    expect(decodeHTMLEntities('Tom &amp; Jerry &#39;quoted&#39; caf&eacute; &#x2014; done')).toBe("Tom & Jerry 'quoted' café — done");
    expect(decodeHTMLEntities('<b>bold</b> &lt;kept&gt;')).toBe('<b>bold</b> <kept>');
  });

  it('returns text without references untouched', () => {
    expect(decodeHTMLEntities('plain excerpt')).toBe('plain excerpt');
    expect(decodeHTMLEntities('')).toBe('');
  });
});

describe('looksLikeHTML', () => {
  it('detects tag pairs, block tags, and links but not prose with angle brackets or entities', () => {
    expect(looksLikeHTML('<p>Agenda</p>')).toBe(true);
    expect(looksLikeHTML('line one<br>line two')).toBe(true);
    expect(looksLikeHTML('<div class="x">block')).toBe(true);
    expect(looksLikeHTML('Join at <a href="https://example.test">the link</a>')).toBe(true);
    expect(looksLikeHTML('Contact <alice@example.test> for R&amp;D questions')).toBe(false);
    expect(looksLikeHTML('Tom &amp; Jerry')).toBe(false);
    expect(looksLikeHTML('if a < b then b > a')).toBe(false);
    expect(looksLikeHTML('<word> in brackets')).toBe(false);
    expect(looksLikeHTML('plain description')).toBe(false);
  });
});
