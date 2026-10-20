#!/usr/bin/env bash
# e2e/scripts/run.sh [project] — build is done by the Makefile caller.
# Starts the binary per worker port, seeds a scratch vault (users + LAN
# codes + tokens), health-waits, runs Playwright, always tears down.
set -euo pipefail

PROJECT="${1:-smoke}"
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()')
SCRATCH=$(mktemp -d /tmp/semiplane-e2e-XXXXXX)
cp -r testdata/vaults/e2e/. "$SCRATCH/"

# Seed the auth surface into the scratch index.db before the binary boots:
# gm / player1 / player2 with fixed passwords, a known LAN code, and a
# bearer token with rules:read scope (for the token-scope rejection test).
# auth.Open is idempotent, so the server's later open on the same DB is a
# no-op for the schema.
mkdir -p "$SCRATCH/.semiplane"
go run ./e2e/scripts/seedusers --db "$SCRATCH/.semiplane/index.db" \
  --gm-pass gm-secret --p1-pass p1-secret --p2-pass p2-secret \
  >/tmp/semiplane-seedusers.log 2>&1

# Populate the derived FTS5 + backlink index from the seed vault so search,
# graph, and backlinks return deterministic results. Reindex is idempotent
# and safe (index is derived; vault files are canonical).
./bin/semiplane reindex -vault "$SCRATCH" >/tmp/semiplane-reindex.log 2>&1

# Deterministic dice for E2E exact assertions (SEMIPLANE_TEST_SEED).
export SEMIPLANE_TEST_SEED="${SEMIPLANE_TEST_SEED:-7357}"

./bin/semiplane -addr "127.0.0.1:$PORT" -vault "$SCRATCH" >/tmp/semiplane-e2e.log 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null || true; rm -rf "$SCRATCH"' EXIT

for _ in $(seq 1 50); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done

if [ ! -d e2e/node_modules ]; then (cd e2e && pnpm install); fi
if sudo -n true 2>/dev/null; then
  (cd e2e && npx playwright install --with-deps chromium)
else
  # No sudo (local sandbox): browser binaries + repo-local OS libs.
  # CI always has sudo and installs OS deps via --with-deps.
  echo "e2e: no sudo — installing browser binaries + repo-local OS libs"
  (cd e2e && npx playwright install chromium)
  ./e2e/scripts/install-deps-nosudo.sh
  # Absolute path: the test step below cds into e2e/, and a relative
  # LD_LIBRARY_PATH would then resolve against the wrong directory.
  LIBSUB=$(find "$(pwd)/e2e/.browser-libs/root" -name 'libnspr4.so' -printf '%h' -quit 2>/dev/null || true)
  if [ -n "$LIBSUB" ]; then
    export LD_LIBRARY_PATH="$LIBSUB${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
  fi
  # Repo-local fonts: without them headless Chromium renders ALL text blank
  # (screenshots + axe scans are unusable). Absolute (see above).
  FONTCONF="$(pwd)/e2e/.browser-libs/fonts.conf"
  if [ -f "$FONTCONF" ]; then
    export FONTCONFIG_FILE="$FONTCONF"
  fi
fi
(cd e2e && BASE_URL="http://127.0.0.1:$PORT" npx playwright test --project="$PROJECT")
