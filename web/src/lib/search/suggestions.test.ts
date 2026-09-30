import { describe, expect, it } from 'vitest';

import { applySuggestion, removeSpan, suggestionAccessibleName, type QuerySuggestion } from './suggestions';

const lastWeek: QuerySuggestion = {
  kind: 'time_window',
  label: 'Past 7 days (Sep 24 to Sep 30, 2026)',
  span: 'last week',
  probability: 0.9,
  filters: [
    { dimension: 'after', values: ['2026-09-24T00:00:00-07:00'] },
    { dimension: 'before', values: ['2026-09-30T23:59:59.999-07:00'] },
  ],
  queryOperators: [],
};

describe('removeSpan', () => {
  it('removes the first exact occurrence and tidies spacing and punctuation', () => {
    expect(removeSpan('texts from Jane Doe about the lease last week', 'from Jane Doe')).toBe(
      'texts about the lease last week',
    );
    expect(removeSpan('lease, last week, and deposit', 'last week')).toBe('lease, and deposit');
    expect(removeSpan('last week budget', 'last week')).toBe('budget');
  });

  it('leaves the query alone when the span is gone', () => {
    expect(removeSpan('lease renewal', 'last week')).toBe('lease renewal');
    expect(removeSpan('lease renewal', undefined)).toBe('lease renewal');
  });
});

describe('applySuggestion', () => {
  it('replaces date bounds and removes the phrase', () => {
    const applied = applySuggestion(
      'lease last week',
      [
        { dimension: 'before', values: ['2026-12-31T23:59:59.999Z'] },
        { dimension: 'participant', values: ['4'] },
      ],
      lastWeek,
    );
    expect(applied.query).toBe('lease');
    expect(applied.filters).toEqual([
      { dimension: 'participant', values: ['4'] },
      { dimension: 'after', values: ['2026-09-24T00:00:00-07:00'] },
      { dimension: 'before', values: ['2026-09-30T23:59:59.999-07:00'] },
    ]);
  });

  it('adds a person as another participant group and operators to the query', () => {
    const withPerson = applySuggestion('notes with Ana', [{ dimension: 'participant', values: ['4'] }], {
      kind: 'person', label: 'With Ana Example', span: 'with Ana', probability: 0.95,
      filters: [{ dimension: 'participant', values: ['7'] }], queryOperators: [],
    });
    expect(withPerson.query).toBe('notes');
    expect(withPerson.filters).toEqual([
      { dimension: 'participant', values: ['4'] },
      { dimension: 'participant', values: ['7'] },
    ]);

    const fromPerson = applySuggestion('notes from Ana', [], {
      kind: 'person', label: 'From Ana Example', span: 'from Ana', probability: 0.95,
      filters: [], queryOperators: ['from:ana@example.com'],
    });
    expect(fromPerson).toEqual({ query: 'notes from:ana@example.com', filters: [] });
  });

  it('replaces a message type or account filter', () => {
    const applied = applySuggestion('texts about rent', [{ dimension: 'message_type', values: ['email'] }], {
      kind: 'message_type', label: 'Texts', span: 'texts', probability: 0.9,
      filters: [{ dimension: 'message_type', values: ['sms', 'imessage'] }], queryOperators: [],
    });
    expect(applied).toEqual({
      query: 'about rent',
      filters: [{ dimension: 'message_type', values: ['sms', 'imessage'] }],
    });
  });
});

describe('suggestionAccessibleName', () => {
  it('says what the chip applies and what it replaces', () => {
    expect(suggestionAccessibleName(lastWeek)).toBe(
      'Apply suggested time period filter: Past 7 days (Sep 24 to Sep 30, 2026), replacing “last week” in the search',
    );
  });
});
