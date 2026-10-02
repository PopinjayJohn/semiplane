#!/usr/bin/env bash
# Mutation checks for internal/realtime/hub.go.
#
# Every assertion in hub_test.go was checked by changing the line it holds,
# confirming the test fails, and restoring. `AGENTS.md` records three of phase 5's
# first drafts of a gate that could not fail, and three more in phase 6, and two
# real bugs were caught by exactly this exercise while hub.go was being written —
# the staleness clock armed in the wrong place, and a room that outlived its last
# peer. A green gate here means nothing on its own.
#
# **Every mutation here compiles.** A mutation that breaks the build is not
# evidence: the tests "fail" because the package is broken, which every test in the
# package would do, and the check would be reporting the compiler rather than the
# assertion. The `NO-BUILD` verdict exists to keep that honest, and four of
# protocol.go's first draft's mutations produced it.
#
# Mutations are `python3` string replacements rather than `sed` expressions,
# because almost every claim in hub.go spans more than one line and a `sed` range
# that does not match exactly either is a silent no-op — which this script would
# report as `NO-OP`, honestly, but forty times.
#
# Usage: internal/realtime/mutate-hub.sh
set -uo pipefail

export PATH=$PATH:/usr/local/go/bin:/root/go/bin

cd "$(dirname "$0")/../.."

SRC=internal/realtime/hub.go
BACKUP=$(mktemp)
cp "$SRC" "$BACKUP"
trap 'cp "$BACKUP" "$SRC"; rm -f "$BACKUP"' EXIT

PASS=0
FAIL=0
LOG=$(mktemp)

# mutate <label> <test-pattern> <old> <new>
#
# `old` must appear exactly once in hub.go; a replacement that matched nothing or
# matched twice is reported rather than run, because "the mutation did not apply"
# and "the mutation was applied" have to be distinguishable or this file measures
# nothing.
# expect <label> <test-pattern> <old> <new> — a mutation this file runs, records, and
# does not count as a failure, with the reason written beside it.
#
# **There are three, and each one is a property with no external observation.** They
# are recorded rather than hidden because a survivor in a mutation table that nobody
# reads is indistinguishable from a bug in the table, and a mutation script that
# claims full coverage it does not have is the gate-wired-to-nothing failure
# `AGENTS.md` warns about — in the one place a green result would otherwise be
# evidence.
#
#   1. `Close` waiting for its sweeper is a happens-before relation between the call
#      returning and a goroutine's last instruction. A goroutine count cannot
#      distinguish "still running" from "not yet scheduled"; an exported channel
#      would be API added for a test.
#   2. The `Get`-before-`Open` discipline has **no observable cost** with the real
#      registry: a refused `Open` returns from `reserve` before it reads or writes
#      anything, so removing the fast path changes a mutex acquisition and a map
#      lookup per join. The `ErrStateOpen` recovery is what makes the slow path
#      *correct*, which is why `TestTheJoinDoesNotOpenASecondAuthority` is the test
#      that matters and this one is not.
#   3. The hub-level `closeOnce` is a second barrier behind each peer's own
#      `closedOnce`. Removing the outer one is invisible because a second pass finds
#      the rooms already empty and `detach` is never reached; removing the inner one
#      is visible and is mutated below. Two guards, one observable failure mode, and
#      the redundant one cannot be held to a test without instrumenting the hub.
EXPECTED=0

expect() {
  local label=$1 pattern=$2 old=$3 new=$4

  mutate "$label" "$pattern" "$old" "$new" || true

  if [ "$LAST" = SURVIVED ]; then
    echo "EXPECTED    $label  <- survives, and has no external observation: see the note"
    EXPECTED=$((EXPECTED + 1))
  fi
}

mutate() {
  local label=$1 pattern=$2 old=$3 new=$4
  LAST=""

  cp "$BACKUP" "$SRC"

  OLD="$old" NEW="$new" python3 - "$SRC" <<-'PY' || { echo "NO-OP       $label (the pattern does not match hub.go)"; FAIL=$((FAIL + 1)); cp "$BACKUP" "$SRC"; return; }
	import os, sys

	path = sys.argv[1]
	old, new = os.environ["OLD"], os.environ["NEW"]
	body = open(path).read()

	if body.count(old) != 1:
	    sys.stderr.write("pattern matched %d times\n" % body.count(old))
	    sys.exit(1)

	open(path, "w").write(body.replace(old, new))
	PY

  if cmp -s "$SRC" "$BACKUP"; then
    echo "NO-OP       $label (sed changed nothing — the mutation does not apply)"
    FAIL=$((FAIL + 1))
    return
  fi

  if ! go vet ./internal/realtime/ >"$LOG" 2>&1; then
    echo "NO-BUILD    $label: $(grep -m1 'hub.*:' "$LOG")"
    FAIL=$((FAIL + 1))
    cp "$BACKUP" "$SRC"
    return
  fi

  if go test -count=1 -timeout 120s -run "$pattern" ./internal/realtime/ >"$LOG" 2>&1; then
    LAST=SURVIVED
    echo "SURVIVED    $label"
    FAIL=$((FAIL + 1))
  else
    LAST=KILLED
    echo "KILLED      $label  <- $(grep -m1 -- '--- FAIL' "$LOG" | sed 's/^ *//')"
    PASS=$((PASS + 1))
  fi

  cp "$BACKUP" "$SRC"
}

echo "== a publish reaches every joined peer and only those =="

# Fan-out to the wrong campaign: the room lookup answered with another campaign's
# peers, which is the "existence oracle" shape S-8 exists to prevent.
mutate "the room lookup ignores the campaign" \
  'TestAPublishReachesEveryJoinedPeerAndOnlyThose' \
  'group, open := h.rooms[campaignID]' \
  'group, open := h.rooms[0]'

# Fan-out to nobody.
mutate "the fan-out loop removed" \
  'TestAPublishReachesEveryJoinedPeerAndOnlyThose' \
  '	for _, peer := range group.peers {
		h.offer(peer, payload)
	}' \
  '	_, _ = group, payload'

# Fan-out to every campaign, so the campaign filter does not exist.
mutate "the fan-out walks every room" \
  'TestAPublishReachesEveryJoinedPeerAndOnlyThose' \
  '	group, open := h.rooms[campaignID]' \
  '	var group *room
	var open bool
	for _, candidate := range h.rooms {
		group, open = candidate, true
	}'

# A new connection handed the room's history, so "since" has an answer nobody sent.
mutate "a join leaves the peer's queue holding the room's history" \
  'TestAPeerThatJoinsAfterAPublishDoesNotReceiveIt' \
  '	group.peers[peer.id] = peer

	return peer, nil' \
  '	group.peers[peer.id] = peer

	backlog := []byte(`{"t":"clock"}`)
	for _, queued := range group.peers {
		select {
		case queued.broadcasts <- backlog:
		default:
		}
	}

	return peer, nil'

echo
echo "== a slow peer is superseded, not queued, and never waited for =="

# Block instead of replacing: the whole point of the non-blocking send, and the
# failure is a browser tab between a dice roll and its answer.
mutate "the broadcast send blocks" \
  'TestASlowPeerIsSupersededRatherThanQueued' \
  '	select {
	case peer.broadcasts <- payload:
		peer.delivered++
		peer.behindSince = time.Time{}

		return

	default:
	}' \
  '	peer.broadcasts <- payload
	peer.delivered++'

# Never replace: drop the newest instead. Same frame count, wrong frame — which is
# the whole reason the assertion compares the *contents* and not just the count.
mutate "a full slot drops the new broadcast instead of replacing it" \
  'TestASlowPeerIsSupersededRatherThanQueued' \
  '	select {
	case <-peer.broadcasts:
		peer.superseded++
		h.superseded.Add(1)

	default:
	}

	select {
	case peer.broadcasts <- payload:
		peer.delivered++

	default:
	}' \
  '	peer.superseded++
	h.superseded.Add(1)'

# Never replace: keep the oldest, so a peer that reads once holds a stale frame.
mutate "a full slot drops the old broadcast instead of the new one" \
  'TestASlowPeerIsSupersededRatherThanQueued' \
  '	select {
	case <-peer.broadcasts:
		peer.superseded++
		h.superseded.Add(1)

	default:
	}

	select {
	case peer.broadcasts <- payload:
		peer.delivered++

	default:
	}' \
  '	select {
	case <-peer.broadcasts:
		peer.superseded++
		h.superseded.Add(1)

	default:
	}'

# A deep buffer, so a burst is queued rather than superseded.
mutate "the broadcast slot is deeper than one" \
  'TestASlowPeerIsSupersededRatherThanQueued|TestABroadcastSlotIsOneDeep' \
  'make(chan []byte, broadcastBuffer)' \
  'make(chan []byte, 64)'

# Superseding counted as delivering, so the ratio an operator reads is wrong.
mutate "a superseded broadcast counted as delivered" \
  'TestASlowPeerIsSupersededRatherThanQueued' \
  '	case <-peer.broadcasts:
		peer.superseded++
		h.superseded.Add(1)' \
  '	case <-peer.broadcasts:
		peer.delivered++
		h.superseded.Add(1)'

echo
echo "== a peer that stops reading goes stale =="

# **The bug this file was written against.** Arming the clock after the
# replacement rather than on arrival-full means every call ends looking healthy, so
# the sweep would never have retired anyone and the leak would be unbounded.
mutate "the staleness clock armed after the replacement" \
  'TestAPeerThatStopsReadingGoesStale' \
  '	if peer.behindSince.IsZero() {
		peer.behindSince = h.clock.Now()
	}' \
  '	if false {
		peer.behindSince = h.clock.Now()
	}'

# The clock never armed at all: the same failure by a shorter route.
mutate "the staleness clock never armed" \
  'TestAPeerThatStopsReadingGoesStale' \
  '	if peer.behindSince.IsZero() {
		peer.behindSince = h.clock.Now()
	}' \
  ''

# The sweep fires once and the goroutine returns, so the hub watches one moment.
mutate "the sweeper sweeps once and returns" \
  'TestAPeerThatStopsReadingGoesStale' \
  '		h.sweep(h.clock.Now())' \
  '		h.sweep(h.clock.Now())
		return'

# The window comparison inverted, so a peer *inside* the window is retired.
mutate "the window comparison inverted" \
  'TestAPeerInsideTheWindowIsNotRetired|TestAPeerThatStopsReadingGoesStale' \
  'return cutoff.Sub(p.behindSince) >= window' \
  'return cutoff.Sub(p.behindSince) <= window'

# `Advance` clears nothing, so a healthy peer is retired too — a table closed
# mid-game, which is worse than a leaked socket because it is visible.
mutate "Advance does not clear the staleness clock" \
  'TestAPeerThatStopsReadingGoesStale' \
  'func (p *Peer) Advance() {
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()

	p.behindSince = time.Time{}
}' \
  'func (p *Peer) Advance() {
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()
}'

# The sweep retires every peer it looks at.
mutate "the sweep retires every peer" \
  'TestAPeerThatStopsReadingGoesStale' \
  '			if !peer.behind(now, h.staleWindow()) {' \
  '			if false {'

# The sweep retires nobody: the leak this exists to prevent, unchanged.
mutate "the sweep retires nobody" \
  'TestAPeerThatStopsReadingGoesStale' \
  '			if !peer.behind(now, h.staleWindow()) {' \
  '			if true {'

# The wait polls and then refuses without ever yielding, so a loser that arrives
# before the winner has seeded is turned away. `Registry.Get` reports a *reservation*
# as absent, which is the whole reason the wait exists.
mutate "the authority wait refuses without yielding" \
  'TestAJoinThatRacesTheWinnersLoadWaitsForIt|TestConcurrentJoinsToOneCampaignAllSucceed' \
  '		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: campaign %d is reserved but not readable: %w",
				ErrStateOpen, campaignID, ctx.Err())

		case <-time.After(authorityPoll):
		}' \
  '		return fmt.Errorf("%w: campaign %d is reserved but not readable",
			ErrStateOpen, campaignID)'

echo
echo "== a room must not outlive its last peer =="

# The other bug the tests caught: a hub that had seen N campaigns held N empty
# rooms for the life of the process and nothing reclaimed them.
mutate "an empty room is left in the map" \
  'TestTheHubDoesNotGrowARoomPerPublish' \
  '		if len(group.peers) == 0 {
			delete(h.rooms, peer.campaignID)
		}' \
  ''

# A publish creates a room for a campaign nobody is watching, so the hub's own
# memory is a function of how many campaigns a process has ever seen.
mutate "a publish creates a room" \
  'TestTheHubDoesNotGrowARoomPerPublish' \
  '	group, open := h.rooms[campaignID]
	if !open {
		return nil
	}' \
  '	group, open := h.rooms[campaignID]
	if !open {
		h.rooms[campaignID] = &room{campaign: campaignID, peers: make(map[int64]*Peer)}
		return nil
	}'

echo
echo "== join, leave, and the single authority =="

# Leave closes the queues on every call, so a route's deferred leave on the error
# path panics on the normal path's close.
mutate "the peer close guard removed" \
  'TestJoinAndLeaveAreIdempotent|TestShutdownWithLivePeersIsCleanAndBounded' \
  '	peer.closedOnce.Do(func() {
		// Set before the closes rather than after, so a concurrent `Send` sees a
		// closed peer rather than a half-closed one. Both happen under the lock, so
		// the window the flag would close does not exist — the flag is here so the
		// invariant is stated where the closes are.
		peer.closedState = true

		close(peer.broadcasts)
		close(peer.answers)
	})' \
  '	peer.closedState = true
	close(peer.broadcasts)
	close(peer.answers)'

# `Get` ignored, so every join calls `Open` and the second is refused — which is
# `state.go`'s deliberate answer arriving in the wrong place.
expect "every join calls Open" \
  'TestAJoinOpensTheStateExactlyOnce|TestTheJoinDoesNotOpenASecondAuthority' \
  '	if _, live := h.states.Get(campaignID); live {
		return nil
	}' \
  '	if false {
		return nil
	}'

# `Open` refused but `Get` not consulted, so a join racing another one fails for a
# campaign that is working perfectly.
mutate "a lost open race is refused instead of waited for" \
  'TestConcurrentJoinsToOneCampaignAllSucceed|TestTheJoinDoesNotOpenASecondAuthority' \
  '	return h.awaitAuthority(ctx, campaignID)' \
  '	return fmt.Errorf("%w: campaign %d", ErrStateOpen, campaignID)'

# The capacity check dropped.
mutate "the per-campaign connection limit dropped" \
  'TestACampaignRefusesMorePeersThanItsLimit' \
  '	if len(group.peers) >= MaxPeersPerCampaign {' \
  '	if false {'

# Input validation dropped, so a negative campaign reaches the registry.
mutate "a non-campaign id accepted" \
  'TestAJoinIsRefusedForAnythingThatIsNotACampaignOrViewer' \
  '	if campaignID <= 0 {' \
  '	if false {'

# Role validation dropped, so a role this build cannot name is admitted and later
# refused by `Encode` on the way out.
mutate "an unknown role accepted" \
  'TestAJoinIsRefusedForAnythingThatIsNotACampaignOrViewer' \
  '	if !who.Role.Valid() {' \
  '	if false {'

echo
echo "== shutdown is clean and bounded =="

# The hub does not wait for its own goroutine, so a shutdown can report itself
# finished while a sweep is still walking a room.
expect "the shutdown does not wait for the sweeper" \
  'TestTheShutdownLeavesNoGoroutineBehind' \
  '		<-h.sweeperDone' \
  ''

# The peers are not closed, so every stream outlives the process — a handler blocked
# on a channel nobody will ever write to.
mutate "the shutdown closes no peers" \
  'TestShutdownWithLivePeersIsCleanAndBounded|TestAJoinAfterTheShutdownIsRefused' \
  '		for id, peer := range group.peers {
				h.detach(peer)
				delete(group.peers, id)
			}' \
  '		for id := range group.peers {
			delete(group.peers, id)
		}'

# The registry is not flushed, so a table loses its last debounced state.
mutate "the shutdown does not flush the registry" \
  'TestTheShutdownFlushesTheStateRegistry' \
  '	return h.states.Close(ctx)' \
  '	return nil'

# A join after the shutdown is admitted, and the client is reported healthy while
# receiving nothing.
mutate "a join after the shutdown is admitted" \
  'TestAJoinAfterTheShutdownIsRefused' \
  '	if h.isClosed() {
		return nil, ErrHubClosed
	}' \
  ''

# The teardown runs twice on the second Close, double-closing a channel.
# The teardown runs on every call, so a route's `defer hub.Close` after a hub
# shutdown runs the whole teardown a second time. Invisible today because each peer's
# own `closeOnce` absorbs it and the rooms are already empty — see the note above.
expect "the shutdown is not guarded against a second call" \
  'TestShutdownWithLivePeersIsCleanAndBounded' \
  '	h.closeOnce.Do(func() {' \
  '	h.closeOnce = sync.Once{}
	h.closeOnce.Do(func() {'

echo
echo "== the roll comes from the resolver =="

# **The claim itself.** The applied frame's payload would come from the client's
# frame rather than from the resolver, which is S-7.3's exact prohibition.
mutate "a client-supplied applied frame is sent before the resolver's" \
  'TestTheRollComesFromTheResolver' \
  '	resolution, err := h.resolve.Resolve(ctx, Intent{' \
  '	peer.Send(&ServerApplied{
		Type:      TypeApplied,
		Seq:       intent.Seq,
		Placement: intent.Args.Placement,
		Op:        intent.Op,
		Args:      []byte(`{}`),
		By:        peer.who.ID,
	})

	resolution, err := h.resolve.Resolve(ctx, Intent{'

# The campaign taken from anywhere but the peer.
mutate "the campaign not taken from the peer's own binding" \
  'TestTheRollComesFromTheResolver' \
  '		Campaign: peer.campaignID,' \
  '		Campaign: 1,'

# The actor taken from the frame rather than the connection.
mutate "the actor taken from the hub rather than the peer" \
  'TestTheRollComesFromTheResolver' \
  '		Actor:    peer.who.ID,' \
  '		Actor:    0,'

# A refusal propagated as an error rather than answered, so the route has to decide
# whether the client is told — and gets it wrong in whichever direction it decides.
mutate "a refusal is not answered" \
  'TestTheResolverRefusalReachesTheClientAsAClosedReason' \
  '	if err != nil {
		return peer.Send(&ServerRejected{
			Type:   TypeRejected,
			Seq:    intent.Seq,
			Reason: reasonFor(err),
		})
	}' \
  '	if err != nil {
		return nil
	}'

# Every refusal becomes `server_error`, so the closed vocabulary is decorative.
mutate "every refusal becomes server_error" \
  'TestTheResolverRefusalReachesTheClientAsAClosedReason' \
  '	rejection, ok := errors.AsType[*RejectionError](err)
	if ok && slices.Contains(rejectReasons, rejection.Reason) {' \
  '	rejection, ok := errors.AsType[*RejectionError](err)
	if ok && false && slices.Contains(rejectReasons, rejection.Reason) {'

# A reason outside the closed set is passed through — which is what `Encode` refuses,
# so the client gets a frame nobody can be told.
mutate "a reason outside the closed set is used" \
  'TestTheResolverRefusalReachesTheClientAsAClosedReason' \
  '	if ok && slices.Contains(rejectReasons, rejection.Reason) {' \
  '	if ok && (slices.Contains(rejectReasons, rejection.Reason) || rejection.Reason != "") {'

# The rejection leaks the resolver's text, which is S-12.3's exact subject: a parser
# quotes the line it choked on, and on a wiki page that line is a callout body.
mutate "a rejection carries the resolver's error text" \
  'TestARejectionCarriesNoResolverText|TestTheResolverRefusalReachesTheClientAsAClosedReason' \
  'func (r *RejectionError) Error() string { return "realtime: intent rejected: " + string(r.Reason) }' \
  'func (r *RejectionError) Error() string {
	if r.Err != nil {
		return "realtime: intent rejected: " + r.Err.Error()
	}
	return "realtime: intent rejected: " + string(r.Reason)
}'

# The reflection assertion's subject: a result field on the hub's own boundary,
# which the codec's grammar says nothing about.
mutate "a Result field appears on Intent" \
  'TestNoFieldHandedToAResolverCouldHoldARollResult' \
  '	Frame *ClientIntent' \
  '	Frame   *ClientIntent
	Result  int64'

# And a differently-named one, which is why the assertion is an allowlist rather
# than a denylist of names somebody thought of.
mutate "a differently-named result field appears on Intent" \
  'TestNoFieldHandedToAResolverCouldHoldARollResult' \
  '	Frame *ClientIntent' \
  '	Frame   *ClientIntent
	Outcome int64'

echo
echo "== the transport limit and the frame class =="

# The transport limit below the codec's, which silently redefines the protocol.
mutate "the transport limit falls below the codec's bound" \
  'TestTheTransportLimitExceedsTheCodecBound' \
  'const MaxTransportReadBytes = MaxClientFrameBytes << 1' \
  'const MaxTransportReadBytes = MaxClientFrameBytes - 1'

# `FrameClass` returns what `errorClass` already returns — R2's finding, unchanged.
mutate "FrameClass returns the dynamic type" \
  'TestFrameClassNamesTheRefusalRatherThanItsType' \
  '	if frameErr, ok := errors.AsType[*FrameError](err); ok {
		return frameErr.Class()
	}

	return "realtime"' \
  '	return fmt.Sprintf("%T", err)'

# Every refusal collapses to one word: the same finding wearing a different hat.
mutate "FrameClass collapses every refusal to one word" \
  'TestFrameClassNamesTheRefusalRatherThanItsType' \
  '		return frameErr.Class()' \
  '		_ = frameErr
		return "rejected"'

# A wrapped refusal loses its class, so a caller that adds its own context gets
# nothing — and every caller wraps before logging.
mutate "a wrapped refusal loses its class" \
  'TestFrameClassNamesTheRefusalRatherThanItsType' \
  '	if frameErr, ok := errors.AsType[*FrameError](err); ok {' \
  '	if frameErr, ok := errors.AsType[*FrameError](err); false && ok {'

# A full reply queue drops the answer instead of closing, which is a `seq` the client
# waits on forever.
mutate "a full reply queue drops the answer instead of retiring the peer" \
  'TestAnOversizeReplyQueueRetiresThePeerRatherThanDroppingAnAnswer' \
  '	p.hub.staled.Add(1)
	p.hub.detach(p)

	return fmt.Errorf("%w: peer %d holds %d", ErrAnswersFull, p.id, MaxQueuedAnswers)' \
  '	p.hub.detach(p)
	p.answers = make(chan []byte, MaxQueuedAnswers)

	return fmt.Errorf("%w: peer %d holds %d", ErrAnswersFull, p.id, MaxQueuedAnswers)'

# The reply queue replaced rather than appended to, so a delta can eat an `applied`.
mutate "the reply queue is a single replacing slot" \
  'TestAnOversizeReplyQueueRetiresThePeerRatherThanDroppingAnAnswer' \
  'make(chan []byte, MaxQueuedAnswers)' \
  'make(chan []byte, 1)'

echo
echo "== no goroutine per connection =="

# **The design claim.** A goroutine per peer passes every other test in this file: a
# peer that is not being read from does not care what is on the other stack.
mutate "a goroutine started per join" \
  'TestJoiningAConnDoesNotStartAGoroutine' \
  '	group.peers[peer.id] = peer

	return peer, nil' \
  '	go func() { <-make(chan struct{}) }()
	group.peers[peer.id] = peer

	return peer, nil'

# A goroutine per *sweep* rather than per connection. A smaller leak — one every 45
# seconds, so under two thousand a day — and the same mistake, and the staleness test
# is where a sweep actually runs, so that is where it is observed.
mutate "a leaking goroutine started per sweep" \
  'TestAPeerThatStopsReadingGoesStale' \
  '		h.sweep(h.clock.Now())' \
  '		go func() { h.sweep(h.clock.Now()); <-make(chan struct{}) }()'

cp "$BACKUP" "$SRC"
rm -f "$LOG"

echo
echo "killed $PASS, survived or inapplicable $FAIL, expected survivors $EXPECTED"
[ "$FAIL" -eq 0 ]