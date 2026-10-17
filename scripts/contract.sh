#!/usr/bin/env bash
# contract: boot the real binary on a random port and assert every
# openapi.yaml operation is served with its spec method (spec→live).
# NOTE: undocumented live routes are NOT yet enumerated/flagged — see
# "Phase 1 review follow-ups" in docs/implementation-plan.md.
#
# Probe rules (Phase 1/E owns this script): a route counts as SERVED when the
# response proves a handler ran — 2xx/3xx/4xx with a JSON, problem+json, or
# event-stream body. It counts as MISSED when the request falls through to
# the SPA fallback (HTML body on /api/v1/*), the mux rejects the method
# (405), or the connection fails (000). Rationale: handler 404s (unknown
# note, unknown page) are proof of service — template probes like
# /api/v1/notes/{path} hit real handlers, not placeholders.
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

# Every live path in openapi.yaml must be served with its spec methods.
# Paths marked `x-slice: planned` at the path-item level are Phase-1 stubs
# and are skipped until their owning stream serves them. Output lines are
# "METHOD /path" (one per operation).
mapfile -t SPEC < <(python3 -c '
import re
spec = []
cur = None
live = False
for line in open("openapi.yaml").read().splitlines():
    m = re.match(r"^  (/[^:]*):$", line)
    if m:
        cur = m.group(1)
        live = True
        continue
    if cur is not None and re.match(r"^    x-slice: planned\s*$", line):
        live = False
        continue
    m = re.match(r"^    (get|post|put|delete|patch):\s*$", line)
    if m and cur is not None and live:
        spec.append(m.group(1).upper() + " " + cur)
print("\n".join(spec))
')
for entry in ${SPEC[@]+"${SPEC[@]}"}; do
  method="${entry%% *}"
  p="${entry#* }"
  # Template probes hit real handlers: substitute a missing-note name so a
  # 404 problem+json (served) is distinguishable from a mux miss.
  url="http://127.0.0.1:$PORT$p"
  url="${url//\{path...\}/contract-probe-missing.md}"
  url="${url//\{path\}/contract-probe-missing.md}"
  if [ "$method" = GET ]; then
    # `|| true`: --max-time on the SSE stream ends curl with exit 28 after
    # headers arrive; the status line was already captured via -w.
    out=$(curl -s --max-time 5 -o /dev/null -w '%{http_code} %{content_type}' "$url" || true)
  else
    # Malformed body on purpose: every POST handler must 400 on undecodable
    # JSON *before* touching vault state, so contract probes stay
    # side-effect free (a `{}` notes probe would create a real note).
    out=$(curl -s --max-time 5 -X "$method" -H 'Content-Type: application/json' -d '}' -o /dev/null -w '%{http_code} %{content_type}' "$url" || true)
  fi
  code="${out%% *}"
  ctype="${out#* }"
  case "$code" in
    000) echo "contract: $method $p not served (no response)"; FAIL=1;;
    405) echo "contract: $method $p not served (405 method not allowed)"; FAIL=1;;
    404|503)
      case "$ctype" in
        application/problem+json*|application/json*|text/event-stream*) ;;
        *) echo "contract: $method $p not served ($code $ctype, want handler body)"; FAIL=1;;
      esac;;
    200|201|204|304|400|401|403|409|429)
      case "$ctype" in
        text/html*) echo "contract: $method $p swallowed by SPA fallback ($code $ctype)"; FAIL=1;;
      esac;;
    *) echo "contract: $method $p unexpected ($code $ctype)"; FAIL=1;;
  esac
done

[ "$FAIL" = 0 ] && echo "contract: ok (${#SPEC[@]} spec operations served)"
exit "$FAIL"
