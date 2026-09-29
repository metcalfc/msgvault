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

- [ ] **Task 0.1 Move and generalize the client.** Create `internal/jev` from
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
- [ ] **Task 0.2 Daemon-grade budget.** Replace the permanent `failed` and
  `unknown` flags with a circuit breaker (N consecutive failures opens for a
  cool-down; half-open probe) and add persisted daily request and cost
  counters in the store (`jev_day_counters(feature, utc_day, requests,
  input_tokens, output_tokens, cost_usd_micros)`). Prices come from config;
  zero prices mean request-count accounting only, never "free".
- [ ] **Task 0.3 Config and credentials.** Add `[jev]` with `enabled`,
  `endpoint` (pinned default), `model`, `api_key_env` (default
  `TYPESAFE_API_KEY`), `request_timeout`, `max_requests_per_day`,
  `max_cost_usd_per_day`, `input_usd_per_million_tokens`,
  `output_usd_per_million_tokens`. Register a stored-credential slot
  `jev/api_key` in `internal/providercredentials` bound to the endpoint
  origin, so the key can be pasted in Settings like the Exa key. Expose the
  `[jev]` keys and the credential slot through the settings API metadata.
- [ ] **Task 0.4 Consent.** Add `jev_feature_consents(feature,
  policy_fingerprint, granted_at, revoked_at)` and a `jev.Gate` modeled on
  `SemanticPersonEmbeddingGate`: the fingerprint hashes feature name,
  question wording, state field list, model, and endpoint. Any change
  requires new consent. CLI: `msgvault jev status`, `msgvault jev consent
  <feature> [--yes]`, `msgvault jev revoke <feature>|--all`. Print a
  disclosure per feature listing exactly which fields leave the machine.
- [ ] **Task 0.5 Docs.** `docs/usage/jev-judgments.md` (what each feature
  sends, thresholds, consent, budget, how to turn off) and configuration
  reference rows. Add the plan to `docs/internal/README.md`.

## Phase 1: enrichment identity verification

Site: `internal/personenrichment/exa.go` (`exaTypedIdentityMatches`,
`exactExaIdentityMatch`, rejection at decode), `request.go`
(`AssessIdentity`), `sixtyfour.go` equivalent,
`internal/store/person_enrichment_results.go`
(`validateEnrichmentHostIdentityAssessment`).

- [ ] **Task 1.1 Deterministic retries first.** When a lookup returns no
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
- [ ] **Task 1.3 Wire the gate and fallback.** No consent or budget means the
  exact rule alone, exactly as today. Add the feature to `jev status`.
- [ ] **Task 1.4 Tests.** Fake Exa result "Susie S." at "Heavybit" for a
  request "Susie Singh"/"Heavybit" accepts with fake Jev {0.95, 0.99, 0.02};
  "Dataherald (YC W21)" vs "Dataherald" accepts; wrong person with matching
  common name rejects; Jev unavailable falls back to rejection; consent
  revoked never sends.

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
- [ ] **Task 3.4 Tests.** "Dataherald, Inc." resolves to existing
  "Dataherald"; unrelated similar name creates new org; alias write is
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
