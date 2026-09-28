# Served Address Book: CardDAV Server in the Daemon

Design for serving msgvault people to iOS and macOS Contacts directly from the
daemon over a Tailscale network. Date: 2026-09-28. Status: agreed; the first slice
is implemented. This is a fork-local feature; it is not intended for upstream.

Implementation status:

- **Landed:** `vcardmap.RenderPersonCard` and `SeedEnvelope` as the shared
  render core, with the client's `preparePublicationEnvelope` calling them;
  `store.PersonCatalogDigestContext` for the ctag; `internal/carddavserver`
  with the full read-only protocol surface and curated-only projection; a
  roundtrip test in `internal/carddav` that discovers and pulls the served
  book through msgvault's own client.
- **Landed (slice 2):** `[carddav.serve]` config, the hashed device
  credential in `tokens/carddav-served.json`, the gate in `internal/api`
  that mounts the handler behind Basic auth with the encrypted-path rule,
  per-client lockout, verified-credential cache, and the optional Tailscale
  login pin; `msgvault carddav serve status|password set|password clear`;
  user docs for configuration, CLI, and the Tailscale and Apple setup.
- **Deviation from the design:** there is no `AuthModeCardDAVDevice`. The
  served paths classify as `required` for the rest of the middleware, which
  keeps the keyless Host guard and operation gate away from them, and the
  gate handler does the actual authentication. The rate-limit exemption
  asks the gate directly. Same effect, one fewer enum value to thread
  through session status and OpenAPI.
- **Not yet:** the management API and Web UI settings section. The CLI
  writes the credential file locally, so it must run on the daemon host.
Nothing in this document changes the existing CardDAV **client** described in
[People and CardDAV](../usage/people-carddav.md).

## Summary

Today a person reaches a phone only indirectly: msgvault publishes the person
to an external address book and the phone syncs that book. This design makes
the daemon itself an address book. iOS and macOS Contacts add it as a CardDAV
account, reachable only over the owner's tailnet. No third party holds the
cards.

The server reuses the rendering path the client uses to publish a person
(`store.LoadPersonVCardSnapshotContext` → `vcardmap.ProjectPersonEnvelope` →
`PrepareWireRender`). It adds a read-only DAV surface under `/dav/`, a
`getctag` derived from the persons table, one hashed device credential
accepted only on that surface, and a `tailscale serve` recipe for HTTPS.

Version 1 serves **every person** in the curated persons table, **read-only**,
with **inferred facts removed**. Estimated size is 1.5k to 2.5k lines
including tests.

## Goals

- Add the daemon as a CardDAV account in iOS and macOS Contacts through the
  standard "Other → CardDAV" flow with autodiscovery.
- Pick up profile changes on the next client refresh without full downloads
  of unchanged cards.
- Never show the phone a fact msgvault only guessed.
- Serve only over an encrypted path.
- Make the device credential incapable of reading anything but the book.

## Non-goals for v1

- Accepting edits or deletions from the device.
- Per-person membership or multiple books.
- RFC 6578 `sync-collection`.
- Embedding Tailscale (`tsnet`).
- CalDAV.

## Existing behavior and invariants

- `internal/carddav` is a client only. Its XML helpers build requests and
  parse `multistatus`; its property types are reusable for server output.
- `preparePublicationEnvelope` (`internal/carddav/mutation.go`) renders a
  person. Two lines couple it to the client: the href from `publicationHref`
  and the version from the remote book.
- `persons.vcard_uid` is stable and unique. `persons.vcard_projection_revision`
  increments whenever projected input changes, including employments and
  contact points; `persons.updated_at` does not track those.
- Inferred facts are employments and attribute values whose provenance is
  extraction or enrichment (`provenanceIsInferred`,
  `internal/store/carddav_inference.go`). Both are visible on the snapshot.
- The HTTP stack accepts Bearer keys, session cookies, and agent tokens, not
  Basic. `requestSecurityMiddleware` applies its Host and Origin rules only to
  keyless-loopback and session modes; an Origin-less `PROPFIND` under any other
  mode passes. The rate limiter allows 10 requests per second per client IP
  and exempts authenticated loopback callers.
- The daemon listens on plain TCP only.

## Decisions

1. **All persons, no membership.** The persons table is the curated, promoted
   set. A separate opt-in would duplicate that curation. Membership can be
   added later without changing the protocol surface.
2. **`getctag` only.** Apple clients re-list hrefs and ETags when the ctag
   changes and multiget only what differs. At hundreds of contacts that is
   cheap. `sync-collection` needs a deletion log that does not exist, so it
   waits.
3. **Curated-only projection.** Inferred employments and attribute values are
   filtered from the snapshot before projection. This replaces the
   publication flow's approval tokens for the served book. Approving inferred
   facts for the phone is a follow-up.
4. **Read-only.** `PUT` and `DELETE` return 403 and the privilege set
   advertises read only. Apple Contacts discards the edit and refetches.
5. **Hand-written DAV, no new dependency.** `emersion/go-webdav` routes
   through its own vCard type, which is lossy against this repo's renderer.
6. **Basic auth over an encrypted path, one hashed credential.** Digest
   needs an HA1 equivalent to the plaintext, so it is rejected.
7. **Normal middleware chain plus one auth mode.** No separate listener or
   chain. Verified against `csrf.go`: the DAV methods pass once the request
   classifies as the new mode.
8. **`tailscale serve`, not `tsnet`.** One command, a valid certificate, no
   dependency. `tsnet` is revisited for container deployments later.

## Architecture

```
iOS / macOS Contacts
        │  HTTPS  host.tailnet.ts.net
        ▼
tailscale serve (TLS, tailnet-only)
        │  HTTP  127.0.0.1:<api_port>, X-Forwarded-Proto: https
        ▼
msgvault daemon mux
   ├── /.well-known/carddav               → 301 /dav/
   ├── /dav/                              → internal/carddavserver
   │      principals/me/
   │      addressbooks/me/msgvault/            collection
   │      addressbooks/me/msgvault/<uid>.vcf   member
   └── /api/v1, /                         → existing API and Web UI
```

New package `internal/carddavserver`:

- `Handler`: `OPTIONS`, `PROPFIND`, `REPORT`, `GET`, `HEAD`; everything else
  403 with a DAV error body.
- `Renderer`: client-free rendering of one person at a requested version.
- `Credential`: load, verify, and failure throttle.

`runServe` constructs the handler and passes it in `api.ServerOptions`;
`setupRouter` registers it on the mux at `/dav/` and `/.well-known/carddav`.

### Middleware fit

- `classifyAPIRequestDirect` gains `AuthModeCardDAVDevice`, set only when the
  path is under `/dav/` and the Basic credential verifies. Any other
  credential on `/dav/` yields `AuthModeRequired`. The device credential is
  never consulted for other paths.
- `apiRequestAuthorized` does not include the new mode, so it cannot reach
  `/api/v1`.
- The rate-limit exempt predicate includes the new mode. Unauthenticated
  `/dav/` requests stay limited, and the credential throttle below handles
  the loopback case.
- The existing CardDAV protective timeout ceiling in `timeoutMiddleware`
  applies to `/dav/` as well.

## Rendering

`Renderer.Render(ctx, personID, version)`:

1. Load the snapshot with `LoadPersonVCardSnapshotContext`.
2. Drop `Employments` and `Attributes` whose source is extraction or
   enrichment.
3. Seed a minimal envelope (`UID`, `FN`) with `SourceRef = "carddav-served"`
   and `Href = "<uid>.vcf"`.
4. `ProjectPersonEnvelope`, delete the client's `serverOwnedProperties`, add
   `PRODID:-//msgvault//served//EN`.
5. `PrepareWireRender(version)`.

`preparePublicationEnvelope` is refactored so its core takes
`(snapshot, seed, version)` and the client wraps it with its href and version
choice. The server calls the same core.

Bodies are rendered on request and cached in memory keyed by
`(person_id, vcard_projection_revision, version)` with a small LRU. Nothing
about rendering is persisted.

**ETag** is `vcard.ETagForBody(body)`.

**CTag** is a hex SHA-256 over `count(*)`, `sum(vcard_projection_revision)`,
`max(id)`, and `max(updated_at)` from `persons`. Every add, delete, merge, and
projected edit moves at least one of those.

Version: `GET` and the default `REPORT` body are vCard 3.0, which Apple
requests and round-trips. A `REPORT` with `address-data version="4.0"` gets
4.0. `supported-address-data` advertises both.

Limits: request bodies 1 MiB; multiget 500 hrefs; `max-resource-size`
1 MiB; `PHOTO` omitted when the card would exceed it.

## Protocol surface

| Request | Response |
|---|---|
| `GET /.well-known/carddav` | `301 Location: /dav/` (relative) |
| `OPTIONS /dav/*` | `DAV: 1, addressbook`; `Allow` lists supported methods |
| `PROPFIND /dav/` | `current-user-principal` = `/dav/principals/me/` |
| `PROPFIND /dav/principals/me/` | `addressbook-home-set` = `/dav/addressbooks/me/`, `displayname`, `principal-URL` |
| `PROPFIND /dav/addressbooks/me/` Depth 1 | one child `msgvault/` |
| `PROPFIND …/msgvault/` Depth 0 | `resourcetype` (collection, addressbook), `displayname`, `getctag`, `supported-address-data`, `supported-report-set`, `max-resource-size`, `current-user-privilege-set` (read) |
| `PROPFIND …/msgvault/` Depth 1 | plus one response per person with `getetag`, `getcontenttype` |
| `REPORT addressbook-multiget` | bodies for requested hrefs; unknown hrefs 404 |
| `REPORT addressbook-query` | all persons; non-empty filters 403 with `supported-filter` |
| `GET`/`HEAD …/<uid>.vcf` | body, `ETag`, `Content-Type: text/vcard; charset=utf-8`, honors `If-None-Match` |
| `PUT`, `DELETE`, `MKCOL`, `MOVE`, `COPY`, `PROPPATCH`, `POST` | 403 `<D:error><D:need-privileges/></D:error>` |

Hrefs are `<vcard_uid>.vcf`, matching the client's `publicationHref`. Apple
echoes hrefs with different escaping; compare by decoded UID. Retired UIDs in
`person_uid_aliases` return 404, never a redirect, so a merged contact
disappears and the survivor reappears on the next ctag change.

## Auth

### Credential

One device credential, created by:

```bash
msgvault carddav serve password set [--username msgvault]
```

The command prints the password once. The daemon stores
`carddav-served.json` in its token directory with private permissions,
containing the username and an argon2id PHC hash. `password set` replaces the
previous credential; `password clear` removes it. Both routes through the
daemon like other CardDAV commands.

Only `Authorization: Basic` is accepted on `/dav/`. Unauthenticated requests
get `401 WWW-Authenticate: Basic realm="msgvault contacts", charset="UTF-8"`.
The well-known redirect and `OPTIONS` answer without credentials because
Apple probes them first.

### Encrypted path requirement

Basic credentials are refused with 403 and one log line unless:

- `RemoteAddr` is loopback and a `trusted_proxies` entry supplied
  `X-Forwarded-Proto: https` (the `tailscale serve` case); or
- `RemoteAddr` is loopback and no forwarded headers are present (local
  testing); or
- `RemoteAddr` is inside a CIDR in `allow_plain_http_from`. The intended
  value is `100.64.0.0/10`, for a daemon bound directly to its Tailscale
  address with iOS **Use SSL** turned off. WireGuard encrypts that hop.

Refusing rather than warning is deliberate: a phone replays a stored password
forever.

### Throttle

Every request through `tailscale serve` arrives from `127.0.0.1`, so the
per-IP limiter cannot see a brute force. Failed Basic attempts are counted per
`(username, client)`, where client is the forwarded address when the proxy is
trusted, else `RemoteAddr`. Ten failures in five minutes lock the pair for one
minute, doubling per further window up to one hour, answered with
`429 Retry-After`.

### Optional Tailscale identity pin

`require_tailscale_login = "you@example.com"` additionally requires the
`Tailscale-User-Login` header to match. The header is honored only from a
`trusted_proxies` address. Defense in depth for a leaked password.

## Configuration

```toml
[server]
bind_addr = "127.0.0.1"
api_port = 8080                    # must be fixed; tailscale serve targets it
api_key = "replace-with-a-long-random-key"
trusted_proxies = ["127.0.0.1"]

[carddav.serve]
enabled = true
display_name = "msgvault"
# allow_plain_http_from = ["100.64.0.0/10"]
# require_tailscale_login = "you@example.com"
```

`enabled = false` (default) registers no routes. Enabled with no credential
answers 401 to everything and shows that in status.

`api_key` is required in practice: `tailscale serve` presents a non-loopback
Host, which keyless mode rejects for the Web UI.

## Surfaces

CLI:

```bash
msgvault carddav serve status         # enabled, URL hint, person count, ctag, credential state, last rejection
msgvault carddav serve password set
msgvault carddav serve password clear
```

API: `GET /api/v1/carddav/serve/status`, `PUT` and `DELETE`
`/api/v1/carddav/serve/credential`. Normal API authentication. The `PUT`
response is the only place the plaintext appears.

Web UI: Settings → CardDAV gains a **Served address book** section with the
server URL to enter on the device, the username, and set/clear.

Docs to update: the CardDAV guide, configuration reference, CLI reference.

## Tailscale exposure

```bash
# once, in the tailnet admin console: enable MagicDNS and HTTPS certificates
tailscale serve --bg 8080
tailscale serve status              # https://host.tailnet-name.ts.net/
```

This proxies the whole daemon at that origin, tailnet-only, with a Let's
Encrypt certificate. The Web UI shares the origin behind `api_key`; its
cookie becomes `Secure` because `trusted_proxies` includes loopback. Tailnet
ACLs restrict which devices may reach port 443.

Path-scoped `--set-path` mappings are not used: Apple discovers at the root,
and Tailscale's prefix handling would need verification. `tailscale funnel`
is out of scope.

## Connecting Apple clients

iOS: Settings → Contacts → Accounts → Add Account → Other → **Add CardDAV
Account**. Server `host.tailnet-name.ts.net`, the device username and
password.

macOS: Contacts → Settings → Accounts → **Other Contacts Account** → CardDAV,
type **Automatic**. If discovery fails, **Advanced** with server path
`/dav/principals/me/`, port 443, SSL on.

Edits on the device show Apple's "could not be saved" alert and revert.
Devices sync only while connected to Tailscale.

## Failure behavior

- Rendering error for one person: omitted from listings, 503 on direct fetch,
  logged once per person per revision. The rest of the book is served.
- Encrypted-path rejection: 403, one log line, shown in status.
- Daemon restart: no state to lose. ETags and ctag derive from the store.

## Testing

- **Loopback protocol tests** start the real `api.Server` with the served
  book enabled and drive it with the repo's own CardDAV client: `Discover`
  against `/.well-known/carddav`, then a pull, then a second pull after a
  profile edit and a person deletion to prove the ctag and ETags move.
  Production code on both sides, no stubs.
- **Apple request fixtures** recorded from real iOS 26 and macOS 26 sessions
  with synthetic people: `PROPFIND` for `getctag`, multiget with Apple's href
  escaping, `addressbook-query` with an empty filter.
- **Auth**: Basic accepted only under the encrypted-path rule; API key and
  session cookie rejected on `/dav/`; device password rejected on `/api/v1`;
  lockout after ten failures; cleared credential rejected.
- **Curated-only**: an inferred employment is absent from the served card and
  present in the publication preview for the same person.
- Manual verification against real Apple clients over `tailscale serve` is
  recorded below when the branch lands, with versions.

## Rollout and follow-ups

1. **v1** as above, behind `[carddav.serve] enabled`.
2. **Membership**: an opt-in table modeled on `person_tracking`, `person
   share`/`unshare`, a Directory toggle. Protocol unchanged.
3. **Inferred facts with approval**: reuse the publication review artifacts
   with a served-book kind, so approved inferred employers reach the phone.
4. **`sync-collection`**: a served change log with 90-day tombstone retention.
5. **Device edits**: `PUT` with `If-Match` enters the existing conflict model
   with the served book as the address-book identity; `DELETE` maps to
   unshare, not person deletion.
6. **`tsnet`** listener for container deployments.

## Load-bearing findings

To be filled in from the first real iOS and macOS connection: Tailscale
version, OS versions, whether Automatic discovery succeeded, and any request
shapes that differed from the fixtures.
