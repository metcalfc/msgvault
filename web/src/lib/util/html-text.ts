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
 * showing as literal angle brackets: a matched open/close tag pair, a
 * block-level or void tag, or a hyperlink. A bare "<alice@example.test>"
 * or an "R&amp;D" in prose is plain text and stays in a <pre>. */
export function looksLikeHTML(text: string): boolean {
  return /<([a-z][a-z0-9]*)\b[^>]*>[\s\S]*?<\/\1\s*>/i.test(text)
    || /<(?:p|br|div|hr|img|li|ul|ol|table|tr|td|th|blockquote|h[1-6])\b[^>]*\/?>/i.test(text)
    || /<a\s[^>]*href\s*=/i.test(text);
}
