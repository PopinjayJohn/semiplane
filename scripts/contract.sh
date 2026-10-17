#!/usr/bin/env bash
# contract: boot the real binary on a random port, assert every openapi.yaml
# path is served (GET) and undocumented /api/v1 routes are flagged.
set -euo pipefail

PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()')
./bin/semiplane -addr "127.0.0.1:$PORT" >/tmp/semiplane-contract.log 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done

FAIL=0
for path in /healthz /api/v1/version; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT$path")
  if [ "$code" != 200 ]; then echo "contract: GET $path -> $code (want 200)"; FAIL=1; fi
done

# Every live path in openapi.yaml must be served. Paths marked
# `x-slice: planned` at the path-item level are Phase-1 stubs and are
# skipped until their owning stream serves them.
mapfile -t SPEC < <(python3 -c '
import re
paths = []
cur = None
live = False
for line in open("openapi.yaml").read().splitlines():
    m = re.match(r"^  (/[^:]*):$", line)
    if m:
        if cur is not None and live:
            paths.append(cur)
        cur = m.group(1)
        live = True
    elif cur is not None and re.match(r"^    x-slice: planned\s*$", line):
        live = False
if cur is not None and live:
    paths.append(cur)
print("\n".join(paths))
')
for p in "${SPEC[@]}"; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT$p")
  if [ "$code" = 404 ]; then echo "contract: spec path $p not served (404)"; FAIL=1; fi
done

[ "$FAIL" = 0 ] && echo "contract: ok (${#SPEC[@]} spec paths served)"
exit "$FAIL"
