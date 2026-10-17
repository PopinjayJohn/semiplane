#!/usr/bin/env bash
# install-deps-nosudo.sh — fetch Chromium OS libs WITHOUT root.
#
# `npx playwright install --with-deps` needs sudo (apt-get install). On machines
# without it (sandboxes, locked-down laptops), apt can still DOWNLOAD .debs and
# dpkg-deb can extract them to a repo-local dir that the test runner puts on
# LD_LIBRARY_PATH. No root, no system mutation, fully idempotent.
#
# Headless Chromium needs shared libs only (no X server, no fonts for assertions).
set -euo pipefail

LIBS_DIR="${1:-$(dirname "$0")/../.browser-libs}"
mkdir -p "$LIBS_DIR/dl"
# Absolute from here on: the script cds into dl/ below, and a relative
# LIBS_DIR would then resolve against the wrong directory (dpkg-deb fails).
LIBS_DIR="$(cd "$LIBS_DIR" && pwd)"
MARKER="$LIBS_DIR/.complete"

# Package per missing .so; alternates cover pre-t64 distros (Ubuntu ≤22.04).
# Lines: "<primary> [fallback...]".
PACKAGES="
libnss3
libnspr4
libatk1.0-0t64 libatk1.0-0
libatk-bridge2.0-0t64 libatk-bridge2.0-0
libcups2t64 libcups2
libdrm2
libxkbcommon0
libxcomposite1
libxdamage1
libxfixes3
libxrandr2
libgbm1
libpango-1.0-0
libcairo2
libasound2t64 libasound2
libatspi2.0-0t64 libatspi2.0-0
libx11-6
libxcb1
libxext6
libxrender1
libxau6
libxdmcp6
libxi6
libxtst6
libxcb-render0
libxcb-shm0
libavahi-client3
libavahi-common3
libfontconfig1
libfreetype6
libfribidi0
libharfbuzz0b
libpixman-1-0
libpng16-16t64 libpng16-16
libthai0
libdatrie1
libgraphite2-3
"

missing_bin() {
  # Echoes browser binaries (headless shell + full chromium) missing .so files.
  # Checks against the ambient LD_LIBRARY_PATH plus $1 (an extra lib dir).
  local bin missing=0 extra="${1:-}"
  local libpath="${extra}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
  for bin in \
    "$HOME/.cache/ms-playwright"/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell \
    "$HOME/.cache/ms-playwright"/chromium-*/chrome-linux64/chrome; do
    [ -x "$bin" ] || continue
    if LD_LIBRARY_PATH="$libpath" ldd "$bin" 2>/dev/null | grep -q "not found"; then
      echo "$bin"
      missing=1
    fi
  done
  return "$missing"
}

lib_subdir() {
  # Multiarch lib dir under $LIBS_DIR/root (Debian/Ubuntu: .../x86_64-linux-gnu).
  find "$LIBS_DIR/root" -name 'libnspr4.so' -printf '%h' -quit 2>/dev/null || true
}

if [ -f "$MARKER" ] && [ -z "$(missing_bin "$(lib_subdir)")" ]; then
  echo "browser-libs: complete, skipping"
  exit 0
fi

cd "$LIBS_DIR/dl"

echo "$PACKAGES" | grep -v '^$' | while read -r line; do
  # shellcheck disable=SC2086
  set -- $line
  for pkg in "$@"; do
    if apt-get download "$pkg" >/dev/null 2>&1; then
      echo "browser-libs: fetched $pkg"
      break
    fi
  done
done

for deb in *.deb; do
  [ -e "$deb" ] || continue
  dpkg-deb -x "$deb" "$LIBS_DIR/root"
done

STILL="$(missing_bin "$(lib_subdir)" || true)"
if [ -n "$STILL" ]; then
  echo "browser-libs: STILL missing libs for:" >&2
  echo "$STILL" >&2
  echo "authoritative list: npx playwright install-deps --dry-run chromium" >&2
  exit 1
fi

touch "$MARKER"
echo "browser-libs: complete in $LIBS_DIR/root"
