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
  it('detects tags and character references but not prose with angle brackets', () => {
    expect(looksLikeHTML('<p>Agenda</p>')).toBe(true);
    expect(looksLikeHTML('Join at <a href="https://example.test">the link</a>')).toBe(true);
    expect(looksLikeHTML('Tom &amp; Jerry')).toBe(true);
    expect(looksLikeHTML('if a < b then b > a')).toBe(false);
    expect(looksLikeHTML('plain description')).toBe(false);
  });
});
