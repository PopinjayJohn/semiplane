#!/usr/bin/env bash
# e2e/scripts/run.sh [project] — build is done by the Makefile caller.
# Starts the binary per worker port, seeds a scratch vault, health-waits,
# runs Playwright, always tears down.
set -euo pipefail

PROJECT="${1:-smoke}"
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()')
SCRATCH=$(mktemp -d /tmp/semiplane-e2e-XXXXXX)
cp -r testdata/vaults/e2e/. "$SCRATCH/"

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
  # No sudo (local sandbox): browser binaries only. CI always has sudo and
  # installs OS deps via --with-deps.
  echo "e2e: no sudo — installing browser binaries without OS deps"
  (cd e2e && npx playwright install chromium)
fi
(cd e2e && BASE_URL="http://127.0.0.1:$PORT" npx playwright test --project="$PROJECT")
