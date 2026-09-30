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
`organization_resolution`, `correspondent_kind`, `cleanup_suggestions`, or
`search_rerank` for those features. `msgvault jev revoke enrichment_identity`
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

1. **Only your own hybrid searches.** Hybrid searches from the Web UI, the
   API (`mode=hybrid`), `msgvault search --mode hybrid`, the MCP
   `search_message_bodies` tool, and Explore's hybrid search mode ask for it.
   Full-text (`--mode fts`) and vector searches never do, and no scheduled or
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
   the filters, the index generation, and the IDs of the leading results, so
   the next page of the same search reuses it without a second request. A
   provider failure is kept the same way, so later pages keep the fused order
   too. Gate states (disabled, no consent, no key) are not kept, so a change
   applies to the next search.

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
  correspondent kind, cleanup suggestions, and hybrid search reranking exist
  today. The other features in the engineering record
  `docs/internal/jev-judgments-plan.md` are proposals.
- Hybrid search reranking has not passed its evaluation gate. Its cached
  orders live in the daemon's memory, so a restart judges the next page of a
  search again.
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
