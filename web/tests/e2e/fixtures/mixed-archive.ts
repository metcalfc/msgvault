import { execFileSync } from 'node:child_process';
import { readFileSync, unlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Page, Route } from '@playwright/test';
import type {
  EntryRow,
  ExploreHTTPResponse,
  IdentityMatchCandidate,
  Person,
  PersonMerge,
  PersonMergeDetail,
  PersonMergeSummary,
  PersonSplit,
  PersonSplitResult,
} from '../../../src/lib/api/generated/models';

export const CHAT_CONVERSATION_COUNT = 100;
export const RAW_CHAT_MESSAGE_COUNT = 100_000;
const FIXTURE_TIME = '2026-01-03T12:00:00Z';

const archivePerson = {
  id: 12,
  display_label: 'Archive Person',
  display_name: 'Archive Person',
  partial_label: false,
  identifiers: [
    {
      type: 'email',
      value: 'person@example.com',
      display_value: 'person@example.com',
      participant_id: 12,
      is_primary: true,
      provenance: 'participant_identifiers',
    },
  ],
  activity_count: 3,
  file_count: 1,
  source_counts: [{ source_type: 'synthetic', count: 3 }],
  first_at: FIXTURE_TIME,
  last_at: FIXTURE_TIME,
  cache_revision: 'mixed-100k',
};

const archiveDomain = {
  domain: 'example.com',
  activity_count: 3,
  person_count: 1,
  file_count: 1,
  source_counts: [{ source_type: 'synthetic', count: 3 }],
  first_at: FIXTURE_TIME,
  last_at: FIXTURE_TIME,
  cache_revision: 'mixed-100k',
};

type MixedArchiveFixture = {
  rawChatMessageCount: number;
  chatConversationCount: number;
  logicalRows: EntryRow[];
  firstPage: ExploreHTTPResponse;
};

export type CapturedTrackingRequest = {
  method: 'GET' | 'PUT';
  path: string;
  includeSensitive?: boolean;
  tracked?: boolean;
};

export type CapturedRelationshipReviewRequest = {
  method: string;
  path: string;
  status: string | null;
};

type InstalledMixedArchive = MixedArchiveFixture & {
  trackingRequests: CapturedTrackingRequest[];
  failNextTrackingMutation: () => void;
  failNextTrackingRead: () => void;
};

let fixturePromise: Promise<MixedArchiveFixture> | undefined;

export function loadMixedArchive(): Promise<MixedArchiveFixture> {
  fixturePromise ??= Promise.resolve().then(() => {
    const fixturePath = join(tmpdir(), `msgvault-mixed-archive-${process.pid}.json`);
    const repositoryRoot = dirname(fileURLToPath(new URL('../../../../package.json', import.meta.url)));
    execFileSync(
      'go',
      [
        'test',
        '-tags',
        'fts5 sqlite_vec',
        './internal/query',
        '-run',
        '^TestWriteMixedArchiveBrowserFixture$',
        '-count=1',
      ],
      {
        cwd: repositoryRoot,
        env: { ...process.env, MSGVAULT_MIXED_ARCHIVE_FIXTURE: fixturePath },
        stdio: ['ignore', 'pipe', 'pipe'],
      },
    );
    const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as MixedArchiveFixture;
    unlinkSync(fixturePath);
    return fixture;
  });
  return fixturePromise;
}

export async function installMixedArchive(page: Page): Promise<InstalledMixedArchive> {
  const fixture = await loadMixedArchive();
  const trackingRequests: CapturedTrackingRequest[] = [];
  let tracked = false;
  let trackedAt: string | null = null;
  let trackingMutationFailure = false;
  let trackingReadFailure = false;
  await page.route('**/api/session', sessionRoute);
  await page.route('**/api/v1/reviews/pending', (route) => route.fulfill({ json: { pending: false, kinds: [] } }));
  await page.route(/\/api\/v1\/entity-labels(?:\?.*)?$/, (route) => route.fulfill({ json: entityLabels(route.request().url()) }));
  await page.route('**/api/v1/settings', (route) =>
    route.fulfill({
      json: {
        settings: [
          { key: 'web.theme', value: { string: 'light' } },
          { key: 'web.density', value: { string: 'compact' } },
        ],
        pending_restart: false,
      },
    }),
  );
  await page.route('**/api/v1/explore', (route) => route.fulfill({ json: fixture.firstPage }));
  await page.route('**/api/v1/explore/groups', (route) =>
    route.fulfill({
      json: {
        rows: [
          {
            key: 'synthetic_chat',
            label: 'Synthetic chat',
            count: CHAT_CONVERSATION_COUNT,
            estimated_bytes: RAW_CHAT_MESSAGE_COUNT * 64,
            latest_at: '2026-01-02T12:00:00Z',
          },
        ],
        total_count: 1,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/explore/preflight', (route) =>
    route.fulfill({
      json: {
        count: 1,
        deletable_count: 1,
        estimated_bytes: 2048,
        cache_revision: 'mixed-100k',
        search_provenance: {},
        unavailable_actions: [],
        action_targets: [],
        operation_token: 'synthetic-operation',
        expires_at: '2026-01-03T12:05:00Z',
      },
    }),
  );
  await page.route('**/api/v1/relationships', (route) =>
    route.fulfill({
      json: {
        rows: [
          {
            canonical_id: 12,
            display_label: 'Archive Person',
            last_at: FIXTURE_TIME,
            member_ids: [12],
            score: 1,
            signals: {
              last_interaction_at: FIXTURE_TIME,
              meeting_count: 0,
              meetings_together: 0,
              modalities: 1,
              received_from_them: 1,
              sent_count: 2,
              sent_to_them: 1,
            },
          },
        ],
        total_count: 1,
        cache_revision: 'mixed-100k',
        identity_revision: 1,
      },
    }),
  );
  await page.route('**/api/v1/relationships/12/timeline', (route) =>
    route.fulfill({
      json: {
        canonical_id: 12,
        identity_revision: 1,
        cache_revision: 'mixed-100k',
        rows: [fixture.logicalRows[0]],
        total_count: 1,
      },
    }),
  );
  await page.route('**/api/v1/participants/search', (route) =>
    route.fulfill({
      json: {
        rows: [archivePerson],
        total_count: 1,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/participants/12', (route) => route.fulfill({ json: archivePerson }));
  await page.route('**/api/v1/participants/12/summary', (route) =>
    route.fulfill({
      json: {
        summary: archivePerson,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/participants/12/timeline', (route) =>
    route.fulfill({
      json: {
        rows: [fixture.logicalRows[0]],
        total_count: 1,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/participants/12/files/search', (route) =>
    route.fulfill({
      json: {
        files: [],
        total_count: 0,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/people/directory*', (route) =>
    route.fulfill({
      json: {
        people: [
          {
            id: 42,
            revision: 1,
            display_name: 'Archive Person',
            primary_channel: 'email',
            contact_state: 'active',
            categories: ['friend'],
            organizations: ['Example Org'],
          },
        ],
      },
    }),
  );
  await page.route('**/api/v1/people', (route) =>
    route.fulfill({
      status: 201,
      json: {
        id: 42,
        revision: 1,
        display_name: 'Archive Person',
        participant_ids: [12],
        vcard_uid: 'urn:uuid:archive-person',
        created_at: FIXTURE_TIME,
        updated_at: FIXTURE_TIME,
      },
    }),
  );
  await page.route('**/api/v1/people/42', (route) =>
    route.fulfill({
      json: {
        id: 42,
        revision: 1,
        display_name: 'Archive Person',
        participant_ids: [12],
        vcard_uid: 'urn:uuid:archive-person',
        created_at: FIXTURE_TIME,
        updated_at: FIXTURE_TIME,
      },
    }),
  );
  await page.route('**/api/v1/people/42/tracking', async (route) => {
    const request = route.request();
    if (request.method() === 'GET') {
      trackingRequests.push({ method: 'GET', path: '/api/v1/people/42/tracking' });
      if (trackingReadFailure) {
        trackingReadFailure = false;
        return route.fulfill({
          status: 503,
          json: {
            error: 'tracking_unavailable',
            message: 'forbidden-private-credential',
          },
        });
      }
      return route.fulfill({ json: { person_id: 42, tracked, tracked_at: trackedAt } });
    }
    if (request.method() === 'PUT') {
      const body = request.postDataJSON() as { tracked?: unknown } | null;
      if (!body || typeof body.tracked !== 'boolean' || Object.keys(body).length !== 1) {
        return route.fulfill({ status: 400, json: { error: 'bad_request', message: 'Invalid synthetic body.' } });
      }
      trackingRequests.push({ method: 'PUT', path: '/api/v1/people/42/tracking', tracked: body.tracked });
      tracked = body.tracked;
      trackedAt = tracked ? FIXTURE_TIME : null;
      if (trackingMutationFailure) {
        trackingMutationFailure = false;
        return route.fulfill({
          status: 503,
          json: {
            error: 'tracking_unavailable',
            message: 'forbidden-private-credential',
          },
        });
      }
      return route.fulfill({ json: { person_id: 42, tracked, tracked_at: trackedAt } });
    }
    return route.fulfill({
      status: 405,
      json: { error: 'method_not_allowed', message: 'Unsupported synthetic method.' },
    });
  });
  await page.route('**/api/v1/person-fact-targets*', (route) => {
    const url = new URL(route.request().url());
    const includeSensitive = url.searchParams.get('include_sensitive') === 'true';
    trackingRequests.push({
      method: 'GET',
      path: '/api/v1/person-fact-targets',
      includeSensitive,
    });
    const common = {
      kind: 'attribute',
      revision: 'forbidden-target-revision',
      value_type: 'text',
      cardinality: 'single',
      choices: null,
      fields: null,
      href: 'https://forbidden.example.test/profile',
      credential: 'forbidden-private-credential',
    };
    return route.fulfill({
      json: {
        version: 'v1',
        fingerprint: 'forbidden-catalog-fingerprint',
        targets: [
          {
            ...common,
            key: 'forbidden-target-key',
            slug: 'timezone',
            universal_id: 'forbidden-universal-id',
            description: 'Time zone',
            sensitive: false,
          },
          ...(includeSensitive
            ? [
                {
                  ...common,
                  key: 'forbidden-sensitive-target-key',
                  slug: 'private-note',
                  universal_id: 'forbidden-sensitive-universal-id',
                  description: 'Private note',
                  sensitive: true,
                },
              ]
            : []),
        ],
      },
    });
  });
  await page.route('**/api/v1/people/42/profile', (route) =>
    route.fulfill({
      json: {
        person: {
          id: 42,
          revision: 1,
          display_name: 'Archive Person',
          participant_ids: [12],
          vcard_uid: 'urn:uuid:archive-person',
          created_at: FIXTURE_TIME,
          updated_at: FIXTURE_TIME,
        },
        names: [
          {
            person_id: 42,
            name_kind: 'preferred',
            original_value: 'Archive Person',
            formatted: 'Archive Person',
            is_derived: false,
            envelope: { id: 1, sensitive: false, source: 'synthetic', created_at: FIXTURE_TIME },
          },
        ],
        contact_points: [],
        addresses: [],
        dates: [],
        categories: [],
        media: [],
      },
    }),
  );
  await page.route('**/api/v1/people/42/attributes*', (route) =>
    route.fulfill({
      json: {
        person_id: 42,
        attributes: [],
      },
    }),
  );
  await page.route('**/api/v1/people/42/contact-state', (route) =>
    route.fulfill({
      json: {
        person_id: 42,
        cadence_status: 'current',
        computed_at: FIXTURE_TIME,
        interaction_count: 3,
        stale: false,
        last_contact_at: FIXTURE_TIME,
      },
    }),
  );
  await page.route('**/api/v1/people/42/employments', (route) =>
    route.fulfill({
      json: {
        employments: [],
      },
    }),
  );
  await page.route('**/api/v1/people/42/relationships', (route) =>
    route.fulfill({
      json: {
        relationships: [],
      },
    }),
  );
  await page.route('**/api/v1/people/42/merges*', (route) =>
    route.fulfill({
      json: {
        merges: [],
        limit: 100,
        offset: Number(new URL(route.request().url()).searchParams.get('offset') ?? 0),
      },
    }),
  );
  await page.route('**/api/v1/people/42/network*', (route) =>
    route.fulfill({
      json: {
        root_person_id: 42,
        depth: Number(new URL(route.request().url()).searchParams.get('depth') ?? 1),
        truncated: false,
        nodes: [
          { id: 'person:42', kind: 'person', entity_id: 42, label: 'Archive Person', hop: 0 },
          { id: 'person:43', kind: 'person', entity_id: 43, label: 'Curated Peer', hop: 1 },
          { id: 'organization:21', kind: 'organization', entity_id: 21, label: 'Shared Organization', hop: 1 },
        ],
        edges: [
          {
            id: 'relationship:31',
            kind: 'relationship',
            source_node_id: 'person:42',
            target_node_id: 'person:43',
            relationship_type_slug: 'colleague',
            label: 'works with',
          },
          {
            id: 'employment:41',
            kind: 'employment',
            source_node_id: 'person:43',
            target_node_id: 'organization:21',
            label: 'Engineer',
          },
        ],
      },
    }),
  );
  await page.route('**/api/v1/relationship-types', (route) => route.fulfill({ json: { relationship_types: [] } }));
  await page.route('**/api/v1/people/42/days*', (route) =>
    route.fulfill({
      json: {
        person_id: 42,
        days: [{ local_date: '2026-01-03', entry_count: 3, event_count: 0, direct_count: 3 }],
        total_count: 1,
      },
    }),
  );
  await page.route('**/api/v1/people/42/files/search', (route) =>
    route.fulfill({
      json: {
        files: [
          {
            id: 42,
            key: 'file:42',
            entry_key: 'message:100001',
            message_id: 100001,
            conversation_id: 100001,
            occurred_at: FIXTURE_TIME,
            source_id: 1,
            source_type: 'synthetic',
            source_identifier: 'archive@example.com',
            containing_title: 'Synthetic email',
            filename: 'durable-person.pdf',
            mime_type: 'application/pdf',
            mime_family: 'pdf',
            size_bytes: 2048,
            content_state: 'metadata_only',
            content_available: false,
          },
        ],
        total_count: 1,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/domains/search', (route) =>
    route.fulfill({
      json: {
        rows: [archiveDomain],
        total_count: 1,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/domains/example.com', (route) => route.fulfill({ json: archiveDomain }));
  await page.route('**/api/v1/domains/example.com/summary', (route) =>
    route.fulfill({
      json: {
        summary: archiveDomain,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/domains/example.com/timeline', (route) =>
    route.fulfill({
      json: {
        rows: [fixture.logicalRows[0]],
        total_count: 1,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/domains/example.com/files/search', (route) =>
    route.fulfill({
      json: {
        files: [],
        total_count: 0,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/files/search', (route) =>
    route.fulfill({
      json: {
        files: [
          {
            id: 1,
            key: 'file:1',
            entry_key: 'message:100001',
            message_id: 100001,
            conversation_id: 100001,
            occurred_at: '2026-01-03T12:00:00Z',
            source_id: 1,
            source_type: 'synthetic',
            source_identifier: 'archive@example.com',
            containing_title: 'Synthetic email',
            filename: 'synthetic.txt',
            mime_type: 'text/plain',
            mime_family: 'text',
            size_bytes: 2048,
            content_state: 'unsupported',
            content_available: true,
          },
        ],
        total_count: 1,
        cache_revision: 'mixed-100k',
        search_provenance: {},
      },
    }),
  );
  await page.route('**/api/v1/files/1', (route) =>
    route.fulfill({
      json: {
        id: 1,
        message_id: 100001,
        conversation_id: 100001,
        filename: 'synthetic.txt',
        mime_type: 'text/plain',
        size_bytes: 2048,
        content_hash: 'a'.repeat(64),
        content_state: 'local_content',
        content_available: true,
      },
    }),
  );
  await page.route('**/api/v1/saved-views', (route) => route.fulfill({ json: { saved_views: [] } }));
  await page.route('**/api/v1/sources/status', (route) => route.fulfill({ json: { sources: [] } }));
  await page.route('**/api/v1/deletions', (route) => route.fulfill({ json: { manifests: [] } }));
  return {
    ...fixture,
    trackingRequests,
    failNextTrackingMutation(): void {
      trackingMutationFailure = true;
    },
    failNextTrackingRead(): void {
      trackingReadFailure = true;
    },
  };
}

type IdentityMatchCandidate = IdentityMatchCandidate;

export type CapturedReviewRequest = {
  method: string;
  path: string;
  query?: Record<string, string>;
  body?: unknown;
  headers: Record<string, string>;
};

export const FACT_LEDGER_FORBIDDEN_MARKERS = [
  'forbidden-target-key',
  'forbidden-target-revision',
  'forbidden-catalog-fingerprint',
  'forbidden-evidence-key',
  'forbidden-content-sha',
  'forbidden-source-ref',
  'forbidden-source-url',
  'forbidden-source-version',
  'forbidden-subject-ref',
  'forbidden-claim-key',
  'forbidden-program-id',
  'forbidden-program-version',
  'forbidden-program-fingerprint',
  'forbidden-value-fingerprint',
  'forbidden-decision-key',
  'forbidden-competing-claim-key',
  'forbidden-pin-actor',
  'forbidden-private-error',
  'forbidden-extension-field',
] as const;

const FACT_TARGET_REVISION = `sha256:${'d'.repeat(64)}`;

// Synthetic names the entity-labels endpoint answers with, so no surface
// falls back to an ID.
const ENTITY_LABELS: Record<'person' | 'participant' | 'organization', Record<number, string>> = {
  person: {
    7: 'Synthetic One', 9: 'Synthetic Two', 19: 'Synthetic Restored', 42: 'Archive Person',
    170: 'Avery Candidate', 180: 'Blair Candidate', 190: 'Casey Candidate',
  },
  participant: {
    171: 'avery@example.com', 181: 'blair@example.com', 191: 'casey@example.com',
    701: 'one@example.com', 702: 'one-alt@example.com', 901: 'lineage@example.com',
  },
  organization: {},
};

function entityLabels(requestURL: string) {
  const url = new URL(requestURL);
  const answer = (kind: keyof typeof ENTITY_LABELS) =>
    url.searchParams.getAll(kind).flatMap((value) => value.split(',')).map(Number)
      .filter((id) => ENTITY_LABELS[kind][id]).map((id) => ({ id, label: ENTITY_LABELS[kind][id] }));
  return { people: answer('person'), participants: answer('participant'), organizations: answer('organization') };
}

function reviewCandidate(id: number, overrides: Partial<IdentityMatchCandidate> = {}): IdentityMatchCandidate {
  return {
    id,
    left_id: id * 10,
    left_kind: 'person',
    right_id: id * 10 + 1,
    right_kind: 'participant',
    basis: 'stable_provider_id',
    normalized_value: `synthetic-${id}@example.com`,
    service_slug: 'synthetic-chat',
    scope_kind: 'workspace',
    scope_value: 'example-space',
    confidence: 0.82,
    source: 'synthetic_import',
    source_ref: `fixture-${id}`,
    state: 'candidate',
    created_at: '2026-01-01T10:00:00Z',
    updated_at: '2026-01-02T11:00:00Z',
    evidence: [
      {
        id: id * 100,
        candidate_id: id,
        evidence_kind: 'provider_identifier',
        source_id: 1,
        source_type: 'synthetic',
        source_identifier: 'synthetic-archive',
        sent_count: 2,
        source: 'synthetic_import',
        evidence_ref: `evidence-${id}`,
        detail: 'Both synthetic endpoints expose the same provider identifier.',
        created_at: '2026-01-01T10:05:00Z',
      },
    ],
    ...overrides,
  };
}

// A contact-only profile imported from an address book whose email matches
// an unbound archive participant: accepting it binds the participant.
function contactMatchCandidate(): IdentityMatchCandidate {
  return reviewCandidate(25, {
    left_kind: 'participant',
    left_id: 250,
    right_kind: 'person',
    right_id: 251,
    basis: 'email',
    normalized_value: 'ada@example.test',
    service_slug: undefined,
    scope_kind: undefined,
    scope_value: undefined,
    confidence: 1,
    source: 'system',
    source_ref: 'contact_match',
    evidence: [
      {
        id: 2500,
        candidate_id: 25,
        evidence_kind: 'contact_point_match',
        source: 'system',
        evidence_ref: 'person_contact_point:2510',
        detail: 'participant 250 participants.email_address=ada@example.test',
        created_at: '2026-01-01T10:05:00Z',
      },
    ],
  });
}

// A support desk that two synthetic agents replied from: the contact matcher
// holds it back as a shared mailbox instead of offering to bind it.
function sharedMailboxCandidate(): IdentityMatchCandidate {
  return reviewCandidate(26, {
    left_kind: 'participant',
    left_id: 260,
    right_kind: 'person',
    right_id: 261,
    basis: 'email',
    normalized_value: 'support@shop.example.test',
    service_slug: undefined,
    scope_kind: undefined,
    scope_value: undefined,
    confidence: 1,
    source: 'system',
    source_ref: 'contact_match',
    evidence: [],
  });
}

const sharedMailboxSignal = {
  address: 'support@shop.example.test',
  reasons: ['role_address', 'several_names'],
  names: ['Avery Stone', 'Blake Rivera'],
};

const reviewEndpointSummaries = [
  { kind: 'participant', id: 260, found: true, display_name: 'Shop Support', addresses: ['support@shop.example.test'] },
  { kind: 'person', id: 261, found: true, display_name: 'Avery Stone', addresses: ['support@shop.example.test'] },
  { kind: 'participant', id: 250, found: true, display_name: 'Ada Sender', addresses: ['ada@example.test'] },
  {
    kind: 'person', id: 251, found: true, display_name: 'Ada Contact',
    addresses: ['ada@example.test', '+15550100100'],
  },
];

/**
 * Installs the intercepted HTTP boundary used by the Directory review browser
 * journeys. This is deliberately not a live-backend fixture: generated-client
 * unit tests and API/store tests own exact transport and persistence proof.
 */
export async function installDirectoryReviewArchive(page: Page) {
  const archive = await installMixedArchive(page);
  const candidates = [
    reviewCandidate(17), reviewCandidate(18), reviewCandidate(19), contactMatchCandidate(), sharedMailboxCandidate(),
  ];
  const people = new Map([
    [7, reviewPerson(7, 4, 'Synthetic One')],
    [9, reviewPerson(9, 2, 'Synthetic Two')],
  ]);
  let phase: 'premerge' | 'postmerge' | 'postsplit' = 'premerge';
  const requests: CapturedReviewRequest[] = [];
  // Identity candidates still open light the Reviews dot.
  await page.route('**/api/v1/reviews/pending', (route) => {
    const waiting = candidates.some((candidate) => candidate.state === 'candidate' || candidate.state === 'conflict');
    return route.fulfill({ json: { pending: waiting, kinds: waiting ? ['identity'] : [] } });
  });
  const decisionGates = new Map<number, { promise: Promise<void>; release: () => void }>();
  const decisionFailures = new Map<number, string>();
  const relationshipRequests: CapturedRelationshipReviewRequest[] = [];
  let failNextRelationshipRead = false;
  let factCatalogMode: 'ready' | 'deferred' | 'failed' | 'malformed' = 'ready';
  let factCatalogGate: Promise<void> | null = null;

  await page.route(/\/api\/v1\/person-relationship-reviews(?:\?.*)?$/, (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const status = url.searchParams.get('status');
    relationshipRequests.push({ method: request.method(), path: url.pathname, status });
    if (failNextRelationshipRead) {
      failNextRelationshipRead = false;
      return route.fulfill({ status: 503, json: { error: 'unavailable', message: 'forbidden-remote-error-marker' } });
    }
    const reviews =
      status === 'pending'
        ? [
            {
              id: 41,
              person_id: 7,
              matched_person_id: 9,
              raw_related_value: 'urn:uuid:synthetic-related-person',
              raw_related_type: 'friend',
              value_kind: 'uri',
              status: 'pending',
              source: 'vcard_import',
              created_at: '2026-01-01T00:00:00Z',
              updated_at: '2026-01-02T00:00:00Z',
              source_ref: 'forbidden-source-ref-marker',
              source_resource_uid: 'forbidden-resource-uid-marker',
              created_by: 'forbidden-creator-marker',
              reviewed_by: 'forbidden-reviewer-marker',
              accepted_relationship_id: 999,
              vcard_identity: { properties: [{ value: 'forbidden-vcard-marker' }] },
              unexpected_href: 'https://forbidden.invalid/private',
              unexpected_header: 'Bearer forbidden-credential-marker',
            },
          ]
        : [];
    return route.fulfill({ json: { reviews } });
  });

  const capture = (request: import('@playwright/test').Request): CapturedReviewRequest => ({
    method: request.method(),
    path: new URL(request.url()).pathname,
    query: Object.fromEntries(new URL(request.url()).searchParams),
    body: request.postDataJSON() ?? undefined,
    headers: request.headers(),
  });

  await page.unroute('**/api/v1/person-fact-targets*');
  await page.route('**/api/v1/person-fact-targets*', async (route) => {
    requests.push(capture(route.request()));
    if (factCatalogMode === 'deferred') {
      await factCatalogGate;
      factCatalogMode = 'ready';
      factCatalogGate = null;
    }
    if (factCatalogMode === 'failed') {
      return route.fulfill({ status: 503, json: { error: 'unavailable', message: 'forbidden-private-error' } });
    }
    if (factCatalogMode === 'malformed') {
      return route.fulfill({
        json: {
          version: 'forbidden-target-revision',
          fingerprint: 'forbidden-catalog-fingerprint',
          targets: [
            {
              kind: 'attribute',
              key: 'forbidden-target-key',
              revision: FACT_TARGET_REVISION,
              value_type: 'string',
              cardinality: 'single',
              choices: null,
              fields: null,
              slug: 'forbidden-extension-field',
              universal_id: 'forbidden-extension-field',
              description: 'Malformed private note',
            },
          ],
        },
      });
    }
    return route.fulfill({
      json: {
        version: 'forbidden-target-revision',
        fingerprint: 'forbidden-catalog-fingerprint',
        targets: [
          {
            kind: 'attribute',
            key: 'forbidden-target-key',
            revision: FACT_TARGET_REVISION,
            value_type: 'string',
            cardinality: 'single',
            choices: null,
            fields: null,
            slug: 'forbidden-extension-field',
            universal_id: 'forbidden-extension-field',
            description: 'Private note',
            sensitive: true,
            extension: 'forbidden-extension-field',
          },
        ],
      },
    });
  });

  await page.route(
    /\/api\/v1\/people\/42\/fact-(?:evidence(?:-status-events)?|claims|decisions|pins)(?:\?.*)?$/,
    (route) => {
      const request = route.request();
      const captured = capture(request);
      requests.push(captured);
      const path = captured.path;
      const target = { kind: 'attribute', key: 'forbidden-target-key', revision: FACT_TARGET_REVISION };
      if (path.endsWith('/fact-evidence-status-events'))
        return route.fulfill({
          json: {
            events: [
              {
                id: 61,
                evidence_key: 'forbidden-evidence-key',
                generation_id: 62,
                source_version: 'forbidden-source-version',
                supported: false,
                reason: 'source-deleted',
                created_at: '2026-01-04T12:00:00Z',
                extension: 'forbidden-extension-field',
              },
            ],
          },
        });
      if (path.endsWith('/fact-evidence'))
        return route.fulfill({
          json: {
            evidence: [
              {
                id: 51,
                evidence_key: 'forbidden-evidence-key',
                authority: 'First party',
                directness: 'Direct',
                identity_score: 94,
                source_class: 'Message',
                event_time: '2026-01-01T12:00:00Z',
                recorded_time: '2026-01-02T12:00:00Z',
                created_at: '2026-01-03T12:00:00Z',
                supported: true,
                excerpt: 'Allowed synthetic evidence excerpt.',
                latest_status: {
                  id: 52,
                  evidence_key: 'forbidden-evidence-key',
                  generation_id: 53,
                  source_version: 'forbidden-source-version',
                  supported: true,
                  reason: 'source-reimported',
                  created_at: '2026-01-03T12:00:00Z',
                },
                content_sha256: 'forbidden-content-sha',
                source_ref: 'forbidden-source-ref',
                source_url: 'forbidden-source-url',
                source_version: 'forbidden-source-version',
                span_start: 4,
                span_end: 9,
                subject_person_id: 42,
                subject_ref: 'forbidden-subject-ref',
                extension: 'forbidden-extension-field',
              },
            ],
          },
        });
      if (path.endsWith('/fact-claims'))
        return route.fulfill({
          json: {
            claims: [
              {
                id: 71,
                claim_key: 'forbidden-claim-key',
                generation_id: 72,
                evidence_ids: [51],
                target,
                submitted_value: 'Allowed synthetic sensitive value.',
                normalized_value: 'Allowed normalized sensitive value.',
                relation: 'equals',
                origin: 'observed',
                valid_from: '2026-01-01T00:00:00Z',
                valid_until: null,
                confidence: { reported_score: 87 },
                created_at: '2026-01-03T12:00:00Z',
                program_id: 'forbidden-program-id',
                program_version: 'forbidden-program-version',
                program_fingerprint: 'forbidden-program-fingerprint',
                value_fingerprint: 'forbidden-value-fingerprint',
                extension: 'forbidden-extension-field',
              },
            ],
          },
        });
      if (path.endsWith('/fact-decisions'))
        return route.fulfill({
          json: {
            decisions: [
              {
                id: 81,
                decision_key: 'forbidden-decision-key',
                claim_key: 'forbidden-claim-key',
                competing_claim_key: 'forbidden-competing-claim-key',
                resolution_id: 82,
                action: 'project',
                reason: 'Highest score',
                projection: { kind: 'attribute', row_id: 83 },
                score: {
                  authority: 1,
                  confidence: 2,
                  corroboration: 3,
                  directness: 4,
                  freshness: 5,
                  source_class: 6,
                  total: 21,
                },
                created_at: '2026-01-03T12:00:00Z',
                extension: 'forbidden-extension-field',
              },
            ],
          },
        });
      if (path.endsWith('/fact-pins'))
        return route.fulfill({
          json: {
            pins: [
              {
                actor: 'forbidden-pin-actor',
                event_id: 91,
                pinned: true,
                target,
                extension: 'forbidden-extension-field',
              },
            ],
          },
        });
      return route.fulfill({ status: 503, json: { error: 'unavailable', message: 'forbidden-private-error' } });
    },
  );

  await page.route(/\/api\/v1\/identity\/match-candidates(?:\?.*)?$/, (route) => {
    requests.push(capture(route.request()));
    const url = new URL(route.request().url());
    // The daemon accepts a comma-separated state list.
    const states = (url.searchParams.get('state') ?? 'candidate').split(',');
    const offset = Number(url.searchParams.get('offset') ?? 0);
    const contactOnly = url.searchParams.get('origin') === 'contact_match';
    const listed = candidates.filter((candidate) =>
      states.includes(candidate.state) && (!contactOnly || candidate.source_ref === 'contact_match'));
    return route.fulfill({
      json: {
        candidates: listed,
        endpoints: reviewEndpointSummaries.filter((endpoint) =>
          listed.some((candidate) =>
            (candidate.left_kind === endpoint.kind && candidate.left_id === endpoint.id) ||
            (candidate.right_kind === endpoint.kind && candidate.right_id === endpoint.id))),
        contact_matches: listed
          .filter((candidate) => candidate.left_kind === 'participant' && candidate.right_kind === 'person')
          .map((candidate) => candidate.id === 26
            ? { candidate_id: candidate.id, classification: 'shared_mailbox', cluster_person_ids: [], shared_mailbox: sharedMailboxSignal }
            : { candidate_id: candidate.id, classification: 'bind', cluster_person_ids: [] }),
        limit: 100,
        offset,
      },
    });
  });

  const enrichmentReviews = [
    {
      attempt_id: 61, person_id: 7, person_display_name: 'Synthetic One', provider_name: 'exa',
      provider_kind: 'exa', exact_class: 'current_company', name_compatible: 0.7, company_same: 0.99,
      name_conflict: 0.1, model: 'jev-1.13.0', judged_at: '2026-01-02T10:00:00Z',
      provider_person_id_known: true,
      returned: {
        name: 'S. One', current_roles: [{ title: 'Engineer', company: 'Example Labs' }],
        location: 'Example City', profile_url_host: 'profiles.example.test',
      },
      claims: [{ target: 'location', value: 'Example City' }],
    },
    {
      attempt_id: 62, person_id: 9, person_display_name: 'Synthetic Two', provider_name: 'exa',
      provider_kind: 'exa', exact_class: 'name', name_compatible: 0.95, company_same: 0.6,
      name_conflict: 0.05, model: 'jev-1.13.0', judged_at: '2026-01-02T09:00:00Z',
      provider_person_id_known: false,
      returned: { current_roles: [], profile_url_host: 'profiles.example.test' },
      claims: [],
    },
  ];
  await page.route(/\/api\/v1\/person-enrichment\/identity-reviews(?:\?.*)?$/, (route) => {
    requests.push(capture(route.request()));
    return route.fulfill({ json: { reviews: enrichmentReviews, limit: 50 } });
  });
  await page.route(/\/api\/v1\/person-enrichment\/identity-reviews\/\d+\/(?:confirm|reject)$/, (route) => {
    const captured = capture(route.request());
    requests.push(captured);
    const match = captured.path.match(/\/(\d+)\/(confirm|reject)$/);
    const attemptID = Number(match?.[1]);
    const index = enrichmentReviews.findIndex((item) => item.attempt_id === attemptID);
    if (index < 0) {
      return route.fulfill({ status: 404, json: { error: 'enrichment_attempt_not_found', message: 'Not found.' } });
    }
    const [decided] = enrichmentReviews.splice(index, 1);
    const confirm = match?.[2] === 'confirm';
    return route.fulfill({
      json: {
        attempt_id: attemptID, person_id: decided!.person_id,
        decision: confirm ? 'confirmed' : 'rejected', reason: confirm ? 'user_confirmed' : 'user_rejected',
        attempt_state: confirm ? 'succeeded' : 'identity_rejected',
        projections: confirm ? 1 : 0, provider_identities_attached: confirm ? 1 : 0, negatives: confirm ? 0 : 1,
      },
    });
  });

  // Identities Jev could not classify wait in Reviews.
  await page.route(/\/api\/v1\/identity\/correspondent-kinds(?:\?.*)?$/, (route) => {
    const captured = capture(route.request());
    requests.push(captured);
    const unclear = new URL(route.request().url()).searchParams.get('kind') === 'unclear';
    return route.fulfill({
      json: {
        records: unclear ? [{
          canonical_id: 71, member_ids: [71], kind: 'unclear', source: 'jev', actor: 'jev:jev-1.13.0',
          display_name: 'Front Desk', addresses: ['frontdesk@example.test'], classified_at: '2026-01-03T12:00:00Z',
          confidence: 0.45, probabilities: { individual_person: 0.45, shared_role_or_team_mailbox: 0.4, unclear: 0.15 },
        }] : [],
      },
    });
  });

  // Marking an identity as not a person resolves its open candidates.
  await page.route(/\/api\/v1\/identity\/correspondent-kinds\/\d+(?:\?.*)?$/, (route) => {
    const captured = capture(route.request());
    requests.push(captured);
    const participantID = Number(captured.path.split('/').at(-1));
    const kind = typeof captured.body === 'object' && captured.body !== null && 'kind' in captured.body
      ? String(captured.body.kind) : 'person';
    let resolved = 0;
    let restored = 0;
    for (const candidate of candidates) {
      if (candidate.left_kind !== 'participant' || candidate.left_id !== participantID) continue;
      if (kind !== 'person' && candidate.state === 'candidate') {
        candidate.state = 'rejected';
        candidate.notes = 'not_a_person';
        resolved += 1;
      } else if (kind === 'person' && candidate.state === 'rejected' && candidate.notes === 'not_a_person') {
        // Clearing the mark returns the resolved candidates to review.
        candidate.state = 'candidate';
        candidate.notes = undefined;
        restored += 1;
      }
    }
    const organizationName = kind === 'organization'
      ? (typeof captured.body === 'object' && captured.body !== null && 'organization_name' in captured.body
        ? String(captured.body.organization_name) : 'Shop Support')
      : undefined;
    return route.fulfill({
      json: {
        record: {
          canonical_id: participantID, member_ids: [participantID], kind, source: 'user',
          addresses: ['support@shop.example.test'], display_name: 'Shop Support', classified_at: '2026-01-03T12:00:00Z',
          ...(organizationName ? { organization_id: 31, organization_name: organizationName } : {}),
        },
        organization_created: kind === 'organization', resolved_candidates: resolved, restored_candidates: restored,
        organization_removed: kind === 'person' && new URL(route.request().url()).searchParams.has('remove_organization_id'),
      },
    });
  });

  await page.route(/\/api\/v1\/identity\/match-candidates\/\d+\/(?:accept|reject)$/, async (route) => {
    const request = route.request();
    const captured = capture(request);
    requests.push(captured);
    const match = captured.path.match(/\/(\d+)\/(accept|reject)$/);
    const candidateID = Number(match?.[1]);
    const decision = match?.[2];
    const candidate = candidates.find((item) => item.id === candidateID);
    if (!candidate || (decision !== 'accept' && decision !== 'reject')) {
      return route.fulfill({ status: 404, json: { error: 'not_found', message: 'Synthetic candidate not found.' } });
    }

    const gate = decisionGates.get(candidateID);
    if (gate) {
      decisionGates.delete(candidateID);
      await gate.promise;
    }
    const failure = decisionFailures.get(candidateID);
    if (failure) {
      decisionFailures.delete(candidateID);
      return route.fulfill({ status: 503, json: { error: 'unavailable', message: failure } });
    }
    if (candidateID === 19 && decision === 'accept') {
      return route.fulfill({
        status: 409,
        json: {
          error: 'person_merge_required',
          message: 'The synthetic identities belong to two durable profiles.',
          profiles: [
            { person: reviewPerson(7, 4, 'Synthetic One'), etag: '"person-7-r4"' },
            { person: reviewPerson(9, 2, 'Synthetic Two'), etag: '"person-9-r2"' },
          ],
        },
      });
    }

    candidate.state = decision === 'accept' ? 'accepted' : 'rejected';
    candidate.notes =
      typeof captured.body === 'object' &&
      captured.body !== null &&
      'notes' in captured.body &&
      typeof captured.body.notes === 'string'
        ? captured.body.notes
        : undefined;
    candidate.decided_at = '2026-01-03T12:00:00Z';
    candidate.decided_by = 'synthetic-reviewer';
    candidate.updated_at = candidate.decided_at;
    return route.fulfill({ json: { candidate, cache_state: 'ready', identity_revision: 8 } });
  });

  await page.unroute('**/api/v1/people/directory*');
  await page.route('**/api/v1/people/directory*', (route) => {
    requests.push(capture(route.request()));
    return route.fulfill({
      json: {
        people: [...people.values()].map((person) => ({
          id: person.id,
          revision: person.revision,
          display_name: person.display_name,
          primary_channel: 'email',
          contact_state: 'active',
          categories: ['synthetic'],
          organizations: [],
        })),
      },
    });
  });

  await page.route(/\/api\/v1\/person-merges\/41(?:\/snapshot)?$/, (route) => {
    const captured = capture(route.request());
    requests.push(captured);
    if (phase === 'premerge') {
      return route.fulfill({
        status: 404,
        json: {
          error: 'not_found',
          message: 'Synthetic merge history does not exist yet.',
        },
      });
    }
    if (captured.path.endsWith('/snapshot')) {
      return route.fulfill({
        json: {
          version: 1,
          sha256: 'synthetic-digest',
          snapshot: { verified: 'synthetic snapshot', contains_private_fixture_data: false },
        },
      });
    }
    return route.fulfill({ json: reviewMergeDetail(phase === 'postsplit') });
  });

  await page.route(/\/api\/v1\/people\/(?:7|9|19)(?:\/.*)?$/, (route) => {
    const request = route.request();
    const captured = capture(request);
    requests.push(captured);
    const match = captured.path.match(/^\/api\/v1\/people\/(\d+)(.*)$/);
    const personID = Number(match?.[1]);
    const suffix = match?.[2] ?? '';
    const person = people.get(personID);
    if (!person)
      return route.fulfill({ status: 404, json: { error: 'not_found', message: 'Synthetic person not found.' } });

    if (suffix === '/merge' && request.method() === 'POST') {
      if (phase !== 'premerge' || !people.has(9)) {
        return route.fulfill({
          status: 409,
          json: {
            error: 'person_revision_conflict',
            message: 'Synthetic profiles are no longer mergeable.',
          },
        });
      }
      const survivor = reviewPerson(7, 5, 'Synthetic One');
      survivor.participant_ids = [701, 702, 901];
      people.set(7, survivor);
      people.delete(9);
      const mergedCandidate = candidates.find((candidate) => candidate.id === 19);
      if (mergedCandidate) {
        mergedCandidate.state = 'accepted';
        mergedCandidate.decided_at = '2026-01-03T12:00:00Z';
        mergedCandidate.decided_by = 'synthetic-reviewer';
        mergedCandidate.updated_at = mergedCandidate.decided_at;
      }
      phase = 'postmerge';
      return route.fulfill({
        headers: { ETag: '"person-7-r5"' },
        json: {
          cache_state: 'ready',
          identity_revision: 9,
          merge: reviewMerge(),
          person: survivor,
          review_candidates: [],
        },
      });
    }

    if (suffix === '/split' && request.method() === 'POST') {
      if (phase !== 'postmerge' || people.has(19)) {
        return route.fulfill({
          status: 409,
          json: {
            error: 'person_revision_conflict',
            message: 'Synthetic restored person already exists.',
          },
        });
      }
      const source = reviewPerson(7, 6, 'Synthetic One');
      source.participant_ids = [701, 702];
      people.set(7, source);
      const restored = reviewPerson(19, 1, 'Synthetic Restored');
      restored.participant_ids = [901];
      people.set(19, restored);
      phase = 'postsplit';
      return route.fulfill({
        headers: { ETag: '"person-7-r6"', 'X-New-Person-ETag': '"person-19-r1"' },
        json: reviewSplitResult(source, restored),
      });
    }

    if (suffix === '/merges' && request.method() === 'GET') {
      return route.fulfill({
        json: {
          merges: personID === 7 && phase !== 'premerge' ? [reviewMergeSummary(phase === 'postsplit')] : [],
          limit: 100,
          offset: Number(new URL(request.url()).searchParams.get('offset') ?? 0),
        },
      });
    }
    if (suffix === '' && request.method() === 'GET') {
      return route.fulfill({ headers: { ETag: `"person-${person.id}-r${person.revision}"` }, json: person });
    }
    if (suffix === '/profile') {
      return route.fulfill({
        headers: { ETag: `"person-${person.id}-r${person.revision}"` },
        json: {
          person,
          names: [],
          contact_points: [],
          addresses: [],
          dates: [],
          categories: [],
          media: [],
        },
      });
    }
    if (suffix === '/attributes') return route.fulfill({ json: { person_id: person.id, attributes: [] } });
    if (suffix === '/contact-state')
      return route.fulfill({
        json: {
          person_id: person.id,
          cadence_status: 'current',
          computed_at: FIXTURE_TIME,
          interaction_count: 2,
          stale: false,
          last_contact_at: FIXTURE_TIME,
        },
      });
    if (suffix === '/employments') return route.fulfill({ json: { employments: [] } });
    if (suffix === '/relationships') return route.fulfill({ json: { relationships: [] } });
    if (suffix === '/days') return route.fulfill({ json: { person_id: person.id, days: [], total_count: 0 } });
    if (suffix === '/files/search')
      return route.fulfill({
        json: {
          files: [],
          total_count: 0,
          cache_revision: 'synthetic-review',
          search_provenance: {},
        },
      });
    if (suffix === '/network')
      return route.fulfill({
        json: {
          root_person_id: person.id,
          depth: 1,
          truncated: false,
          nodes: [
            { id: `person:${person.id}`, kind: 'person', entity_id: person.id, label: person.display_name, hop: 0 },
          ],
          edges: [],
        },
      });
    return route.fulfill({ status: 404, json: { error: 'not_found', message: 'Synthetic review route not found.' } });
  });

  return {
    archive,
    candidates,
    requests,
    relationshipRequests,
    failNextRelationshipRead(): void {
      failNextRelationshipRead = true;
    },
    deferFactCatalog(): () => void {
      let release = () => undefined;
      factCatalogMode = 'deferred';
      factCatalogGate = new Promise<void>((resolve) => {
        release = resolve;
      });
      return release;
    },
    failFactCatalog(): void {
      factCatalogMode = 'failed';
    },
    malformFactCatalog(): void {
      factCatalogMode = 'malformed';
    },
    holdNextDecision(candidateID: number): () => void {
      let release = () => undefined;
      const promise = new Promise<void>((resolve) => {
        release = resolve;
      });
      decisionGates.set(candidateID, { promise, release });
      return release;
    },
    failNextDecision(candidateID: number, message: string): void {
      decisionFailures.set(candidateID, message);
    },
  };
}

function reviewPerson(id: number, revision: number, displayName: string): Person {
  return {
    id,
    revision,
    display_name: displayName,
    participant_ids: id === 7 ? [701, 702] : [901],
    vcard_uid: `synthetic-${id}`,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-02T00:00:00Z',
  };
}

function reviewMerge(): PersonMerge {
  return {
    id: 41,
    survivor_person_id: 7,
    absorbed_person_id: 9,
    current_person_id: 7,
    survivor_vcard_uid: 'synthetic-7',
    absorbed_vcard_uid: 'synthetic-9',
    survivor_revision_before: 4,
    absorbed_revision_before: 2,
    survivor_revision_after: 5,
    actor: 'web',
    snapshot_version: 1,
    snapshot_sha256: 'synthetic-digest',
    created_at: '2026-01-03T12:00:00Z',
  };
}

function reviewMergeDetail(splitCompleted = false): PersonMergeDetail {
  return {
    merge: reviewMerge(),
    participants: [
      { merge_id: 41, participant_id: 901, origin_side: 'absorbed', ...(splitCompleted ? { split_id: 55 } : {}) },
      { merge_id: 41, participant_id: 701, origin_side: 'survivor' },
    ],
    rows: [
      {
        merge_id: 41,
        table_name: 'person_names',
        original_row_key: 'synthetic-row',
        snapshot_path: 'synthetic/path',
        action: 'copied',
        origin_side: 'absorbed',
        provenance_kind: 'synthetic',
        participant_id: 901,
        ...(splitCompleted ? { split_id: 55 } : {}),
      },
    ],
    splits: splitCompleted ? [reviewSplit()] : [],
    review_candidates: [],
  };
}

function reviewMergeSummary(splitCompleted = false): PersonMergeSummary {
  return {
    merge: reviewMerge(),
    participant_count: 2,
    pending_candidate_count: 0,
    row_action_counts: { copied: 1 },
    row_count: 1,
    split_count: splitCompleted ? 1 : 0,
  };
}

function reviewSplit(): PersonSplit {
  return {
    id: 55,
    merge_id: 41,
    source_person_id: 7,
    new_person_id: 19,
    new_person_uid: 'synthetic-19',
    source_revision_before: 5,
    source_revision_after: 6,
    exact_reversal: false,
    actor: 'web',
    created_at: '2026-01-04T12:00:00Z',
  };
}

function reviewSplitResult(source: Person, restored: Person): PersonSplitResult {
  return {
    exact_reversal: false,
    cache_state: 'ready',
    identity_revision: 10,
    source_person: source,
    new_person: restored,
    split: reviewSplit(),
    ambiguous_rows: [{ table_name: 'person_names', original_row_key: 'synthetic-row', action: 'ambiguous' }],
    unrestored_rows: [],
    uid_alias_disposition: 'partial',
  };
}

function sessionRoute(route: Route) {
  return route.fulfill({ json: { auth_mode: 'loopback', https: false, plain_http_warning: false } });
}
