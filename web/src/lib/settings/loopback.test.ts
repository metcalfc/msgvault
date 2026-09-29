import { describe, expect, it } from 'vitest';

import { isLoopbackURL } from './loopback';

describe('isLoopbackURL', () => {
  it.each([
    ['http://localhost:11434/v1', true],
    ['http://127.0.0.1:8080', true],
    ['http://[::1]:8080/v1', true],
    ['http://embed.localhost/v1', true],
    ['https://api.example.com/v1', false],
    ['http://10.0.0.5:8080', false],
    ['http://127.example.com', false],
    ['', false],
    [undefined, false],
    ['not a url', false],
  ])('reads %j as local: %s', (value, want) => {
    expect(isLoopbackURL(value)).toBe(want);
  });
});
