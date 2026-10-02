#!/usr/bin/env bash
# Mutation checks for internal/realtime/protocol.go.
#
# Every assertion in protocol_test.go and protocol_internal_test.go was checked by
# changing the line it holds, confirming the test fails, and restoring.
# `AGENTS.md` records that three of phase 5's first drafts of a gate could not
# fail, and that phase 6 found three more, so a green gate here means nothing on
# its own.
#
# **Every mutation here compiles.** A mutation that breaks the build is not
# evidence: the tests "fail" because the package is broken, which every test in the
# package would do, and the check would be reporting the compiler rather than the
# assertion. The `NO-BUILD` verdict exists to keep that honest — four of the first
# draft's mutations produced it, and each had to be rewritten.
#
# Usage: internal/realtime/mutate.sh
set -uo pipefail

export PATH=$PATH:/usr/local/go/bin:/root/go/bin

cd "$(dirname "$0")/../.."

SRC=internal/realtime/protocol.go
TEST=internal/realtime/protocol_test.go
BACKUP=$(mktemp)
TEST_BACKUP=$(mktemp)
cp "$SRC" "$BACKUP"
cp "$TEST" "$TEST_BACKUP"
trap 'cp "$BACKUP" "$SRC"; cp "$TEST_BACKUP" "$TEST"; rm -f "$BACKUP" "$TEST_BACKUP"' EXIT

PASS=0
FAIL=0
LOG=$(mktemp)

restore() {
  cp "$BACKUP" "$SRC"
  cp "$TEST_BACKUP" "$TEST"
}

# mutate <label> <test-pattern> <sed-on-protocol.go> [sed-on-protocol_test.go]
#
# The optional fourth expression exists because two mutations are *coordinated*: a
# type is renamed in `protocol.go` and every use of it in the test file must be
# renamed too, or the mutation does not compile and proves nothing. Those two rows
# are the reason the test file is mutable here at all — everything else leaves it
# alone, because a mutation that also edits the assertion it is testing is not a
# mutation.
mutate() {
  local label=$1 pattern=$2 expr=$3 testExpr=${4:-}

  restore
  [ "$expr" != ":" ] && sed -i "$expr" "$SRC"

  if [ -n "$testExpr" ]; then
    sed -i "$testExpr" "$TEST"
  fi

  if cmp -s "$SRC" "$BACKUP" && cmp -s "$TEST" "$TEST_BACKUP"; then
    echo "NO-OP       $label (sed changed nothing — the mutation does not apply)"
    FAIL=$((FAIL + 1))
    return
  fi

  if ! go vet ./internal/realtime/ >"$LOG" 2>&1; then
    echo "NO-BUILD    $label: $(grep -m1 'protocol.*:' "$LOG")"
    FAIL=$((FAIL + 1))
    restore
    return
  fi

  if go test -count=1 -run "$pattern" ./internal/realtime/ >"$LOG" 2>&1; then
    echo "SURVIVED    $label"
    FAIL=$((FAIL + 1))
  else
    echo "KILLED      $label  <- $(grep -m1 -- '--- FAIL' "$LOG" | sed 's/^ *//')"
    PASS=$((PASS + 1))
  fi

  restore
}

echo "== the size bound must come before the decode =="

# The length check deleted: an oversize frame is valid JSON, so it decodes and is
# accepted.
mutate "length check deleted" \
  'TestReadFrameRefusesAnOversizeFrameWithoutReadingOrDecodingIt' \
  '/if len(data) > MaxClientFrameBytes {/,+2d'

# The read limit lifted, so ReadFrame drains the reader and the panicky reader fires.
mutate "read limit lifted to the outbound bound" \
  'TestReadFrameRefusesAnOversizeFrameWithoutReadingOrDecodingIt' \
  's/MaxClientFrameBytes+1/MaxServerFrameBytes/'

# Off-by-one in the refusing direction: >= instead of >.
mutate "bound refuses at the limit" \
  'TestAFrameAtTheLimitIsStillAccepted' \
  's/if len(data) > MaxClientFrameBytes {/if len(data) >= MaxClientFrameBytes {/'

echo
echo "== a client's seq cannot become a version =="

# **Both halves of the top-level unknown-field defence removed together.**
#
# Neither half alone is killable, and that was the finding. The allowlist exists to
# classify the refusal and to say *before* the typed decode which frame arrived;
# `DisallowUnknownFields` catches the same bytes afterwards. Removing one leaves the
# other, so a test asserting only "this frame is refused" cannot tell which did it —
# and a mutation that survives for a reason nobody can name is exactly the state
# `AGENTS.md` warns about. This row removes both, and it is the one that says the
# property is tested at all.
mutate "unknown fields accepted at the top level, both defences removed" \
  'TestAClientSuppliedVersionIsRefused|TestAClientSuppliedRollResultIsRefused' \
  's/if _, ok := shape.fields\[key\]; !ok {/if _, ok := shape.fields[key]; ok \&\& false {/; /decoder.DisallowUnknownFields()/d'

# DisallowUnknownFields off, so an unknown field *inside* args is ignored. This one
# is killable on its own, because the allowlist only sees the envelope.
mutate "unknown fields inside args accepted" \
  'TestAClientSuppliedRollResultIsRefused' \
  '/decoder.DisallowUnknownFields()/d'

# A Version field on ClientIntent — the reflection test must notice.
mutate "a Version field appears on ClientIntent" \
  'TestNoInboundFieldCarriesAVersion' \
  's|^\tArgs IntentArgs `json:"args"`$|\tArgs IntentArgs `json:"args"`\n\tVersion Version `json:"version"`|'

# `ClientHello.Since` retyped as a `Version`, and the validation narrowed to suit, so
# the hello carries a version under a name that does not say so. This is the swap
# `TestNoInboundFieldCarriesAVersion` exists for, and it is the failure mode a type
# alias would introduce: the wire field stays `since` and only Go notices.
# `ClientHello.Since` retyped as a `Version`, and its validation narrowed to suit, so
# the hello carries a version under a wire name that does not say so. This is the
# swap `TestNoInboundFieldCarriesAVersion` exists for, and it is the failure a type
# alias would introduce: the wire field stays `since` and only Go notices.
# Scoped by line range because three types carry a `*Since` and only the hello's is
# the one being swapped.
mutate "hello's since becomes a Version" \
  'TestNoInboundFieldCarriesAVersion' \
  '515,525s|Since \*Since `json:"since,omitempty"`|Version *Version `json:"since,omitempty"`|; 1025,1040s|hello\.Since|hello.Version|g' \
  's/hello\.Since/hello.Version/g'

echo
echo "== S-7.3: the client supplies no result =="

# A result-shaped field on IntentArgs.
mutate "a Result field appears on IntentArgs" \
  'TestNoInboundFieldCouldHoldARollResult' \
  's|^\tReason string `json:"reason,omitempty"`$|\tReason string `json:"reason,omitempty"`\n\tResult int `json:"result,omitempty"`|'

# A differently-named result field. This row is why the closed list is a list: `Sum`
# is not on any denylist of result-shaped names, and a denylist would have passed it.
mutate "a differently-named result field appears" \
  'TestNoInboundFieldCouldHoldARollResult' \
  's|^\tReason string `json:"reason,omitempty"`$|\tReason string `json:"reason,omitempty"`\n\tSum float64 `json:"sum,omitempty"`|'

# A name added to the closed list that no struct carries, so the list is describing
# a protocol this build does not have and a reader would believe it.
mutate "the closed field list names a field that does not exist" \
  'TestTheClosedListIsNotAStrangerThanTheStructs' \
  ':' \
  's|"args", "since",$|"args", "since", "seed",|'

# The payload goes opaque — a `json.RawMessage` is where a result would arrive.
mutate "PresenceArgs gains an opaque payload" \
  'TestNoInboundPayloadIsOpaque' \
  's|^\tFocus PlacementID `json:"focus,omitempty"`$|\tFocus PlacementID `json:"focus,omitempty"`\n\tExtra json.RawMessage `json:"extra,omitempty"`|'

echo
echo "== a rejected frame never echoes its input =="

# The decoder's error wrapped through, so a *json.SyntaxError reaches the caller.
mutate "the json error is wrapped rather than dropped" \
  'TestARejectedFrameUnwrapsToASentinelNotADecoderError' \
  's|return nil, reject(classifyJSON(err), ErrNotJSON)|return nil, fmt.Errorf("%w: %w", ErrNotJSON, err)|'

# An error interpolating the offending field name. Restricted by line range: a bare
# substitution also matches `reject`'s own default-return site, which has no `key`
# in scope, and a mutation that does not compile is not evidence.
mutate "an error interpolates the offending field name" \
  'TestARejectedFrameNeverEchoesItsInput|TestTheClosedListIsNotAStrangerThanTheStructs' \
  '965,985s|return reject("unknown_field", ErrUnknownField)|return reject("unknown_field: "+key, ErrUnknownField)|'

# An error interpolating a rejected op — the other attacker-chosen string, and the
# one a plugin's op name would arrive in. Restricted to `validateIntent`, whose
# receiver is `intent`; the other `bad_token` sites have other receivers.
mutate "an error interpolates the rejected op" \
  'TestARejectedFrameNeverEchoesItsInput' \
  '1040,1048s|return reject("bad_token", ErrBadToken)|return reject("bad_token: "+string(intent.Op), ErrBadToken)|'

echo
echo "== counters are refused rather than absorbed =="

# The range check dropped, so an absurd counter is accepted.
mutate "the counter range check dropped" \
  'TestEveryRejectionClassIsReachable' \
  's/if err != nil || value > maxCounter {/if err != nil {/'

# The negative check dropped, so -1 wraps to a very large unsigned value.
mutate "the negative counter check dropped" \
  'TestEveryRejectionClassIsReachable' \
  '/case first == .-.:/,+1d'

# A counter written in another spelling accepted: `7e0` and `7.0` are numbers to a
# float parser, and a counter that can be written three ways is a counter whose log
# lines disagree about the same value.
mutate "a counter written as an exponent accepted" \
  'TestEveryRejectionClassIsReachable' \
  's|if err != nil \|\| value > maxCounter {|if wide, wideErr := strconv.ParseFloat(string(data), 64); wideErr == nil \&\& wide <= maxCounter { value = uint64(wide); err = nil }\n\tif err != nil \|\| value > maxCounter {|'

# The non-digit first byte accepted, so a string lands where a number belongs and
# the class is wrong — a client bug reported as a range violation.
mutate "a non-numeric counter accepted" \
  'TestEveryRejectionClassIsReachable' \
  's/case first < .0. || first > .9.:/case false:/'

echo
echo "== an unknown message type is refused, not ignored =="

# An unrecognised `t` answered with a hello instead of a refusal.
mutate "an unknown t defaults to hello" \
  'TestEveryRejectionClassIsReachable|TestARejectedFrameNeverEchoesItsInput' \
  's/shape, known := clientGrammar\[frameType\]/shape, known := clientGrammar[frameType]; if !known { shape, known = clientGrammar[TypeHello], true }/'

echo
echo "== a cursor is a pair =="

# The element count unchecked, which is what encoding/json does to [2]float64.
mutate "a cursor's element count unchecked" \
  'TestEveryRejectionClassIsReachable' \
  's/if len(pair) != 2 {/if len(pair) < 1 {/'

# Only one coordinate checked, on both the inbound and the outbound path.
mutate "only a cursor's first coordinate checked" \
  'TestEncodeRefusesAFrameThatWouldBeWrong' \
  's/return finite(cursor\[0\]) \&\& finite(cursor\[1\])/return finite(cursor[0])/'

echo
echo "== resync is a delta or a snapshot, never a partial =="

# Pruning ignored, so a `since` the log no longer covers is answered with a delta.
mutate "the pruning check dropped" \
  'TestASatisfiableSinceIsAnsweredWithADeltaAndAnUnsatisfiableOneWithASnapshot' \
  '/if log.Pruned()/,+2d'

# A `since` from the future accepted.
mutate "the future-since check dropped" \
  'TestASatisfiableSinceIsAnsweredWithADeltaAndAnUnsatisfiableOneWithASnapshot' \
  '/if \*since > log.Latest()/,+2d'

# A nil log answered with a delta — failing toward "send nothing".
mutate "a nil log answers with a delta" \
  'TestASatisfiableSinceIsAnsweredWithADeltaAndAnUnsatisfiableOneWithASnapshot' \
  's/if log == nil || since == nil {/if since == nil {/'

# An absent `since` answered with a delta — failing toward "send nothing" for the
# client that connected cold.
mutate "an absent since answers with a delta" \
  'TestASatisfiableSinceIsAnsweredWithADeltaAndAnUnsatisfiableOneWithASnapshot' \
  's/if log == nil || since == nil {/if log == nil {/'

echo
echo "== Encode refuses what it would otherwise write =="

# The closed RejectReason set opened to anything the caller likes. A plugin's error
# text is what would arrive.
mutate "a reject reason outside the closed set encodes" \
  'TestTheRejectionReasonsAreAClosedSet' \
  's|return slices.Contains(rejectReasons, reason)|return slices.Contains(rejectReasons, reason) \|\| reason != ""|'

# A snapshot with no state accepted — a client holding `null` for the table.
mutate "a snapshot with no state encodes" \
  'TestEncodeRefusesAFrameThatWouldBeWrong' \
  's/if snapshot == nil || len(snapshot.State) == 0 {/if snapshot == nil {/'

# The role check dropped, so a `Role` this build has no name for goes out.
mutate "an unknown role encodes" \
  'TestEncodeRefusesAFrameThatWouldBeWrong' \
  's/if !snapshot.You.Role.Valid() {/if false {/'

# The `t` check dropped, so a forgotten Type field encodes as "".
mutate "the frame type is not checked against the Go type" \
  'TestEncodeRefusesAFrameThatWouldBeWrong' \
  's/return checkType(clock.Type, TypeClock)/return nil/'

# The outbound frame bound dropped.
mutate "the outbound frame bound dropped" \
  'TestEncodeBoundsTheWholeFrameNotOnlyItsParts' \
  '/if len(data) > MaxServerFrameBytes {/,+2d'

# Only a delta's first change checked, so a later one may name no placement.
mutate "only a delta's first change is checked" \
  'TestEncodeRefusesAFrameThatWouldBeWrong' \
  's/for i := range delta.Changes {/for i := range delta.Changes[:1] {/'

# A change's placement not required, so a delta carries a change addressed to
# nothing.
mutate "a delta change may name no placement" \
  'TestEncodeRefusesAFrameThatWouldBeWrong' \
  's/if !validOpToken(change.Op) || !validPlacementToken(change.Placement) {/if !validOpToken(change.Op) {/'

# The op charset widened to any printable token, so an op can carry a path.
mutate "the op charset widened" \
  'TestEveryRejectionClassIsReachable' \
  's/if name == "" || len(name) > maxToken || name\[0\] < .a. || name\[0\] > .z. {/if name == "" || len(name) > maxToken {/'

echo
echo "== one frame per message =="

# Trailing bytes tolerated: the probe decode stops at the end of the first value
# rather than requiring the slice to hold one.
mutate "trailing bytes tolerated" \
  'TestEveryRejectionClassIsReachable' \
  's/if err := json.Unmarshal(data, \&probe); err != nil {/if err := json.NewDecoder(bytes.NewReader(data)).Decode(\&probe); err != nil {/'

echo
echo "== the sealed grammar and its allowlist agree =="

# An allowlist entry with no field behind it: accepted, then silently dropped.
mutate "an allowlist entry with no field behind it" \
  'TestTheAllowlistAndTheStructTagsAgree' \
  's/fields:   set("t", "args"),/fields:   set("t", "args", "shout"),/'

# A field with no allowlist entry: refused, so a legitimate field is unusable.
mutate "an allowlist entry removed for a field that exists" \
  'TestTheAllowlistAndTheStructTagsAgree' \
  's/fields:   set("t", "seq", "op", "args"),/fields:   set("t", "op", "args"),/'

# The required set emptied, so an intent with no seq decodes and can never be
# answered.
mutate "the required-field set emptied" \
  'TestEveryRejectionClassIsReachable|TestAnIntentWithNoOpIsRefused|TestTheAllowlistAndTheStructTagsAgree' \
  's/required: \[\]string{"t", "seq", "op"},/required: []string{"t"},/'

# A frame type's marker returning the wrong `t`.
mutate "a frame's own t is wrong" \
  'TestTheSealHoldsForEveryFrameInTheGrammar' \
  's/func (f \*ClientIntent) frameType() Type { return TypeIntent }/func (f *ClientIntent) frameType() Type { return TypeHello }/'

restore
rm -f "$LOG"

echo
echo "killed $PASS, survived or inapplicable $FAIL"
[ "$FAIL" -eq 0 ]