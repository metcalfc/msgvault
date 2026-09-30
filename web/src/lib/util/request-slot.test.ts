import { describe, expect, it } from 'vitest';
import { RequestSlot } from './request-slot';

describe('RequestSlot', () => {
  it('aborts a superseded read and prevents its cleanup from settling the new read', () => {
    const slot = new RequestSlot();
    const old = slot.begin();
    const current = slot.begin();
    expect(old.signal.aborted).toBe(true);
    expect(slot.owns(old)).toBe(false);
    expect(slot.finish(old)).toBe(false);
    expect(slot.owns(current)).toBe(true);
    expect(slot.finish(current)).toBe(true);
    expect(slot.finish(current)).toBe(false);
  });

  it('invalidates cancelled reads even if their transport later completes', () => {
    const slot = new RequestSlot();
    const old = slot.begin();
    slot.cancel();
    expect(old.signal.aborted).toBe(true);
    expect(slot.finish(old)).toBe(false);
    const current = slot.begin();
    expect(slot.owns(current)).toBe(true);
  });
});
