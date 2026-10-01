#!/usr/bin/env bash
# Validate media references using the real source matcher, without building the site.
set -euo pipefail

if ! command -v rg >/dev/null 2>&1; then
  printf 'rg not found; cannot validate docs media references\n' >&2
  exit 127
fi

public_doc_globs=(
  --glob '!docs/superpowers/**'
  --glob '!docs/internal/**'
  --glob '!docs/scripts/**'
  --glob '!docs/screenshots/**'
  --glob '!docs/diagrams/**'
  --glob '!docs/assets/**'
  --glob '!docs/site/**'
  --glob '!docs/zensical-public-docs.*/**'
  --glob '!docs/vercel-build.sh'
)

root_media_refs="$(
  (rg -n -o '(<img[^>]+src="/|!\[[^]]*\]\(/)[^)" >]+\.(png|svg|jpg|jpeg|webp|gif)' docs README.md "${public_doc_globs[@]}" || true) \
    | grep -Ev '(^|[^[:alnum:]_-])/?assets/(static|generated)/' \
    || true
)"
if [[ -n "$root_media_refs" ]]; then
  printf 'docs media references must use /assets/static or /assets/generated:\n%s\n' "$root_media_refs" >&2
  exit 1
fi

source_media_refs="$(
  (rg -n -o '(https://msgvault\.io/[^)" >]+\.(png|svg|jpg|jpeg|webp|gif)|(^|[^[:alnum:]_./-])/?(concepts/[^)" >]+\.(png|jpg|jpeg|webp|gif)|tui-[^)" >]+\.svg|stats\.svg|list-senders\.svg|how-it-works\.svg|oauth-multi-account\.svg|og-image\.(png|svg)|favicon(-192|-512)?\.(png|svg)))' docs README.md "${public_doc_globs[@]}" || true) \
    | grep -Ev 'https://msgvault\.io/assets/(static|generated)/|(^|[^[:alnum:]_-])/?assets/(static|generated)/' \
    || true
)"
if [[ -n "$source_media_refs" ]]; then
  printf 'docs source media references must use /assets/static or /assets/generated:\n%s\n' "$source_media_refs" >&2
  exit 1
fi
