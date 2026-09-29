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
   | Uncertain | non-exact field ≥ 0.50 | Attempt state `identity_uncertain`, probabilities stored, no claim applied |
   | Rejected | otherwise, or Jev unavailable | Exactly today's rejection |

   `person enrichment status` lists `identity_uncertain` attempts with their
   probabilities for review. `person facts decisions` shows the attempt's
   claims as identity-rejected decisions.

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

## Turn it off

- `msgvault jev revoke --all` stops every feature at the next request without
  touching configuration.
- `enabled = false` under `[jev]` or under a feature section turns the gate
  off; the exact rules apply exactly as before Jev existed.
- Delete the stored key in Settings, or unset the environment variable, to
  make the credential check fail closed.

Stored judgments and counters stay in the archive for audit; they hold
probabilities and outcomes, not the compared values.

## Limitations

- Only the enrichment identity check exists today. The other features in the
  engineering record `docs/internal/jev-judgments-plan.md` are proposals.
- `identity_uncertain` attempts appear in `person enrichment status`; the web
  Facts view shows their claims only as identity-rejected decisions.
- Budgets count requests per UTC day. A process restart resets the in-memory
  breaker but not the daily counters.
