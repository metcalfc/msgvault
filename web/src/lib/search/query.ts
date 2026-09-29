import type { ExploreFilter, ExploreSearchMode } from '../explore/models';
import { dateInputBound, type DateDimension } from '../explore/date-range';

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

/** after:/before: take a day; the daemon also reads RFC3339 and US-style
 * dates, which stay text operators rather than guessing a local day. */
function operatorDateBound(value: string, dimension: DateDimension): string | undefined {
  const day = /^\d{4}[-/]\d{2}[-/]\d{2}$/.test(value) ? value.replaceAll('/', '-') : '';
  return day ? dateInputBound(day, dimension) : undefined;
}

function withFilterValue(filters: ExploreFilter[], dimension: ExploreFilter['dimension'], value: string): ExploreFilter[] {
  const existing = filters.find((filter) => filter.dimension === dimension);
  if (!existing) return [...filters, { dimension, values: [value] }];
  if (existing.values.includes(value)) return filters;
  return filters.map((filter) => (filter === existing ? { dimension, values: [...filter.values, value] } : filter));
}

/**
 * Moves operators that have an Explore filter dimension out of the query
 * and into filters, so each shows as its own removable chip:
 * after:/before: (a YYYY-MM-DD day), message_type:, and list:/list-id:.
 * Everything else stays in the query text for the daemon to apply —
 * including from:/to:/cc:/bcc:, whose direction the participant filter
 * cannot express, and has:attachment, subject:, label:, larger:/smaller:,
 * which have no filter dimension.
 */
export function extractQueryFilters(
  query: string,
  filters: readonly ExploreFilter[],
): { query: string; filters: ExploreFilter[]; moved: boolean } {
  let next: ExploreFilter[] = [...filters];
  const kept: string[] = [];
  let moved = false;
  for (const token of tokenizeQuery(query)) {
    const split = isQuotedPhrase(token) ? undefined : splitOperatorToken(token);
    const value = split ? unquoteValue(split.value).trim() : '';
    if (split && value) {
      if (split.operator === 'after' || split.operator === 'before') {
        const bound = operatorDateBound(value, split.operator);
        if (bound) {
          next = [...next.filter((filter) => filter.dimension !== split.operator), { dimension: split.operator, values: [bound] }];
          moved = true;
          continue;
        }
      } else if (split.operator === 'message_type') {
        next = withFilterValue(next, 'message_type', value.toLowerCase());
        moved = true;
        continue;
      } else if ((split.operator === 'list' || split.operator === 'list-id') && !value.startsWith('(')) {
        next = withFilterValue(next, 'mailing_list', value);
        moved = true;
        continue;
      }
    }
    kept.push(token);
  }
  return { query: moved ? kept.join(' ') : query, filters: moved ? next : [...filters], moved };
}

export interface QueryOperatorChip {
  /** Position of the token in tokenizeQuery(query). */
  index: number;
  token: string;
  label: string;
}

const OPERATOR_LABELS: Record<string, string> = {
  from: 'From', to: 'To', cc: 'Cc', bcc: 'Bcc', subject: 'Subject', label: 'Label', l: 'Label',
  list: 'List', 'list-id': 'List', before: 'Before', after: 'After', older_than: 'Older than',
  newer_than: 'Newer than', larger: 'Larger than', smaller: 'Smaller than', message_type: 'Type',
  conversation_id: 'Conversation',
};

function operatorChipLabel(operator: string, value: string): string {
  if (operator === 'has') return /^attachments?$/i.test(value) ? 'Has attachment' : `Has ${value}`;
  return `${OPERATOR_LABELS[operator] ?? operator}: ${value}`;
}

/** The operators still in the query text, each as a chip the user can remove. */
export function queryOperatorChips(query: string): QueryOperatorChip[] {
  return tokenizeQuery(query).flatMap((token, index) => {
    if (!isOperatorToken(token)) return [];
    const split = splitOperatorToken(token)!;
    return [{ index, token, label: operatorChipLabel(split.operator, unquoteValue(split.value)) }];
  });
}

/** The query without the token at `index` (as numbered by tokenizeQuery). */
export function withoutQueryToken(query: string, index: number): string {
  return tokenizeQuery(query).filter((_, position) => position !== index).join(' ');
}

const HAS_ATTACHMENT = /^has:"?attachments?"?$/i;

export function queryHasAttachmentOperator(query: string): boolean {
  return tokenizeQuery(query).some((token) => HAS_ATTACHMENT.test(token));
}

/** Adds or removes has:attachment, which has no filter dimension and so
 * lives in the query text. */
export function withAttachmentOperator(query: string, wanted: boolean): string {
  const tokens = tokenizeQuery(query).filter((token) => !HAS_ATTACHMENT.test(token));
  return (wanted ? [...tokens, 'has:attachment'] : tokens).join(' ');
}
