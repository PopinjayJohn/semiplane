#!/usr/bin/env bash
#
# Verify that every internal link in the built site resolves to a file that
# actually exists in the output directory.
#
# Hugo checks the *content* refs it can see, but not the ones a layout or a
# shortcode constructs by hand, and not a link that resolves to a path that was
# renamed. Those are exactly the links that rot silently, and a docs site that
# 404s is worse than no docs site.
#
# This is a check, not a memory: the UI specification's §10.9 rule is that a
# finding from an agent-assisted sweep becomes a committed check, so that the
# same finding cannot come back unnoticed.
#
#   check-site-links.sh <published-dir> <base-path>
#
# <base-path> is the URL prefix the site is served under, e.g. /semiplane .
set -euo pipefail

DIR="${1:?usage: check-site-links.sh <published-dir> <base-path>}"
BASE="${2:-}"
[ -d "$DIR" ] || { printf 'error: %s is not a directory\n' "$DIR" >&2; exit 1; }

# Collapse repeated slashes, drop a trailing slash, and strip the base path.
resolve() {
  local path="$1"
  path="${path%%#*}"
  path="${path%%\?*}"
  [ -n "$path" ] || { printf '/'; return; }
  if [ -n "$BASE" ] && [ "${path#"$BASE"}" != "$path" ]; then
    path="${path#"$BASE"}"
  fi
  while [ "${path}" != "${path//\/\//\/}" ]; do path="${path//\/\//\/}"; done
  [ "${path%/}" != "$path" ] && path="${path%/}"
  printf '%s' "$path"
}

broken=0
checked=0

# Only real anchor tags are collected. The design records quote HTML in code
# fences, and a bare href= scan picks up those examples as if they were links --
# while restricting to "<a " misses them, because inside a code block the tag
# itself is escaped to &lt;a . Hugo's minifier also drops attribute quotes, so
# both href="..." and bare href=... have to be matched.
while IFS= read -r href; do
  case "$href" in
    ''|'#'*|'&'*|http://*|https://*|mailto:*|tel:*|data:*) continue ;;
  esac
  checked=$((checked + 1))
  target="$(resolve "$href")"
  for candidate in "$DIR$target" "$DIR$target/index.html" "$DIR$target.html"; do
    if [ -e "$candidate" ]; then
      target=""
      break
    fi
  done
  if [ -n "$target" ]; then
    printf 'broken link: %s -> %s\n' "$href" "$target" >&2
    broken=$((broken + 1))
  fi
done < <(grep -rhoE '<a [^>]*href=("[^"]*"|[^ >]*)' "$DIR" --include='*.html' \
        | sed 's/.*href=//; s/^"//; s/"$//' | sort -u)

if [ "$checked" -eq 0 ]; then
  printf 'error: found no internal links at all. The extraction pattern no longer matches the output, so this check is vacuous.\n' >&2
  exit 1
fi

if [ "$broken" -gt 0 ]; then
  printf '\n%d broken link(s) of %d internal link(s)\n' "$broken" "$checked" >&2
  exit 1
fi
printf 'check-site-links: %d internal link(s) resolve\n' "$checked"
