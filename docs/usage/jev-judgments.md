# Jev judgments

Jev is TypeSafe's System One model. msgvault asks it narrow, typed questions:
a yes/no question (a Noul), a pick from fixed options (a Choice), or a Score.
Jev returns only probabilities. msgvault's code keeps every workflow,
threshold, and side effect.

## When msgvault uses Jev

msgvault uses Jev only where a judgment is genuinely needed, such as whether
two differently written names are one person, what kind of correspondent an
address is, or whether an excerpt states a claim. Deterministic checks belong
in code: equal email addresses, equal phone numbers, exact domain matches, and
normalized identifiers. Similarity and text generation belong to embeddings
and local chat models through Ollama. Some features below still ask Jev about
things code could decide; their sections describe today's behavior and end
with a **Planned** note.

## What leaves the machine, and when

Every feature is off by default. A feature sends nothing until you enable
`[jev]`, enable the feature, provide an API key, and record consent for the
feature's exact policy. Scheduled and other unattended runs also need the
feature's `automatic = true`.

One developer command is the exception. [`msgvault eval --rerank-jev`](../cli-reference.md#optional-jev-reranking)
does not read `[jev]`, the key stored in Settings, recorded consent, or the
daily budgets. Naming a shape on its command line is the opt-in for that run.
It requires `--doc-key=message`, `--rerank-cost-stop-usd`,
`--rerank-input-usd-per-million`, and `--rerank-output-usd-per-million`, and
reads the key from `TYPESAFE_API_KEY`. For each topic it sends the query (at
most 4 KiB) and up to `--rerank-top` (at most 30) retrieved messages, each as
subject, sender (the display name, or the address when there is no name),
date, and cleaned body text capped at 2 KiB, under
`--rerank-max-requests` (default 1000) requests per run.

When Jev is off, over budget, slow, or failing, a feature falls back to its
rules or does nothing. No command, search, sync, or daemon job fails because
of it. The table below says what each feature does without Jev.

## Features at a glance

| Feature | What Jev decides | When it runs (all opt-in) | What leaves the machine | Without Jev |
|---|---|---|---|---|
| [`enrichment_identity`](#feature-enrichment-identity-check) | Whether a returned profile whose name or company did not match exactly is the requested person | Manual enrichment; scheduled with `automatic` | Requested name, company, and email domain; returned name parts, location, up to 5 current roles, up to 10 past companies, profile URL host. No messages or addresses | Exact name-and-company rule; partial matches are rejected |
| [`organization_resolution`](#feature-organization-resolution) | Which of up to 8 shortlisted organizations a name is, unless code settled it by an exact name on a unique registrable domain; whether two titles are one role | Manual enrichment and sweeps; scheduled with `automatic` | Organization names, up to 5 domains and 5 other names per candidate, up to 4 title pairs | An exact name on a unique registrable domain still resolves in code; otherwise exact lookup, a new organization is created, and titles stay separate |
| [`correspondent_kind`](#feature-correspondent-kind) | Person, shared mailbox, list, automated, or marketing | `kinds build`; cache builds with `automatic` (up to 200) | Display name, up to 5 addresses split into local part and domain, counts, header counts, up to 8 subjects of 160 characters. No bodies | Rules only; the rest stay unclassified and rank as people |
| [`cleanup_suggestions`](#feature-cleanup-suggestions) | Impersonation, pressure, and mail category | `suggest-cleanup` only; never automatic | Sender name and domains, up to 10 link hosts, SPF/DKIM/DMARC results, subject (200 characters), **first 500 characters of the body** | No suggestions |
| [`search_rerank`](#feature-hybrid-search-reranking) | Whether each leading result answers the query | Hybrid searches that request it; never automatic. Not recommended yet | Query (4 KiB); per result, up to 2 KiB of subject, sender, date, and **cleaned body**; up to 30 results | Fused hybrid order |
| [`meeting_event_kind`](#feature-meeting-event-kind) | What kind of event a calendar series is, unless the invite lists of its events settle it | `meetings judge`; cache builds with `automatic` (up to 200) | Redacted title (160 characters), duration, recurrence, attendee counts. No names | Attendee-count weight |
| [`meeting_action_assignee`](#feature-meeting-action-assignee) | Which attendee owns an action item | `meetings judge`; cache builds with `automatic` (up to 200 meetings) | Meeting title, attendee labels without addresses, item title (200) and description (500), redacted | No inferred assignee |
| [`query_understanding`](#feature-explore-query-understanding) | Which filters a typed search asks for | Web UI searches you type; never automatic | Redacted query, candidate date windows, people-index names, account labels | No chips; the search is unchanged |
| [`sweep_claim_grounding`](#feature-people-sweep-claim-grounding) | Whether cited excerpts state a claim and it is still current | Manual sweeps and briefs; scheduled with `automatic` | Fact, relation, value (500 characters); **up to 3 cited excerpts of 1,000 characters**, redacted | The chat model's own confidence |
| [`person_duplicates`](#feature-duplicate-people) | Whether the names on two identity clusters are one human. Shared mailboxes, phone numbers, and provider accounts are decided in code | `person judge`; cache builds with `automatic` (up to 200 name pairs) | Up to 3 names per side, whether each side's addresses are personal or at an organization, and whether both share an organization domain. No addresses | Pairs that share a mailbox, phone number, or provider account still become candidates; name pairs are not proposed |
| [`person_profile_choices`](#feature-person-profile-choices) | Primary role, display name, and whether two merged free-text or URL values are the same fact, when normalization leaves a real choice | `person judge`; cache builds with `automatic` (up to 200 of each) | Roles (organization, title, start month), names (160 characters), conflict values (300 characters) | The current primary role and the promotion name stay; merge conflicts equal after normalization close in code, the rest wait for you |

Each feature's section below is the full contract: its questions as sent,
thresholds, and exact fields. This is `main` functionality; check the
[changelog](../changelog.md) for the release it ships in.

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

   [jev.sweep_claim_grounding]   # sends message excerpts the person wrote
   enabled = true
   # automatic = true   # also ground claims during scheduled people sweeps

   [jev.person_duplicates]   # sends display names, never addresses
   enabled = true
   # automatic = true   # also judge new pairs at each cache build

   [jev.person_profile_choices]
   enabled = true
   # automatic = true   # also make profile choices at each cache build
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
`query_understanding`, `sweep_claim_grounding`,
`person_duplicates`, or `person_profile_choices` for those features.
`msgvault jev revoke enrichment_identity`
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
- **Token budget.** TypeSafe accepts at most 32,000 tokens of state plus the
  longest question, and 64,000 tokens per request; it answers a larger request
  with HTTP 400 `max_tokens_exceeded`. msgvault cannot run the provider's
  tokenizer, so it estimates tokens pessimistically: one per four letters,
  one per digit or symbol, and more for non-ASCII text, never under one per
  four bytes. Before sending, every request must estimate at most 30,000
  tokens of state plus longest question and 60,000 tokens overall.
- **Packing.** Features that put several items in one request (search
  reranking's batched shape, people sweep claim grounding, duplicate people, meeting action assignee, correspondent kind,
  meeting event kind, and cleanup suggestions) send fewer items per request
  when the items are dense, instead of failing. If TypeSafe still answers
  `max_tokens_exceeded`, the request is split once more and resent; a
  single search candidate that cannot fit is cut to fit. A
  `max_tokens_exceeded` answer does not count toward the circuit breaker.
- **Provider errors.** On a failed request, msgvault reads at most 4 KiB of
  the response and keeps only its `error_type` token (for example
  `provider returned HTTP 400 (max_tokens_exceeded)`). No other response
  text is logged or reported.
- **Logging.** Logs carry question IDs, answer types, probabilities,
  confidence, latency, token counts, and budget state. They never carry the
  state that was sent.
- **Failure never fails the user-facing operation.** When a judgment cannot
  run, the caller uses its pre-Jev decision and reports the category, such as
  `consent_required`, `breaker_open`, `request_limit`, `state_too_large`, or
  `timeout`.

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
   (`eu.example.com` and `example.com`).
3. **Same name on the same registrable domain, no Jev.** Code settles the
   fact only when all of these hold; everything else goes to Jev:
   - The fact's domain has a registrable domain from the public suffix list,
     so `mail.example.com` counts as `example.com` and `mail.example.co.uk` as
     `example.co.uk`, but `example.co.uk` never matches `other.co.uk`.
   - That registrable domain is not a consumer mail domain (`gmail.com`,
     `yahoo.co.uk`, `gmx.de`, ISP mail, and similar) or a platform that hosts
     many organizations (`linkedin.com`, `google.com`, `github.com`,
     `github.io`, `gitlab.com`, `medium.com`, `substack.com`, `wikipedia.org`,
     `bit.ly`, and similar). A profile URL such as
     `https://www.linkedin.com/company/example` never settles anything, and
     the companies that own those domains, such as Google, LinkedIn, or
     GitHub, are never settled in code either; Jev decides them.
   - Exactly one active company organization (not retired, not merged into
     another) has any domain on it, counting every such organization and
     every active domain, including ones the shortlist leaves out and ones you
     rejected in a review.
   - The fact's name equals that organization's name or one of its alternate
     names once case, punctuation, and these words are removed: `inc`,
     `incorporated`, `llc`, `llp`, `lp`, `ltd`, `limited`, `corp`,
     `corporation`, `co`, `company`, `plc`, `gmbh`, `ag`, `sa`, `sas`, `bv`,
     `nv`, `pty`, `srl`, `oy`, `ab`, `the`, `and`, `of`. A shared domain under
     a different name is a judgment and goes to Jev.
   - You have not rejected that name, compared the same way, for that
     organization.

   This catches what the exact lookup misses: "Example Labs, Inc." at
   `example.com`, or "Example Labs" at `eu.example.com`, when "Example Labs"
   is known at `example.com`. It runs even when Jev is off or not consented.
4. **One request.** Otherwise, when the shortlist is empty, the organization
   is created as before and nothing is sent. When it is not, Jev picks which shortlisted organization, if
   any, the name is, with the whole shortlist exactly as before. Jev also
   answers one yes/no question per job-title pair (at most four) that the
   person already has at the organizations involved. After code settled the
   organization, the request carries only the title pairs there, and no
   request is made when there are none.

| Result | Condition | Outcome |
|---|---|---|
| Domain alias | step 3 settled it | The name, and its domain when the organization lacks it, become lookup keys of that organization. Source `system`, source_ref `rule:organization_resolution:registrable_domain`, no confidence. |
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
missed and a shared domain did not settle it, and one `title_same_role_N` per
title pair. A shared-domain decision sends no `reference` or `candidates`.

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
such texts as `candidates[]` in one request, or splits them across several
requests when they would exceed the [token budget](#budgets-and-safety); the
per-candidate shape sends one as `candidate` per request. No attachments, recipients, labels, or
identifiers are sent. `msgvault jev consent search_rerank` prints this
disclosure, including a line that says message body text is sent.

### The questions, exactly as sent

- Batched: `candidate_0` to `candidate_29` (Noul): "Could `candidates[i]` be
  the best answer to `query`?" Yes means "The `candidates[i]` contains the
  specific information needed to answer the query." No means "The
  `candidates[i]` is only topically similar or does not contain the needed
  evidence." A request asks only as many as it has results; when the results
  are split across requests, each request numbers its own candidates from 0.
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
2. **Invite-list rules, no Jev.** A series is decided in code and never
   sent when every one of its events that is not cancelled satisfies the
   same rule:

   | Every event | Kind |
   |---|---|
   | You organized it, and no one but you is invited (no attendee list, or only your own addresses) | `personal_hold_or_logistics` |
   | A timed event you organized, with you and exactly one other address invited | `one_on_one` |

   One edited instance is enough to send the series to Jev instead. An
   invite someone else sent stays with Jev, because a webinar or marketing
   invite can list only you and its host. So does an all-day event for two,
   which is as often a trip or a hold as a meeting. An other address the
   archive classifies as a mailing list, shared mailbox, automated sender,
   or organization is not one person, so that series goes to Jev; a list
   address that is not classified counts as one person.
3. **Jev, only with consent.** Every other series is asked one Choice, ten
   series per request. A recurring series is one question, asked once; a
   standalone event is its own series. The newest event that is a meeting
   describes the series.

The rules run whenever `msgvault meetings judge` runs, whether or not Jev is
enabled, and before an analytics cache build only when a meeting feature is
enabled with `automatic = true`. A rule's decision only keeps the series from
being sent. Calendar sync clears it whenever it writes or cancels one of the
series' events. Each run also starts by checking every stored rule decision
against current data (the series' events, your addresses, and correspondent
kinds) and reopens any that no longer hold, so their series are decided again
in that run. A rule decision written while a sync was changing the series is
corrected the same way on the next run. A series a rule settled as a personal
hold keeps the attendee-count weight: rule kinds never change weights.

Code maps Jev's answer when its probability is at least 0.60:

| Answer | Meeting weight |
|---|---|
| `one_on_one` | 1 |
| `small_working_meeting` | 1 |
| `social` | 0.5, or 1 when only you and one other person are invited |
| `large_group_or_all_hands` | 0.25 |
| `external_webinar_or_marketing` | 0 |
| `personal_hold_or_logistics` | 0 |

Below 0.60, and for a series a rule decided or Jev has not judged, the
attendee-count weight stays. A rule's kind needs no weight of its own: a
two-person event already weighs 1, and a hold has no one else to count. A
series weighed 0 by Jev also stops counting as contact in last-contact
dates; its events are re-projected when the judgment is stored. A Jev
judgment is stored per series (`calendar_event_kinds`, with its
probabilities and the model) and never revisited, and the next analytics
cache build publishes the new weights. `meetings judge` runs by hand. With
`automatic = true`, each analytics cache build also judges up to 200 new
series first.

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

## Retired: people sweep evidence relevance

The `sweep_evidence_rerank` feature asked Jev which retrieved older messages
bear on each fact before the people sweep's chat model read them. Choosing
relevant messages is a similarity task, so the sweep now does it with local
embeddings and no longer sends excerpts to Jev for it. See
[evidence relevance](people-automation.md#evidence-relevance) for how it
works and what leaves the machine.

- An existing `[jev.sweep_evidence_rerank]` section still loads but has no
  effect. You can delete it.
- A consent recorded for `sweep_evidence_rerank` no longer authorizes any
  request. `msgvault jev revoke sweep_evidence_rerank` clears it.

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

## Feature: duplicate people

Feature name: `person_duplicates`. Setting: `[jev.person_duplicates]`.
Command: [`msgvault person judge`](../cli-reference.md#person-judge).

The same person often writes from a personal and a work address, and each
address becomes its own identity. This feature finds likely pairs and puts
them in front of you; it never links or merges anything by itself. Code
decides every pair that shares an exact identifier. Jev judges only whether
two sets of names belong to one person.

1. **Pairs, in code.** Two identity clusters with an email address are
   proposed when they share any of these:
   - an address that delivers to the same mailbox, under the
     [mailbox rule](people.md#addresses-that-share-a-mailbox) (case, plus
     tags, Gmail dots, and googlemail.com). Participant addresses and email
     identifiers both count; relay and robot mailboxes such as
     `reply+<token>@` never do;
   - a complete phone number, from a participant's phone or any
     phone-shaped identifier, compared in E.164 form. Only a number written
     with its country code (`+` or `00`, at least eight digits) or a
     ten-digit North American number with valid area and exchange codes
     counts; a local number such as `555-0100` never does;
   - a provider user ID observed on the same service and scope;
   - a display name (at least two words and five letters, compared ignoring
     case, punctuation, and word order) on different addresses;
   - a distinctive local part (the part before `@`, at least five
     characters, not a role, list, or no-reply address) at different domains.

   A value shared by more than five clusters is too common to mean one
   person and proposes nothing, and so does a name with a team or service
   word such as "Support" or "via". For a mailbox, phone number, or provider
   account the count includes every cluster that has it, including your own
   identities, non-people, and identities without an email address.
2. **Left out.** Your own identities; clusters classified as anything but a
   person (by you, a rule, or Jev); clusters that look like a shared mailbox;
   pairs already bound to one person; pairs with any existing identity match
   candidate, including one you rejected; and a pair where one side was
   rejected for, or detached from, the other side's person. Unlinking two
   identities, including ones you linked by hand, and splitting a merged
   person each record a rejected match between the two halves, so they are
   never proposed again. Your earlier rejections follow each half of a
   later split: after unlinking A from B-C and then B from C, A stays apart
   from C as well. Rejections the system restores on its own, such as those
   a "not a person" mark made, do not count as your decision. These rules
   apply to every pair, so an exact match never overrides your earlier "not
   the same person".
3. **Exact matches, in code.** A pair that shares a mailbox, phone number, or
   provider account becomes a reviewable candidate without asking Jev, even
   when Jev is off or cannot answer, and however many name pairs are waiting:
   basis `email`, `phone`, or `stable_provider_id`, the
   shared value as its normalized value, confidence 1, and evidence that
   reads `decided in code: same_mailbox` (or `same_phone`,
   `same_provider_id`).
4. **Names, Jev only with consent.** The remaining pairs share only a name or
   a local part. A pair is sent only when both sides have a display name to
   judge. A pair with no name on either side is remembered and proposed
   again when a side changes (the remembered inputs include each side's
   names). A pair with a name on one side only is not remembered, so it is
   taken up again as soon as the other side has a name. Twenty pairs per request, one Noul each. `--limit` and the
   cache-build cap count only these pairs.
5. **Code decides what you see.** A name pair judged 0.30 or more likely to
   be one person becomes a reviewable identity match candidate: basis
   `display_name`, the probability as confidence. Below 0.30 the judgment is
   only remembered. Either way the pair is not asked again until one side's
   identities, names, or addresses change.

Every candidate has source `system` and is listed under **Reviews → Possible
duplicate people** (API
`GET /identity/match-candidates?origin=person_duplicate`). Accepting a
candidate links the two identities through the normal identity link path.
When both already belong to different saved people, accepting offers the
usual merge, where you choose the survivor. Rejecting keeps the decision, so
the pair is never proposed again. Only you can accept a duplicate-people
candidate; msgvault refuses any automatic acceptance, whatever its basis.
`person judge` runs by hand; with `automatic = true`, each analytics cache
build first writes every exact match and sends up to 200 new name pairs.

### What leaves the machine

Per name pair, under `pairs.pair_N`:

- `first.names[]` and `second.names[]`: up to three display names each side
  uses, cut to 120 characters. A name containing an email address or phone
  number is not sent, and neither is an address token: a name with no
  spaces that equals one of that side's local parts ignoring case and
  separators, such as `john.smith` or `JSmith` for `john.smith@` or
  `jsmith@`. A spaced name such as "John Smith" is a name and is sent.
- `first.address_kinds[]` and `second.address_kinds[]`: `personal` (a
  consumer mail provider such as gmail.com) and/or `organization` (any other
  domain), for up to five addresses each side uses.
- `same_organization_domain`: true when both sides have an address at the
  same organization domain (compared by registrable domain, so
  `mail.example.com` and `example.com` match).
- `signals[]`: `same_display_name`, `same_local_part`, or both.

No email address, local part, domain, phone number, provider ID, message,
subject, or profile data is sent. Pairs decided in code are never sent. Your
own identities are never proposed, so they are never sent.

The address kinds and the shared-domain flag are the context the name
question needs: the same full name at a personal and a work address, or at
two addresses of one organization, is usually one person. The domain itself
would name the person's employer, so it stays on the machine.

### The question, exactly as sent

`same_person_1` through `same_person_20` (Noul): "Do the names in
`pairs.pair_N.first` and `pairs.pair_N.second` belong to the same human
being? `pairs.pair_N.signals` says what the two sides share, `address_kinds`
says whether each side writes from a personal mail provider or an
organization's domain, and `pairs.pair_N.same_organization_domain` says
whether both sides use one organization's domain." Yes means "The names
belong to one person, for example the same full name at a personal and a work
address, or a name and its initials or nickname on two addresses with the
same distinctive address name." No means "Different people who share a
common name, a person and a team, company, or service, or not enough to
tell." A request with fewer than twenty pairs sends only their questions.
`msgvault jev consent person_duplicates` prints the same disclosure.

## Feature: person profile choices

Feature name: `person_profile_choices`. Setting:
`[jev.person_profile_choices]`. Command:
[`msgvault person judge`](../cli-reference.md#person-judge).

Three small profile questions have a plain rule that is often wrong. Code
settles what normalization can decide, then asks Jev only the choices that
remain. Code writes an answer only above a fixed threshold and never over
anything you set:

| Question | Asked when | Written when | Otherwise |
|---|---|---|---|
| Primary current role | A person has two to six current employments, every one found automatically (none you entered or imported from contacts), and you have not pinned employment | A role scores 0.80 or more: it becomes the primary employment | The primary role stays as the rule left it: the first current role saved while none was primary. If that role ends, no role is primary until another current role is saved or you choose one |
| Display name | A person was promoted from identities that use two to six different names, and the name has not changed since promotion | A name scores 0.80 or more: the person is renamed to it | The promotion name stays: the name of the lowest-numbered (first recorded) identity |
| Merge conflict | A person merge left two values of a non-sensitive, single-value field in conflict, the absorbed value was not set by you, and both are free text or URLs | Both are 0.95 or more likely the same fact: the survivor's value is kept and the conflict closes, reviewed by `jev` | The survivor's value stays current and the conflict stays pending for you |

Setting the primary role yourself pins employment, and renaming a person
ends the display-name question; neither is ever replaced. Each person is
asked again only when their roles or names change, and each conflict once.
`person judge` runs by hand; with `automatic = true`, each analytics cache
build makes up to 200 of each first.

### Settled in code first

These steps run on every `person judge`, with or without Jev, and send
nothing. They cover every eligible person and conflict on each run;
`--limit` and the cache build's cap of 200 apply only to what is then sent
to Jev, and a failed Jev request does not stop them.

- **Primary role.** When every current role has the same organization name
  and title after text folding (below), there is nothing to choose: the
  current primary role stays and the person is not asked. Roles that fold to
  the same organization and title are offered once, as the current primary
  role if it is one of them.
- **Display name.** When every name folds to the same text, the person is
  not asked. The promotion name stays unless it is not in proper case and
  exactly one other name differs from it only in case and is: then the
  person is renamed to that spelling, so `John Smith` replaces
  `JOHN SMITH`. Proper case means the name has a word of three or more
  letters, and every such word starts with a capital and has a lower-case
  letter; `jOHN sMITH` and `Mj` are not. With two differing proper-case
  spellings, the promotion name stays. Names that fold to the same text are
  offered once, in the promotion name's spelling (or its proper-case
  variant) if it is one of them, and the cap of six counts these offered
  names, not every spelling.

  Case variants only become a display-name question for people promoted
  from this release on; earlier promotions whose names differed only in
  case recorded no promotion name to revisit. People already judged whose
  names include case variants are listed once more, because their names now
  read differently; like every display-name question, at most `--limit` of
  them (200 per cache build) are sent to Jev per run.
- **Merge conflict.** Two values that are equal under the field's rule close
  the conflict, reviewed by `rule:normalized`. The kept value is yours when
  only the absorbed value was set by you (entered, or imported from
  contacts); otherwise the survivor's value is kept, so a value you set on
  the survivor is never replaced. Conflicts an earlier version recorded as
  never sent are compared too, and still never sent.

Each field type has its own rule:

| Field | Equal when | Kept apart |
|---|---|---|
| Text and text area | Text folding matches | Accents, digits, symbols, superscripts, and punctuation next to a number |
| Email | The trimmed, lower-cased addresses match | Any other difference; an address is never sent to Jev |
| Phone | Both parse to the same E.164 number | Different numbers; a phone number is never sent to Jev |
| URL | Both reduce to the same canonical `http`/`https` URL: scheme and host case, default port, `.` and `..` path segments, trailing slash, fragment, and tracking parameters such as `utm_*` are ignored | Path and other query differences, including letter case |
| Select | The option values match exactly | Different options; never sent to Jev |
| Number, yes/no, date, timestamp | The stored values match exactly | Different values; never sent to Jev |

Text folding does four things, and nothing else:

| Step | Effect | Stays different |
|---|---|---|
| Composition and width | Composed and decomposed accents match; full-width and half-width characters read as ordinary ones (`Ｌｉｓｂｏｎ` is `Lisbon`) | Superscripts, subscripts, and ligatures: `2⁵` and `25` |
| Simple case folding | Upper and lower case match, one character for one | `Weiß` and `Weiss` |
| Separating punctuation as a space | Period, comma, semicolon, colon, question and exclamation marks, quotes and apostrophes, brackets, ellipsis, middle dot, and dashes: `Smith-Jones` is `Smith Jones`, `St.` is `St` | The same marks next to a number: `1.000` and `1,000`, `1.5` and `1,5`, `3.14` and `3:14`, `(5)` and `5`, `-5` and `5` |
| Spaces | Runs of spaces collapse | |

Letters, digits, accents, symbols, and other punctuation such as `%`, `#`,
`&`, `@`, `/`, and `*` are kept: `50%` and `50`, and `José` and `Jose`, stay
different. A phone or URL value that does not parse as one is compared with
text folding.

Without Jev, roles, names, and conflicts that normalization does not settle
keep the rule's choice or stay pending, and are offered to Jev on a later run
once it is on. A conflict that can never be sent (a number, yes/no, date,
timestamp, or select difference, an absorbed value you set, or a value the
next section excludes) is recorded so it is not listed again for Jev and
stays pending for you. So is a person with fewer than two names that can be
sent, until their names change.

### What leaves the machine

Each request carries only one question's state:

- Primary role, under `roles.role_N`: each current role's `organization`
  name, job `title`, and `start` year and month.
- Display name, under `names.name_N`: each distinct display name the
  person's identities use, cut to 160 characters. A name containing an email
  address or phone number is never sent.
- Merge conflicts, under `conflicts.conflict_N` (up to eight per request):
  the field's label as `field`, and the two values as `first` (the
  survivor's) and `second`, each sent whole. A conflict with a value over 300
  characters, or with an email address or phone number, is never sent and
  stays pending for you: a cut value could hide the difference.

Options and values settled in code are never sent. No addresses, messages,
or other profile fields, and nothing about you.

### The questions, exactly as sent

- `primary_role` (Choice): "Which of `roles` is this person's primary current
  role? An option whose key is absent from `roles` never applies." Options
  `role_1` to `role_6` ("`roles.role_N` is the person's main current job.")
  and `unclear` ("No single current role stands out as the main one.").
- `display_name` (Choice): "Which of `names` should a contact list show for
  this person? An option whose key is absent from `names` never applies."
  Options `name_1` to `name_6` ("`names.name_N` is the person's own full
  name, written the way they use it.") and `unclear` ("None of the names is
  clearly the person's own name.").
- `same_value_1` to `same_value_8` (Noul): "Do
  `conflicts.conflict_N.first` and `conflicts.conflict_N.second` state the
  same fact for `conflicts.conflict_N.field`?" Yes means "The same fact
  written differently: formatting, abbreviation, spelling variant, or more
  or less detail that does not disagree." No means "Different facts, or one
  contradicts the other."

`msgvault jev consent person_profile_choices` prints the same disclosure.

## Turn it off

- `msgvault jev revoke --all` stops every feature at the next request without
  touching configuration or restarting anything.
- Delete the stored key in Settings, or unset the environment variable and
  restart, to make the credential check fail closed at the next request.
- `enabled = false` under `[jev]` or under a feature section turns the gate
  off once the daemon restarts; each feature then behaves as the
  [Without Jev](#features-at-a-glance) column says.

Stored judgments and counters stay in the archive for audit; they hold
probabilities and outcomes, not the compared values.

## Limitations

- Only the enrichment identity check, organization resolution,
  correspondent kind, cleanup suggestions, hybrid search reranking, meeting
  event kind, meeting action assignee, Explore query understanding, people
  sweep claim grounding, duplicate people, and person profile choices exist
  today. The other features in
  the engineering record `docs/internal/jev-judgments-plan.md` are proposals.
- Hybrid search reranking has not passed its evaluation gate. Its cached
  orders live in the daemon's memory, so a restart judges the next page of a
  search again.
- Meeting event kind asks Jev about each calendar series once. A series
  whose nature changes later keeps its first Jev kind; only a rule's
  decision is made again after the series changes.
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
- Duplicate people compares only identities with an email address; chat
  identities without one, and person embedding neighbors, are not proposed
  yet.
- Budgets count requests per UTC day. A process restart resets the in-memory
  breaker but not the daily counters.
