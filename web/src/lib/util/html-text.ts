/** Text-level helpers for archive strings that arrive HTML-flavoured. */

/** Decodes HTML character references ("&amp;", "&#39;", "&eacute;") in a
 * plain-text excerpt. A textarea's innerHTML parse never runs scripts or
 * creates elements, so tag-like fragments come back as their literal text. */
export function decodeHTMLEntities(text: string): string {
  if (!text || !text.includes('&')) return text;
  if (typeof document === 'undefined') return text;
  const decoder = document.createElement('textarea');
  decoder.innerHTML = text;
  return decoder.value;
}

/** True when a plain body carries markup worth rendering rather than
 * showing as literal angle brackets — an opening or closing tag, or a
 * character reference. */
export function looksLikeHTML(text: string): boolean {
  return /<\/?[a-z][^>]*>/i.test(text) || /&(?:[a-z]+|#\d+|#x[0-9a-f]+);/i.test(text);
}
