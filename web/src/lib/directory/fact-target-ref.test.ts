import { describe, expect, it } from 'vitest';

import { encodeFactTargetRef } from './fact-target-ref';

const revision = `sha256:${'a'.repeat(64)}`;

describe('fact target references', () => {
  it('encodes colon-bearing keys with the revision suffix', () => {
    const encoded = encodeFactTargetRef({ kind: 'attribute', key: 'work:email:primary', revision });

    expect(encoded).toBe(`attribute:work:email:primary:${revision}`);
  });

  it.each([
    [{ kind: 'relationship', key: 'email', revision }],
    [{ kind: 'attribute', key: '', revision }],
    [{ kind: 'attribute', key: ' email ', revision }],
    [{ kind: 'employment', key: 'role', revision: 'v1' }],
    [{ kind: 'employment', key: 'role', revision: `sha256:${'A'.repeat(64)}` }],
    [{ kind: 'employment', key: 'role', revision: ` sha256:${'a'.repeat(64)}` }]
  ])('rejects a malformed generated target %#', (target) => {
    expect(encodeFactTargetRef(target)).toBeUndefined();
  });

});
