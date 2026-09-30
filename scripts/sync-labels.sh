#!/usr/bin/env bash
#
# Reconcile .github/labels.yml against the labels that exist on the remote.
#
#   sync-labels.sh apply    create or update every label in the manifest
#   sync-labels.sh prune    delete repo labels that are NOT in the manifest
#   sync-labels.sh check    fail if the repo and the manifest disagree
#
# Never called by `make check`. It needs the network and a token, so it lives
# behind its own targets; `make labels-check` is what CI runs.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${REPO_ROOT}/.github/labels.yml"
REPO="${SEMIPLANE_REPO:-PopinjayJohn/semiplane}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v gh >/dev/null || die "gh is not installed (https://cli.github.com)"
command -v jq >/dev/null || die "jq is not installed"
command -v yq >/dev/null || die "yq is not installed (run: make tools)"
[ -f "$MANIFEST" ] || die "missing $MANIFEST"
[ -n "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ] || gh auth status >/dev/null 2>&1 \
  || die "gh is not authenticated (set GH_TOKEN, or run 'gh auth login')"

# The key is what we match on across all three commands. GitHub label names are
# case-insensitive but otherwise exact, so the key is a plain downcase — no
# space normalisation, which would make `area: wiki` and `area:wiki` look like
# two labels. The real name is always what we send to the API. The manifest is
# YAML, so yq converts it before jq shapes it.
manifest_json() {
  yq -o=json -I=0 '.' "$MANIFEST" \
    | jq -c '[ .[] | {name, color, description, key: (.name | ascii_downcase)} ]'
}

remote_json() {
  gh label list --repo "$REPO" --limit 500 --json name,color,description \
    | jq -c '[ .[] | {name, color, description, key: (.name | ascii_downcase)} ]'
}

key_of() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

cmd_apply() {
  local manifest remote created=0 updated=0 name color description
  manifest="$(manifest_json)"
  remote="$(remote_json)"

  while IFS=$'\t' read -r name color description; do
    if [ -z "$name" ]; then continue; fi
    if jq -e --arg n "$(key_of "$name")" 'any(.[]; .key == $n)' <<<"$remote" >/dev/null; then
      updated=$((updated + 1))
    else
      created=$((created + 1))
    fi
    gh label create "$name" --repo "$REPO" --color "$color" \
      --description "$description" --force >/dev/null \
      || die "failed to create or update label: $name"
  done < <(jq -r '.[] | [.name, .color, .description] | @tsv' <<<"$manifest")

  printf 'apply: %d created, %d updated on %s\n' "$created" "$updated" "$REPO"
}

cmd_prune() {
  local manifest remote extras deleted=0 key name
  manifest="$(manifest_json)"
  remote="$(remote_json)"

  extras="$(comm -23 \
    <(jq -r '.[].key' <<<"$remote" | sort) \
    <(jq -r '.[].key' <<<"$manifest" | sort))"
  [ -n "$extras" ] || { printf 'prune: no extra labels on %s\n' "$REPO"; return 0; }

  while IFS= read -r key; do
    [ -n "$key" ] || continue
    name="$(jq -r --arg k "$key" 'first(.[] | select(.key == $k) | .name) // ""' <<<"$remote")"
    [ -n "$name" ] || die "cannot resolve a repo label name for key: $key"
    gh label delete "$name" --repo "$REPO" --yes >/dev/null \
      || die "failed to delete label: $name"
    printf '  deleted: %s\n' "$name"
    deleted=$((deleted + 1))
  done <<<"$extras"

  printf 'prune: %d labels deleted from %s\n' "$deleted" "$REPO"
}

cmd_check() {
  local manifest remote drift extras
  manifest="$(manifest_json)"
  remote="$(remote_json)"

  drift="$(jq -nr --argjson m "$manifest" --argjson r "$remote" '
    [ $m[] as $l
      | ($r | map(select(.key == $l.key)) | .[0]) as $have
      | select($have == null
               or ($have.color | ascii_downcase) != ($l.color | ascii_downcase)
               or ($have.description // "") != ($l.description // ""))
      | "  \($l.name) — repo has: \(if $have == null
              then "absent"
              else "color=" + $have.color + ", description=\"" + $have.description + "\""
              end)" ]
    | .[]' 2>/dev/null || true)"

  if [ -n "$drift" ]; then
    printf 'label drift against %s:\n' "$MANIFEST" >&2
    printf '%s\n' "$drift" >&2
    printf '\nrun: make labels\n' >&2
    return 1
  fi

  extras="$(comm -23 \
    <(jq -r '.[].key' <<<"$remote" | sort) \
    <(jq -r '.[].key' <<<"$manifest" | sort))"
  if [ -n "$extras" ]; then
    printf 'note: labels on the repo but not in the manifest:\n' >&2
    sed 's/^/  /' <<<"$extras" >&2
    printf 'run: make labels-prune to remove them\n' >&2
  fi

  printf 'check: %d labels match %s\n' "$(jq 'length' <<<"$manifest")" "$MANIFEST"
}

case "${1:-}" in
  apply) cmd_apply ;;
  prune) cmd_prune ;;
  check) cmd_check ;;
  *)     die "usage: $(basename "$0") {apply|prune|check}" ;;
esac
