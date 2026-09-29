import { describe, expect, it } from 'vitest';

import { distinctLabels } from './distinct';

describe('distinctLabels', () => {
  it('numbers only repeated labels by their position among the repeats', () => {
    expect(distinctLabels(['Avery', 'Blair', 'Avery', 'Avery'])).toEqual([
      'Avery (1 of 3)', 'Blair', 'Avery (2 of 3)', 'Avery (3 of 3)'
    ]);
  });

  it('leaves distinct labels unchanged', () => {
    expect(distinctLabels(['Avery', 'Blair'])).toEqual(['Avery', 'Blair']);
  });
});
