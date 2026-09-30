---
last_edited: "2026-09-08"
title: Archive Remote Email Images
description: Save images hosted by email senders for offline reading, with explicit tracking consent.
---

Save externally hosted email images so you can read them later without
contacting the sender again. Remote image archiving is **off by default**:
downloading an image can activate tracking pixels and disclose the archive
server's IP address to the image host.

Embedded email attachments are already part of normal mail archiving. This
feature covers images that the message asks a website to load.

## Archive images in existing mail

Start with a small scan of one source. Use the source ID from
`msgvault list-accounts`:

```bash
msgvault archive-remote-images --allow-tracking --source-id 3 --limit 100
```

`--limit` counts messages scanned, not images downloaded. Omit `--source-id`
to scan all email sources; omit `--limit` for an unlimited scan:

```bash
msgvault archive-remote-images --allow-tracking
```

Every invocation requires `--allow-tracking`, even if automatic archiving is
already enabled. The command reports messages scanned, images downloaded,
images already archived, and errors. Successful downloads remain stored if
other downloads fail or you interrupt the command. Rerun it to retry missing
images; stored images are reused.

For a large initial sync or import, finish importing mail before running this
command. Slow image hosts otherwise delay each message as it is ingested.

## Include images during future syncs and imports

Enable [`[sync].archive_remote_images`](/docs/configuration/#sync) in the
daemon's `config.toml`, then restart the daemon:

```toml
[sync]
archive_remote_images = true
```

This enables downloads during Gmail and IMAP sync and EML, EMLX, MBOX, and PST
imports. Use the backfill command above for messages already archived.
Disabling the setting stops automatic downloads and keeps images already
stored.

## Read and export the archive

The Web UI's message and conversation views use local copies when available.
Images that were not archived still follow the reader's remote-image consent
control. Opening an archived image does not fetch a replacement from its host
if its local file is missing.

Downloaded images are inline attachments. They count toward attachment totals
and appear in the Files view, including small logos and tracking pixels. Use
the normal [attachment export commands](/docs/usage/exporting/) to save their
bytes.

The original raw MIME and stored HTML stay unchanged. An `export-eml` therefore
preserves the original message, including its original image URLs; it does not
embed the newly archived images into that EML file.

## Spam and trash never load images

A remote image tells its host that the message was opened, and junk senders
are exactly the ones who should not learn that. msgvault therefore never
fetches remote images for a message in a spam, junk, trash, or
deleted-items folder. A folder counts when:

- the provider marks it: Gmail's `SPAM` and `TRASH` labels, an IMAP folder
  with the `\Junk` or `\Trash` special-use attribute under any name (such as
  "Junk Email" or a localized name), or Microsoft 365's Junk Email and
  Deleted Items folders; or
- a source that marks nothing (mbox, PST) names it Spam, Junk, Junk Email,
  Junk E-mail, Bulk Mail, Trash, Deleted Items, Deleted Messages, or Bin,
  in any letter case.

For those messages:

- Sync, import, and `archive-remote-images` skip them.
- The Web UI reader shows the image count but no **Load images** button.
- The daemon's image proxy refuses them with `403 remote_images_blocked`.

The proxy also requires the message ID and fetches only an image that
message's stored body references (`403 remote_image_not_referenced`
otherwise), so it cannot be used to fetch arbitrary URLs. IMAP folder roles
are recorded at the next sync of each folder.

Moving a message out of spam or trash lifts the block for later reads and
backfills.

## Coverage and limits

Archiving reads HTTP(S) `<img src>` URLs, including URLs beginning with `//`.
It stores PNG, JPEG, GIF, and WebP images. It does not download SVG, CSS
backgrounds, `srcset` alternatives, external stylesheets, or linked pages.

| Limit | Value |
|---|---|
| Distinct image URLs per message | 64 |
| Each image | 10 MiB |
| Total newly downloaded image bytes per message | 30 MiB |
| HTML message body processed | 8 MiB |
| Time per image fetch | 15 seconds |
| Time budget per message | 60 seconds |
| Redirects per image | 3 |

A message can be archived even when an image exceeds a limit, is unavailable,
or fails to download. Review image errors separately from mail-import results.
Old sender URLs may already have expired; msgvault cannot reconstruct an image
that the host no longer serves.
