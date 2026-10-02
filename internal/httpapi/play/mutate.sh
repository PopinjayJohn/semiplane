#!/bin/sh
# Mutation check for the play route's tests.
#
# Each mutation is a one-line change to the product, and each must make at least one
# test fail. A mutation that changes nothing is a test wired to nothing — the exact
# failure AGENTS.md records for phase 5's gate drafts and for the a11y package that
# contributed no §10.2 audit while `make a11y` printed `ok`.
#
# Not committed: it is a development instrument, and a script in the repository is a
# script somebody has to keep working. The report quotes its output.

set -eu

cd "$(dirname "$0")/../../.."
export PATH=$PATH:/usr/local/go/bin

PKG=./internal/httpapi/play/
HANDLER=$PKG/play.go
LOOP=$PKG/loop.go

pass=0
fail=0
expected=0

restore() {
	git checkout -- "$HANDLER" "$LOOP" 2>/dev/null || true
}

# mutate_expect_miss NAME FILE FROM TO TEST
#
# A mutation recorded as *expected* not to fail, with the reason beside it. A script
# that only ever reports hits cannot be read as evidence, because a set of mutations
# that all fail proves the tests are sensitive and says nothing about the checks that
# were never attempted.
mutate_expect_miss() {
	name=$1 file=$2 from=$3 to=$4 test=$5 reason=${6:-}

	restore

	if ! grep -qF -- "$from" "$file"; then
		echo "SKIP  $name: the anchor is not in $file"
		fail=$((fail + 1))

		return
	fi

	python3 - "$file" "$from" "$to" <<-'PY'
	import pathlib, sys
	path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
	p = pathlib.Path(path)
	s = p.read_text()
	if old not in s:
	    sys.exit("anchor missing")
	p.write_text(s.replace(old, new, 1))
	PY

	if go test -count=1 -timeout 120s -run "$test" "$PKG" >/dev/null 2>&1; then
		echo "as-expected-miss  $name: $test still passed (net/http clears the "\
"deadline on hijack, so the call is not observable)"
		expected=$((expected + 1))
	else
		echo "UNEXPECTED-HIT  $name: $test failed; the note beside the mutation is wrong"
		fail=$((fail + 1))
	fi

	restore
}

# mutate_all NAME FILE FROM TO TEST
#
# Replaces **every** occurrence rather than the first, so a one-line anchor can express
# "remove all three call sites" — which is the mutation that matters for a contract
# stated over several places.
mutate_all() {
	name=$1 file=$2 from=$3 to=$4 test=$5

	restore

	if ! grep -qF -- "$from" "$file"; then
		echo "SKIP  $name: the anchor is not in $file"
		fail=$((fail + 1))

		return
	fi

	python3 - "$file" "$from" "$to" <<-'PY'
	import pathlib, sys
	path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
	p = pathlib.Path(path)
	s = p.read_text()
	if old not in s:
	    sys.exit("anchor missing")
	p.write_text(s.replace(old, new))
	PY

	if go test -count=1 -timeout 120s -run "$test" "$PKG" >/dev/null 2>&1; then
		echo "MISS  $name: $test still passed"
		fail=$((fail + 1))
	else
		echo "HIT   $name: $test failed as expected"
		pass=$((pass + 1))
	fi

	restore
}

# mutate_expect_miss_all NAME FILE FROM TO TEST
mutate_expect_miss_all() {
	name=$1 file=$2 from=$3 to=$4 test=$5

	restore

	if ! grep -qF -- "$from" "$file"; then
		echo "SKIP  $name: the anchor is not in $file"
		fail=$((fail + 1))

		return
	fi

	python3 - "$file" "$from" "$to" <<-'PY'
	import pathlib, sys
	path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
	p = pathlib.Path(path)
	s = p.read_text()
	if old not in s:
	    sys.exit("anchor missing")
	p.write_text(s.replace(old, new))
	PY

	if go test -count=1 -timeout 120s -run "$test" "$PKG" >/dev/null 2>&1; then
		echo "as-expected-miss  $name"
		expected=$((expected + 1))
	else
		echo "UNEXPECTED-HIT  $name: $test failed; the note beside the mutation is wrong"
		fail=$((fail + 1))
	fi

	restore
}

# mutate NAME FILE FROM TO TEST
mutate() {
	name=$1 file=$2 from=$3 to=$4 test=$5

	restore

	if ! grep -qF -- "$from" "$file"; then
		echo "SKIP  $name: the anchor is not in $file"
		fail=$((fail + 1))

		return
	fi

	python3 - "$file" "$from" "$to" <<-'PY'
	import pathlib, sys
	path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
	p = pathlib.Path(path)
	s = p.read_text()
	if old not in s:
	    sys.exit("anchor missing")
	p.write_text(s.replace(old, new, 1))
	PY

	if go test -count=1 -timeout 120s -run "$test" "$PKG" >/dev/null 2>&1; then
		echo "MISS  $name: $test still passed"
		fail=$((fail + 1))
	else
		echo "HIT   $name: $test failed as expected"
		pass=$((pass + 1))
	fi

	restore
}

# 1. The gate. `Mount` is the whole of the S-8 matrix on this route.
mutate "the play gate is dropped from Mount" \
	"$HANDLER" \
	'mux.Handle("GET /c/{slug}/play", campaigns.RequirePlay(handler))' \
	'mux.Handle("GET /c/{slug}/play", handler)' \
	'TestThePlayRouteAnswersTheS8Matrix'

# 2. The gate swapped for the read gate: a public wiki page would be a public table.
mutate "the play gate is swapped for the read gate" \
	"$HANDLER" \
	'campaigns.RequirePlay(handler)' \
	'campaigns.RequireRead(handler)' \
	'TestThePlayRouteAnswersTheS8Matrix'

# 3. Origin enforcement removed entirely: `CheckOrigin: true` in one line.
mutate "Origin is not enforced" \
	"$HANDLER" \
	'if !originAllowed(r) {' \
	'if false {' \
	'TestAnOriginFromAnotherSiteIsRefusedBeforeTheHandshake'

# 4. Origin enforcement made a bare false: anything *carrying* an Origin is refused,
#    which is the other way to get the attack wrong.
mutate "Origin becomes a bare false" \
	"$HANDLER" \
	'if !originAllowed(r) {' \
	'if len(r.Header.Get("Origin")) > 0 {' \
	'TestASameOriginHeaderIsAdmitted'

# 5. Only the "present" branch exists, so an absent Origin is refused.
mutate "the absent-Origin branch is inverted" \
	"$HANDLER" \
	'if origin == "" {
		// Branch one: no browser, therefore no cross-origin request, therefore
		// nothing to refuse. Stated as a decision so that a reader auditing this
		// function sees an answer rather than an absence of one.
		return true
	}' \
	'if origin == "" {
		return false
	}' \
	'TestTheAbsentOriginTakesTheOtherBranchAndIsAdmitted'

# 6. Same-origin compared against a constant rather than the request's own host.
mutate "same-origin is compared against a fixed host" \
	"$HANDLER" \
	'return strings.EqualFold(parsed.Host, r.Host)' \
	'return strings.EqualFold(parsed.Host, "semiplane.invalid")' \
	'TestASameOriginHeaderIsAdmitted'

# 7. The scheme list admits everything, so `ws://this-host` and `file://` are taken.
mutate "any scheme is accepted as an origin" \
	"$HANDLER" \
	'	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return false
	}' \
	'	_ = parsed' \
	'TestAnOriginThatIsNotThisInstancesPageIsRefused'

# 7b. The same-origin comparison dropped entirely.
mutate "the host is never compared" \
	"$HANDLER" \
	'	return strings.EqualFold(parsed.Host, r.Host)' \
	'	return true' \
	'TestAnOriginFromAnotherSiteIsRefusedBeforeTheHandshake'

# 8. Every advance removed, which is the whole contract.
mutate_all "Advance is never called" \
	"$LOOP" \
	'			peer.Advance()' \
	'			_ = peer' \
	'TestAPeerThatDrainedIsNotRetiredByTheSweep'

# 8b/8c. One call site at a time, both expected to miss. The contract is proven by #8
#      above, which removes all three; these two record that no single *other* call
#      site is separately observable, and the reason is the same for both:
#      `take`'s arm is reached only when a burst is already queued at the top of a loop
#      iteration, which needs a writer that has blocked on a peer's TCP window, and the
#      answers arm is a race between two paths the reader can wake.
mutate_expect_miss "Advance is not called on an answer" \
	"$LOOP" \
	'		case payload, open := <-answers:
			if !open {
				return closeCodeRetired, closeShutdown
			}

			peer.Advance()
' \
	'		case payload, open := <-answers:
			if !open {
				return closeCodeRetired, closeShutdown
			}
' \
	'TestAnAnswerWithNoBroadcastStillReachesTheClient' \
	'the answers arm is one of two woken paths, so removing its advance is not separately observable'

mutate_expect_miss "Advance is not called while draining a burst" \
	"$LOOP" \
	'			peer.Advance()

			if err := h.write(ctx, slug, writer, payload); err != nil {
				return false
			}' \
	'			if err := h.write(ctx, slug, writer, payload); err != nil {
				return false
			}' \
	'TestAPeerThatDrainedIsNotRetiredByTheSweep' \
	'take is reached only when a burst is queued at the top of a loop iteration, which needs a writer blocked on a peers TCP window'

# 10. The answers arm of the select is removed, so an answer is only ever taken by the
#     non-blocking drain. That is "there is effectively one queue for the select", and
#     the loop then blocks with the answer already queued.
mutate "the select has no answers arm" \
	"$LOOP" \
	'		select {
		case payload, open := <-answers:
			if !open {
				return closeCodeRetired, closeShutdown
			}

			peer.Advance()

			if err := h.write(ctx, slug, writer, payload); err != nil {
				return closeCodeTransport, closeReason(err)
			}

		case payload, open := <-broadcasts:' \
	'		select {
		case payload, open := <-broadcasts:' \
	'TestAnAnswerWithNoBroadcastStillReachesTheClient'

# 11. The frame's own campaign trusted: the codec's refusal removed.
mutate "a frame with a campaign field is tolerated" \
	"$LOOP" \
	'		if err != nil {
			// The class and not the text, and this is the most important log line on
			// the route: a codec error quotes the frame that broke it, and a socket
			// is a public endpoint (S-12.3). `errFrame` wraps it only so the writer
			// can tell a client fault from a transport fault; nothing else travels.
			exit <- readExit{err: fmt.Errorf("%w: %w", errFrame, err)}

			return
		}' \
	'		if err != nil {
			_ = err

			return
		}' \
	'TestTheCampaignAndTheActorComeFromTheConnectionNotTheFrame'

# 12. The close reason carries the codec's text rather than its class.
mutate "the close reason carries the codec error text" \
	"$LOOP" \
	'		return websocket.StatusPolicyViolation, "frame_" + realtime.FrameClass(err)' \
	'		return websocket.StatusPolicyViolation, "frame_" + err.Error()' \
	'TestAMalformedFrameClosesTheConnectionWithAClassAndNotTheBytes'

# 13. The peer's own close code is discarded.
mutate "the peer close code is not honoured" \
	"$LOOP" \
	'	if closed, isClose := errors.AsType[*websocket.CloseError](err); isClose {
		return closed.Code, closeNormal
	}' \
	'	if _, isClose := errors.AsType[*websocket.CloseError](err); isClose {
		return websocket.StatusNormalClosure, closeNormal
	}' \
	'TestCloseForNamesTheCauseRatherThanTheSymptom'

# 14. The transport's read limit replaced by the codec's own bound.
mutate "the read limit is the codec bound rather than the transport's" \
	"$HANDLER" \
	'	if h.ReadLimit <= 0 {
		return defaultReadLimit
	}' \
	'	if h.ReadLimit <= 0 {
		return realtime.MaxClientFrameBytes
	}' \
	'TestTheReadLimitIsFlooredAtTheCodecsOwnBound'

# 15. The refusals become cacheable.
mutate "a refusal is cacheable" \
	"$HANDLER" \
	'	header.Set("Cache-Control", "private, no-store")' \
	'	header.Set("Cache-Control", "public, max-age=60")' \
	'TestARefusalIsPrivateAndNoStore'

# 16. A missing hub is admitted as a working socket.
mutate "a missing hub is admitted" \
	"$HANDLER" \
	'	if h.Hub == nil {' \
	'	if false {' \
	'TestARouteWithNoHubRefusesRatherThanOpeningASocket'

# 17. The write deadline is not cleared. **Expected to MISS**, and the script says so:
#     `net/http` clears both deadlines itself when a connection is hijacked, so this
#     call is belt and braces and nothing on the wire can tell the two apart. Reported
#     rather than hidden.
mutate_expect_miss "the server write deadline is not cleared" \
	"$LOOP" \
	'	clearWriteDeadline(ctx, w)' \
	'	_ = w' \
	'TestALiveSocketOutlivesTheServersWriteTimeout' \
	'net/http clears both deadlines itself on a hijacked connection, so the route call is belt and braces'

# 18. The reader is never waited for, so a shutdown leaks it.
mutate "the reader goroutine is not joined" \
	"$LOOP" \
	'	<-readerDone' \
	'	_ = readerDone' \
	'TestAShutdownWaitsForAReaderThatIsInsideAResolver'

# 19. The frame rate is never counted, so a client may send without limit.
mutate "the frame rate is not counted" \
	"$LOOP" \
	'		if !meter.allow(nowFunc()) {' \
	'		if false {' \
	'TestAClientThatOutrunsTheFrameRateIsClosedWithAPolicyViolation'

# 19b. The rate trip is misdiagnosed as a codec refusal, so a client is told it sent
#      bad bytes when it sent too many. Both are 1008, which is why the socket test's
#      status assertion cannot see it and the close-code table can.
mutate "a frame-rate trip is reported as a codec refusal" \
	"$LOOP" \
	'			exit <- readExit{err: errFrameRate}' \
	'			exit <- readExit{err: errFrame}' \
	'TestAClientThatOutrunsTheFrameRateIsClosedWithAPolicyViolation'

# 20. The meter's window comparison is off by one boundary.
mutate "the meter window comparison is exclusive" \
	"$LOOP" \
	'	if m.since.IsZero() || now.Sub(m.since) >= m.window {' \
	'	if m.since.IsZero() || now.Sub(m.since) > m.window {' \
	'TestTheFrameMeterAllowsExactlyItsLimitPerWindow'

# 21. The meter's budget is not reset per window, so a client is retired twice.
mutate "the meter budget is not reset per window" \
	"$LOOP" \
	'		m.since = now
		m.count = 0' \
	'		m.since = now' \
	'TestTheFrameMeterAllowsExactlyItsLimitPerWindow'

# 22. `hello` and `presence` are answered by closing the connection.
mutate "a hello is treated as a fatal error" \
	"$LOOP" \
	'	intentFrame, isIntent := frame.(*realtime.ClientIntent)
	if !isIntent {
		return nil
	}' \
	'	intentFrame, isIntent := frame.(*realtime.ClientIntent)
	if !isIntent {
		return wrapf("unsupported frame")
	}' \
	'TestHelloAndPresenceAreRoutedWithoutEndingTheConnection'

# 23. The read bound is not armed, so a peer that stops talking is never released.
mutate "the read bound is not armed" \
	"$LOOP" \
	'		readCtx, cancel := context.WithTimeout(ctx, h.readTimeout())' \
	'		readCtx, cancel := context.WithCancel(ctx)' \
	'TestAHubShutdownClosesALiveSocketAndLeaksNoGoroutine'

# 24. The socket is not closed, so the client is never told anything.
mutate "the socket is never closed" \
	"$LOOP" \
	'	if err := writer.Close(code, reason); err != nil {' \
	'	if err := error(nil); err != nil {' \
	'TestAMalformedFrameClosesTheConnectionWithAClassAndNotTheBytes'

echo
echo "hits: $pass   misses: $fail   expected-misses: $expected"
[ "$fail" -eq 0 ]
