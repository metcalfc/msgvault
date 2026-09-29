# Jev Judgments Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.
> Work one phase at a time, one focused commit per task. Each phase must leave
> `main`-equivalent behavior intact when its feature flag or consent is off.

**Goal:** Replace a set of brittle string-matching and hand-rule decisions
with narrow, typed judgments from TypeSafe's System One model (Jev), while code
keeps every workflow, threshold, and side effect. Nothing leaves the machine
without an explicit, revocable consent recorded per policy fingerprint, and
every Jev-backed path degrades to today's behavior when Jev is off, over
budget, slow, or wrong.

**Architecture:** One shared client package owns transport, bounds, budgets, a
circuit breaker, and typed questions (Choice, Noul, Score). Each feature builds
a small, deterministic state object in code, asks a handful of independent
questions in one request, and consumes probabilities with explicit thresholds.
Judgments are stored as inputs to existing deterministic logic (aliases,
candidate confidence, stored classes), never as direct writes to user-owned
profile facts. Replays stay deterministic because stored answers, not live
calls, feed the resolver.

**Tech stack:** Go 1.27, Cobra, SQLite (mattn), DuckDB cache, Testify,
`encoding/json/v2`. Jev endpoint `https://api.typesafe.ai/v1/systemone`,
model pinned to `jev-1.13.0`. Live API docs: https://docs.typesafe.ai/llms.txt
(primitives, confidence, api pages). Read them before writing request code;
do not invent field names.

---

## Working rules

- Work on `feat/jev-judgments`; never on `main`.
- Testify only (`require` for setup, `assert` for checks), `(want, got)`.
  Run Go tests with `-tags "fts5 sqlite_vec"`. `go fmt ./...` and
  `go vet ./...` before each commit; `make lint-ci` when available.
- Tests talk to a local fake Jev HTTP server, never the real endpoint. Record
  request bodies in tests so question wording and state shape are asserted.
- Never run `make install`, never touch `~/.msgvault`, never push, never open
  issues or pull requests.
- No feature calls Jev unless (a) its config section is enabled, (b) an API
  key resolves, and (c) a consent row for the current policy fingerprint is
  active. Automatic paths (sync, cache build, nightly jobs) additionally
  require the feature's `automatic = true` setting; default `false`.
- Every outbound request carries a total deadline from the caller's context
  and must never fail the user-facing operation on error: fall back to the
  pre-Jev decision and report `jev: skipped:<category>` through the existing
  safe-failure categories.
- Log no state content. Log question ids, answer types, probabilities,
  confidence, latency, token usage, and budget state only.
- Synthetic names and example addresses in fixtures. Never real people.
- One focused commit per task. End each phase with a short summary in the
  commit message body of what is now possible and what remains off by default.

## Phase 0: shared client and consent (prerequisite)

- [x] **Task 0.1 Move and generalize the client.** Create `internal/jev` from
  `internal/vector/rerank/typesafe.go`. Keep `Budget`, `send()` bounds
  (timeout, no redirects, JSON content-type check, response cap), request
  cap, `SafeFailure`, pinned endpoint and model. Replace the fixed Noul with:
  `Question{ID, Type (choice|noul|score), Instructions any, Criteria any}`
  and `Answer{Type, Choice string, Probabilities map[string]float64,
  Confidence float64, Score float64, Noul float64}`. Decode all three answer
  types. Per-caller bounds replace the rerank-specific 4 KiB / 2 KiB limits.
  Add `Request{State any, Questions []Question, Deadline}` with a helper that
  splits a batched state into multiple requests under the request cap.
  Rerank package becomes a thin consumer; `msgvault eval` output unchanged.
- [x] **Task 0.2 Daemon-grade budget.** Replace the permanent `failed` and
  `unknown` flags with a circuit breaker (N consecutive failures opens for a
  cool-down; half-open probe) and add persisted daily request and cost
  counters in the store (`jev_day_counters(feature, utc_day, requests,
  input_tokens, output_tokens, cost_usd_micros)`). Prices come from config;
  zero prices mean request-count accounting only, never "free".
- [x] **Task 0.3 Config and credentials.** Add `[jev]` with `enabled`,
  `endpoint` (pinned default), `model`, `api_key_env` (default
  `TYPESAFE_API_KEY`), `request_timeout`, `max_requests_per_day`,
  `max_cost_usd_per_day`, `input_usd_per_million_tokens`,
  `output_usd_per_million_tokens`. Register a stored-credential slot
  `jev/api_key` in `internal/providercredentials` bound to the endpoint
  origin, so the key can be pasted in Settings like the Exa key. Expose the
  `[jev]` keys and the credential slot through the settings API metadata.
- [x] **Task 0.4 Consent.** Add `jev_feature_consents(feature,
  policy_fingerprint, granted_at, revoked_at)` and a `jev.Gate` modeled on
  `SemanticPersonEmbeddingGate`: the fingerprint hashes feature name,
  question wording, state field list, model, and endpoint. Any change
  requires new consent. CLI: `msgvault jev status`, `msgvault jev consent
  <feature> [--yes]`, `msgvault jev revoke <feature>|--all`. Print a
  disclosure per feature listing exactly which fields leave the machine.
- [x] **Task 0.5 Docs.** `docs/usage/jev-judgments.md` (what each feature
  sends, thresholds, consent, budget, how to turn off) and configuration
  reference rows. Add the plan to `docs/internal/README.md`.

## Phase 1: enrichment identity verification

Site: `internal/personenrichment/exa.go` (`exaTypedIdentityMatches`,
`exactExaIdentityMatch`, rejection at decode), `request.go`
(`AssessIdentity`), `sixtyfour.go` equivalent,
`internal/store/person_enrichment_results.go`
(`validateEnrichmentHostIdentityAssessment`).

- [x] **Task 1.1 Deterministic retries first.** When a lookup returns no
  entity, retry once with name variants built in code (drop middle
  initial/name, drop suffixes like Jr., collapse "Last, First"). No Jev.
- [ ] **Task 1.2 Semantic identity check.** When exactly one of name or
  current company matches exactly and the other does not, ask one request
  with three Nouls over `{requested:{name, company, email_domain},
  returned:{name, first_name, last_name, location, current_roles[],
  past_companies[] (capped 10), profile_url_host}}`:
  `name_compatible`, `company_same`, `name_conflict`. Accept when the
  non-exact field scores ≥ 0.90 and `name_conflict` ≤ 0.20; emit an
  `IdentityMatch` with a new reason `semantic_name_company` and score 900.
  Between 0.50 and 0.90: new result state `identity_uncertain`, stored with
  the probabilities, surfaced in `enrichment status` and the Facts view, no
  claims committed. Below 0.50: reject as today.
  > Done except the Facts view: `identity_uncertain` attempts are listed by
  > `person enrichment status` (CLI/JSON) and their claims appear as
  > identity-rejected decisions; a per-person API field for the web Facts view
  > needs an API contract decision and is left open.
- [x] **Task 1.3 Wire the gate and fallback.** No consent or budget means the
  exact rule alone, exactly as today. Add the feature to `jev status`.
- [x] **Task 1.4 Tests.** Fake Exa result "Priya R." at "Example Capital" for a
  request "Priya Ramanathan"/"Example Capital" accepts with fake Jev {0.95, 0.99, 0.02};
  "Example Labs (YC W21)" vs "Example Labs" accepts; wrong person with matching
  common name rejects; Jev unavailable falls back to rejection; consent
  revoked never sends.

## Phase 1b: identifier-based bind and merge candidates (no Jev)

Motivation: a Google Contacts CardDAV import on 2026-09-28 created 1,985
contact-only profiles. Matching their contact points against observed
participants by exact email or phone shows 257 that duplicate a profile that
already owns the matching participant, and 242 whose matching participant is
unbound. The sync matches cards against existing profiles' contact points
only; nothing matches cards against observed participants. This phase closes
that gap deterministically and gives the later Jev dedup (Phase 8.3) a much
smaller, cleaner candidate pool. Exact identifiers are evidence; names are
not, so nothing in this phase looks at display names.

Sites: `internal/store/identity_match_candidates.go` (candidate table with
`display_name` basis and `Confidence` already defined, nothing writes them),
`internal/store/person_merge_review_candidates`, `person merge-candidate`
CLI, the Reviews tab, `internal/store/person_contact_points.go`,
`participants.email_address` / `participants.phone_number`, and
`participant_identifiers` (imessage, phone, username, email for chat
sources).

Scoping notes (verified against main @ 63690d36): the identity-match table
already has `email` and `phone` bases, so no new basis; `confidence` must be
NULL for `carddav_import` sources, so candidates use source `system`;
`AcceptIdentityMatchCandidateContext` today links participants or maps a
card to a person and never merges profiles, so a new participant-to-person
branch is required; there is no public bind API, so a bind is promote-then-
merge with the contact profile as survivor, which keeps its vCard UID and is
reversible through split; system auto-accept is refused for every basis
except `stable_provider_id`, so auto-apply is deferred; `person
merge-candidate` decides post-merge attribute conflicts and is the wrong CLI.
Merge is refused while either side is published or has an unresolved CardDAV
conflict, so those candidates show as blocked. A rejected candidate row is
already the negative for a pair, but it is deleted if an untouched imported
profile is removed and re-imported.

- [x] **Task 1b.1 Candidate query (1 to 1.5 days).** Contact-only profiles
  are persons with no `person_participants` rows. Join their active email
  and phone contact points to `LOWER(participants.email_address)`, to
  `participants.phone_number` normalized in Go with
  `textimport.NormalizePhone` (values are mostly E.164 but not enforced), and
  to email or phone `participant_identifiers`. Classify at cluster level via
  `sortedComponentMembers` and `personIDsForParticipantsTx`: `bind` when the
  cluster has no person, `merge` when exactly one other person, `ambiguous`
  otherwise. Exclude owner participants using the rule at
  `cmd/msgvault/cmd/build_cache.go:603` widened to their clusters, exclude
  rejected rows, and mark published or conflicted sides as blocked.
- [ ] **Task 1b.2 Candidate rows (0.5 to 1 day).** Endpoints
  participant-to-person, basis `email` or `phone`, source `system`,
  confidence 1.0, `normalized_value` set. Record the contact point id and
  matched identifier in `identity_match_evidence`. The unique index gives
  idempotency.
- [ ] **Task 1b.3 Accept path (1.5 to 2 days).** New branch in
  `AcceptIdentityMatchCandidateContext`: `bind` promotes the participant
  cluster then merges the new profile into the contact profile as survivor;
  `merge` returns `PersonBindingConflictError` so the existing 409
  `person_merge_required` flow and merge dialog let the user pick the
  survivor. Mark the candidate accepted before merging;
  `reconcilePersonIdentityCandidatesTx` retargets rows on the absorbed side.
- [ ] **Task 1b.4 Reviews tab (1.5 to 2 days).** Resolve both endpoints to
  names and addresses (cards show raw kind and id today) and add a "Contacts
  that match your archive" filter. Accept, reject, and the merge dialog are
  reused.
- [ ] **Task 1b.5 CLI (0.5 to 1 day).** New `person contact-matches
  list|accept|reject|build` through the daemon.
- [ ] **Task 1b.6 Scheduling (0.5 day).** Refresh candidates at the end of a
  successful `carddav.Service.Sync`, and add a small dedicated daily job
  modeled on the SQLite maintenance job, not the people sweep, since mail
  and chat syncs also create participants.
- [ ] **Task 1b.7 Tests (1.5 to 2 days).** Cluster-level bind and merge,
  ambiguity, owner exclusion by email and by phone identifier, a
  Beeper-style non-canonical phone, rejected pair suppressed across reruns
  plus the re-import gap, published and conflict blocking, remote card update
  and deletion after absorption, split restoring the card mapping, idempotent
  reruns.
- [ ] **Deferred: auto-apply.** Needs an explicit exception to the
  system-accept rule; if added later, limit to `bind` with one contact
  profile, one cluster, not blocked, no rejection. Auto-merge stays out.

## Phase 2: correspondent kind

Site: `internal/query/relationships.go` reciprocity gate,
`internal/identityindex/compact_sql.go`, `list-senders`, enrichment
eligibility in `internal/store/person_enrichment_work.go`.

- [ ] **Task 2.1 Deterministic pre-classification.** New
  `internal/correspondentkind`: parse `List-Unsubscribe`, `Auto-Submitted`,
  `Precedence`, `list_id`, `CATEGORY_*` labels, provider bot flags, `noreply`
  local parts, SMS short codes (≤ 6 digits), and a freemail-domain list.
  Assign `automated` or `mailing_list` in code when signals are decisive.
- [ ] **Task 2.2 Jev classification for the remainder.** Store table
  `correspondent_kinds(canonical_id, kind, probabilities_json,
  confidence, source ('rule'|'jev'|'user'), identity_revision,
  classified_at)`. One Choice per identity, batched 10 per request as
  `identities[i]`, options `individual_person`,
  `shared_role_or_team_mailbox`, `mailing_list_or_group`,
  `automated_notification_or_transactional`, `marketing_or_newsletter`,
  `unclear`. State per identity: display label, addresses (local part and
  domain separately), counts (sent, received, meetings), list_id and
  category shares, header presence counts, up to 5 subjects from them and 3
  from the owner. No bodies.
- [ ] **Task 2.3 Consumers.** Relationships gate: hide unless
  `individual_person ≥ 0.60` or user override; `--all` bypasses. Export the
  kind on the people parquet. `list-senders --kind`. Enrichment: skip
  non-persons with reason `not_a_person` instead of failing. `person kind
  set <participant> <kind>` for overrides, and a review list for `unclear`.
- [ ] **Task 2.4 Scheduling.** `msgvault kinds build` one-time backfill over
  identities above a message floor; incremental at cache build only when
  `[jev.correspondent_kind].automatic = true`.
- [ ] **Task 2.5 Tests.** Rule-only paths need no fake Jev. Fake Jev returns
  a mailing list for a "team@" alias the owner replied to once; assert it is
  hidden from Relationships and skipped by enrichment.

## Phase 3: organization resolution and value equivalence

Site: `internal/store/person_fact_organization.go`,
`organizations.go` (`NormalizeOrganizationName`),
`person_fact_projection.go` employment identity, `employments.go` titles.

- [ ] **Task 3.1 Shortlist in code.** On exact-lookup miss, build ≤ 8
  candidates by token overlap, prefix, trigram on `name_normalized` and
  `organization_names`, plus domain siblings.
- [ ] **Task 3.2 Judgment.** One request: Choice `org_ref` over candidate
  keys plus `new_organization`; Noul `title_same_role` per title pair.
  Confidence ≥ 0.85 writes a durable `organization_names` alias with
  provenance `jev` and model version, then the deterministic lookup reruns.
  0.50 to 0.85 creates an org-merge review candidate. Below: create as today.
- [ ] **Task 3.3 Employment fingerprint.** Let same-role titles corroborate
  by mapping through the alias table before fingerprinting, so Exa and
  sweep claims for the same role add up. Resolver arithmetic unchanged.
- [ ] **Task 3.4 Tests.** "Example Labs, Inc." resolves to existing
  "Example Labs"; unrelated similar name creates new org; alias write is
  idempotent; replay with stored alias needs no Jev call.

## Phase 4: hybrid search reranking

Site: post-stage in `internal/vector/hybrid/engine.go` after `FusedSearch`;
consumers `internal/api/handlers.go`, `internal/mcp/handlers.go`,
`internal/api/explore.go`.

- [ ] **Task 4.1 Engine stage.** Optional reranker interface on the engine:
  top K = min(30, n), candidate text = subject, from, date, preprocessed body
  ≤ 2 KiB, existing Noul wording. Sort by noul, tie-break RRF, tail
  unchanged. Response carries `rerank {status, model, scored}`; explain
  output carries the score.
- [ ] **Task 4.2 Stability.** Cache reranked order keyed on (query, filter
  hash, generation, top-K id digest) so pages agree; Explore reorders the
  snapshot prefix before `issueSnapshot`. Batch body hydration replaces the
  per-candidate `GetMessageContext` loop.
- [ ] **Task 4.3 Config and gate.** `[jev.rerank]` `enabled`, `shape`
  (batched|per_candidate), `top`, `message_types_excluded`. Consent feature
  `search_rerank`. Never on for `--mode fts` or automatic searches.
- [ ] **Task 4.4 Eval gate.** Run `msgvault eval` rerank against the target
  in commit 01ea4543 (≥ +0.05 Hit@10, p95 < 2 s) and record results in the
  docs before recommending `enabled = true`.

## Phase 5: cleanup suggestions and deletion protection

Site: `cmd/msgvault/cmd/stage_delete.go`, `internal/api/deletions.go`,
`internal/deletion/manifest.go`.

- [ ] **Task 5.1 Plain-code protection.** Deletion staging warns on (and
  `--protect` skips) STARRED, owner-sent, and `individual_person` senders.
  Stop remote-image fetch for SPAM and TRASH labels.
- [ ] **Task 5.2 Suspicion scoring.** `msgvault suggest-cleanup` over a pool
  code narrows (SPAM/Promotions, never replied, sender not a person, has
  links). State per message: from name and domain, reply-to domain, link
  hosts, SPF/DKIM/DMARC results, To/Cc vs Bcc, labels, thread replied,
  sender kind, subject, first 500 chars. Questions: Noul `impersonation`,
  Noul `pressure`, Choice `category`. Composite score in code with hard
  signals. ≥ 0.80 lists as suspected phishing; staging still requires a
  user action; never auto-delete.
- [ ] **Task 5.3 Review surfacing.** `show-deletion` and the Web UI deletion
  review list staged messages that scored personal/work ≥ 0.50 as "possibly
  worth keeping".

## Phase 6: query understanding for Explore

Site: `internal/api/explore.go` (`prepareExploreRequest`), Svelte search
bar.

- [ ] **Task 6.1 Option generation in code.** Date-phrase dictionary to
  windows, directory matches to person candidates, accounts, message types.
- [ ] **Task 6.2 Batched judgment.** Choices `message_type`, `time_window`,
  `person`, `person_role`, `account`; Noul `natural_language`. Runs in
  parallel with the search, 800 ms budget, dropped if late.
- [ ] **Task 6.3 UI.** Suggested chips at confidence ≥ 0.80; click applies
  and removes the source span. Offer hybrid when `natural_language ≥ 0.70`
  and full-text returns zero rows. Only the query and option labels leave.

## Phase 7: meetings and calendar

- [ ] **Task 7.1 Plain-code calendar fixes.** Skip resource attendees,
  owner-declined events, `outOfOffice`/`focusTime`/`workingLocation`,
  transparent events; cap attendee count for activity weight.
- [ ] **Task 7.2 Action-item assignee.** Choice among email-bearing
  attendees plus `owner` and `none_or_unclear`, batched per meeting at
  import; store `assignee_participant_id` with confidence at ≥ 0.80 and a
  provenance of `inferred`; add `assignee_person_id` filter.
- [ ] **Task 7.3 Event kind.** Choice over one_on_one, small_working_meeting,
  large_group_or_all_hands, external_webinar_or_marketing,
  personal_hold_or_logistics, social; recurring series asked once; weights
  meeting activity, confidence < 0.60 keeps count-based weight.

## Phase 8: sweep and remaining people items

- [ ] **Task 8.1 Sweep evidence rerank** before the chat LLM using the
  shared Noul reranker; drop < 0.20.
- [ ] **Task 8.2 Claim grounding.** Nouls `stated` and `current` per claim
  replace the chat LLM's self-reported confidence as `ReportedScore`.
- [ ] **Task 8.3 Duplicate-person candidates.** Code proposes pairs (same
  display name across addresses, same local part across domains, person
  embedding neighbors); Noul `same_person` batched 20 per request; write
  `IdentityMatchCandidate` with basis `display_name` and confidence; never
  auto-accept; drop < 0.30.
- [ ] **Task 8.4 Small ones.** Primary current role Choice when two or more
  current roles are system-set; display-name Choice at promotion when two
  or more distinct names; merge attribute conflict Noul at ≥ 0.95.

## Plain-code fixes to land alongside (no Jev)

- [ ] Strip stopwords and use word boundaries for the subject boost; fall
  back to OR when the AND-ed BM25 leg returns zero hits.
- [ ] `getAccountID` case-insensitive with valid accounts in the error;
  document the `message_type:` operator in the MCP catalog; tokenised AND
  matching in `profileMatchesPeopleQuery`.
- [ ] Keep signature blocks in the employment evidence lane.

## Out of scope

Anything that changes resolver thresholds or user pins; auto-accepting merges,
deletions, or identity links; sending message bodies for correspondent
classification; document-chunk reranking until message reranking has passed
its eval gate.
