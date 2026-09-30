#!/usr/bin/env bash
# Install the pinned Tailwind CSS standalone binary into .toolbin/.
#
# Two integrity checks, in this order, and both are load-bearing:
#
#   1. The release manifest's SHA256 is compared against TAILWIND_MANIFEST_SHA256
#      from the Makefile. This is the single committed pin, and it is what makes
#      the per-platform digests trustworthy -- they come from a manifest whose
#      own hash is committed here.
#   2. The downloaded binary is compared against its line in that manifest.
#
# Checking only the binary would mean committing four digests that nobody
# re-verifies against upstream; checking only the manifest would mean trusting
# whatever the manifest says about the binary it describes. Doing only one of
# them is the omission this script exists to prevent.
#
# The binary is ~110MB and is never committed. `.toolbin/` is gitignored, exactly
# as `GOBIN` is in ci.yml.

set -euo pipefail

readonly TOOLBIN="${TOOLBIN:-.toolbin}"
readonly TAILWIND_BIN="${TOOLBIN}/tailwindcss"
readonly REPO="tailwindlabs/tailwindcss"

: "${TAILWIND_VERSION:?TAILWIND_VERSION must be set}"
: "${TAILWIND_MANIFEST_SHA256:?TAILWIND_MANIFEST_SHA256 must be set}"

# The release tag carries the `v` -- `download/v4.3.3/...` resolves and
# `download/4.3.3/...` does not -- and every pin in the Makefile already has it.
# Normalising rather than assuming means neither form in the Makefile can
# produce a URL that 404s on a prefix nobody thought about.
case "${TAILWIND_VERSION}" in
v*) readonly TAG="${TAILWIND_VERSION}" ;;
*) readonly TAG="v${TAILWIND_VERSION}" ;;
esac
# Scratch directory, read by the EXIT trap below. Declared here because the
# trap runs after main's frame is gone, and `set -u` would otherwise turn a
# cleanup that fails into an unbound-variable error masking the real failure.
TMP=""

log() { printf '    %s\n' "$*" >&2; }
die() { printf '==> %s\n' "$*" >&2; exit 1; }

# Map `uname` output onto the asset name the release publishes. Tailwind names
# the Apple builds `macos-*`, not `darwin-*`, so the translation is not a rename
# of the kernel's own string.
asset_name() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"

  case "${arch}" in
    x86_64 | amd64) arch="x64" ;;
    aarch64 | arm64) arch="arm64" ;;
    *) die "unsupported architecture: ${arch}" ;;
  esac

  case "${os}" in
    Linux) printf 'tailwindcss-linux-%s' "${arch}" ;;
    Darwin) printf 'tailwindcss-macos-%s' "${arch}" ;;
    MINGW* | MSYS* | CYGWIN*) printf 'tailwindcss-windows-%s.exe' "${arch}" ;;
    *) die "unsupported operating system: ${os}" ;;
  esac
}

verify_manifest() {
  local manifest="$1"
  local actual
  actual="$(sha256sum "${manifest}" | cut -d' ' -f1)"

  if [ "${actual}" != "${TAILWIND_MANIFEST_SHA256}" ]; then
    die "Tailwind ${TAILWIND_VERSION} manifest digest mismatch
    expected ${TAILWIND_MANIFEST_SHA256}
    actual   ${actual}
    The pin lives in the Makefile as TAILWIND_MANIFEST_SHA256. If upstream
    legitimately re-released the tag, update the pin there in the same commit
    that changes TAILWIND_VERSION."
  fi
}

verify_binary() {
  local binary="$1" manifest="$2" asset="$3"
  local expected actual
  # The manifest lines are `./name  sha256`, in that order.
  expected="$(awk -v want="./${asset}" '$2 == want { print $1 }' "${manifest}")"

  if [ -z "${expected}" ]; then
    die "${asset} is not listed in the ${TAILWIND_VERSION} manifest"
  fi

  actual="$(sha256sum "${binary}" | cut -d' ' -f1)"

  if [ "${actual}" != "${expected}" ]; then
    die "${asset} digest mismatch
    expected ${expected}
    actual   ${actual}"
  fi
}

main() {
  local asset
  asset="$(asset_name)"
  # Not `local`: the EXIT trap below runs after main's frame is gone, and
  # `set -u` turns a trap that reads a dead local into an unbound-variable
  # failure that masks the real error.
  TMP="$(mktemp -d)"
  trap 'rm -rf "${TMP}"' EXIT

  if [ -x "${TAILWIND_BIN}" ] && "${TAILWIND_BIN}" --help >/dev/null 2>&1; then
    log "tailwindcss already present at ${TAILWIND_BIN}"
    return 0
  fi

  log "fetching the ${REPO} ${TAILWIND_VERSION} release manifest"
  curl --fail --silent --show-error --location --max-time 120 \
    --proto '=https' --tlsv1.2 \
    --output "${TMP}/sha256sums.txt" \
    "https://github.com/${REPO}/releases/download/${TAG}/sha256sums.txt"
  verify_manifest "${TMP}/sha256sums.txt"

  log "fetching ${asset} (about 110MB)"
  curl --fail --silent --show-error --location --max-time 600 \
    --proto '=https' --tlsv1.2 \
    --output "${TMP}/${asset}" \
    "https://github.com/${REPO}/releases/download/${TAG}/${asset}"
  verify_binary "${TMP}/${asset}" "${TMP}/sha256sums.txt" "${asset}"

  mkdir -p "${TOOLBIN}"
  install -m 0755 "${TMP}/${asset}" "${TAILWIND_BIN}"
  log "installed ${TAILWIND_BIN}"
}

main "$@"
