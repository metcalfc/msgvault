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

   [jev.correspondent_kind]
   enabled = true
   # automatic = true   # also classify new identities at each cache build

   [jev.cleanup_suggestions]
   enabled = true      # msgvault suggest-cleanup only; never automatic

   # [jev.rerank]       # sends message body text; not recommended until
   # enabled = true     # the evaluation gate passes (see below)

   [jev.meeting_event_kind]
   enabled = true
   # automatic = true   # also judge new calendar series at each cache build

   [jev.meeting_action_assignee]
   enabled = true
   # automatic = true   # also infer assignees for new meetings at each cache build

   [jev.query_understanding]
   enabled = true      # searches you type in the Web UI only; never automatic
   [jev.sweep_evidence_rerank]   # sends message excerpts the person wrote
   enabled = true
   # automatic = true   # also judge during the daemon's scheduled people sweeps

   [jev.sweep_claim_grounding]   # sends message excerpts the person wrote
   enabled = true
   # automatic = true   # also ground claims during scheduled people sweeps
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

Consent is per feature: run the same two `consent` commands with
`organization_resolution`, `correspondent_kind`, `cleanup_suggestions`,
`search_rerank`, `meeting_event_kind`, `meeting_action_assignee`,
`query_understanding`, `sweep_evidence_rerank`, or `sweep_claim_grounding`
for those features. `msgvault jev revoke enrichment_identity`
or `msgvault jev revoke --all` stops the next request immediately.

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
each organization a fact names without an ID. Every write checks, inside its
own transaction, that the run still holds an unexpired lease, and is refused
otherwise. The steps are:

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

Merging organizations in the directory carries their reviews, aliases, and
title mappings to the surviving organization, so a decision made before the
merge keeps applying whichever organization survives. When both were asked
about the same name, a decision beats an open question and the later of two
decisions wins. Titles either organization treated as one role stay one role,
under the surviving organization's title.

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

## Feature: correspondent kind

Feature name: `correspondent_kind`. Setting: `[jev.correspondent_kind]`.
Command: [`msgvault kinds build`](../cli-reference.md#kinds-build).

Many archive identities are not people: receipts, notifications, mailing
lists, team aliases. They crowd relationship rankings and waste enrichment
lookups. A [user decision](people.md#records-that-arent-people) always wins;
below it, `kinds build` classifies identities above a message floor in two
steps:

1. **Rules, no Jev.** A chat provider's bot flag, an SMS short code, a
   no-reply address, the list's own posting address, or `Auto-Submitted:
   auto-generated` decides on its own. Bulk headers (`List-Unsubscribe`,
   `Precedence: bulk`) decide only for a sender you never wrote to, with at
   least three messages, whose messages were not relayed by a list. The
   Gmail Promotions category also needs a bulk header, ten messages, and a
   non-freemail address, so a person Gmail files under Promotions is left
   for Jev. Rules write `automated` or `mailing_list`.
2. **Jev, only with consent.** The rest are asked one Choice each, ten
   identities per request. Code maps the answer:

   | Answer | Stored kind | Effect |
   |---|---|---|
   | `individual_person` ≥ 0.60 | `person` | Ranked as today |
   | `shared_role_or_team_mailbox` ≥ 0.60 | `shared_mailbox` | Listed as a labelled row; left out of matching and enrichment |
   | `mailing_list_or_group` ≥ 0.60 | `mailing_list` | Left out of People, rankings, matching, and enrichment |
   | `automated_notification_or_transactional` or `marketing_or_newsletter` ≥ 0.60 | `automated` | Same as a mailing list |
   | anything else | `unclear` | Left out of rankings only; waits in review |

So a Jev-classified identity appears in relationship rankings only when its
`individual_person` probability is at least 0.60, unless you mark it a
person. Enrichment skips a profile made only of automated, mailing-list,
organization, or ignored identities with outcome `not_a_person`.

Review unclear identities in **Reviews → Unclear correspondents**, or with
`msgvault person kind list --kind unclear` and `msgvault person kind set`.
`msgvault list-senders --kind automated` lists senders by kind. The
analytics cache's `relationship_people` dataset carries each identity's
effective kind, its source, and the Jev `individual_person` probability.

`kinds build` runs by hand. With `automatic = true`, each analytics cache
build also classifies up to 200 new identities, rules first and then Jev;
rules do not run at cache build without it. Your own identities are never
classified or sent.

### What leaves the machine

Per identity, under `identities[i]`:

- `label`: the display name
- `addresses[].local_part` and `addresses[].domain`: up to five email
  addresses, split; phone numbers are never sent
- `counts.sent`, `counts.received`, `counts.meetings`: messages the identity
  sent, messages you sent it, and meetings or events it attended
- `list_id_share` and `category_shares`: the share of its messages carrying a
  List-Id and each Gmail category label
- `header_counts.sampled`, `.list_unsubscribe`, `.auto_submitted`,
  `.precedence_bulk`, `.list_id`: how many of up to five sampled messages
  carried each header, never the header values
- `subjects_from_them[]` (up to five) and `subjects_from_owner[]` (up to
  three): recent subjects, each cut to 160 characters

No message bodies, no identifiers, no phone numbers, and nothing about your
own identities.

### The question, exactly as sent

`kind_0` through `kind_9` (Choice), one per identity in the request: "What
kind of correspondent is `identities[i]`? Judge from its label, address
parts, message counts, list and category shares, header counts, and
subjects." The options are:

- `individual_person`: one human writing as themselves, including from a work
  address.
- `shared_role_or_team_mailbox`: a role or team address that several people
  read or write from, such as a support desk or a team alias people reply to.
- `mailing_list_or_group`: a list or group address that relays messages from
  many members.
- `automated_notification_or_transactional`: machine-generated
  notifications, receipts, alerts, security or account messages.
- `marketing_or_newsletter`: marketing, promotions, or a newsletter sent in
  bulk.
- `unclear`: the evidence does not support any other option.

A request with fewer than ten identities sends only their questions.
`msgvault jev consent correspondent_kind` prints the same disclosure.

## Feature: cleanup suggestions

Feature name: `cleanup_suggestions`. Setting: `[jev.cleanup_suggestions]`.
Command: [`msgvault suggest-cleanup`](../cli-reference.md#suggest-cleanup).

Cleanup suggestions help you find junk worth deleting, and warn you before
you delete mail that looks personal. They are suggestions only: nothing is
ever staged or deleted automatically, and `automatic` has no effect because
the feature only runs when you ask.

1. **The pool, in code.** Live email labeled `SPAM` (or an IMAP `Junk`
   label) or `CATEGORY_PROMOTIONS`, that you did not send, in a conversation
   you never wrote in, from a sender not
   [classified as a person](#feature-correspondent-kind), with at least one
   link in its body. Newest first, up to `--limit` (default 50) per run.
   Messages already judged are skipped unless you pass `--rejudge`.
2. **Jev, only with consent.** Four messages per request, three questions
   each (below).
3. **The score, in code.** `0.45 × impersonation + 0.20 × pressure + 0.35 ×
   P(phishing_or_scam)`, plus hard signals: DMARC fail +0.20, SPF fail or
   softfail +0.10, DKIM fail +0.10, a Reply-To on another domain +0.10, no
   link on the sender's domain +0.05, a spam label +0.05; SPF, DKIM, and DMARC
   all passing −0.15. The result is clamped to 0–1. Hard signals alone top
   out at 0.60, so they never mark a message by themselves.

| Result | Where it shows |
|---|---|
| Score ≥ 0.80 | Listed by `suggest-cleanup` as suspected phishing |
| `personal` + `work` ≥ 0.50 | Listed as possibly worth keeping when the message is in a staged deletion batch (`show-deletion` and Web UI deletion review) |

To act on a suspected message, stage it yourself with
`msgvault stage-delete --ids`, review it, and run `delete-staged`.
Authentication results come only from an `Authentication-Results` header
stamped by a receiving server you trust: `mx.google.com` for Gmail sources,
plus any authserv-ids listed in `trusted_authserv_ids`. The topmost such
header wins. A header with any other authserv-id could have been written by
the sender and is ignored, so an IMAP source reports `unknown` until you list
its server. Only the header block of the stored raw message is read, and a
message whose header block cannot be decoded sends `unknown`.

### What leaves the machine

Per message, under `messages[i]`:

- `from_name` and `from_domain`: the sender's display name (cut to 120
  characters) and the domain of its address; never the local part
- `reply_to_domain`: the domain of the `Reply-To` address, if any
- `link_hosts[]`: up to ten distinct host names of links in the body; never
  paths or query strings
- `authentication.spf`, `.dkim`, `.dmarc`: the receiving server's verdicts,
  such as `pass`, `fail`, `softfail`, `none`, or `unknown`
- `addressed_as`: `to_or_cc` when one of your addresses is a visible
  recipient, otherwise `bcc_or_undisclosed`
- `labels[]`: system labels only, such as `SPAM` or `CATEGORY_PROMOTIONS`;
  your own label names are never sent
- `thread_replied`: whether you wrote in the conversation
- `sender_kind`: the sender's [correspondent kind](#feature-correspondent-kind),
  or `unclassified`
- `subject`: cut to 200 characters
- `body_start`: **the first 500 characters of the message's text** (the
  plain-text body, or the HTML body with tags removed)

No addresses, no attachments, no text past the first 500 characters, and
nothing about mail you sent.

### The questions, exactly as sent

For each message `i` in the request:

- `impersonation_i` (Noul): "Does `messages[i]` pretend to come from a brand,
  organization, or person that `from_domain`, `reply_to_domain`, and
  `link_hosts` show it is not from?"
- `pressure_i` (Noul): "Does `messages[i]` pressure the reader to act at
  once: urgency, threats, account suspension, prizes, or requests for
  credentials or payment?"
- `category_i` (Choice): "What kind of mail is `messages[i]`? Judge from its
  sender, subject, opening text, labels, and authentication results." The
  options are `personal`, `work`, `transactional_or_account`,
  `marketing_or_newsletter`, `phishing_or_scam`, and `other_junk`.

A request with fewer than four messages sends only their questions.
`msgvault jev consent cleanup_suggestions` prints the same disclosure,
including the criteria for every answer.

## Feature: hybrid search reranking

Feature name: `search_rerank`. Setting: [`[jev.rerank]`](../configuration.md#jevrerank).
It is off by default and **not recommended until the evaluation gate below
passes**. Unlike the other features, it sends message body text.

Hybrid search fuses full-text (BM25) and vector rankings with reciprocal rank
fusion. A message that shares words or topic with the query can outrank the
one that actually answers it. This feature asks Jev, for each of the leading
results, whether the message contains the information the query asks for,
and reorders those results by that probability.

1. **Only searches that ask for it.** Each request opts in: the Web UI's
   hybrid search sets `rerank` on its Explore request, and
   `msgvault search --mode hybrid` sets `rerank=true` on `GET /api/v1/search`.
   An API client must pass the same parameter. MCP searches
   (`search_message_bodies`, `semantic_search_messages`) ask only when
   `[jev.rerank] mcp = true`, because an assistant may search while nobody is
   watching; with it off, MCP never causes a Jev call. Full-text
   (`--mode fts`) and vector searches never do, and no scheduled or
   background search ever does, so the feature has no `automatic` switch.
2. **The leading results only.** Code takes the first `top` (at most 30)
   fused results, loads them in one batched primary-key lookup, and leaves
   out any whose message type is in `message_types_excluded`. Fewer than two
   remaining means nothing is sent.
3. **One judgment per result.** Each result gets one Noul. Code sorts the
   judged results by probability, breaking ties by fused score, and puts them
   back into the positions judged results held. Excluded results and
   everything after the first `top` keep their fused positions.
4. **Pages agree.** The order is kept for ten minutes, keyed on the query,
   every search filter (accounts, dates, message types, labels, senders, and
   the rest), the index generation, and the IDs of the leading results in
   order. A message that is deleted or filtered out changes those IDs, so a
   stale order is never reused. The next page of the same search reuses the
   order without a second request, and concurrent identical searches share
   one request. Every search passes the consent, configuration, and budget
   checks itself before it may reuse a kept order or join a request in
   flight, so after `msgvault jev revoke` no search is reranked. A provider
   failure is kept the same way, so later pages keep
   the fused order too, but it never replaces an order already kept. Gate
   states (disabled, no consent, no key) are not kept, so a change applies to
   the next search.
5. **Bounded wait.** A search waits at most `[jev] request_timeout` (default
   10s) for the judgment and otherwise keeps the fused order. When every
   search waiting on a judgment has left, the judgment is cancelled, so no
   further requests are sent for it, and nothing is kept.

Any failure leaves the fused order and never fails the search. Hybrid
responses carry `rerank` with `status` (`applied` or `skipped`), `reason` for
a skip (for example `consent_required`, `timeout`, `too_few_candidates`),
`model`, `scored`, and `cached`, and `timings.rerank_ms`. With `explain=1`
each judged result's score breakdown carries `rerank`, its probability; the
CLI's `--explain` table adds a `JEV` column and a `Jev rerank:` line.

### What leaves the machine

- `query`: the search's free text (at most 4 KiB)
- Per judged message, one text of at most 2 KiB made of:
  - `Subject:` the subject line (cut to 300 bytes)
  - `From:` the sender's display name, or the address when there is no name
    (never a phone number)
  - `Date:` the sent date (`YYYY-MM-DD`)
  - the message body after the same cleaning semantic search applies before
    embedding: quoted replies, signatures, HTML, base64 blobs, and tracking
    parameters removed per `[vector.preprocess]`, cut so the whole text fits
    in 2 KiB

**Message body text leaves the machine.** The batched shape sends up to 30
such texts as `candidates[]` in one request; the per-candidate shape sends one
as `candidate` per request. No attachments, recipients, labels, or
identifiers are sent. `msgvault jev consent search_rerank` prints this
disclosure, including a line that says message body text is sent.

### The questions, exactly as sent

- Batched: `candidate_0` to `candidate_29` (Noul): "Could `candidates[i]` be
  the best answer to `query`?" Yes means "The `candidates[i]` contains the
  specific information needed to answer the query." No means "The
  `candidates[i]` is only topically similar or does not contain the needed
  evidence." A request asks only as many as it has results.
- Per candidate: `matches` (Noul): "Could `candidate` be the best answer to
  `query`?", with the same yes and no wording for `candidate`.

### Evaluation gate

Reranking is recommended only after
[`msgvault eval --rerank-jev`](../cli-reference.md#eval) shows, on the target
collection (matched TREC Legal 2010 messages, `--doc-key message`, hybrid
mode, `-n` of at least 10), that one complete request shape gains at least
**0.05 absolute Hit@10** over the fused hybrid ranking with a **p95 latency
under 2 s**. The eval builds its candidate text exactly as search does and
prints the gate as `pass`, `fail`, or `not_evaluated` (JSON `rerank_gate`).

The gate has not been run yet: it spends TypeSafe credit on archive mail and
needs the archive owner's go-ahead. No results are recorded, so
`enabled = false` remains the recommendation. TypeSafe's published gain was
measured against BM25 alone, not against fused hybrid search.

## Feature: meeting event kind

Feature name: `meeting_event_kind`. Setting: `[jev.meeting_event_kind]`.
Command: [`msgvault meetings judge`](../cli-reference.md#meetings-judge).

Relationship rankings weigh a shared meeting more than a sent message.
Without help, an all-hands with two hundred people, a vendor webinar,
and a one-on-one all look alike. Code already sets the weight from the
attendee count ([how calendar events count as meetings](meetings.md#how-calendar-events-count-as-meetings));
this feature asks Jev what kind of event a calendar series is:

1. **Rules, no Jev.** A series none of whose recent events is a meeting
   (cancelled, declined by you, out of office, focus time, working location,
   or marked free) is recorded as not a meeting and never sent.
2. **Jev, only with consent.** Every other series is asked one Choice, ten
   series per request. A recurring series is one question, asked once; a
   standalone event is its own series. The newest event that is a meeting
   describes the series.

Code maps the answer when its probability is at least 0.60:

| Answer | Meeting weight |
|---|---|
| `one_on_one` | 1 |
| `small_working_meeting` | 1 |
| `social` | 0.5 |
| `large_group_or_all_hands` | 0.25 |
| `external_webinar_or_marketing` | 0 |
| `personal_hold_or_logistics` | 0 |

Below 0.60 the attendee-count weight stays. A series weighed 0 this way also
stops counting as contact in last-contact dates; its events are re-projected
when the judgment is stored. The judgment is stored per series
(`calendar_event_kinds`, with its probabilities and the model) and never
revisited, and the next analytics cache build publishes the new weights.
`meetings judge` runs by hand. With `automatic = true`, each analytics cache
build also judges up to 200 new series first.

### What leaves the machine

Per series, under `events[i]`:

- `title`: the event title, cut to 160 characters, with email addresses
  replaced by `[email]` and phone numbers by `[phone]`
- `all_day` and `duration_minutes`: whether it is all day, and its length
- `recurring` and `occurrences`: whether it repeats, and how many of its
  events are archived
- `attendee_count` and `external_attendee_count`: how many people were
  invited, and how many of them have an address outside your calendar
  account's domain
- `organized_by_owner`: whether you organized it

No attendee names or addresses, no descriptions or locations, no conference
links, and nothing that identifies you.

### The question, exactly as sent

`event_kind_0` through `event_kind_9` (Choice), one per series in the
request: "What kind of calendar event is `events[i]`? Judge from its title,
length, recurrence, and attendee counts." The options are:

- `one_on_one`: two people meeting: a one-on-one, a check-in, or an interview
  with one other person.
- `small_working_meeting`: a few people working together: a team sync, a
  planning or design session, a customer or partner call.
- `large_group_or_all_hands`: a large group: an all-hands, a town hall, a
  department meeting, or a broadcast where most attendees listen.
- `external_webinar_or_marketing`: a webinar, a marketing event, a product
  demo for many registrants, or a conference session run by an outside
  organizer.
- `personal_hold_or_logistics`: not a meeting with others: a personal hold, a
  reminder, travel, a commute, a meal block, or other logistics.
- `social`: a social gathering: a team lunch, a party, drinks, or a
  celebration.

`msgvault jev consent meeting_event_kind` prints the same disclosure.

## Feature: meeting action assignee

Feature name: `meeting_action_assignee`. Setting:
`[jev.meeting_action_assignee]`. Command:
[`msgvault meetings judge`](../cli-reference.md#meetings-judge).

Meeting tools often record "Casey to send the draft" without saying who owns
it, so a person's action items cannot be listed. For an action item whose
source has no assignee name or address, this feature asks Jev which attendee
owns it:

1. Only items with no source assignee are asked about. The meeting tool's
   assignee is never replaced.
2. One meeting at a time, up to eight items per request, each item is one
   Choice among the meeting's attendees with an address (up to 12), you
   (`owner`), and `none_or_unclear`. A meeting with more than 12 such
   attendees is not sent.
3. An attendee or `owner` at 0.80 or more is stored as the item's inferred
   assignee (`meeting_action_assignees`, provenance `inferred`, with the
   probability as confidence and the full probabilities). Anything else is
   stored as `none_or_unclear`, so the item is not asked again. The row keeps
   a fingerprint of every input (meeting title, attendees, item title and
   description); when the meeting changes and any of them differs, the item
   is asked again. Only meetings changed since their last check are read,
   so a contact's renamed display name alone does not trigger a new
   judgment. When a later import gives the item a source assignee, the
   inference is dropped.

Action item listings (HTTP, MCP, CLI, and the Web UI) show the inferred
assignee separately from the source's, and `assignee_person_id` lists a
person's items from either. `meetings judge` runs by hand. With
`automatic = true`, each analytics cache build first handles up to 200 newly
imported meetings.

### What leaves the machine

Per request:

- `meeting.title`: the meeting title, cut to 160 characters
- `attendees.attendee_N.label`: each attendee's display name, or the local
  part of their address (the part before `@`) when no name is known, cut to
  120 characters. An address or phone number inside a name is dropped, a
  name that is only an address becomes its local part, and a name with
  nothing left becomes `attendee N`.
- `action_items.item_N.title` and `.description`: the item's text, cut to 200
  and 500 characters

In the meeting title and item text, email addresses become `[email]` and
phone numbers `[phone]`. Before matching, text is decoded (percent-escapes,
Unicode compatibility forms, Unicode dashes and spaces), and `mailto:`,
`tel:`, and spelled-out forms such as "name at domain dot com" count as
addresses. Decoding is only used to find identifiers: everything else is
sent exactly as written. Any run of 7 or more digits reads as a phone
number, except dates and dotted versions such as `1.2.3`; numbers joined by
a slash are judged separately, and a run longer than 15 digits is redacted
whole. An ambiguous number such as a meeting ID may be redacted too.

Your own identities are never attendees: you are the `owner` option, and
your name and addresses are never sent. No addresses, transcripts, summaries,
or notes leave the machine.

### The question, exactly as sent

`assignee_1` through `assignee_8` (Choice), one per item in the request:
"Who is responsible for `action_items.item_N` from the meeting
`meeting.title`? Match names in its title and description to attendee labels.
An option whose key is absent from `attendees` never applies." The options
are:

- `attendee_1` through `attendee_12`: `attendees.attendee_N` is the one person
  responsible for the item.
- `owner`: the person whose meeting notes these are, who is not in
  `attendees`: the notes' "I", "me", or "my", or an item addressed to the
  reader.
- `none_or_unclear`: no single person: the whole group, someone not listed,
  or the text does not say who.

A request with fewer than eight items sends only their questions.
`msgvault jev consent meeting_action_assignee` prints the same disclosure.

## Feature: Explore query understanding

Feature name: `query_understanding`. Setting:
[`[jev.query_understanding]`](../configuration.md#jevquery_understanding).
Off by default.

A search such as "texts from Ana last week" mixes words to match with
filters: a message type, a person, and a time period. Full-text search
requires every word, so it finds nothing. This feature suggests the filters
the query asks for, as chips under the Web UI's search bar.

1. **Only searches you type.** The Web UI asks after you submit a query in
   the Search bar or the header search field. Restored links, Saved Views,
   refreshes, applied suggestions, MCP, the CLI, and delegated agent tokens
   never ask, so the feature has no `automatic` switch.
2. **Beside the search, never in front of it.** The request
   (`POST /api/v1/explore/query-understanding`) runs alongside the search.
   Candidate lookup and the judgment together have 800 ms; a judgment that is
   not back by then is dropped (status `late`) and no chips appear. Any
   failure only means no chips; the search is never slower or failed by it.
3. **Candidates come from code.** Before anything is sent:
   - A date-phrase dictionary turns phrases such as "today", "last week",
     "past 3 days", "this month", "in 2025", "Q3", month names, and "since
     March 2026" into local calendar days in your browser's time zone. Weeks
     start on Monday.
     Ambiguous phrases offer both readings (for example "last week" is the
     previous Monday-to-Sunday week or the past 7 days). At most 4 windows.
   - Words such as "emails", "texts", "on slack", "meeting notes", or
     "invites" name message types.
   - With two or more accounts, a word matching an account's type, its
     display name, or its domain name offers that account. At most 6.
   - Other words are looked up in the people index, full names first, at
     most 6 lookups and 4 people. A person is offered only when every looked-up
     word is a whole word of their name, ignoring case and accents: "martha"
     offers Martha Example, "art" does not.
   Nothing is sent when there are no candidates and the query has fewer than
   three words.
4. **One request.** Jev answers Choices `message_type`, `time_window`,
   `person`, `person_role`, and `account`, and the Noul `natural_language`.
   A request asks only the questions its candidates need.
5. **Thresholds.** A chosen option at 0.80 or more becomes a chip; `none`
   never does. A message type is only offered when a query word named it.
   A person is a participant filter, which matches any of the person's
   identities in any role. When `person_role` is `sender` or `recipient` at
   0.80 or more and the person has exactly one email address, the chip uses
   one `from:` or `to:` operator instead, which keeps the direction but only
   matches email. A person with several addresses keeps the participant
   filter, because repeated `from:` operators must all match. When a full-text search returns nothing
   and `natural_language` is 0.70 or more, the search note offers **Try
   hybrid search**.
6. **You apply it.** A chip removes the words it came from (with a leading
   "from", "in", or "on") at the position the daemon reported, so an earlier
   copy of the same words, such as a quoted phrase, stays. It adds its filter: a person narrows the existing
   people, and a date bound, message type, or account replaces the current
   one. The other chips stay offered for the rewritten query.

The endpoint answers `status` (`judged`, `skipped`, or `late`), `reason` for
a skip (for example `disabled`, `consent_required`, `no_candidates`,
`request_limit`), `suggestions`, `natural_language`, and `offer_hybrid`.

### What leaves the machine

- `query.text`: the query as typed, with email addresses replaced by
  `[email]` and phone numbers by `[phone]` (the redaction meeting action
  assignee uses)
- `time_windows.window_N.label`: a window's description and dates, such as
  "Past 7 days (Sep 24 to Sep 30, 2026)"
- `people.person_N.label`: each candidate's name from the people index, cut
  to 120 characters. An address or phone number inside a name is dropped, a
  name that is only an address becomes its local part, and a person with
  nothing left is not offered.
- `accounts.account_N.label`: the account's type, its display name when that
  is a name, and its domain, such as "gmail account named Work at
  example.com". Never the address.

No messages, bodies, participant IDs, or addresses leave the machine.

### The questions, exactly as sent

- `message_type` (Choice): "Does `query.text` ask only for one kind of
  message? Choose the kind it asks for." Options: `email`, `text_message`
  (SMS, MMS, iMessage, or RCS), `whatsapp`, `slack`, `discord`, `teams`,
  `google_chat`, `facebook_messenger`, `calendar_event`,
  `meeting_transcript`, and `none` (the query does not limit the kind, or
  names a kind only as its topic).
- `time_window` (Choice): "Does `query.text` limit results to a time period?
  Choose the entry of `time_windows` that means what the query means. An
  option whose key is absent from `time_windows` never applies." Options
  `window_1` to `window_4` and `none` (no time limit, or the date words are
  part of the topic).
- `person` (Choice): "Does `query.text` ask for messages with a specific
  person listed in `people`? Choose that person. An option whose key is
  absent from `people` never applies." Options `person_1` to `person_4` and
  `none`.
- `person_role` (Choice): "If `query.text` asks for messages with a person,
  did that person send them, receive them, or either?" Options `sender`,
  `recipient`, `either`.
- `account` (Choice): "Does `query.text` ask for messages in one of the
  user's own accounts listed in `accounts`? Choose that account. An option
  whose key is absent from `accounts` never applies." Options `account_1` to
  `account_6` and `none`.
- `natural_language` (Noul): "Is `query.text` a natural-language question or
  description rather than keywords to match exactly?"

`msgvault jev consent query_understanding` prints the same disclosure.

## Feature: people sweep evidence relevance

Feature name: `sweep_evidence_rerank`. Setting:
`[jev.sweep_evidence_rerank]`. Runs inside the
[people sweep](people-automation.md#run-and-inspect-a-sweep).

For each fact it looks for (each target in the fact catalog, such as
employment), the people sweep retrieves up to `context_per_target` older
messages the person wrote and sends them to its chat model next to the newly
changed messages. Many retrieved messages say nothing about the fact. This
feature asks Jev, before the chat model sees them, which ones bear on it:

1. **Newly changed messages are never judged.** They are always sent, so the
   sweep's progress over the archive is unchanged.
2. **One request per target** that retrieved context, up to 30 messages per
   request, using the same question as
   [hybrid search reranking](#feature-hybrid-search-reranking). A sensitive
   target's context is never judged.
3. **Code drops a message below 0.20** for every target that retrieved it. A
   message some target kept, or could not judge, stays.
4. **When a packet must shrink** to fit the sweep's request limit, the least
   relevant context leaves first; without judgments it shrinks from the end,
   as before.

Any gate, budget, or provider failure keeps every retrieved message, exactly
as without the feature. A manual `msgvault person sweep run` or brief request
may ask; the daemon's scheduled sweeps ask only with `automatic = true`.

### What leaves the machine

Per request:

- `query`: the fact's catalog description, such as "Current and historical
  employment, including organization, title, role, department, location, and
  partial start and end dates". Never a name or an address.
- `candidates[]`: **for each retrieved message, its date and up to 2 KiB of
  excerpt text** from a message the person sent on a source that
  authenticates its sender (the same messages the sweep admits as the
  person's own evidence). Email addresses become `[email]` and phone numbers
  `[phone]`.

The excerpts are message text the person wrote. Your own identities, the
person's name, and their addresses are never sent as fields, and nothing from
messages other people wrote is sent.

### The question, exactly as sent

`candidate_0` to `candidate_29` (Noul): "Could `candidates[i]` be the best
answer to `query`?" Yes means "The `candidates[i]` contains the specific
information needed to answer the query." No means "The `candidates[i]` is
only topically similar or does not contain the needed evidence." A request
asks only as many as it has messages.
`msgvault jev consent sweep_evidence_rerank` prints the same disclosure.

## Feature: people sweep claim grounding

Feature name: `sweep_claim_grounding`. Setting:
`[jev.sweep_claim_grounding]`. Runs inside the
[people sweep](people-automation.md#run-and-inspect-a-sweep).

The people sweep's chat model proposes facts (claims) about a person and
reports its own confidence in each. A model's confidence in itself is a weak
signal. This feature asks Jev two questions per claim instead, after
extraction and before the claims are applied:

- **Stated**: do the excerpts the claim cites explicitly say what it asserts?
- **Current**: is it still true as of the newest cited excerpt?

Code sets the claim's reported score (0–1000) to `round(1000 × stated ×
current)`, replacing the model's number. The fact resolver's rules, its
thresholds, and your pins still decide whether anything changes; grounding
adds, drops, and reorders nothing. Eight claims go in each request.

A claim keeps the model's score when its target is sensitive, when its value
contains an email address or phone number (it is never sent), or when Jev is
unavailable. Any gate, budget, or provider failure keeps the model's score
for every claim not yet grounded. A manual `msgvault person sweep run` or
brief request may ask; the daemon's scheduled sweeps ask only with
`automatic = true`.

### What leaves the machine

Per claim, under `claims.claim_N`:

- `fact`: the fact's catalog description, such as "Job title"
- `relation`: `support`, `contradict`, or `supersede`
- `value`: the proposed value, such as a title or an employment record with
  its organization name; a value with an address or phone number is never
  sent
- `evidence[]`: **up to three of the excerpts the claim cites, newest first,
  each with its date and up to 1,000 characters of text** from a message the
  person sent on a source that authenticates its sender. Email addresses
  become `[email]` and phone numbers `[phone]`.

Your own identities, the person's name, and their addresses are never sent
as fields, and nothing from messages other people wrote is sent.

### The questions, exactly as sent

For each claim `N` (1 to 8) in the request:

- `stated_N` (Noul): "Do the excerpts in `claims.claim_N.evidence`, written
  by the person, explicitly say what `claims.claim_N` asserts: that they
  `relation` (support, contradict, or supersede) `value` for `fact`?" Yes
  means "An excerpt says it directly about the person who wrote it; no guess
  or inference is needed." No means "The assertion is implied, guessed, about
  someone else, a joke or hypothetical, or not in the excerpts."
- `current_N` (Noul): "Is what `claims.claim_N` asserts still true as of the
  newest excerpt date in `claims.claim_N.evidence`?" Yes means "Nothing in
  the excerpts says it ended, changed, or was only planned." No means "The
  excerpts say it ended, changed, was only planned, or describe a past
  state."

A request with fewer than eight claims sends only their questions.
`msgvault jev consent sweep_claim_grounding` prints the same disclosure.

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

- Only the enrichment identity check, organization resolution,
  correspondent kind, cleanup suggestions, hybrid search reranking, meeting
  event kind, meeting action assignee, Explore query understanding, people
  sweep evidence relevance, and people sweep claim grounding exist today.
  The other features in
  the engineering record `docs/internal/jev-judgments-plan.md` are proposals.
- Hybrid search reranking has not passed its evaluation gate. Its cached
  orders live in the daemon's memory, so a restart judges the next page of a
  search again.
- Meeting event kind asks each calendar series once. A series whose nature
  changes later keeps its first kind.
- Query understanding reads English date phrases and type words only, and
  its from:/to: chips match email, not chats. A search whose 800 ms budget
  runs out simply shows no chips.
- There is no way yet to set or correct an action item's assignee yourself;
  an inferred assignee you disagree with stays until the item changes.
- Correspondent kind does not revisit an identity once a rule or Jev
  classified it, even after links or new messages; mark it yourself with
  `msgvault person kind set`.
- Facts from different saves for the same organization and role do not add
  up: the newest save for a role replaces the older one, as it always has.
- Attempts decided before provider person IDs were kept can only be refused
  by profile URL; a provider that returns the same person under a new URL is
  not caught by that negative.
- Budgets count requests per UTC day. A process restart resets the in-memory
  breaker but not the daily counters.
