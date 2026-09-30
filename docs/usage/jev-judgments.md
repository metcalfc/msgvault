# Jev judgments

Jev judgments let a few narrow decisions that used to be string matching ask
TypeSafe's System One model (Jev) a handful of typed yes/no questions instead.
Your code keeps every workflow, threshold, and side effect; Jev only returns
probabilities. Nothing leaves your machine until you enable Jev, enable the
feature, store an API key, and record consent for the feature's exact policy.
Every Jev-backed path falls back to today's behavior when Jev is off, over
budget, slow, or wrong.

This page covers what each feature sends, the thresholds code applies, how
consent and budgets work, and how to turn everything off. This is `main`
functionality; check the [changelog](../changelog.md) for the release it ships
in.

## How a judgment works

1. Code builds a small state object from data it already holds and asks a few
   independent questions in one request. Each question is a Noul (a yes/no
   question that returns the probability of yes), a Choice, or a Score.
2. Jev returns a probability per question. Code applies fixed thresholds and
   stores the judgment as an input to the existing deterministic logic. It
   never writes a judgment straight into a profile fact.
3. Every request runs through one gate that rechecks, immediately before
   sending, that `[jev]` is enabled, the feature is enabled, an API key
   resolves, and consent is active for the feature's current policy. Scheduled
   and other unattended paths additionally need the feature's `automatic = true`.

The daemon reads `[jev]` when it starts. Editing the section, in `config.toml`
or in Settings, takes effect after the daemon restarts, and Settings marks
every `jev.*` key restart-required. Consent and the API key are checked live:
`msgvault jev revoke` stops the next request and a key pasted in Settings is
used by the next request, no restart needed. `msgvault jev status` reports the
configuration on disk.

The policy a consent covers is a fingerprint of the feature name, the exact
question wording, the list of state fields that leave the machine, the model,
and the endpoint. Changing any of them changes the fingerprint and requires a
new `msgvault jev consent`.

## Turn it on

1. Set the `[jev]` section in `config.toml` (see the
   [configuration reference](../configuration.md#jev)):

   ```toml
   [jev]
   enabled = true

   [jev.identity_verification]
   enabled = true
   # automatic = true   # also let the daemon's scheduled enrichment runs ask

   [jev.organization_resolution]
   enabled = true
   # automatic = true   # also let scheduled enrichment and sweep runs ask
   ```

2. Provide an API key. Either paste it in Settings under **Jev judgments**
   (it is stored bound to the endpoint origin and never shown again) or set
   the environment variable named by `api_key_env` (default
   `TYPESAFE_API_KEY`) for the daemon.
3. Review the disclosure and consent:

   ```bash
   msgvault jev consent enrichment_identity        # prints exactly what is sent
   msgvault jev consent enrichment_identity --yes  # records consent
   msgvault jev status                             # consent, credential, today's counters
   ```

Consent is per feature. `msgvault jev revoke enrichment_identity` or
`msgvault jev revoke --all` stops the next request immediately.

## Budgets and safety

- **Daily limits** are per feature and persisted in the archive, so they
  survive daemon restarts. `max_requests_per_day` caps requests (default 500).
  `max_cost_usd_per_day` caps measured spend (default 1.00 USD) and applies only
  when `input_usd_per_million_tokens` or `output_usd_per_million_tokens` is set;
  without prices, requests are counted and no cost is reported or assumed.
- **Circuit breaker.** Three consecutive provider failures pause requests for
  30 seconds; one probe request then runs and either closes the breaker or
  reopens it.
- **Bounds.** Requests use the pinned endpoint and model
  (`https://api.typesafe.ai/v1/systemone`, `jev-1.13.0`), refuse redirects,
  require a JSON response from the same model, and cap request and response
  sizes. Each request has a timeout (`request_timeout`, default 10s).
- **Logging.** Logs carry question IDs, answer types, probabilities,
  confidence, latency, token counts, and budget state. They never carry the
  state that was sent.
- **Failure never fails the user-facing operation.** When a judgment cannot
  run, the caller uses its pre-Jev decision and reports the category, such as
  `consent_required`, `breaker_open`, `request_limit`, or `timeout`.

## Feature: enrichment identity check

Feature name: `enrichment_identity`. Setting: `[jev.identity_verification]`.

External [person enrichment](people-enrichment.md) accepts a returned person
only when both the name and the current company match the request exactly.
Real providers return "Priya R." for "Priya Ramanathan" or "Example Labs (YC W21)"
for "Example Labs", which the exact rule rejects.

Two things change, in order:

1. **Deterministic retry, no Jev.** When Exa returns no entity, the worker
   retries once with a code-built name variant: a middle name or initial
   dropped, a suffix such as Jr. or PhD dropped, or "Last, First" reordered.
   The retry is a second provider call: it is counted against the provider's
   `max_requests_per_run` and `max_requests_per_day` before it is sent, is
   skipped when either cap would be exceeded, and its identity is recorded and
   checked against suppressions like the first call. A returned name that
   equals such a variant of the requested name counts as the exact name.
2. **Semantic check, only with consent.** When exactly one of name and current
   company matched exactly and the other did not, the worker asks three
   independent Nouls about the requested and returned identity. The
   non-exact field decides:

   | Result | Condition | Outcome |
   |---|---|---|
   | Accepted | non-exact field ≥ 0.90 and `name_conflict` ≤ 0.20 | Reason `semantic_name_company`, identity score 900 (the same as an exact name-and-company match), claims flow through the resolver as usual |
   | Uncertain | non-exact field ≥ 0.50 | Attempt state `identity_uncertain`, probabilities stored, no claim applied until you decide |
   | Rejected | otherwise, or Jev unavailable | Exactly today's rejection |

   `person facts decisions` shows an undecided attempt's claims as
   identity-rejected decisions.

### Confirm or reject an uncertain identity

Only you decide an `identity_uncertain` attempt; nothing decides it
automatically. Open **Reviews → Enrichment identities** in the Web UI, or use
`msgvault person enrichment review list|accept|reject`.

Each attempt shows the person, the three probabilities, and what the provider
returned as far as the archive kept it: the name, current roles, and location
from the attempt's stored claims, and the host of its profile URL. The raw
returned identity is never stored.

- **Confirm** (`accept`) means the returned identity is this person. The
  attempt's stored claims are applied again at identity score 1000, the
  verified level, with reason `user_confirmed`. The provider person ID is
  attached at confidence 1000, so later lookups skip the identity check. The
  attempt becomes `succeeded` and the profile's refresh is scheduled.
- **Reject** means it is someone else. The attempt becomes `identity_rejected`
  with reason `user_rejected`, and a negative is recorded for this person. A
  later result naming that provider person ID is rejected for this person,
  whatever its identity check says. Attempts recorded before this change kept
  no provider person ID, so their negative uses the profile URL instead.

A decision applies only while the attempt is still `identity_uncertain`; a
second decision fails without changing anything. Confirming needs the person
to be tracked, and fails when the provider identity already belongs to
another person.

### What leaves the machine

Only these fields, and only for a result that already matched on one of name
or company:

- `requested.name`, `requested.company`, `requested.email_domain` (the
  domain of the request email, never the address)
- `returned.name`, `returned.first_name`, `returned.last_name`,
  `returned.location`
- `returned.current_roles[].title`, `returned.current_roles[].company` (at
  most 5), `returned.past_companies[]` (at most 10), `returned.profile_url_host`

No message content, no addresses, no identifiers.

### The questions, exactly as sent

- `name_compatible` (Noul): "Could `returned.name` be the same person as
  `requested.name`?" Yes means the returned name is the requested name, or an
  initial, abbreviation, nickname, reordering, or transliteration of it. No
  means it belongs to a different person or only shares a common first name.
- `company_same` (Noul): "Do `requested.company` and the current employer in
  `returned.current_roles` name the same organization?" Yes allows a legal
  suffix, an accelerator batch tag, a former name, or a parent and its
  well-known product. No means a different organization, a competitor, or only
  a similar-sounding name.
- `name_conflict` (Noul): "Is `returned.name` clearly a different person from
  `requested.name`?" Yes means a given name or surname differs in a way an
  initial, nickname, or reordering cannot explain.

`msgvault jev consent enrichment_identity` prints the same disclosure.

## Feature: organization resolution

Feature name: `organization_resolution`. Setting:
`[jev.organization_resolution]`.

Employment facts name organizations as text. The exact lookup matches a name
only after lowercasing and collapsing spaces, so "Example Labs, Inc." or
"Example Labs (YC W21)" creates a second organization beside "Example Labs",
and "General Partner" and "Partner" at one firm become two jobs. This feature
decides both questions once and stores the answer, so the exact lookup and
employment projection get them right from then on.

It runs before an enrichment result or a people sweep's facts are saved, for
each organization a fact names without an ID. It writes nothing unless the
run still holds its lease: each write first renews it. The steps are:

1. **Exact lookup, no Jev.** A name and domain that already resolve to one
   organization are used as they are.
2. **Shortlist in code, no Jev.** On a miss, code picks at most eight existing
   organizations whose name or alternate name shares words, a prefix, or
   letter patterns with it, or whose domain shares its registrable domain
   (`eu.example.com` and `example.com`). No shortlist means the organization
   is created as before, and nothing is sent.
3. **One request.** Jev picks which shortlisted organization, if any, the
   name is, and answers one yes/no question per job-title pair (at most four)
   that the person already has at the organizations involved.

| Result | Condition | Outcome |
|---|---|---|
| Alias | best candidate ≥ 0.85 | The name, and its domain when the organization lacks it, become lookup keys of that organization. Source `system`, source_ref `jev:organization_resolution:<model>`, the probability as confidence. |
| Review | best candidate ≥ 0.50 | The organization is created as before, and an organization match review asks you whether the two are the same. |
| New organization | otherwise, or Jev unavailable | Exactly today's behavior. |
| Same role | `title_same_role_N` ≥ 0.85 | The claimed title maps to the known title at that organization. |

Employment projection maps titles through these mappings before comparing
facts. Two facts for "Partner" and "General Partner" at one organization then
add up as one role, and a later fact for the same role updates the existing
employment instead of adding a second current job. A new employment shows the
known title. Resolver scores and thresholds are unchanged.

Aliases and title mappings are stored in the archive, and projection reads
only them, never Jev. Replaying or re-resolving facts gives the same answer
with Jev off, and a name that already resolved is never asked about again.

### Confirm or reject an organization match

Open **Reviews → Organization matches** in the Web UI, or use
`GET /api/v1/organization-match-reviews` and
`POST /api/v1/organization-match-reviews/{id}/accept|reject`. Each review
shows the proposed name and domain, the existing organization, and the
probability.

- **Same organization** (`accept`) merges the organization that was created
  for the name into the existing one, which keeps the name as a former name.
  The name and domain are added to the existing organization with user
  provenance. When no separate organization exists, only the alias is added.
- **Different organization** (`reject`) keeps that organization off the name's
  shortlist from now on.

Merging organizations in the directory carries their reviews, rejections, and
title mappings to the surviving organization, so a decision made before the
merge keeps applying.

Accepting fails when more than one organization has the proposed name, or
when the merge would give a person two current jobs with the same title at
one organization; resolve those in the directory first.

### What leaves the machine

Only organization names, domains, and job titles:

- `reference.name`, `reference.domain`: the organization name and domain from
  the fact
- `candidates.candidate_N.name`, `candidates.candidate_N.domains[]`,
  `candidates.candidate_N.other_names[]` for each shortlisted organization
  (N is 1 to 8; at most five domains and five alternate names each)
- `title_pairs.pair_N.organization`, `title_pairs.pair_N.title`,
  `title_pairs.pair_N.other_title` (N is 1 to 4)

No message content, no people's names, no addresses, no identifiers. A
request carries only the questions it needs: `org_ref` when the lookup
missed, and one `title_same_role_N` per title pair.

### The questions, exactly as sent

- `org_ref` (Choice): "Which organization in `candidates` is the same
  real-world organization as `reference`? Allow a legal suffix, an
  accelerator batch tag, a former name, a regional office, or a shared domain.
  An option whose key is absent from `candidates` never applies." Options
  `candidate_1` to `candidate_8` ("`candidates.candidate_N` is the same
  organization as `reference`.") and `new_organization` ("No organization in
  `candidates` is `reference`: it is a different organization, a competitor,
  or only has a similar name.").
- `title_same_role_N` (Noul, N is 1 to 4): "Do `title_pairs.pair_N.title` and
  `title_pairs.pair_N.other_title` name the same role at
  `title_pairs.pair_N.organization`?" Yes means the same role for one person:
  a synonym, an abbreviation, a longer or shorter form, or a formal and an
  informal name for it. No means different roles: a different function, a
  clearly different seniority, or unrelated positions.

`msgvault jev consent organization_resolution` prints the same disclosure.
At most eight organizations are asked about per saved set of facts, within one
minute; the rest resolve as before.

## Turn it off

- `msgvault jev revoke --all` stops every feature at the next request without
  touching configuration or restarting anything.
- Delete the stored key in Settings, or unset the environment variable and
  restart, to make the credential check fail closed at the next request.
- `enabled = false` under `[jev]` or under a feature section turns the gate
  off once the daemon restarts; the exact rules then apply exactly as before
  Jev existed.

Stored judgments and counters stay in the archive for audit; they hold
probabilities and outcomes, not the compared values.

## Limitations

- Only the enrichment identity check and organization resolution exist
  today. The other features in the engineering record
  `docs/internal/jev-judgments-plan.md` are proposals.
- Facts from different saves for the same organization and role do not add
  up: the newest save for a role replaces the older one, as it always has.
- Attempts decided before provider person IDs were kept can only be refused
  by profile URL; a provider that returns the same person under a new URL is
  not caught by that negative.
- Budgets count requests per UTC day. A process restart resets the in-memory
  breaker but not the daily counters.
