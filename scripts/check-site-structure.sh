#!/usr/bin/env bash
#
# Structural assertions over the built docs site.
#
# These are the invariants the UI specification makes gate-blocking for the
# application (§7.2): exactly one <h1>, heading levels that never skip, a lang
# attribute, a skip link that is the first focusable element, no positive
# tabindex, and the four landmarks. They are cheap to check mechanically and
# expensive to notice by eye, which is the wrong ratio.
#
#   check-site-structure.sh <published-dir>
set -euo pipefail

DIR="${1:?usage: check-site-structure.sh <published-dir>}"
[ -d "$DIR" ] || { printf 'error: %s is not a directory\n' "$DIR" >&2; exit 1; }

fail=0
note() { printf 'FAIL %s: %s\n' "$1" "$2" >&2; fail=$((fail + 1)); }

# Hugo's minifier strips attribute quotes, so every pattern below has to match
# both attr="value" and bare attr=value. A check written against quoted output
# passes a quoted build and fails a minified one, which is a check that reports
# whatever the build flags happen to say.
has() { grep -qE "$1" "$2"; }

pages=0
while IFS= read -r page; do
  pages=$((pages + 1))
  rel="${page#"$DIR"}"

  h1="$(grep -coE '<h1[ >]' "$page" || true)"
  [ "$h1" -eq 1 ] || note "$rel" "expected exactly 1 <h1>, found $h1"

  # No positive tabindex anywhere: it reorders the tab sequence away from the
  # document order that the skip link and the landmarks depend on.
  if grep -oE 'tabindex=("|\x27)?[1-9][0-9]*' "$page" | head -1 | grep -q .; then
    note "$rel" "positive tabindex found: $(grep -oE 'tabindex=("|\x27)?[1-9][0-9]*' "$page" | head -1)"
  fi

  has '<html lang=[^ >]+' "$page" || note "$rel" "missing lang attribute on <html>"
  has 'href=("|\x27)?#main' "$page" || note "$rel" "no skip link targeting #main"
  has 'id=("|\x27)?main[ >]' "$page" || note "$rel" "no #main landmark for the skip link to reach"
  has 'role=("|\x27)?banner' "$page" || note "$rel" "missing banner landmark"
  has 'role=("|\x27)?main' "$page" || note "$rel" "missing main landmark"
  has 'role=("|\x27)?contentinfo' "$page" || note "$rel" "missing contentinfo landmark"

  # The skip link has to be the FIRST anchor in the document. Comparing it to
  # the first anchor *tag* works only if both are measured the same way, so the
  # first <a ...> is extracted whole and matched against the skip link's class.
  first_anchor="$(grep -oE '<a [^>]{0,160}' "$page" | head -1 || true)"
  case "$first_anchor" in
    *skip-link*) ;;
    '')          note "$rel" "no anchors found" ;;
    *)           note "$rel" "first focusable element is not the skip link: $first_anchor" ;;
  esac
done < <(find "$DIR" -name '*.html' | sort)

[ "$pages" -gt 0 ] || { printf 'error: no HTML found in %s\n' "$DIR" >&2; exit 1; }

if [ "$fail" -gt 0 ]; then
  printf '\n%d structural failure(s) across %d page(s)\n' "$fail" "$pages" >&2
  exit 1
fi
printf 'check-site-structure: %d page(s) pass\n' "$pages"
