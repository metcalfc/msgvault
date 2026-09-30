import { describe, expect, it } from 'vitest';

import {
  contactLink, contactPointLinkInput, formattedAddress, mapLink, organizationIdentifierLink, type ContactLinkInput
} from './contact-links';

describe('contactLink', () => {
  const linked: Array<[string, ContactLinkInput, string, boolean]> = [
    // Email and phone
    ['email', { kind: 'email', value: 'ada@example.com' }, 'mailto:ada@example.com', false],
    ['email with plus tag', { kind: 'email', value: 'ada+news@example.com' }, 'mailto:ada%2Bnews@example.com', false],
    ['email prefers normalized', { kind: 'email', value: ' Ada@Example.com ', normalized: 'ada@example.com' }, 'mailto:ada@example.com', false],
    ['E.164 phone', { kind: 'phone', value: '+15555550123' }, 'tel:+15555550123', false],
    ['formatted E.164 phone', { kind: 'phone', value: '+1 (555) 555-0123' }, 'tel:+15555550123', false],
    ['phone from normalized', { kind: 'phone', value: '(555) 555-0123', normalized: '+15555550123' }, 'tel:+15555550123', false],
    // Stored URI
    ['stored https uri', { kind: 'social', service: 'github', value: 'ada', uri: 'https://github.com/ada-lovelace' }, 'https://github.com/ada-lovelace', true],
    ['stored http uri upgraded', { kind: 'url', value: 'example.com', uri: 'http://example.com/ada' }, 'https://example.com/ada', true],
    ['stored mailto uri', { kind: 'contact_uri', value: 'Ada', uri: 'mailto:ada@example.com' }, 'mailto:ada@example.com', false],
    ['disallowed uri falls through to template', { kind: 'impp', service: 'telegram', value: 'ada_lovelace', uri: 'tg://resolve?domain=ada_lovelace', services: { telegram: { profile_url_template: 'https://t.me/{username}', uri_scheme: 'tg' } } }, 'https://t.me/ada_lovelace', true],
    // Value already a URL
    ['https value', { kind: 'url', value: 'https://example.com/about' }, 'https://example.com/about', true],
    ['http value upgraded', { kind: 'url', value: 'http://example.com:80/about' }, 'https://example.com/about', true],
    ['bare host url', { kind: 'url', value: 'www.example.com/ada' }, 'https://www.example.com/ada', true],
    ['linkedin full url', { kind: 'social', service: 'linkedin', value: 'https://www.linkedin.com/in/ada-lovelace/' }, 'https://www.linkedin.com/in/ada-lovelace/', true],
    ['scheme-less profile url', { kind: 'social', value: 'github.com/ada' }, 'https://github.com/ada', true],
    // Server template
    ['template fills stripped handle', { kind: 'social', service: 'github', value: '@Ada', services: { github: { profile_url_template: 'https://github.com/{username}' } } }, 'https://github.com/Ada', true],
    ['template uses normalized', { kind: 'handle', service: 'x', value: '@Ada', normalized: 'ada', services: { x: { profile_url_template: 'https://x.com/{username}' } } }, 'https://x.com/ada', true],
    ['template encodes the handle', { kind: 'handle', service: 'reddit', value: 'a&b', services: { reddit: { profile_url_template: 'https://www.reddit.com/user/{username}' } } }, 'https://www.reddit.com/user/a%26b', true],
    // Built-in fallbacks
    ['github', { kind: 'social', service: 'github', value: 'ada-l' }, 'https://github.com/ada-l', true],
    ['mastodon', { kind: 'social', service: 'mastodon', value: '@ada@social.example.org' }, 'https://social.example.org/@ada', true],
    ['mastodon without leading at', { kind: 'social', service: 'mastodon', value: 'ada@Social.Example.org' }, 'https://social.example.org/@ada', true],
    ['linkedin slug', { kind: 'social', service: 'linkedin', value: 'ada-lovelace' }, 'https://www.linkedin.com/in/ada-lovelace', true],
    ['linkedin in/slug', { kind: 'social', service: 'linkedin', value: 'in/ada-lovelace' }, 'https://www.linkedin.com/in/ada-lovelace', true],
    ['linkedin company', { kind: 'social', service: 'linkedin', value: 'company/example-co' }, 'https://www.linkedin.com/company/example-co', true],
    ['linkedin host path', { kind: 'social', service: 'linkedin', value: 'linkedin.com/in/ada' }, 'https://www.linkedin.com/in/ada', true],
    ['linkedin template skipped for a path', { kind: 'social', service: 'linkedin', value: 'company/example-co', services: { linkedin: { profile_url_template: 'https://www.linkedin.com/in/{username}' } } }, 'https://www.linkedin.com/company/example-co', true],
    ['x', { kind: 'handle', service: 'x', value: '@ada_l' }, 'https://x.com/ada_l', true],
    ['twitter alias', { kind: 'handle', service: 'twitter', value: 'ada_l' }, 'https://x.com/ada_l', true],
    ['bluesky', { kind: 'handle', service: 'bluesky', value: '@ada.bsky.social' }, 'https://bsky.app/profile/ada.bsky.social', true],
    ['instagram', { kind: 'handle', service: 'instagram', value: 'ada.l' }, 'https://www.instagram.com/ada.l', true],
    ['facebook', { kind: 'handle', service: 'facebook', value: 'ada.lovelace' }, 'https://www.facebook.com/ada.lovelace', true],
    ['threads', { kind: 'handle', service: 'threads', value: '@ada.l' }, 'https://www.threads.com/@ada.l', true],
    ['youtube handle', { kind: 'handle', service: 'youtube', value: '@AdaTalks' }, 'https://www.youtube.com/@AdaTalks', true],
    ['youtube channel id', { kind: 'handle', service: 'youtube', value: 'UCabcdefghijklmnopqrstuv' }, 'https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv', true],
    // The daemon's seeded template and strip_at_lower normalization must not
    // lowercase a case-sensitive channel ID into an @handle.
    ['youtube channel id with the seeded template', { kind: 'handle', service: 'youtube', value: 'UCAbCdEfGhIjKlMnOpQrStUv',
      normalized: 'ucabcdefghijklmnopqrstuv', services: { youtube: { profile_url_template: 'https://www.youtube.com/@{username}' } } },
    'https://www.youtube.com/channel/UCAbCdEfGhIjKlMnOpQrStUv', true],
    ['youtube handle with the seeded template', { kind: 'handle', service: 'youtube', value: '@AdaTalks', normalized: 'adatalks',
      services: { youtube: { profile_url_template: 'https://www.youtube.com/@{username}' } } }, 'https://www.youtube.com/@adatalks', true],
    ['bluesky did', { kind: 'handle', service: 'bluesky', value: 'did:plc:abcdefghijklmnopqrstuvwx' }, 'https://bsky.app/profile/did:plc:abcdefghijklmnopqrstuvwx', true],
    ['bluesky did with the seeded template', { kind: 'handle', service: 'bluesky', value: 'did:plc:abcdefghijklmnopqrstuvwx',
      services: { bluesky: { profile_url_template: 'https://bsky.app/profile/{username}' } } }, 'https://bsky.app/profile/did:plc:abcdefghijklmnopqrstuvwx', true],
    ['matrix', { kind: 'impp', service: 'matrix', value: '@ada:example.org' }, 'https://matrix.to/#/%40ada%3Aexample.org', true],
    ['telegram', { kind: 'impp', service: 'telegram', value: '@ada_lovelace' }, 'https://t.me/ada_lovelace', true]
  ];

  it.each(linked)('links %s', (_name, input, href, external) => {
    const link = contactLink(input);
    expect(link?.href).toBe(href);
    expect(link?.external).toBe(external);
    expect(link?.label).toBeTruthy();
  });

  const refused: Array<[string, ContactLinkInput]> = [
    ['javascript url', { kind: 'url', value: 'javascript:alert(1)' }],
    ['mixed-case javascript url', { kind: 'social', service: 'github', value: 'JaVaScRiPt:alert(1)' }],
    ['javascript stored uri with no fallback', { kind: 'url', value: 'not a url', uri: 'javascript:alert(1)' }],
    ['data url', { kind: 'url', value: 'data:text/html,<script>alert(1)</script>' }],
    ['vbscript url', { kind: 'url', value: 'vbscript:msgbox(1)' }],
    ['file url', { kind: 'url', value: 'file:///etc/passwd' }],
    ['sms url', { kind: 'impp', value: 'sms:+15555550123' }],
    ['tg url', { kind: 'impp', service: 'telegram', value: 'tg://resolve?domain=ada' }],
    ['matrix url', { kind: 'impp', service: 'matrix', value: 'matrix:u/ada:example.org' }],
    ['credentials in url', { kind: 'url', value: 'https://user:secret@example.com/' }],
    ['username in url', { kind: 'url', value: 'https://user@example.com/' }],
    ['loopback host', { kind: 'url', value: 'http://127.0.0.1:8080/api' }],
    ['localhost', { kind: 'url', value: 'https://localhost/' }],
    ['private host', { kind: 'url', value: 'https://192.168.1.1/' }],
    ['single-label host', { kind: 'url', value: 'https://intranet/' }],
    ['dot-local host', { kind: 'url', value: 'https://printer.local/' }],
    ['template to a private host', { kind: 'handle', service: 'custom', value: 'ada', services: { custom: { profile_url_template: 'http://10.0.0.5/{username}' } } }],
    ['template with a javascript scheme', { kind: 'handle', service: 'custom', value: 'ada', services: { custom: { profile_url_template: 'javascript:alert("{username}")' } } }],
    ['non-E.164 phone', { kind: 'phone', value: '555-0123' }],
    ['national phone', { kind: 'phone', value: '(555) 555-0123' }],
    ['phone with leading zero country', { kind: 'phone', value: '+0155555501' }],
    ['phone too long', { kind: 'phone', value: '+1234567890123456' }],
    ['tel with non-E.164', { kind: 'contact_uri', value: 'tel:5550123' }],
    ['invalid email', { kind: 'email', value: 'not-an-email' }],
    ['email with header injection', { kind: 'email', value: 'ada@example.com?bcc=eve@example.com' }],
    ['unknown service handle', { kind: 'handle', service: 'kakaotalk', value: 'ada' }],
    ['invalid github handle', { kind: 'social', service: 'github', value: 'ada lovelace' }],
    ['mastodon to a private host', { kind: 'social', service: 'mastodon', value: '@ada@10.0.0.1' }],
    ['empty value', { kind: 'url', value: '  ' }],
    // Dot segments would resolve away and retarget the link.
    ['dot-dot handle through a template', { kind: 'handle', service: 'linkedin', value: '..', services: { linkedin: { profile_url_template: 'https://www.linkedin.com/in/{username}' } } }],
    ['dot handle through a template', { kind: 'handle', service: 'github', value: '@.', services: { github: { profile_url_template: 'https://github.com/{username}' } } }],
    ['all-dots handle through a suffixed template', { kind: 'handle', service: 'custom', value: '...', services: { custom: { profile_url_template: 'https://example.com/u/{username}/profile' } } }],
    ['dot-dot through a suffixed template', { kind: 'handle', service: 'custom', value: '..', services: { custom: { profile_url_template: 'https://example.com/u/{username}/profile' } } }],
    ['dot-dot instagram handle', { kind: 'handle', service: 'instagram', value: '..' }],
    ['dot-dot facebook handle', { kind: 'handle', service: 'facebook', value: '..' }],
    ['dot-dot linkedin slug', { kind: 'social', service: 'linkedin', value: 'in/..' }],
    ['encoded dot-dot linkedin slug', { kind: 'social', service: 'linkedin', value: '%2e%2e' }],
    ['other scheme for bluesky', { kind: 'handle', service: 'bluesky', value: 'javascript:alert(1)' }],
    ['non-plc did for bluesky', { kind: 'handle', service: 'bluesky', value: 'did:web:example.com' }],
    ['encoded bcc in a mailto uri', { kind: 'contact_uri', value: 'Ada', uri: 'mailto:ada@example.com%3Fbcc=eve@example.com' }],
    ['encoded cc in an email value', { kind: 'email', value: 'ada@example.com%3Fcc%3Deve@example.com' }],
    ['ampersand body in an email domain', { kind: 'email', value: 'ada@example.com&body=hi' }]
  ];

  it.each(refused)('refuses %s', (_name, input) => {
    expect(contactLink(input)).toBeUndefined();
  });

  it('allows the mailto of a clean address even when the stored uri adds headers', () => {
    for (const uri of [
      'mailto:ada@example.com?subject=hi', 'MAILTO:ada@example.com?CC=eve@example.com',
      'mailto:ada@example.com?Bcc=eve@example.com&body=hi'
    ]) {
      expect(contactLink({ kind: 'contact_uri', value: 'Ada', uri })?.href).toBe('mailto:ada@example.com');
    }
  });

  it('keeps a template that places the handle before a suffix path', () => {
    expect(contactLink({ kind: 'handle', service: 'custom', value: 'ada', services: {
      custom: { profile_url_template: 'https://example.com/u/{username}/profile' }
    } })?.href).toBe('https://example.com/u/ada/profile');
  });
});

describe('mapLink', () => {
  it('uses exact coordinates from a geo uri', () => {
    expect(mapLink({ geo_uri: 'geo:37.7749,-122.4194;u=35', original_value: '1 Main St' })?.href)
      .toBe('https://www.google.com/maps/search/?api=1&query=37.7749%2C-122.4194');
  });

  it('searches the formatted address without coordinates', () => {
    const link = mapLink({ original_value: '1 Main St\nSpringfield' });
    expect(link?.href).toBe('https://www.google.com/maps/search/?api=1&query=1%20Main%20St%2C%20Springfield');
    expect(link?.external).toBe(true);
  });

  it('ignores an out-of-range geo uri and falls back to the address', () => {
    expect(mapLink({ geo_uri: 'geo:99,500', locality: 'Springfield', region: 'IL' })?.href)
      .toBe('https://www.google.com/maps/search/?api=1&query=Springfield%2C%20IL');
  });

  it('returns nothing for an empty address', () => {
    expect(mapLink({})).toBeUndefined();
  });

  it('joins components when there is no stored text', () => {
    expect(formattedAddress({ street_address: '1 Main St', locality: 'Springfield', country_name: 'US' }))
      .toBe('1 Main St, Springfield, US');
  });
});

describe('organizationIdentifierLink', () => {
  it('links a primary domain over https', () => {
    expect(organizationIdentifierLink('domain', 'example.com')?.href).toBe('https://example.com/');
  });

  it('refuses a private or single-label domain', () => {
    expect(organizationIdentifierLink('domain', 'intranet')).toBeUndefined();
    expect(organizationIdentifierLink('domain', 'corp.internal')).toBeUndefined();
  });

  it('links a LinkedIn slug as a company page and keeps a full path', () => {
    expect(organizationIdentifierLink('linkedin', 'example-co')?.href).toBe('https://www.linkedin.com/company/example-co');
    expect(organizationIdentifierLink('linkedin', 'school/example-university')?.href)
      .toBe('https://www.linkedin.com/school/example-university');
    expect(organizationIdentifierLink('linkedin', 'https://www.linkedin.com/company/example-co/')?.href)
      .toBe('https://www.linkedin.com/company/example-co/');
  });

  it('leaves registry identifiers unlinked', () => {
    expect(organizationIdentifierLink('duns', '123456789')).toBeUndefined();
    expect(organizationIdentifierLink('tax_id', '12-3456789')).toBeUndefined();
  });
});

describe('contactPointLinkInput', () => {
  it('passes the stored template for the point service', () => {
    expect(contactPointLinkInput({
      address_kind: 'username', service_slug: 'GitHub', original_value: '@Example', normalized_value: 'example',
      profile_url_template: 'https://github.com/{username}'
    })).toEqual({
      kind: 'username', service: 'github', value: '@Example', normalized: 'example', uri: undefined,
      services: { github: { profile_url_template: 'https://github.com/{username}' } }
    });
  });
});

