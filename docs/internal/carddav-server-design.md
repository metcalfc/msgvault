# Served Address Book: CardDAV Server in the Daemon

Design for serving msgvault people to iOS and macOS Contacts directly from the
daemon over a Tailscale network. Date: 2026-09-28. Status: proposal, not
implemented. Nothing in this document changes the existing CardDAV **client**
described in [People and CardDAV](../usage/people-carddav.md).

## Summary

Today a person reaches a phone only indirectly: msgvault publishes the person
to an external address book (Google, Fastmail, iCloud) and the phone syncs
that book. This design makes the daemon itself an address book. iOS and macOS
Contacts add it as a CardDAV account, reachable only over the owner's tailnet.
The archive stays on the owner's hardware and no third party holds the cards.

The server reuses the rendering path the client already uses to publish a
person (`store.LoadPersonVCardSnapshotContext` →
`vcardmap.ProjectPersonEnvelope` → `PrepareWireRender`) and the same
inference-review gate. It adds:

- a **served book**: one CardDAV address book collection under `/dav/`;
- **membership**: an explicit per-person opt-in, independent of publication;
- a **change log** so clients can use `sync-collection` and `getctag`;
- **device passwords**: HTTP Basic credentials that unlock only the DAV tree;
- a **Tailscale exposure recipe** using `tailscale serve` for HTTPS.

Version 1 is **read-only**. Edits made on the phone are rejected and revert.
Accepting edits is a later phase that routes device writes into the existing
conflict machinery.

## Goals

- Add the daemon as a CardDAV account in iOS Contacts and macOS Contacts with
  the standard "Other → CardDAV" flow, with server autodiscovery, and see the
  chosen people appear.
- Keep the phone in sync with profile changes within one client refresh cycle,
  using incremental sync rather than full downloads.
- Expose nothing to the phone that the publication flow would not export. The
  inference-review rule applies unchanged.
- Serve only over an encrypted path. On Tailscale that means `tailscale serve`
  HTTPS by default, with WireGuard-only plain HTTP as an explicit opt-in.
- Make the device credential incapable of reading anything except the served
  book. A stolen phone password must not read mail.

## Non-goals

- Accepting edits or deletions from the device (phase 2, see Rollout).
- Replacing the CardDAV client, the publication flow, or the write-target
  book. Both directions can be active at once.
- Multiple served books, groups, or per-device membership.
- Embedding Tailscale (`tsnet`) in the binary. Considered and deferred, see
  Decisions.
- Serving calendars (CalDAV) or Apple push notifications for CardDAV.

## Existing behavior and invariants

- `internal/carddav` is a client only (`types.go:1`). Its XML helpers build
  requests and parse `multistatus` responses; nothing encodes server
  responses. Its property types (`MultiStatus`, `PropStat`, `Properties`) are
  reusable for the server's XML.
- A person is rendered for export by `preparePublicationEnvelope`
  (`internal/carddav/mutation.go`). It is coupled to a remote book row and to
  the client's origin through `publicationHref`. The server needs a
  client-free variant of the same three steps.
- `persons.vcard_uid` is stable and unique. Merges retire the absorbed UID
  into `person_uid_aliases`. Deleting a person retires its UID forever; there
  are no person tombstones today.
- `persons.vcard_projection_revision` increments whenever projected input
  changes. It is a ready-made per-person change counter.
- Inferred facts never leave msgvault without approval of the exact card
  (`ErrCardDAVInferenceReviewRequired`, `person_carddav_inference_state`,
  `approved_body_sha256` on `carddav_publications`).
- The HTTP stack accepts Bearer keys, session cookies, and agent tokens. It
  does not accept Basic. In keyless mode it rejects any Host that is not a
  loopback authority (`keylessLoopbackHostAllowed`). `isSafeMethod` treats
  `PROPFIND` and `REPORT` as mutations. The rate limiter allows 10 requests
  per second per client IP and exempts authenticated loopback callers.
- The daemon listens on plain TCP only. Docs recommend a reverse proxy or
  Tailscale for HTTPS, and `trusted_proxies` governs forwarded headers.

## Decisions

1. **Same listener, separate security domain.** The DAV tree mounts on the
   existing mux at `/dav/` and `/.well-known/carddav`. It bypasses the API
   middleware (request security, CSRF, operation gate, global rate limit) and
   runs its own authentication and throttle. It keeps the outer recover,
   logger, request-ID, and timeout middleware. A second port would force a
   second `tailscale serve` mapping and a second firewall rule for no
   security gain, because the credential boundary is enforced per route
   either way.
2. **Hand-written DAV server, no new dependency.** The repo already owns a
   vCard codec and CardDAV XML types. The server needs six methods and three
   reports. Adding `emersion/go-webdav` would bring a second vCard model and a
   backend interface that does not fit the store's projection model.
3. **Explicit membership, not "all people" and not "published people".** The
   served book is a new destination. Reusing `carddav_publications.desired`
   would make "publish to Google" and "show on my phone" the same switch.
   Mirroring the published set is a one-line follow-up once membership exists.
4. **Read-only in v1.** Apple Contacts tolerates a read-only book: a `PUT` or
   `DELETE` answered with 403 makes the app discard the edit and refetch. This
   avoids inventing a second conflict model before the first one has been
   exercised from the other side.
5. **Basic auth over TLS, with device passwords.** Apple clients send Basic
   or Digest. Digest requires storing an HA1 that is equivalent to the
   plaintext password, so it is rejected. Basic is accepted only over an
   encrypted path, see Auth.
6. **`tailscale serve`, not `tsnet`, for v1.** `tailscale serve` gives a
   Let's Encrypt certificate for the MagicDNS name with one command and no
   Go dependency. `tsnet` would make the daemon its own tailnet node, which is
   attractive for containers, but it adds a large dependency and a second
   identity to manage. Revisit after the protocol surface is stable.
7. **Deletions get a change log.** `sync-collection` must report removed
   members. Rather than adding person tombstones, the served book keeps its
   own append-only change log scoped to membership.

## Architecture

```
iOS / macOS Contacts
        │  HTTPS  host.tailnet.ts.net
        ▼
tailscale serve (TLS termination, tailnet-only)
        │  HTTP  127.0.0.1:<api_port>, X-Forwarded-Proto: https
        ▼
msgvault daemon mux
   ├── /.well-known/carddav      → 301 /dav/
   ├── /dav/…                    → internal/carddavserver (Basic auth, throttle)
   │      principals/me/
   │      addressbooks/me/msgvault/           collection
   │      addressbooks/me/msgvault/<uid>.vcf  member
   └── /api/v1, /                → existing API and Web UI (unchanged)
```

New package `internal/carddavserver` (name distinct from the client package):

- `Handler`: `http.Handler` implementing `OPTIONS`, `PROPFIND`, `REPORT`,
  `GET`, `HEAD`, and rejecting `PUT`, `DELETE`, `MKCOL`, `MOVE`, `COPY`,
  `PROPPATCH`, `POST` with 403 and a DAV error body.
- `Renderer`: client-free rendering of one member to a vCard body at a
  requested version, plus ETag.
- `Catalog`: reads membership and the change log, refreshes stale rendered
  bodies, answers "list members", "get member", "changes since token".
- `Authenticator`: Basic parsing, device-password verification, failure
  throttle, encrypted-path check.

Store additions live in `internal/store` as `carddav_served_*.go`.

### Wiring

`runServe` builds `carddavserver.New(store, cfg.CardDAV.Serve, logger)` next
to the CardDAV controller and passes it in `api.ServerOptions`. `setupRouter`
registers the DAV handler on the mux before wrapping the API in its
middleware, so the DAV chain is:

```
requestID → logger → recover → timeout(DAV ceiling) → DAV auth → DAV throttle → Handler
```

## Data model

### Membership

```sql
CREATE TABLE carddav_served_members (
  person_id   INTEGER PRIMARY KEY REFERENCES persons(id) ON DELETE CASCADE,
  added_at    TEXT NOT NULL
);
```

Row presence is the state, following `person_tracking`. Adding a member
appends a `changed` entry to the log. Removing one appends `removed`.

Merges: when a member is absorbed, the survivor becomes a member if it was not
one already; the log records `removed` for the absorbed UID and `changed` for
the survivor. Reversing the merge repeats the same transitions in reverse.
Publication blocks merges today; served membership does not, because there is
no remote state to reconcile.

### Rendered resources

```sql
CREATE TABLE carddav_served_resources (
  person_id            INTEGER PRIMARY KEY REFERENCES persons(id) ON DELETE CASCADE,
  vcard_uid            TEXT NOT NULL,
  body_v30             BLOB NOT NULL,
  body_v40             BLOB NOT NULL,
  etag                 TEXT NOT NULL,           -- vcard.ETagForBody(body_v30)
  projection_revision  INTEGER NOT NULL,        -- persons.vcard_projection_revision at render
  approved_body_sha256 TEXT,                    -- inference review, see Privacy
  approved_inference_revision INTEGER,
  rendered_at          TEXT NOT NULL
);
```

Bodies are cached, not authoritative. A collection request first refreshes
every member whose `persons.vcard_projection_revision` is greater than
`projection_revision`, in one transaction, and appends a `changed` log entry
for each. Rendering is therefore lazy, happens once per profile change, and
never blocks a profile edit.

### Change log

```sql
CREATE TABLE carddav_served_changes (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  vcard_uid  TEXT NOT NULL,
  removed    BOOLEAN NOT NULL DEFAULT FALSE,
  at         TEXT NOT NULL
);
```

- **CTag** is `max(seq)`, formatted as a quoted string.
- **sync-token** is `urn:msgvault:carddav-sync:<seq>`.
- `sync-collection` with token `<n>` returns one entry per UID with
  `seq > n`, keeping only the latest entry per UID: `removed` entries become
  `404` responses, others carry `getetag`.
- Retention: entries older than 90 days are compacted to the newest entry per
  UID, and `removed` entries older than 90 days are dropped. A token older
  than the oldest retained `seq` returns `403` with
  `<D:valid-sync-token/>`, and the client performs a full resync as RFC 6578
  requires.

The PostgreSQL schema mirrors these three tables with `BIGSERIAL` and
`TIMESTAMPTZ`. All three are `CREATE TABLE IF NOT EXISTS` in both schema
files; no data migration is needed.

### Device passwords

```sql
CREATE TABLE carddav_served_credentials (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  label         TEXT NOT NULL,                 -- "Chad's iPhone"
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,                 -- argon2id, PHC string
  created_at    TEXT NOT NULL,
  last_used_at  TEXT,
  revoked_at    TEXT
);
```

The plaintext is shown once at creation and never stored. Revocation is a
timestamp, so a revoked device shows up in status with its last use.

## Protocol surface

Paths are fixed. `me` is literal: the daemon has one owner.

| Request | Response |
|---|---|
| `GET /.well-known/carddav` | `301 Location: /dav/` (relative, so scheme comes from the proxy) |
| `OPTIONS /dav/*` | `DAV: 1, addressbook`, `Allow` lists supported methods |
| `PROPFIND /dav/` | `current-user-principal` = `/dav/principals/me/` |
| `PROPFIND /dav/principals/me/` | `addressbook-home-set` = `/dav/addressbooks/me/`, `displayname`, `principal-URL` |
| `PROPFIND /dav/addressbooks/me/` Depth 1 | one child collection `msgvault/` |
| `PROPFIND /dav/addressbooks/me/msgvault/` Depth 0 | `resourcetype` (collection, addressbook), `displayname`, `getctag`, `sync-token`, `supported-address-data` (3.0, 4.0), `supported-report-set`, `max-resource-size`, `current-user-privilege-set` (read, read-current-user-privilege-set) |
| same, Depth 1 | plus one response per member with `getetag`, `getcontenttype` |
| `REPORT addressbook-multiget` | bodies for the requested hrefs; unknown hrefs get `404` |
| `REPORT addressbook-query` | all members; filters other than an empty filter return `403` with `supported-filter` |
| `REPORT sync-collection` | see Change log |
| `GET`/`HEAD …/<uid>.vcf` | body, `ETag`, `Content-Type: text/vcard; charset=utf-8`, honors `If-None-Match` |
| `PUT`, `DELETE`, `MKCOL`, `MOVE`, `COPY`, `PROPPATCH`, `POST` | `403` with `<D:error><D:need-privileges/></D:error>` |

Hrefs are `<vcard_uid>.vcf` under the collection, matching the client's own
`publicationHref` convention. Apple sends the same hrefs back in multiget,
sometimes with a different escaping; the resolver compares by decoded UID,
which `pull_test.go` already documents from the client side.

Version selection: the default and `GET` body is vCard 3.0, because Apple
Contacts requests and round-trips 3.0. A `REPORT` whose `address-data`
element carries `content-type="text/vcard" version="4.0"` receives the 4.0
body. Both bodies come from one `PrepareWireRender` call per version.

Limits: request bodies capped at 1 MiB; multiget capped at 500 hrefs;
`max-resource-size` advertised as 1 MiB; `PHOTO` is included only when the
encoded card stays under that size, otherwise omitted with a debug log.

## Rendering

`Renderer.Render(ctx, personID)`:

1. `snapshot := store.LoadPersonVCardSnapshotContext(ctx, personID)`.
2. Seed a minimal envelope with `UID` and `FN`, as `preparePublicationEnvelope`
   does when no remote resource exists, with `SourceRef = "carddav-served"`
   and `Href = "<uid>.vcf"`.
3. `vcardmap.ProjectPersonEnvelope(snapshot, seed)`.
4. Delete server-owned properties (the same `serverOwnedProperties` set the
   client strips), then add `PRODID:-//msgvault//served//EN` and `REV` from
   `persons.updated_at`.
5. `PrepareWireRender(Version30)` and `PrepareWireRender(Version40)`;
   `etag = vcard.ETagForBody(body_v30)`.

To share steps 2 to 4 with the client without a client instance,
`preparePublicationEnvelope` is split so its envelope-preparation core takes
a `(snapshot, seedEnvelope, version)` triple and neither the book row nor
`s.client`. The client keeps its href and version selection around that core.

## Auth

### Credential

Only `Authorization: Basic` is accepted on the DAV tree. The API key, browser
sessions, agent tokens, and the daemon runtime token are ignored there, and a
device password is never accepted on `/api/v1`. The two credential domains do
not overlap.

Unauthenticated requests receive `401 WWW-Authenticate: Basic realm="msgvault
contacts", charset="UTF-8"`. The well-known redirect and `OPTIONS` answer
without credentials because Apple probes them before it has sent any.

Verification is argon2id with a constant-time compare of the derived key.
Success updates `last_used_at` at most once per hour.

### Encrypted path requirement

A request carrying Basic credentials is rejected with `403` and a single
log line unless one of these holds:

- the connection's `RemoteAddr` is loopback and the effective scheme, as
  derived from a `trusted_proxies` entry, is `https` (the `tailscale serve`
  case);
- `RemoteAddr` is loopback and no forwarded headers are present (a local
  test client);
- `RemoteAddr` falls inside a CIDR listed in `[carddav.serve]
  allow_plain_http_from`. The intended value is the Tailscale range
  `100.64.0.0/10`, where WireGuard already encrypts the hop and the daemon is
  bound directly to the tailnet address. This is the only way to use the
  served book without `tailscale serve`, and iOS must then have **Use SSL**
  turned off.

Refusing rather than warning is deliberate. A phone will retry a stored
password forever; one plaintext hop on an untrusted network is one too many.

### Throttle

The DAV tree does not use the global per-IP limiter, because every request
through `tailscale serve` arrives from `127.0.0.1` and Apple bursts
`PROPFIND` and multiget in parallel. Instead:

- Failed authentications are counted per `(username, client)` where client
  is the forwarded address when the proxy is trusted, else `RemoteAddr`.
  Ten failures within five minutes lock that pair for one minute, doubling per
  further window up to one hour. Locked requests get `429 Retry-After`.
- Authenticated requests are limited to 200 requests per minute per
  credential, which is far above Apple's sync behavior and far below what a
  scraper wants.

### Optional Tailscale identity pin

`[carddav.serve] require_tailscale_login = "you@example.com"` makes the
handler additionally require the `Tailscale-User-Login` header to equal that
value. `tailscale serve` sets it for tailnet-internal requests. The header is
honored only when the request came through a `trusted_proxies` address,
because anyone can send a header. This is defense in depth for a leaked
device password, not a replacement for one.

## Privacy and inference review

The served card is the publication card. Everything that applies to
publishing applies here, including Private Notes mapping to `NOTE` and person
briefs being excluded.

Inferred facts follow the same gate. When a member's current inferred facts
differ from `approved_inference_revision` on its served resource:

- an existing rendered body keeps being served unchanged, and the member is
  reported as `review_required` in status and the Web UI;
- a member with no rendered body yet is not listed in the collection at all
  until approved, and `person share` exits non-zero with the same
  `carddav_inference_review_required` code publication uses.

Review reuses the existing preview and approval commands with a book selector:

```bash
msgvault person share 7 --preview
msgvault person share 7 --approve <approval_token>
```

The approval token binds the exact 3.0 body, the served book identity, and
the inferred-fact revision, using the same review-artifact tables as
publication with a distinct `kind`. Whether that requires widening
`person_carddav_inference_state` to be per destination is an implementation
question; the rule that no unapproved inferred fact is served is not.

## Configuration

```toml
[server]
bind_addr = "127.0.0.1"
api_port = 8080
api_key = "replace-with-a-long-random-key"
trusted_proxies = ["127.0.0.1"]   # tailscale serve forwards from loopback

[carddav.serve]
enabled = true
display_name = "msgvault"          # book name shown in Contacts
# allow_plain_http_from = ["100.64.0.0/10"]
# require_tailscale_login = "you@example.com"
```

`enabled = false` (the default) removes the routes entirely, so `/dav/`
returns 404 like any unknown path. Enabling with zero unrevoked credentials
serves 401 to everything, which is safe and obvious in status.

`api_key` is required in practice because `tailscale serve` presents a
non-loopback Host, which keyless mode rejects for the Web UI. The DAV tree is
unaffected by that check because it never runs in loopback mode.

## Surfaces

### CLI

```bash
msgvault carddav serve status                 # enabled, URL hints, member count, ctag, credentials, review_required list
msgvault carddav serve password add "Chad's iPhone" [--username chad]
msgvault carddav serve password list
msgvault carddav serve password revoke <id>
msgvault carddav serve members                # list members with review state
msgvault person share <id> [--preview | --approve <token>]
msgvault person unshare <id>
```

`share` is a different verb from `publish` on purpose: publish sends a card
out to a remote book, share lets devices fetch it from here. Commands route
through the daemon like the other CardDAV commands.

### API

Management under `/api/v1/carddav/serve/`: `status`, `credentials`
(GET, POST, DELETE `{id}`), `members/{person_id}` (GET, POST, DELETE) with
`preview` and `approve` mirroring `publications`. These use the normal API
authentication. The credential creation response is the only place the
plaintext appears.

### Web UI

- Directory profile: a **Share to my devices** toggle beside **Publish to
  CardDAV**, with the same review redirect to the CLI when approval is
  required.
- Settings → CardDAV: a **Served address book** section showing the server
  URL to enter on the device, the credential list with add and revoke, and
  the review-required members.

## Tailscale exposure

Operator steps on the Linux host:

```bash
# once, in the tailnet admin console: enable MagicDNS and HTTPS certificates
tailscale serve --bg 8080
tailscale serve status         # prints https://host.tailnet-name.ts.net/
```

This proxies the whole daemon at `https://host.tailnet-name.ts.net/` with a
Let's Encrypt certificate, tailnet-only. The Web UI is reachable at the same
origin, protected by `api_key` and the session flow, and its cookie becomes
`Secure` because `trusted_proxies` includes loopback. Tailnet ACLs can
restrict which devices may reach port 443 on the host.

Path-scoped `tailscale serve --set-path` mappings are not recommended for
v1: Apple discovers through `/.well-known/carddav` at the root, and Tailscale's
prefix handling for mounted paths would need verification.

Phones and laptops must be connected to Tailscale to sync. `tailscale funnel`
would expose the book publicly and is out of scope.

## Connecting Apple clients

iOS: Settings → Contacts → Accounts → Add Account → Other → **Add CardDAV
Account**. Server `host.tailnet-name.ts.net`, the device username and
password, any description. iOS fetches `/.well-known/carddav`, follows the
redirect, finds the principal, and lists one book named by `display_name`.

macOS: Contacts → Settings → Accounts → **Other Contacts Account** → CardDAV,
account type **Automatic**, the same server and credentials. If autodiscovery
fails, choose **Manual** with server address `host.tailnet-name.ts.net`, or
**Advanced** with server path `/dav/principals/me/` and port 443 with SSL.

Because v1 is read-only, editing a shared contact on the device shows Apple's
"could not be saved" alert and reverts. Contacts can still be copied into an
iCloud or On My Mac group.

## Failure behavior

- Store unavailable or rendering error on one member: that member is omitted
  from listings and returns `503` on direct fetch; the rest of the book is
  served. The failure is logged once per member per revision.
- Inference review pending: served body unchanged, see Privacy.
- Certificate or proxy misconfiguration: the encrypted-path rule returns 403
  and one log line naming the reason; status shows the last rejection.
- Daemon restart: no in-memory state matters. ETags, CTag, and sync tokens
  come from the store.

## Testing

- **Loopback protocol tests** start the real `api.Server` with the served
  book enabled and drive it with the repo's own CardDAV **client**:
  `Discover` against `/.well-known/carddav`, `Sync` with sync-collection,
  then a second `Sync` after a profile edit and a membership removal. This
  is production code on both sides and proves discovery, ETags, and
  incremental sync without a stub.
- **Apple request shapes** recorded from a real iOS 26 and macOS 26 session
  are replayed as fixtures: `PROPFIND` for `getctag`, `addressbook-multiget`
  with Apple's href escaping, `sync-collection` with and without a token.
  Fixtures use synthetic people and example addresses.
- **Auth tests**: Basic accepted only under the encrypted-path rule; API key
  and session cookie rejected on `/dav/`; device password rejected on
  `/api/v1`; lockout after ten failures; revoked credential rejected.
- **Review gate**: an unapproved inferred change leaves the served body
  unchanged and flags the member; approval updates body, ETag, and CTag.
- **Merge and delete**: absorbed member becomes a `404` in sync-collection,
  survivor changes; deleting a member person produces a `removed` entry in
  the same transaction.
- Manual verification with real Apple clients over `tailscale serve` is
  recorded in this document's "Load-bearing findings" section when the
  branch lands, with the Tailscale and OS versions used.

## Rollout and follow-ups

1. **Phase 1 (this design):** read-only server, membership, change log, device
   passwords, `tailscale serve` docs, CLI and API. Ships behind
   `[carddav.serve] enabled`.
2. **Phase 2: accept device edits.** Treat the device as a remote editor:
   `PUT` with `If-Match` lands in the existing conflict model with the served
   book as the address book identity, so `keep_local` and `keep_remote`
   apply unchanged. `DELETE` maps to unshare, not person deletion.
3. **Phase 3: `tsnet` listener.** The daemon joins the tailnet itself with
   `ListenTLS`, for containers where running `tailscaled` on the host is
   awkward. Auth and protocol stay the same.
4. **Follow-ups:** a `members = "published"` mirror mode; multiple books by
   category; CalDAV for meetings using the same principal.

## Open questions

- Should `person share` default to also publishing when a write target
  exists, or stay independent? This design keeps them independent.
- Whether `person_carddav_inference_state` must become per destination, or
  whether the served book can reuse the publication's approval when the
  bodies are byte-identical.
- Photo policy: include `PHOTO` by default, or opt in per member. This design
  includes it under the size cap.
