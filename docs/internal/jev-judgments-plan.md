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
- [x] **Task 1.2 Semantic identity check.** When exactly one of name or
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
  > Uncertain attempts are reviewed in Reviews → Enrichment identities and
  > `person enrichment review`: confirm re-applies the stored claims at score
  > 1000 (reason `user_confirmed`) and attaches the provider person ID; reject
  > records a per-person negative (reason `user_rejected`) that the commit
  > recheck enforces. The returned identity is shown from stored claims and
  > the evidence host, since the raw returned identity is never stored.
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
- [x] **Task 1b.2 Candidate rows (0.5 to 1 day).** Endpoints
  participant-to-person, basis `email` or `phone`, source `system`,
  confidence 1.0, `normalized_value` set. Record the contact point id and
  matched identifier in `identity_match_evidence`. The unique index gives
  idempotency.
- [x] **Task 1b.3 Accept path (1.5 to 2 days).** New branch in
  `AcceptIdentityMatchCandidateContext`: `bind` promotes the participant
  cluster then merges the new profile into the contact profile as survivor;
  `merge` returns `PersonBindingConflictError` so the existing 409
  `person_merge_required` flow and merge dialog let the user pick the
  survivor. Mark the candidate accepted before merging;
  `reconcilePersonIdentityCandidatesTx` retargets rows on the absorbed side.
- [x] **Task 1b.4 Reviews tab (1.5 to 2 days).** Resolve both endpoints to
  names and addresses (cards show raw kind and id today) and add a "Contacts
  that match your archive" filter. Accept, reject, and the merge dialog are
  reused.
- [x] **Task 1b.5 CLI (0.5 to 1 day).** New `person contact-matches
  list|accept|reject|build` through the daemon.
- [x] **Task 1b.6 Scheduling (0.5 day).** Refresh candidates at the end of a
  successful `carddav.Service.Sync`, and add a small dedicated daily job
  modeled on the SQLite maintenance job, not the people sweep, since mail
  and chat syncs also create participants.
- [x] **Task 1b.7 Tests (1.5 to 2 days).** Cluster-level bind and merge,
  ambiguity, owner exclusion by email and by phone identifier, a
  Beeper-style non-canonical phone, rejected pair suppressed across reruns
  plus the re-import gap, published and conflict blocking, remote card update
  and deletion after absorption, split restoring the card mapping, idempotent
  reruns.
  > The re-import gap does not occur: a rejected (or accepted) candidate is
  > user-owned state (`personHasUserOwnedStateTx`), so removing the card keeps
  > the profile and its decision, and the re-imported card binds back to it
  > by address. The test pins that behavior instead. Owner exclusion also
  > compares phone-shaped owner identities after normalization, because
  > participant phones are not stored in one form.
- [ ] **Deferred: auto-apply.** Needs an explicit exception to the
  system-accept rule; if added later, limit to `bind` with one contact
  profile, one cluster, not blocked, no rejection. Auto-merge stays out.

## Phase 2: correspondent kind

Site: `internal/query/relationships.go` reciprocity gate,
`internal/identityindex/compact_sql.go`, `list-senders`, enrichment
eligibility in `internal/store/person_enrichment_work.go`.

> **User overrides already exist.** `correspondent_kinds` shipped with the
> "Not a person" action: one row per participant and source (`user`,
> `rule`, `jev`) with `kind`, `organization_id`, `confidence`,
> `probabilities_json`, `identity_revision`, `actor`, and `classified_at`.
> It is keyed by participant rather than `canonical_id`: a classification
> writes every current cluster member, and readers resolve a cluster from
> all members, because the canonical (smallest) ID changes on link and
> unlink. A `user` row always outranks `rule` and `jev`; a `user` row of
> kind `person` is the explicit "this is a person" override. The kind
> vocabulary lives in `internal/correspondentkind` (`person`,
> `organization`, `shared_mailbox`, `ignored`) and is validated in Go, so
> this phase adds `automated` and `mailing_list` there without a table
> rebuild. Enrichment already skips profiles made only of organization or
> ignored identities with outcome `not_a_person`; rankings already leave
> those clusters out unless `include_not_people` is set; `person kind set`
> is the override CLI. The deterministic shared-mailbox rule
> (`correspondentkind.DetectSharedMailbox`: role local parts or several
> people's names) holds back contact matches. Tasks below now write
> `rule`/`jev` rows into that table and read effective kinds through it.

- [x] **Task 2.1 Deterministic pre-classification.** New
  `internal/correspondentkind`: parse `List-Unsubscribe`, `Auto-Submitted`,
  `Precedence`, `list_id`, `CATEGORY_*` labels, provider bot flags, `noreply`
  local parts, SMS short codes (≤ 6 digits), and a freemail-domain list.
  Assign `automated` or `mailing_list` in code when signals are decisive.
  > No header column exists, so header presence is read from the header
  > block of up to five sampled raw messages per cluster, by primary key.
  > The only stored provider bot flags are Discord's webhook and automated
  > identifier types. `list_id` alone never decides (people post through
  > lists): `mailing_list` needs the sender to be the List-Id's own posting
  > address, and the bulk-header and Promotions rules apply only to senders
  > the owner never wrote to. Promotions also needs a bulk header and ten
  > messages; freemail is counter-evidence. Only a user decision suppresses
  > identity matches; rule and jev rows never do. Runs record clusters they
  > leave undecided (`correspondent_kind_evaluations`) and visit
  > never-evaluated clusters first, so capped cache-build passes progress.
  > `unclear` joined the vocabulary as a Jev-only kind; `automated` and
  > `mailing_list` are also user-settable.
- [x] **Task 2.2 Jev classification for the remainder.** Store results in
  the existing `correspondent_kinds` table (see the note above) with
  source `jev`, `probabilities_json`, `confidence`, and
  `identity_revision`. One Choice per identity, batched 10 per request as
  `identities[i]`, options `individual_person`,
  `shared_role_or_team_mailbox`, `mailing_list_or_group`,
  `automated_notification_or_transactional`, `marketing_or_newsletter`,
  `unclear`. State per identity: display label, addresses (local part and
  domain separately), counts (sent, received, meetings), list_id and
  category shares, header presence counts, up to 5 subjects from them and 3
  from the owner. No bodies.
  > `internal/kindclassify`. The policy has ten Choice questions
  > (`kind_0`..`kind_9`); a short batch sends only its questions through
  > `Service.JudgeQuestions`, which keeps the consented wording. An option
  > at or above 0.60 decides (`individual_person` → `person`, marketing →
  > `automated`); anything else stores `unclear`. Phones are never sent.
- [x] **Task 2.3 Consumers.** Relationships gate: hide unless
  `individual_person ≥ 0.60` or user override; `--all` bypasses. Export the
  kind on the people parquet. `list-senders --kind`. Enrichment: skip
  non-persons with reason `not_a_person` instead of failing. `person kind
  set <participant> <kind>` for overrides already exists; add a review list
  for `unclear`.
  > Shared mailboxes stay listed in rankings as labelled rows, the rule
  > that shipped with the user override; every other non-person kind and
  > `unclear` is hidden. There is no relationships CLI, so the `--all`
  > bypass is the API's `include_not_people`. `list-senders --kind` resolves
  > the kind live through `GET /aggregates?sender_kind=`. `relationship_people`
  > carries `correspondent_kind`, `correspondent_kind_source`, and
  > `individual_person`. The review list is `person kind list --kind
  > unclear` and Reviews → Unclear correspondents.
- [x] **Task 2.4 Scheduling.** `msgvault kinds build` one-time backfill over
  identities above a message floor; incremental at cache build only when
  `[jev.correspondent_kind].automatic = true`.
- [x] **Task 2.5 Tests.** Rule-only paths need no fake Jev. Fake Jev returns
  a mailing list for a "team@" alias the owner replied to once; assert it is
  hidden from Relationships and skipped by enrichment.

## Phase 3: organization resolution and value equivalence

Site: `internal/store/person_fact_organization.go`,
`organizations.go` (`NormalizeOrganizationName`),
`person_fact_projection.go` employment identity, `employments.go` titles.

- [x] **Task 3.1 Shortlist in code.** On exact-lookup miss, build ≤ 8
  candidates by token overlap, prefix, trigram on `name_normalized` and
  `organization_names`, plus domain siblings.
- [x] **Task 3.2 Judgment.** One request: Choice `org_ref` over candidate
  keys plus `new_organization`; Noul `title_same_role` per title pair.
  Confidence ≥ 0.85 writes a durable `organization_names` alias with
  provenance `jev` and model version, then the deterministic lookup reruns.
  0.50 to 0.85 creates an org-merge review candidate. Below: create as today.
  > The judgment runs before a generation is committed (enrichment and
  > people sweep), never inside the projection transaction. The threshold is
  > the probability Jev gives the best candidate option. Consent binds fixed
  > questions, so the request carries `org_ref` plus the pair slots it needs,
  > `title_same_role_1` to `_4`, each worded once in the policy. Provenance
  > `jev` is recorded as source `system` with source_ref
  > `jev:organization_resolution:<model>` and the probability as confidence,
  > because `jev` is not a profile provenance a user can write. An alias also
  > adds the reference's domain when the organization lacks it, since the
  > exact lookup requires every key to match. There was no organization
  > merge review surface, so `organization_match_reviews` backs a new Reviews
  > kind (API `/organization-match-reviews`): accept merges the organization
  > projection created for the name, reject keeps the pair off the shortlist.
- [x] **Task 3.3 Employment fingerprint.** Let same-role titles corroborate
  by mapping through the alias table before fingerprinting, so Exa and
  sweep claims for the same role add up. Resolver arithmetic unchanged.
  > Title aliases live in `organization_title_aliases`, one canonical title
  > per role. Projection maps both the claimed title (when the organization
  > resolves to an existing one) and stored employment titles before
  > fingerprinting, and finds the employment row for any title of the role,
  > so the canonical title is what a new projection writes. Claims corroborate
  > within one generation; across generations the store already keeps only
  > the newest generation per organization and title, which now means a
  > later title for the same role corrects the one employment instead of
  > adding a second.
- [x] **Task 3.4 Tests.** "Example Labs, Inc." resolves to existing
  "Example Labs"; unrelated similar name creates new org; alias write is
  idempotent; replay with stored alias needs no Jev call.

## Phase 4: hybrid search reranking

Site: post-stage in `internal/vector/hybrid/engine.go` after `FusedSearch`;
consumers `internal/api/handlers.go`, `internal/mcp/handlers.go`,
`internal/api/explore.go`.

- [x] **Task 4.1 Engine stage.** Optional reranker interface on the engine:
  top K = min(30, n), candidate text = subject, from, date, preprocessed body
  ≤ 2 KiB, existing Noul wording. Sort by noul, tie-break RRF, tail
  unchanged. Response carries `rerank {status, model, scored}`; explain
  output carries the score.
  > `hybrid.Reranker` runs only for `ModeHybrid` requests that opt in:
  > `GET /search?rerank=true` (the CLI sets it), Explore's `rerank` field
  > (the Web UI sets it), and MCP only with `[jev.rerank] mcp = true`. The candidate is one
  > text (`Subject:`, `From:`, `Date:`, cleaned body) within the existing
  > 2 KiB candidate bound, so the wire shape matches the recorded captures.
  > Results whose type is excluded keep their slots; judged results fill
  > the slots judged results held. The response also carries `reason`,
  > `cached`, and `timings.rerank_ms`.
- [x] **Task 4.2 Stability.** Cache reranked order keyed on (query, filter
  hash, generation, top-K id digest) so pages agree; Explore reorders the
  snapshot prefix before `issueSnapshot`. Batch body hydration replaces the
  per-candidate `GetMessageContext` loop.
  > A reranked search fetches at least `top` fused results so every page
  > offers the same prefix. The cache is in memory (256 entries, ten
  > minutes) and also pins transient provider failures, not gate states;
  > a failure never replaces a cached order. Concurrent misses share one
  > judgment, bounded by the request timeout and cancelled (uncached) when
  > its last waiter leaves. Each caller passes the gate (`Service.Admit`)
  > before reading the cache or joining, and the admitted policy
  > fingerprint is part of the key.
  > `Store.GetMessagesWithBodiesByIDsContext` loads bodies with one
  > `message_id IN (...)` lookup, for search and `msgvault eval` alike.
- [x] **Task 4.3 Config and gate.** `[jev.rerank]` `enabled`, `shape`
  (batched|per_candidate), `top`, `message_types_excluded`. Consent feature
  `search_rerank`. Never on for `--mode fts` or automatic searches.
  > `FeatureSpec.BodyNotice` makes the disclosure say body text leaves the
  > machine. `Service.JudgeAll` sends a feature's independent judgments
  > (per-candidate shape) after one gate check.
- [ ] **Task 4.4 Eval gate.** Run `msgvault eval` rerank against the target
  in commit 01ea4543 (≥ +0.05 Hit@10, p95 < 2 s) and record results in the
  docs before recommending `enabled = true`.
  > The harness is ready: the eval sends the production candidate text and
  > prints the gate (`rerank_gate`: pass, fail, or not_evaluated per shape),
  > and a fake-provider test covers it. The run against the real archive
  > and TypeSafe waits for the archive owner's go-ahead; until it passes,
  > the docs keep `enabled = false` as the recommendation.

## Phase 5: cleanup suggestions and deletion protection

Site: `cmd/msgvault/cmd/stage_delete.go`, `internal/api/deletions.go`,
`internal/deletion/manifest.go`.

- [x] **Task 5.1 Plain-code protection.** Deletion staging warns on (and
  `--protect` skips) STARRED, owner-sent, and `individual_person` senders.
  Stop remote-image fetch for SPAM and TRASH labels.
  > Staging is daemon-side, so `POST /deletions` reports `protection` counts
  > and takes `protect`; the CLI flag and a Web UI checkbox send it. A person
  > sender is an explicit classification only: a user `person` decision, or a
  > Jev row at or above 0.60 `individual_person` that no user decision
  > overrides; unclassified senders are not protected, or nearly everything
  > would be. Spam and trash (and IMAP `Junk`) are skipped by sync, import,
  > and backfill archiving, hidden from the reader's consent button, and
  > refused by the proxy when the request names the message (`message_id`,
  > which the Web UI always sends).
- [x] **Task 5.2 Suspicion scoring.** `msgvault suggest-cleanup` over a pool
  code narrows (SPAM/Promotions, never replied, sender not a person, has
  links). State per message: from name and domain, reply-to domain, link
  hosts, SPF/DKIM/DMARC results, To/Cc vs Bcc, labels, thread replied,
  sender kind, subject, first 500 chars. Questions: Noul `impersonation`,
  Noul `pressure`, Choice `category`. Composite score in code with hard
  signals. ≥ 0.80 lists as suspected phishing; staging still requires a
  user action; never auto-delete.
  > `internal/cleanupsuggest`, feature `cleanup_suggestions`, four messages
  > per request (`impersonation_i`, `pressure_i`, `category_i`). Category
  > options: personal, work, transactional_or_account,
  > marketing_or_newsletter, phishing_or_scam, other_junk. Score: 0.45
  > impersonation + 0.20 pressure + 0.35 P(phishing), plus DMARC fail
  > +0.20, SPF fail/softfail +0.10, DKIM fail +0.10, Reply-To on another
  > registrable domain +0.10, no link on the sender's domain +0.05, spam
  > label +0.05, full SPF/DKIM/DMARC pass −0.15; hard signals alone top out
  > at 0.60. Authentication comes from the topmost Authentication-Results
  > in the stored header block (the bounded prefix decode used by
  > correspondent kinds, failing closed to `unknown`). Only system labels
  > are sent. Results live in `cleanup_suggestions` keyed by message.
  > `automatic` has no effect: there is no unattended path.
- [x] **Task 5.3 Review surfacing.** `show-deletion` and the Web UI deletion
  review list staged messages that scored personal/work ≥ 0.50 as "possibly
  worth keeping".
  > "personal/work" is the sum of the two category probabilities. Manifests
  > hold provider IDs, so the lookup maps them back through the manifest's
  > source reference; a manifest without one (older TUI batches) lists
  > nothing. `GET /deletions/{id}` carries up to 50 with a total count; the
  > CLI prints up to 20. Messages never judged are not listed.

## Phase 6: query understanding for Explore

Site: `internal/api/explore.go` (`prepareExploreRequest`), Svelte search
bar.

- [x] **Task 6.1 Option generation in code.** Date-phrase dictionary to
  windows, directory matches to person candidates, accounts, message types.
  > `internal/queryunderstand`. Date phrases become local-day windows in the
  > browser's zone (ambiguous ones offer both readings, at most 4); type
  > words name fixed message type options; with two or more accounts, a word
  > matching an account's type, display name, or domain offers it; name
  > words are looked up in the people completion index (observed people
  > merged with curated profiles), full names first, at most 6 lookups and
  > 4 people, keeping only names whose whole words (case and accents
  > folded) include every looked-up word. Each candidate keeps the exact
  > query text it came from and its position.
- [x] **Task 6.2 Batched judgment.** Choices `message_type`, `time_window`,
  `person`, `person_role`, `account`; Noul `natural_language`. Runs in
  parallel with the search, 800 ms budget, dropped if late.
  > Feature `query_understanding`, `[jev.query_understanding]` (no automatic
  > switch), endpoint `POST /explore/query-understanding` (API 2.43.0). It is
  > a separate request the Web UI sends beside `POST /explore`, so the search
  > never waits; the endpoint bounds candidates and judgment to 800 ms and
  > answers `late` otherwise. Slots (`window_N`, `person_N`, `account_N`)
  > keep the consented wording fixed; a request asks only the questions its
  > candidates need. Labels go through meetingjudge's identifier redaction.
  > A confident sender or recipient with exactly one email address becomes
  > one from:/to: operator (repeated operators are AND-ed); otherwise a
  > participant filter. Delegated agents are skipped.
- [x] **Task 6.3 UI.** Suggested chips at confidence ≥ 0.80; click applies
  and removes the source span. Offer hybrid when `natural_language ≥ 0.70`
  and full-text returns zero rows. Only the query and option labels leave.
  > Only a query typed in the Search bar or header field asks; restores,
  > Saved Views, and applied chips do not. Remaining chips stay offered for
  > the rewritten query.

## Phase 7: meetings and calendar

- [x] **Task 7.1 Plain-code calendar fixes.** Skip resource attendees,
  owner-declined events, `outOfOffice`/`focusTime`/`workingLocation`,
  transparent events; cap attendee count for activity weight.
  > Calendar sync no longer makes rooms and equipment participants and
  > records the owner's RSVP and the attendee count in event metadata.
  > `internal/meetingweight` owns the rules: an excluded event is no
  > interaction in relationship rankings or the activity spine; otherwise a
  > meeting weighs 1 up to 10 attendees and 10/n above. The cache exports
  > the non-unit weights as `meeting_weights` (cache schema 32), and
  > `relationship_daily.meeting_weight` replaces the meeting count in the
  > score while `meeting_count` still counts events. Events synced before
  > this change need a resync to record the owner's RSVP; the attendee count
  > falls back to the stored attendee rows. Temperature summaries still
  > count every event with the owner present.
- [x] **Task 7.2 Action-item assignee.** Choice among email-bearing
  attendees plus `owner` and `none_or_unclear`, batched per meeting at
  import; store `assignee_participant_id` with confidence at ≥ 0.80 and a
  provenance of `inferred`; add `assignee_person_id` filter.
  > Feature `meeting_action_assignee` in `internal/meetingjudge`, run by
  > `msgvault meetings judge` and, with `automatic = true`, at each cache
  > build (after sync or import) rather than inside the import transaction.
  > One Choice per item, eight items per request, over fixed slots
  > `attendee_1`..`attendee_12` plus `owner` and `none_or_unclear`; a meeting
  > with more attendees is recorded without a request. Only items with no
  > source assignee name or address are asked. Rows live in
  > `meeting_action_assignees` keyed by (meeting, ordinal) with the item
  > title they judged; below 0.80 the row is `none_or_unclear` so the item
  > is not asked again. The table admits `user` rows that inference never
  > replaces, but no user edit surface exists yet. The owner's identities
  > are never attendees and never sent. `assignee_person_id` (API 2.42.0,
  > MCP, CLI) matches the source assignee address or the inferred
  > participant; rows carry `inferred_assignee`. Titles, labels, and item
  > text are redacted (addresses and phone numbers) before sending; each
  > row stores a fingerprint of its inputs and is re-judged when they
  > change; a projection that gains a source assignee drops the inference,
  > and listings ignore it. Event kinds weighing 0 also leave the activity
  > spine: storing one requeues the series for projection.
- [x] **Task 7.3 Event kind.** Choice over one_on_one, small_working_meeting,
  large_group_or_all_hands, external_webinar_or_marketing,
  personal_hold_or_logistics, social; recurring series asked once; weights
  meeting activity, confidence < 0.60 keeps count-based weight.
  > `internal/meetingjudge`, feature `meeting_event_kind`, command
  > `msgvault meetings judge` (daemon-run), automatic at cache build. A
  > series is a calendar conversation; its newest event that is a meeting
  > describes it, and one with none is stored by rule as `not_a_meeting`
  > without a request. State is the title, length, recurrence, occurrence
  > count, attendee and external-attendee counts, and whether the owner
  > organized it; no names, addresses, or descriptions. Confidence is the
  > probability of the chosen kind. Kind weights: one-on-one and working
  > meeting 1, social 0.5, all-hands 0.25, webinar and hold 0. Rows live in
  > `calendar_event_kinds`, are never replaced, and bump the meeting weight
  > revision so a derived cache refresh republishes weights.

## Phase 8: sweep and remaining people items

- [x] **Task 8.1 Sweep evidence rerank** before the chat LLM using the
  shared Noul reranker; drop < 0.20.
  > Feature `sweep_evidence_rerank` (`internal/sweepjudge`), wired into the
  > worker as `peoplesweep.ContextJudge`. Only retrieved context is judged:
  > seeds (newly changed messages) always reach the chat model so cursor
  > progress is unchanged. One request per target that retrieved context,
  > asking the search reranker's batched `candidate_i` Nouls with the
  > target's catalog description as `query` and each item's date and
  > redacted excerpt (addresses and phones replaced) as a candidate. An
  > item is dropped only when every target that retrieved it judged it
  > below 0.20; a target that could not be judged (sensitive, gate, budget,
  > or provider failure) keeps its items. When a packet must shrink, the
  > least relevant context leaves first, which without judgments is the old
  > trim-from-the-end order. A per-attempt memo keeps the several
  > assemblies of one attempt to one judgment per target. Relevance scores
  > are not stored: they only shape what the chat model reads, like the
  > model call itself, and replays resolve from the stored generation
  > (claims, cited evidence, reported scores), never from live judgments.
- [x] **Task 8.2 Claim grounding.** Nouls `stated` and `current` per claim
  replace the chat LLM's self-reported confidence as `ReportedScore`.
  > Feature `sweep_claim_grounding` (`internal/sweepjudge`), wired into the
  > worker as `peoplesweep.ClaimGrounder` after every extraction batch and
  > before organization resolution and apply. Eight claims per request, two
  > Nouls each (`stated_N`, `current_N`) over the target description, the
  > relation, the rendered value, and up to three cited excerpts (newest
  > first, 1,000 characters, addresses and phones redacted). The score is
  > `round(1000 × stated × current)`. Sensitive targets, values that carry
  > an address or phone number, and every claim after a failure keep the
  > model's score; claims are never added, dropped, or reordered, and the
  > resolver arithmetic is unchanged. The reported score is only the
  > resolver's confidence term (score / 10, at most 100 of the 750 apply
  > threshold); source class, directness, authority, freshness, and
  > corroboration carry the rest, so grounding shifts margins but never
  > applies a claim by itself. The grounded score is stored on the claim, so
  > replays need no live call.
- [x] **Task 8.3 Duplicate-person candidates.** Code proposes pairs (same
  display name across addresses, same local part across domains, person
  embedding neighbors); Noul `same_person` batched 20 per request; write
  `IdentityMatchCandidate` with basis `display_name` and confidence; never
  auto-accept; drop < 0.30.
  > Feature `person_duplicates` (`internal/persondedup`), run by
  > `msgvault person judge` (daemon-run) and, with `automatic = true`, at
  > each cache build (200 pairs). `Store.PersonDuplicateProposalsContext`
  > groups email-bearing identity clusters by normalized display name (two
  > or more words, order-insensitive, no team or service words) and by
  > distinctive local part at different domains; groups larger than five
  > clusters propose nothing. Owner clusters, clusters with any non-person
  > effective kind, shared-mailbox signals, pairs bound to one person, pairs
  > with any participant-to-participant candidate, and rejected
  > participant-to-person decisions across the pair are excluded.
  > `person_duplicate_judgments` remembers every judgment by the pair's
  > cluster roots and an inputs fingerprint. A probability ≥ 0.30 writes a
  > participant-to-participant candidate (basis `display_name`, source
  > `system`, source_ref `person_duplicate`, the probability as confidence,
  > signals as evidence); the system never accepts it, a user accept links
  > the participants, and two bound people return the 409 merge flow.
  > Reviews gains the **Possible duplicate people** origin
  > (`origin=person_duplicate`, API 2.44.0). State is display names (names
  > carrying an address or phone are dropped) and full email addresses;
  > phones are never sent.
- [ ] **Deferred: person embedding neighbors** as a third proposal source.
  Needs a way to read a person's own published vector (neither vector
  backend exposes one) and to open the active person generation from the
  judge command; add it as another proposal source feeding the same
  judgment when that exists.
- [x] **Task 8.4 Small ones.** Primary current role Choice when two or more
  current roles are system-set; display-name Choice at promotion when two
  or more distinct names; merge attribute conflict Noul at ≥ 0.95.
  > One feature, `person_profile_choices` (`internal/profilejudge`), run by
  > `msgvault person judge` next to duplicate people and, with
  > `automatic = true`, at each cache build. Each request asks one of the
  > three consented question groups (`JudgeQuestions`). Primary role: two
  > to six current employments, none declared (user, CardDAV, vCard), and
  > no employment pin; a role at ≥ 0.80 is promoted without writing a pin,
  > so a later user choice wins, and the store rechecks the role set under
  > the write. Display name: promotion records `person_display_name_seeds`
  > when the cluster used two or more distinct names; while the name still
  > equals the seed, a name at ≥ 0.80 renames through the normal CAS rename,
  > and any rename in between wins. The rename is asynchronous (the judge
  > run), not inside the promotion transaction. Merge conflicts: pending
  > scalar conflicts of non-sensitive fields whose absorbed value is not
  > declared, eight per request; ≥ 0.95 rejects the absorbed value (keeps
  > the survivor's) with reviewer `jev`. Judgments live in
  > `person_profile_judgments` (fingerprinted per person and kind) and
  > `person_merge_conflict_judgments`. Values or names with an address or
  > phone are never sent. Resolver thresholds are unchanged.

## Plain-code fixes to land alongside (no Jev)

- [x] Strip stopwords and use word boundaries for the subject boost; fall
  back to OR when the AND-ed BM25 leg returns zero hits.
- [x] `getAccountID` case-insensitive with valid accounts in the error;
  document the `message_type:` operator in the MCP catalog; tokenised AND
  matching in `profileMatchesPeopleQuery`.
- [x] Keep signature blocks in the employment evidence lane.
  > The people sweep keeps the signature block of a message the person
  > authenticated as sender (the only messages admitted as their own
  > evidence), since a signature is where a title and employer are stated.
  > Quoted replies are still dropped, and an unauthenticated sender's
  > signature is still stripped.

## Out of scope

Anything that changes resolver thresholds or user pins; auto-accepting merges,
deletions, or identity links; sending message bodies for correspondent
classification; document-chunk reranking until message reranking has passed
its eval gate.
