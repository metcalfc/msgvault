import { describe, expect, it } from 'vitest';

import { applySuggestion, removeSpanAt, suggestionAccessibleName, type QuerySuggestion } from './suggestions';

const lastWeek: QuerySuggestion = {
  kind: 'time_window',
  label: 'Past 7 days (Sep 24 to Sep 30, 2026)',
  span: 'last week',
  spanStart: 6,
  spanEnd: 15,
  probability: 0.9,
  filters: [
    { dimension: 'after', values: ['2026-09-24T00:00:00-07:00'] },
    { dimension: 'before', values: ['2026-09-30T23:59:59.999-07:00'] },
  ],
  queryOperators: [],
};

describe('removeSpanAt', () => {
  it('removes the span at its position and tidies spacing and punctuation', () => {
    const query = 'texts from Jane Doe about the lease last week';
    expect(removeSpanAt(query, 6, 19).query).toBe('texts about the lease last week');
    expect(removeSpanAt('lease, last week, and deposit', 7, 16).query).toBe('lease, and deposit');
    expect(removeSpanAt('last week budget', 0, 9).query).toBe('budget');
    expect(removeSpanAt('budget, last week', 8, 17).query).toBe('budget');
  });

  it('maps later indexes onto the new query', () => {
    const removal = removeSpanAt('emails from Ana last week', 7, 15);
    expect(removal.query).toBe('emails last week');
    expect(removal.map(0)).toBe(0);
    expect(removal.map(9)).toBeUndefined();
    expect(removal.map(16)).toBe(7);
    expect(removal.map(25)).toBe(16);
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
      kind: 'person', label: 'With Ana Example', span: 'with Ana', spanStart: 6, spanEnd: 14, probability: 0.95,
      filters: [{ dimension: 'participant', values: ['7'] }], queryOperators: [],
    });
    expect(withPerson.query).toBe('notes');
    expect(withPerson.filters).toEqual([
      { dimension: 'participant', values: ['4'] },
      { dimension: 'participant', values: ['7'] },
    ]);

    const fromPerson = applySuggestion('notes from Ana', [], {
      kind: 'person', label: 'From Ana Example', span: 'from Ana', spanStart: 6, spanEnd: 14, probability: 0.95,
      filters: [], queryOperators: ['from:ana@example.com'],
    });
    expect(fromPerson).toMatchObject({ query: 'notes from:ana@example.com', filters: [] });
  });

  it('removes the candidate occurrence, not an earlier quoted copy', () => {
    const query = '"last week" notes last week';
    const applied = applySuggestion(query, [], { ...lastWeek, spanStart: 18, spanEnd: 27 });
    expect(applied.query).toBe('"last week" notes');
  });

  it('leaves the query alone when the span moved', () => {
    expect(applySuggestion('notes about rent', [], lastWeek).query).toBe('notes about rent');
  });

  it('rebases the other suggestions onto the rewritten query', () => {
    const query = 'texts from Ana last week';
    const texts: QuerySuggestion = {
      kind: 'message_type', label: 'Texts', span: 'texts', spanStart: 0, spanEnd: 5, probability: 0.9,
      filters: [{ dimension: 'message_type', values: ['sms'] }], queryOperators: [],
    };
    const person: QuerySuggestion = {
      kind: 'person', label: 'With Ana', span: 'from Ana', spanStart: 6, spanEnd: 14, probability: 0.9,
      filters: [{ dimension: 'participant', values: ['7'] }], queryOperators: [],
    };
    const week = { ...lastWeek, spanStart: 15, spanEnd: 24 };
    const first = applySuggestion(query, [], person);
    expect(first.query).toBe('texts last week');
    const [textsNext, weekNext] = [texts, week].map(first.rebase);
    expect(first.query.slice(textsNext!.spanStart, textsNext!.spanEnd)).toBe('texts');
    expect(first.query.slice(weekNext!.spanStart, weekNext!.spanEnd)).toBe('last week');
    const second = applySuggestion(first.query, first.filters, weekNext!);
    expect(second.query).toBe('texts');
    expect(applySuggestion(second.query, second.filters, second.rebase(textsNext!)).query).toBe('');
  });

  it('replaces a message type or account filter', () => {
    const applied = applySuggestion('texts about rent', [{ dimension: 'message_type', values: ['email'] }], {
      kind: 'message_type', label: 'Texts', span: 'texts', spanStart: 0, spanEnd: 5, probability: 0.9,
      filters: [{ dimension: 'message_type', values: ['sms', 'imessage'] }], queryOperators: [],
    });
    expect(applied).toMatchObject({
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
