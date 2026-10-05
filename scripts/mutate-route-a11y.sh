#!/usr/bin/env bash
# Mutation evidence for the route §10.2/§10.6 audits in `internal/httpapi/wiki`
# and `internal/httpapi/assets`, and for the §10.9 sweep-conversion audits in
# `internal/web/sweep_test.go`.
#
# Each entry mutates ONE line of product code that an assertion holds, runs the
# assertion's own test, and requires it to FAIL. A mutation that leaves the gate
# green is an assertion that is not holding anything — and three of phase 5's
# first-draft gate tests could not fail, which is the failure this script exists to
# make impossible to ship.
#
# It mutates `.templ` and `.css` sources and restores them with `git checkout`, so it
# **refuses to run on a dirty worktree**: an uncommitted change in one of the files
# it restores would be discarded, and a script that can eat your work is not a
# script anybody should keep.
#
# A `.css` mutation is followed by a `make css` rebuild, because the sweep audits
# read the *built* stylesheet: mutating the source without rebuilding would test
# the build's staleness rather than the audit. The rebuild is also what runs
# after the restore, so the tree is left as it was found.
#
# Usage: `scripts/mutate-route-a11y.sh` (from a clean checkout, with
# `.toolbin/templ` present). Exits non-zero if any mutation left the gate green.
set -u

export PATH=$PATH:/usr/local/go/bin:/root/go/bin
cd "$(dirname "$0")/.."

if [ -n "$(git status --porcelain -- '*.templ' '*.go' '*.css')" ]; then
  echo "refusing to run on a dirty worktree; this script restores the files it"
  echo "mutates with 'git checkout --' and would discard uncommitted work."
  echo
  git status --short
  exit 2
fi

LOG=$(mktemp)
FAILURES=0

run_case() {
  local name="$1" pkg="$2" test="$3" file="$4" from="$5" to="$6"
  local templ_needs=no css_needs=no
  [[ "$file" == *.templ ]] && templ_needs=yes
  [[ "$file" == *.css ]] && css_needs=yes

  if ! grep -qF -- "$from" "$file"; then
    echo "BAD   $name — the anchor is gone from $file: $from" | tee -a "$LOG"
    FAILURES=$((FAILURES + 1))

    return
  fi

  python3 - "$file" "$from" "$to" <<'PY'
import sys
path, frm, to = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(path).read()
assert s.count(frm) >= 1, frm
# EVERY occurrence, not the first: several of these strings also appear in a
# package doc comment, and mutating only the comment is a mutation that renders
# nothing -- which reads in the log as "the assertion holds nothing" when the
# mutation is what was wrong. A doc comment is not rendered, so changing it too is
# harmless; the test failing is what proves the element changed.
open(path, 'w').write(s.replace(frm, to))
PY

  local gen
  if [[ "$templ_needs" == yes ]]; then
    if ! gen=$(.toolbin/templ generate 2>&1); then
      echo "BAD   $name — templ refused the mutation (a tag mismatch is the usual"
      echo "      cause; templ needs balanced tags, so <h1>x</h1> cannot become <h2>x</h1>)" | tee -a "$LOG"
      echo "$gen" | tail -2 | sed 's/^/      /' | tee -a "$LOG"
      git checkout -- "$file"
      .toolbin/templ generate >/dev/null 2>&1
      FAILURES=$((FAILURES + 1))

      return
    fi
  fi

  local css_out
  if [[ "$css_needs" == yes ]]; then
    if ! css_out=$(make css 2>&1); then
      echo "BAD   $name — make css refused the mutation:" | tee -a "$LOG"
      echo "$css_out" | tail -3 | sed 's/^/      /' | tee -a "$LOG"
      git checkout -- "$file"
      make css >/dev/null 2>&1
      FAILURES=$((FAILURES + 1))

      return
    fi
  fi

  local out
  out=$(go test -count=1 -run "$test" "$pkg" 2>&1)
  if grep -qE '^(FAIL|--- FAIL)' <<<"$out"; then
    local why
    why=$(grep -m1 -E '[a-z_]+_test\.go:[0-9]+:' <<<"$out" | sed 's/^ *//' | cut -c1-150)
    echo "OK    $name  →  FAILS as required" | tee -a "$LOG"
    echo "            $why" | tee -a "$LOG"
  else
    echo "BAD   $name  →  STILL GREEN. The assertion holds nothing." | tee -a "$LOG"
    echo "$out" | tail -3 | sed 's/^/            /' | tee -a "$LOG"
    echo "            [debug] $file now carries the mutation: $(grep -cF -- "$to" "$file" || true)" | tee -a "$LOG"
    FAILURES=$((FAILURES + 1))
  fi

  git checkout -- "$file"
  [[ "$templ_needs" == yes ]] && .toolbin/templ generate >/dev/null 2>&1
  [[ "$css_needs" == yes ]] && make css >/dev/null 2>&1
}

WEB=./internal/web
SHELLCSS=internal/web/static/css/shell.css
PLAYCSS=internal/web/static/css/play.css
TVCSS=internal/web/static/css/tv.css
WIKI=./internal/httpapi/wiki
ASSETS=./internal/httpapi/assets
COMP=internal/web/components
CHROME=internal/web/components/chrome
ART=internal/web/components/wiki.templ
SHELL=internal/web/components/shell.templ
RAIL=$CHROME/rail.templ

run_case "h1 count (wiki)"            "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$ART" \
  '<h1 id="page-heading" data-testid="page-title">{ view.Heading }</h1>' '<p id="page-heading" data-testid="page-title">{ view.Heading }</p>'

run_case "landmark label (rail)"      "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$RAIL" \
  'aria-label="Utilities"' 'aria-label="Extras"'

run_case "positive tabindex"          "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$SHELL" \
  'class="shell-main target" role="main" tabindex="-1"' 'class="shell-main target" role="main" tabindex="1"'

run_case "inline outline suppression" "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$ART" \
  '<h1 id="page-heading" data-testid="page-title">' '<h1 id="page-heading" data-testid="page-title" style="outline: none">'

run_case "skip link target removed"   "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$SHELL" \
  '@ui.SkipLink("main", "Skip to content", "skip-to-content")' '@ui.SkipLink("nowhere", "Skip to content", "skip-to-content")'

run_case "test hook removed"          "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$ART" \
  '<div class="page-body" data-testid="page-body">' '<div class="page-body">'

run_case "vocabulary in a comment"    "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$SHELL" \
  '<body>' '<body><!-- the world of this account -->'

run_case "vocabulary in a data attr"  "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$RAIL" \
  'data-shell-rail' 'data-world="true"'

run_case "aria-hidden on a landmark"  "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$RAIL" \
  'role="complementary"' 'role="complementary" aria-hidden="true"'

run_case "heading level skip"         "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$COMP/states.templ" \
  '<h2 id="instance-rail-heading">Instance</h2>' '<h4 id="instance-rail-heading">Instance</h4>'

run_case "aria-labelledby unresolved" "$WIKI"   'TestEveryRouteSatisfiesTheStructuralContract' "$COMP/states.templ" \
  'aria-labelledby="instance-rail-heading"' 'aria-labelledby="instance-rail-heading-gone"'

run_case "target class removed"       "$WIKI"   'TestEveryRouteCarriesTheTargetClassOnEveryFocusStop' "$CHROME/header.templ" \
  'class="target header-theme"' 'class="header-theme"'

run_case "assets h1 count"            "$ASSETS" 'TestEveryRouteSatisfiesTheStructuralContract' "$ART" \
  '<h1 id="page-heading" data-testid="page-title">{ view.Heading }</h1>' '<p id="page-heading" data-testid="page-title">{ view.Heading }</p>'

run_case "assets skip link target"    "$ASSETS" 'TestEveryRouteSatisfiesTheStructuralContract' "$SHELL" \
  '@ui.SkipLink("rail", "Skip to utilities", "skip-to-utilities")' '@ui.SkipLink("nope", "Skip to utilities", "skip-to-utilities")'

run_case "assets target class"        "$ASSETS" 'TestEveryRouteCarriesTheTargetClassOnEveryFocusStop' "$CHROME/header.templ" \
  'class="shell-brand target"' 'class="shell-brand"'

run_case "assets landmark label"      "$ASSETS" 'TestEveryRouteSatisfiesTheStructuralContract' "$RAIL" \
  'aria-label="Utilities"' 'aria-label="Bits"'

# The §10.9 sweep-conversion audits. Each mutates the stylesheet source the
# built-file audit reads (rebuilt by run_case), so a green run means the audit
# holds nothing rather than that the build is stale.
run_case "short header restored"       "$WEB" 'TestTheBuiltStylesheetCollapsesTheChromeInShortLandscape' "$SHELLCSS" \
  '--header-h: var(--header-h-short);' '--header-h: var(--header-h);'

run_case "bottom-bar token dropped"    "$WEB" 'TestTheBuiltStylesheetReservesRoomForTheBottomBar' "$SHELLCSS" \
  'padding-block-end: calc(var(--bottom-bar-h) + env(safe-area-inset-bottom));' 'padding-block-end: env(safe-area-inset-bottom);'

run_case "compact map takes taps"     "$WEB" 'TestTheBuiltStylesheetKeepsTheCompactMapFromTakingGestures' "$PLAYCSS" \
  ':root[data-ui="compact-short"] .play-map {
  pointer-events: none;' ':root[data-ui="compact-short"] .play-map {
  pointer-events: auto;'

run_case "play sheet covers the bar"  "$WEB" 'TestTheBuiltStylesheetAnchorsThePlaySheetAboveTheActionBar' "$PLAYCSS" \
  'inset-block-end: var(--action-bar-h);' 'inset-block-end: 0;'

run_case "tv strip gutter removed"     "$WEB" 'TestTheBuiltStylesheetSpacesTheTvStripByTheTargetGap' "$TVCSS" \
  'gap: var(--target-gap);' 'gap: 0;'

echo
echo "cases that did not fail: $FAILURES"
echo "log: $LOG"
exit $(( FAILURES > 0 ))