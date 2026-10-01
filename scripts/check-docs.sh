#!/usr/bin/env bash
set -euo pipefail

failed=0

fail() {
  printf '%s\n' "$1" >&2
  failed=1
}

if [[ -e "zensical.toml" ]]; then
  fail 'Zensical config must live under docs/: zensical.toml'
fi

if [[ -e "vercel.json" ]]; then
  fail 'Vercel config must live under docs/: vercel.json'
fi

tracked_media="$(
  git ls-files docs 2>/dev/null | grep -E '\.(png|svg|jpg|jpeg|webp|gif)$' || true
)"
if [[ -n "$tracked_media" ]]; then
  printf 'docs image media must live in docs asset branches, not main:\n%s\n' "$tracked_media" >&2
  failed=1
fi

tracked_hydrated_assets="$(
  git ls-files docs/assets/static docs/assets/generated 2>/dev/null || true
)"
if [[ -n "$tracked_hydrated_assets" ]]; then
  printf 'hydrated docs assets must be ignored, not tracked:\n%s\n' "$tracked_hydrated_assets" >&2
  failed=1
fi

if [[ "$failed" -ne 0 ]]; then
  exit 1
fi

python_bin="${PYTHON:-}"
if [[ -z "$python_bin" ]]; then
  if command -v python3 >/dev/null 2>&1; then
    python_bin="python3"
  elif command -v python >/dev/null 2>&1; then
    python_bin="python"
  else
    printf 'python not found; cannot validate docs markdown sources\n' >&2
    exit 127
  fi
fi
"$python_bin" docs/scripts/check_markdown_sources.py

bash "$(dirname "$0")/check-docs-media.sh"

bash docs/assets/hydrate-assets.sh

if ! command -v uv >/dev/null 2>&1; then
  printf 'uv not found; install uv before running docs-check\n' >&2
  exit 1
fi

(
  cd docs
  uv run --frozen bash ./vercel-build.sh
  uv run --frozen python scripts/check_built_site.py
  uv run --frozen python scripts/selftest_check_built_site.py
  uv run --frozen python scripts/check_vercel_redirects.py
)
