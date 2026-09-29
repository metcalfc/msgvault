import type { ExploreSearchMode } from '../explore/models';

/**
 * Client-side mirror of the daemon's Gmail-style query tokenizer
 * (internal/search/parser.go). It only classifies tokens; the daemon stays
 * the authority on what each operator means.
 */

/** Operators the daemon's parser applies as filters. */
export const KNOWN_QUERY_OPERATORS: ReadonlySet<string> = new Set([
  'from', 'to', 'cc', 'bcc', 'subject', 'label', 'l', 'list', 'list-id', 'has',
  'before', 'after', 'older_than', 'newer_than', 'larger', 'smaller',
  'message_type', 'conversation_id',
]);

/** Splits a query the way the daemon does: spaces separate tokens, quoted
 * phrases stay whole, and op:"quoted value" stays one token. */
export function tokenizeQuery(query: string): string[] {
  const tokens: string[] = [];
  let current = '';
  let inQuotes = false;
  let quoteChar = '';
  let afterColon = false;
  let opQuoted = false;
  let escaped = false;
  for (const char of query) {
    if (inQuotes && escaped) {
      current += char;
      escaped = false;
    } else if (inQuotes && char === '\\') {
      current += char;
      escaped = true;
    } else if ((char === '"' || char === "'") && !inQuotes) {
      inQuotes = true;
      quoteChar = char;
      opQuoted = afterColon;
      if (!afterColon && current.length > 0) {
        tokens.push(current);
        current = '';
      }
      if (afterColon) current += char;
      afterColon = false;
    } else if (inQuotes && char === quoteChar) {
      inQuotes = false;
      if (opQuoted) {
        current += char;
        tokens.push(current);
        current = '';
      } else if (current.length > 0) {
        tokens.push(`"${current}"`);
        current = '';
      }
      quoteChar = '';
      opQuoted = false;
    } else if (char === ' ' && !inQuotes) {
      if (current.length > 0) {
        tokens.push(current);
        current = '';
      }
      afterColon = false;
    } else {
      current += char;
      afterColon = char === ':';
    }
  }
  if (current.length > 0) tokens.push(current);
  return tokens;
}

export function isQuotedPhrase(token: string): boolean {
  return token.length > 2 && token.startsWith('"') && token.endsWith('"');
}

export interface OperatorToken {
  operator: string;
  value: string;
}

/** Recognizes op:value and the message_type=value spelling. */
export function splitOperatorToken(token: string): OperatorToken | undefined {
  const colon = token.indexOf(':');
  if (colon >= 0) return { operator: token.slice(0, colon).toLowerCase(), value: token.slice(colon + 1) };
  const equals = token.indexOf('=');
  if (equals >= 0 && token.slice(0, equals).toLowerCase() === 'message_type') {
    return { operator: 'message_type', value: token.slice(equals + 1) };
  }
  return undefined;
}

/** Strips one pair of surrounding double quotes and unescapes \" and \\. */
export function unquoteValue(value: string): string {
  if (value.length < 2 || !value.startsWith('"') || !value.endsWith('"')) return value;
  return value.slice(1, -1).replace(/\\(["\\])/g, '$1');
}

/** True when the token is a filter the daemon applies rather than text. */
export function isOperatorToken(token: string): boolean {
  if (isQuotedPhrase(token)) return false;
  const split = splitOperatorToken(token);
  return split !== undefined && KNOWN_QUERY_OPERATORS.has(split.operator);
}

/** Bare words, quoted phrases, and unknown op:value tokens: the terms a
 * semantic search can embed. */
export function freeTextTerms(query: string): string[] {
  return tokenizeQuery(query).filter((token) => !isOperatorToken(token));
}

export function hasFreeText(query: string): boolean {
  return freeTextTerms(query).length > 0;
}

/**
 * Semantic and hybrid search embed free text, so a query made only of
 * filters cannot run in those modes. Such a request runs as full text; the
 * chosen mode stays selected for the next query.
 */
export function effectiveSearchMode(query: string, mode: ExploreSearchMode): ExploreSearchMode {
  if (mode === 'full_text' || query.trim() === '') return mode;
  return hasFreeText(query) ? mode : 'full_text';
}

/** True when the request ran as full text although another mode was chosen. */
export function searchModeFellBack(query: string, mode: ExploreSearchMode): boolean {
  return effectiveSearchMode(query, mode) !== mode;
}
