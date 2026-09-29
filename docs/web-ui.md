---
last_edited: "2026-09-29"
title: Web UI
description: Browse messages and files, maintain people, and monitor archive work from your browser.
---

# Web UI

The Web UI lets you search across your archive, read messages, browse files
and meetings, maintain the people you know, and see whether sync and indexing
work has finished. It is embedded in the release binary and served by
`msgvault serve`; you do not need a separate web application process.

This page describes current `main`. The navigation below (People, Inbox,
Files, Meetings, and Activity, with readable addresses) is newer than the
latest release, which uses Everything, Relationships, and Directory
workspaces instead. Check the [changelog](changelog.md) for the release it
ships in.

| Your question | Where to go |
|---|---|
| Where is that message, conversation, or meeting? | [Inbox](#inbox-and-search), or [Search](#inbox-and-search) when you type a query |
| Who have I been in contact with? | [People](#people) |
| What do I know about this person? | A person's page in [People](#people) |
| Where is an attachment, image, or video? | [Files](#files-and-containing-context) |
| Which meetings happened, and what came out of them? | [Meetings](#meetings) |
| Which identity matches or profile facts need my decision? | [Reviews](#reviews), in the gear menu |
| Can I return to this search later? | [Saved Views](#saved-views) |
| Did sync, enrichment, or indexing finish? | [Activity](#activity): Sources and Operations |
| What is staged for deletion? | [Activity](#activity): Deletions |
| How do I change the daemon's configuration, theme, or density? | [Settings](#settings-and-restart-behavior) |

## Start and discover the URL

```bash
msgvault build-cache
msgvault serve
```

`build-cache` prepares the analytical tables before you open the browser. You
can also run `serve` directly and let the default startup maintenance build a
missing or stale cache in the background.

Foreground startup prints `API server: http://HOST:PORT`. The default
`server.api_port = 0` chooses a free port. Local CLI commands discover that
address through the private daemon runtime record under the configured msgvault
home; browsers should use the printed URL. Configure a fixed port for a stable
remote bookmark:

```toml
[server]
bind_addr = "127.0.0.1"
api_port = 8080
```

The default loopback deployment is trusted. If an API key is active and the
request is not loopback-trusted, `/` still loads the public shell and the UI asks
for the key. A successful login creates an expiring, in-memory browser session.
Daemon restarts, logout, expiry, and API-key activation invalidate sessions.
Existing bearer-key API clients are unchanged.

## Remote access and HTTPS

For remote access, set a strong API key and bind deliberately:

```toml
[server]
bind_addr = "0.0.0.0"
api_port = 8080
api_key = "replace-with-a-long-random-key"
```

HTTPS at a reverse proxy is recommended. Forwarded scheme and host headers are
accepted only from explicitly trusted proxy addresses or CIDRs:

```toml
[server]
trusted_proxies = ["127.0.0.1", "192.0.2.8/32"]
```

Do not add a whole client network merely to silence proxy warnings. When HTTPS
is known through a trusted proxy, the browser cookie uses `Secure`. Plain HTTP
on an encrypted private network is supported as an explicit tradeoff, but the UI
warns that its session cookie travels without TLS. `HttpOnly` and
`SameSite=Strict` do not encrypt that traffic.

## Find your way around

Primary navigation has five places: **People**, **Inbox**, **Files**,
**Meetings**, and **Activity**.

- The header search field submits to Search. It appears on every surface
  except Inbox and Search, which keep their own full search bar with modes
  and chips.
- The bookmark button beside the header search field opens your
  [Saved Views](#saved-views).
- The gear menu holds **Settings**, **Reviews**, and **Saved Views**. When
  you open the menu, Reviews shows how many decisions are waiting.
- The archive status dot stays in the header. Theme and density live in
  [Settings > Appearance](#appearance).

The command palette (`P`) runs the same moves by name:

- **Go to People**, **Go to Inbox**, **Go to Files**, **Go to Meetings**,
  **Go to Activity**, **Go to Settings**, and **Go to Saved Views**.
- **Go to person…** opens People with its search box ready for a name.
- **Theme: Light**, **Theme: Dark**, **Theme: System**, and **Theme: Daemon
  default** set this browser's theme. **Density: …** commands set its density.

See [Keyboard controls](#keyboard-controls) for search and other shortcuts.

### Addresses and bookmarks

Every place has a readable address you can bookmark or share. The person
opening it still needs access to the archive.

| Address | Opens |
|---|---|
| `/people` | The People list |
| `/people/<id>` | A saved person's Overview |
| `/people/<id>/timeline`, `/files`, `/meetings`, `/profile`, `/maintenance` | A saved person's tab |
| `/people/contact-<id>` and `/people/contact-<id>/<tab>` | An archive contact who is not saved yet |
| `/people/domains?domain=example.com` | The domains view for one domain |
| `/inbox` | The Inbox, on the last 7 days (`/inbox?since=7d`) |
| `/inbox?since=30d`, `/inbox?since=all` | The Inbox over 30 days or all time |
| `/inbox?after=…&before=…` | The Inbox between two instants |
| `/search?q=…&mode=full_text` | Search results; `mode` is `full_text`, `semantic`, or `hybrid` |
| `/files` | Files |
| `/meetings` | Meetings; filter with `?person=<participant id>`, `&account=<source id>`, and `&since=30d`, `90d`, or `all` |
| `/meetings/<id>` | One meeting |
| `/messages/<id>` | One message |
| `/activity/sources`, `/activity/operations`, `/activity/deletions` | An Activity tab |
| `/settings/<section>`, such as `/settings/search` | A Settings section |
| `/reviews`, `/saved-views` | Reviews or Saved Views |

The `explore` query parameter remains only for advanced state such as
grouping chains, columns, and per-surface filters. It appears only when that
state differs from the defaults. Keyboard focus and scroll position stay out
of the address; browser history keeps them so Back and Forward restore them.
The page title names the surface, such as `Inbox · msgvault`, or the subject
of the open message or meeting.

Older `/?workspace=…&explore=…` links still open the same view, and the
address is rewritten to the new form. An old ranked relationships link
(`?workspace=relationships`) opens People filtered to **Not saved**.

## Inbox and search

<figure class="screenshot" data-lightbox>
  <img src="/docs/assets/static/analytical-light-compact-darwin.png" alt="Table of archived email in light theme with compact rows" loading="lazy">
  <figcaption>Browse archived email. Select the image to view it at full size.</figcaption>
</figure>

The screenshots use a curated public Enron research-data fixture. Authentic
names and message text are intentional; the repository's `docs-fixtures`
branch records provenance, attribution, and the content review. This fixture
contains email only; it does not illustrate chat, calendar, or attachment content.

The Inbox opens on the last seven days, newest first. It is a compact,
sortable table of logical entries: one row per email, calendar event, meeting
note, other durable item, or chat conversation. Raw chat fragments appear
only after drilling into a conversation.

Each day's calendar events fold into one **N events** line that previews
their titles. Select **Show** to expand them inline and **Hide** to fold them
again, so mail and texts interleave around them.

Type a query and the Inbox becomes Search; headings and landmarks say which
one you are in. Search results keep their per-thread grouping. Filter,
Group by, Show as, and Search form a shareable view; see
[Addresses and bookmarks](#addresses-and-bookmarks).

Search mode is always explicit:

- **Full text** matches words in the text index.
- **Semantic** ranks only content covered by the current embedding generation.
- **Hybrid** combines keyword matches with semantic ranking where it is
  available.

The context strip reports semantic coverage. Disabled, building, stale,
incomplete, unavailable, and ready are different states; msgvault never silently
changes the requested mode. Semantic-only results cannot include unembedded
content. Hybrid retains full-text coverage and labels the semantic contribution.

When a search fails, the UI explains what happened and keeps your query.
**Timed out** means the selected search backend did not finish within the request budget; it is an
error, not an empty result, and the query and filters remain available to retry.
**Incompatible mode** means the daemon, browser contract, or current index
cannot safely honor the selected search mode. Update or rebuild the named
component, or deliberately select a supported mode. Msgvault does not quietly
substitute full-text search for either state.

## Read messages

Open a `/messages/<id>` link to go directly to an archived message. CLI
`search --json` and `show-message --json` responses include a `web_url` when
the selected daemon has an HTTP address. The link uses that daemon's address;
the person opening it still needs access to the archive. Links to chat messages
open a bounded part of the conversation around the selected message, with
controls to load earlier or later messages.

A message page opens inside the app, with the header and navigation. Its
**Back** button returns to where you were when you opened it from inside the
app, and to the Inbox otherwise.

Click an entry in the Inbox or Search to open its preview below the results. On wide
windows, choose **Preview position → Right** to read beside the results.
Drag the divider to resize either layout, or focus it and use the arrow keys.
Double-click the divider to reset its size. The browser remembers your layout
choice and each layout's size. Narrow windows use the preview below the results
and restore your right-side layout when there is room again.

HTML email follows the app's dark theme by replacing sender-defined text,
background, and border colors. Images keep their original colors. Choose
**Use original colors** above a message to see its authored colors on a white
background, or **Use app colors** to return to dark reading. This override
applies to the open message. Light mode preserves designed email colors.

## Meetings

Meetings lists calendar events and meeting transcripts that already happened,
newest first. It shows the last 30 days by default. Narrow it by person,
account, and window (30 days, 90 days, or all time); the choices appear in
the [address](#addresses-and-bookmarks).

Open a meeting to see its page:

- A calendar event shows its event card.
- A transcript shows the transcript, plus action items with their assignee
  and status.
- Attendees and speakers are person pills that open the person.

When a person's last contact was a meeting, their page links to that meeting.

## Meeting context and follow-ups

Filter the Inbox to meetings to see **Meeting activity and follow-ups**.
Select meeting rows to export their context as JSON or Markdown, with transcripts
included only when requested. Participant and domain reading panes and a
person's **Meetings** tab show meeting metrics and recorded actions for their
current scope.
**Open archived meeting** opens the source evidence; Back restores the originating
view. See the [meeting guide](usage/meetings.md#export-context-and-read-follow-ups)
for selection limits, source coverage, unknown duration, and action filters.

## Cache states

The web tables share one analytical cache across message types. When it is missing,
building, stale, or unavailable, the UI names that state and offers the
available recovery steps. Run `msgvault build-cache` for an explicit rebuild, or leave `analytics.auto_build_cache =
true` for daemon startup to build a stale cache. With `analytics.engine =
"duckdb"`, startup fails if no usable cache can be produced.

## Files and containing context

Files is a searchable table of attachment date, filename, type, size, person or
domain, source, containing item, and content availability. Archived images and
PDFs open in application-controlled viewers. Metadata-only, missing,
unsupported, and previewable content remain distinct. From a file, navigate to
its containing item and then its email or chat conversation.

Filter by filename and file type. On a person's **Files** tab, choose a
media gallery or file table and narrow the relationship to **From them**,
**To them**, or **Group conversations**. These directions describe the
containing messages; they do not identify people pictured in an image.

Turn on **Hosted visual search** to describe image or video content, or supply
a JPEG, PNG, or WebP query image. The UI discloses that the query goes to the
configured provider. It requires the separate
[visual index](/docs/usage/vector-search/#visual-attachment-search); ordinary
filename browsing does not use that provider.

### Images in email

The reader displays archived inline images and leaves remote images unloaded
until you choose **Load images**. That choice applies to the current item and
fetches images through the daemon. Moving to another item resets it.

For unattended, offline preservation of remote images, see
[Archive Remote Email Images](/docs/usage/remote-images/). Reader permission
to load an image and permission to archive remote images during ingest are
separate choices.

## People

<figure class="screenshot" data-lightbox>
  <img src="/docs/assets/static/relationships-dark-comfortable-darwin.png" alt="A selected person's activity calendar and email timeline in dark theme" loading="lazy">
  <figcaption>Select a person to explore their activity and messages.</figcaption>
</figure>

People is one list of everyone you have been in contact with, most recent
contact first. It combines two kinds of rows:

- **Saved people** are profiles you keep in the Directory across sources.
- **Archive contacts** are identity clusters observed in your archive that
  you have not saved yet. Their rows are marked **Not saved**.

Each row shows one identifier: the best email address, else a phone number,
else a handle. Narrow the list with the search box and the **Saved**, **Not
saved**, **Has name**, **Category**, and **Organization** filters. **Domains**
opens the [domains view](#domains).

Every human has one page. Opening someone from the list, a message's name
pill (**Open person**), or a search result lands on that page. An archive
contact who has already been saved opens the saved person instead.

Archive contacts combine identifiers backed by explicit archive identity
evidence; msgvault does not merge records merely because their display names
match. Source identities that mean "me," saving a profile, display-name
overrides, and typed profile attributes are separate curated operations; see
[People, Profiles, and Source Identities](/docs/usage/people/).

### A saved person's page

| Tab | What it shows |
|---|---|
| Overview | Name, contact methods, last contact, and an attribute summary |
| Timeline | Mail, texts, and meetings interleaved for their busiest archive identity, with a switch between identities |
| Files | Files exchanged with them |
| Meetings | Their meetings, with [meeting activity and follow-ups](#meeting-context-and-follow-ups) |
| Profile | Structured profile, attributes, organizations, relationships, and network |
| Maintenance | Profile maintenance tracking, briefs, CardDAV publication, merge history, and identities |

Edit structured profile information, attributes, employment, and typed
relationships on the Profile tab. Curated display names also appear in
message views, analytics, and exports while source identifiers remain
available.

The Profile tab's network view can request one, two, or three hops and
optionally include ended records. It visualizes at most 250 nodes and 500
connections, while an always-present list groups the same connections by hop
for keyboard and screen reader use. Person and organization names in this
view come from durable profiles. Edges come only from curated typed
relationships and employments (including shared organizations), never
messages, participant co-occurrence, or inferred communication activity.

On the Maintenance tab, the **Last time we talked** card summarizes the
person's recent chat and text messages. Enroll the person, generate a brief,
and expand a sentence to check its sources. You can reject a brief or inspect
the dates and status of earlier versions. Generation requires a consented
provider and uses its budget; see [person briefs](/docs/usage/people-briefs/)
for setup and supported sources.

The Maintenance tab also lists the person's identities. Link or unlink an
identity there, or use **Same person…** to connect another one.

### An archive contact's page

An archive contact's page has **Overview** (an activity calendar),
**Timeline**, **Files**, and **Meetings** tabs. Choose **Save to Directory**
to keep the contact as a saved person. If saving fails, the reason appears
on the page.

### Domains

The domains view provides the same activity-and-files analysis for an exact
domain fact. A domain is not treated as an inferred organization identity.
Selecting a grouped person or domain in the Inbox opens its inspector in the
current context, including chronologically ordered related files.

### Reviews

Open **Reviews** from the gear menu. It brings together identity matches,
fact review, and imported relationships. Inspect the evidence before
accepting or rejecting a candidate. Conflicts between existing profiles
require an explicit merge decision. Merge history and reversal follow the
boundaries documented in [People](/docs/usage/people/).

## Saved Views

Saved Views persist useful analytical contexts in the daemon, so the same
library is available from every authenticated browser connected to this
single-user archive. Open them from the gear menu or from the bookmark button
beside the header search field. A view records its query, explicit search mode, filters,
grouping, presentation, sort, and visible columns.
Selection is intentionally not saved. The inspector stays pinned; the browser
does not save or apply inspector pin preferences.

Each record carries a schema version. An incompatible record remains visible,
but cannot be opened or edited: automatic migration is not attempted. Remove it
after confirmation and save the current context again. Updates and deletion use
the record revision as an optimistic-concurrency guard. If another browser
changes the view first, msgvault reports a conflict and requires you to reload
and review the latest revision instead of overwriting it.

The daemon validates every definition against the version-1 vocabulary
before saving it, so any stored view can be opened here, run by an API client
through `POST /api/v1/saved-views/{id}/run`, or executed by an AI assistant
with the [MCP server](/docs/usage/chat/#saved-views)'s `run_saved_view` tool.
All three read the same records and see the same revisions.

## Activity

Activity has three tabs: [Sources](#sources-and-sync-status),
[Operations](#operations), and [Deletions](#deletions).

## Sources and sync status

Sources is a status view. For each source it shows schedule information,
an active run's processed, added, and error counts, the latest terminal result,
and the last successful sync separately. A failed status request remains an
error rather than becoming an empty source list. Failed runs expose their
run-level and item-level errors, and a terminal result older than 24 hours is
marked `stale_last_result`.

`Sync now` is available only when that source reports the capability. A `202
Accepted` response means the daemon accepted the request, not that work has
finished. While the page is visible, the UI polls source status with bounded
backoff to show the run and live progress; it opens no streaming connection.
Status and `Sync now` requests time out after 20 seconds. Sync errors offer
`Refresh` to check source status without starting another sync. If `Sync now`
times out, refresh before trying again; the daemon may have accepted it.
Polling pauses while the tab is hidden and resumes when it is visible again.
A failed first load keeps its error on screen
until you select `Retry`. When the scheduler holds the sync lock without an
active run, the UI makes up to eight automatic refresh attempts, including
failed or timed-out requests; after that it stops automatic refresh, shows
`Automatic refresh paused. Source status may be
stale.`, and you can select `Refresh` to poll again. If the accepted run never
appears, the UI reports `sync_start_not_observed` rather than claiming success.
Conflicting runs and unavailable capabilities retain their explicit errors or
reasons. Full resync, pause/resume, schedule editing, and source add/remove are
not available in Sources.

Viewing Sources does not repair runs left marked as running after a crash.
They remain active until the daemon restarts or another sync for that source
acquires its lock and recovers them. Until then, `Sync now` stays unavailable
and status polling continues while the tab is visible.

## Operations

Operations answers two different questions: what is configured and ready now,
and what happened during a particular run. Its overview groups work into five
lanes:

| Lane | Work shown |
|---|---|
| Messages | Source sync and message embeddings |
| Facts | People sweeps, person embeddings, and external enrichment |
| Contacts | CardDAV sync |
| Documents | Document extraction and document embeddings |
| Attachments | Visual embeddings |

Filter history by lane, kind of work, state, and start date. Open a run to
inspect its progress, outcome, timestamps, and available diagnostics. Queued,
running, succeeded, partial, failed, and cancelled are distinct states.
Missing history is reported as unavailable instead of looking like no work
has ever run.

Links open source status, CardDAV settings, or detailed document and visual
index status. The latter show coverage and the current prerequisites for
processing. Operations offers **Start CardDAV sync**, **Build visual
index**, or **Resume visual index** only when the daemon advertises that
action. Source **Sync now** remains in Sources. Document extraction still
requires the explicit CLI upload workflow in
[Document Indexing](/docs/usage/document-indexing/).

While Operations is visible it refreshes status and run history. The filters
and selected run are kept in the URL, so browser Back and Forward restore the
view. If paging history becomes inconsistent after a change, use **Restart
operation history** to load a fresh snapshot.

## Deletions

The Inbox and Search support explicit row selection and select-all-matching
for the current query and filters. `d` and `D` open Activity > Deletions, where the
daemon first preflights the selection and reports any unavailable action before
the UI offers a separate staging confirmation. Deletions lists, inspects,
and cancels manifests; it cannot execute deletion against a provider. Use the
explicit `msgvault delete-staged` CLI workflow for that final operation.

## Keyboard controls

Tab keeps its normal browser meaning. Outside inputs and content viewers:

| Key | Action |
|---|---|
| `j` / `k`, arrows | Move row focus |
| `Home` / `End`, `PgUp` / `PgDn` | Navigate large tables |
| `Enter` | Open or drill into the focused row |
| `Esc` | Close the current shell layer or restore prior context |
| `/`, `Cmd/Ctrl+K` | Focus search |
| `Space` | Toggle the focused row |
| `A` / `x` | Select visible rows / clear selection |
| `d` / `D` | Review deletion staging |
| `f`, `g`, `s`, `r` | Filter, group, sort, reverse sort |
| `?` | Searchable shortcut help |
| `P` | [Command palette](#find-your-way-around) |

Destructive keys open a review; they never execute deletion immediately.
Shortcuts are suspended while typing and inside message/file content.

## Settings and restart behavior

Settings edits the daemon's `config.toml` from the browser. For every
`config.toml` setting the daemon supplies the category, section, label,
description, and allowed values, so the browser never decides on its own what
a setting means. Those categories are Appearance, Daemon, Archive, Search,
Sources, Attachments, Person enrichment, and Integrations. Larger categories
split into titled sections, for example Search has separate sections for the
text embedding provider, the embedding schedule, and visual attachment search.
The CardDAV account category is a separate browser-owned workflow with its own
save action; it is not part of the daemon's settings catalog.

Each row shows the setting name and one sentence about what it does. Limits
live on the control itself: a number input carries its minimum and maximum,
and a syntax hint such as the accepted duration format sits under the control
only when the syntax needs one. Settings where zero means "off", such as an
attachment size cap that falls back to the provider default, show a switch.
Switch it off and the row states what happens instead; switch it on and a
value input appears, starting from a suggested value. Rows you have changed
carry an amber dot, the footer counts unsaved changes, and Discard throws them
away. Save is disabled until something changes.

Schedules are one line. A Presets menu offers common schedules such as every
hour, every day at 03:00, or weekdays at 09:00, plus Off for schedules that
can be empty and Custom. Choosing Custom opens the expression editor beside
the menu, starting from the preset you had. The five fields are tinted, and
while the editor has focus or the pointer is over it a small card names the
fields (minute, hour, day, month, weekday) and says in plain English when the
schedule runs; a mistake names the field and the problem before you save. A
Time zone menu at the end of the line runs the schedule in a chosen IANA zone
instead of the daemon's own clock, shown as "Server time"; the choice is
stored as a `CRON_TZ=` prefix on the schedule. The CardDAV account form uses
the same field, and the Sources and CardDAV status views describe stored
schedules the same way.

### Appearance

Appearance holds the daemon's default theme and density. Its **This browser**
block overrides them for the current browser only; the daemon defaults apply
everywhere else. The command palette's **Theme** and **Density** commands set
the same per-browser choices.

### When changes take effect

Each category states once how its changes take effect. Appearance settings
apply right away. Every other `config.toml` category takes effect after the
daemon restarts, and after a save the page shows "Saved. Restart the daemon to
apply these changes." until it does. Two exceptions apply right away and say
so beside their controls: person-enrichment provider API keys, and the CardDAV
account, which saves through its own form. Saving makes targeted edits to `config.toml`
while preserving comments. A stale edit is rejected after another browser or
a hand edit changes the configuration; reload before saving again.

Host-managed values, such as the listener address and the server API key
(`server.api_key`), show their current value with a Host-managed tag and no
input. Change them in `config.toml` on the daemon host. After the API key
changes and the daemon restarts, old browser sessions end and the login
screen appears.

### Provider policies and credentials

Create or edit named person-enrichment policies for Exa and SixtyFour in
Settings. Provider checks and consent still govern whether enrichment can
run; configuration alone does not authorize a provider. The TUI shows these
policies read-only. See [External Person Enrichment](/docs/usage/people-enrichment/)
for the provider lifecycle.

Provider credentials for embeddings, enrichment, and sweeps are write-only,
and so are the task integration key and the daemon's own API key. Each key is
one line: a read-only box, a pencil button, and a trash button that removes
a stored key. The box shows `None` when no key is set, or a masked
hint of the set key, its first three and last three characters, such as
`sk-…x9Q`, so you can tell which key is in place. A key under twelve
characters shows as dots instead. The pencil opens a dialog to paste the
new key, and the dialog says when it takes effect: a person-enrichment key
applies right away, the text and visual embedding keys are stored at once
but used after the daemon restarts, and the task integration key is saved
with the rest of the page. A key that comes from an environment variable
says so under the line and cannot be cleared from the browser.

Credentials have a separate revision from `config.toml`. When changing both
an endpoint or model and its credential, save the endpoint/model first, then
the credential. This binds the key to the destination it was entered for.

### CardDAV contacts

CardDAV settings manage contact accounts and discovered address books. Choose
which books participate in sync, lookup, and publishing; publishing also
enables contact sync for that book. The workspace offers incremental and full
sync, recent run history, and conflict review. A conflict shows local and
remote versions before you choose which to keep. See
[People and CardDAV](/docs/usage/people-carddav/) for setup and publishing rules.

## Optional integration states

The optional task integration is server-side and provider-neutral. Msgvault
shows disabled, discovering, authentication required, reachable but
incompatible, partial, stale, unavailable, or ready instead of presenting a
failed lookup as “no links.” Credentials never enter browser types, URLs, or
error messages. The archive remains fully usable while the integration is
absent or unhealthy.
