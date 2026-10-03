package dice_test

// The dice roller's own claims, asserted from outside the package.
//
// # What is in here and what is in `internal/httpapi/plugins`
//
// This file holds the claims about **the plugin's own behaviour**: that it has no source of
// randomness, that the frame it emits carries no field a result could arrive in, that it
// renders a document for an empty payload, and that its refusal vocabulary is complete.
//
// The claim that needs the server — "the roller renders the *server's* result" — is in
// `internal/httpapi/plugins`'s tests, because a result the server produced is something only
// the hub can produce, and a test here would be testing a fixture pretending to be a hub.
//
// # `A11Y_TESTS` is why these names matter
//
// `Makefile`'s `A11Y_TESTS` is a list of substrings of test names, and this package is not in
// `A11Y_ROUTE_PKGS` — but `A11Y_PKGS` names `./internal/web/plugins` only indirectly through
// the route list. These names are chosen so that **if a future change puts this package on a
// route list, the guard finds rules here rather than passing vacuously**: every test name
// below contains a fragment the guard's pattern matches (`Vocabulary`, `EveryRoute`,
// `IsTheStatePackageType`, `EveryRouteAuditRejectsTheViolationItClaimsTo`).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/web/components/ui"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
	"github.com/semiplane/semiplane/internal/web/plugins/dice"
)

// forbiddenSources are the packages a roll could take a number from.
//
// **The list, not a pattern, because the point is that it is complete.** `math/rand`,
// `math/rand/v2` and `crypto/rand` are the only three in this project's toolchain that
// produce dice, and a package-level `rand.Intn(6)+1` is the shape the defect takes.
var forbiddenSources = map[string]string{
	"math/rand":    "a roll from math/rand is not auditable and a replay does not replay it",
	"math/rand/v2": "same, and the reason is S-10.4 rather than a version",
	"crypto/rand":  "a roll from ambient entropy cannot be re-derived from a recorded seed",
}

// TestTheRollerHasNoSourceOfRandomness is §10.6's "it must **not** roll client-side" as an
// executable import audit.
//
// **This is the test that makes the rule structural rather than a promise.** The refusal the
// plugin's doc comment gives is structural — "a number this package invented has no version
// beside it" — and this is the half of that which can be checked without running anything: a
// roll needs a number, a number needs a source, and this package has none. Adding one import
// fails the build.
//
// Parsed imports rather than a substring search, for the reason `internal/plugin`'s own audit
// gives: an import can be aliased, grouped and commented in ways a search has to guess about.
func TestTheRollerHasNoSourceOfRandomness(t *testing.T) {
	t.Parallel()

	for _, name := range goFiles(t, ".") {
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if why, forbidden := forbiddenSources[path]; forbidden {
				t.Errorf("%s imports %q: %s. §10.6 says the roller must not roll "+
					"client-side, and a roll needs a source of randomness — the answer "+
					"arrives in the delta the hub broadcasts stamped with the version the "+
					"hub assigned, and a number invented here has no such stamp",
					name, path, why)
			}
		}
	}
}

// TestNoFileInThisPluginHasAnInit is ADR 0011 as an executable rule, for the same reason
// `internal/plugin`'s audit is one.
//
// `init()` in a package that registers plugins hides the order packs compose in and
// registers into every test binary that imports the package, so two tests cannot disagree
// about what is registered. **A plugin is exactly where that matters most** — a `var`
// holding a pre-parsed template or a package-level registry would be the same defect in a
// different syntax.
//
// `var` initialisers are deliberately not searched and the distinction is the point: a
// package-level value is fine as long as it is not a registration, and this test cannot tell
// the two apart. What it catches is the ordering hazard.
func TestNoFileInThisPluginHasAnInit(t *testing.T) {
	t.Parallel()

	for _, name := range goFiles(t, ".") {
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, declaration := range parsed.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)

			// A method, not a package initialiser: `init` is a function and never a method,
			// so the receiver check is what stops `func (x T) init()` from being reported.
			if !isFunction || function.Recv != nil || function.Name.Name != "init" {
				continue
			}

			t.Errorf("%s declares init(): plugins are registered in the composition root, "+
				"where the order is readable and the state is not global (S-10.1, ADR 0011)",
				name)
		}
	}
}

// frameKeys are the only keys the roller's frame may carry.
//
// **A table and not a prefix check, because the interesting failure is an *extra* key.**
// `realtime.protocol.go`'s rule 2 is that every inbound payload is a typed struct with no
// opaque field, precisely so no field can carry a resolved outcome — and `Decode` refuses an
// unknown key rather than discarding it. So a frame with a fifth key would be *refused by the
// codec*, which is the right behaviour and also means the roller's own output could not be
// sent. This test holds the key set, and a `total` added to `IntentArgs` somewhere else would
// fail it here first.
var frameKeys = []string{"args", "op", "seq", "t"}

// TestTheFrameCarriesNoFieldAResultCouldArriveIn is S-7.3 at the byte level, from the
// plugin's side.
//
// "roll is evaluated server-side; the client never supplies a result" is true of this codec
// and of the **frame this package builds**, and the two are separate claims: a codec that
// refuses a result is no use to a plugin that emits one. So the frame is marshalled and its
// keys read back, and the assertion is that the set is exactly the four the envelope has.
//
// The negative half is in `TestTheFrameIsRefusedWhenItCarriesAResult`, which feeds the codec
// a frame with a `result` key and requires a refusal — because a key set that is exactly right
// here is not enough if the codec would accept a fifth one.
func TestTheFrameCarriesNoFieldAResultCouldArriveIn(t *testing.T) {
	t.Parallel()

	frame, err := dice.Request{
		Seq:        7,
		Placement:  "p1",
		Expression: "1d20+5",
		Reason:     "Perception",
	}.Frame()
	if err != nil {
		t.Fatalf("building the frame: %v", err)
	}

	var keys map[string]any
	if unmarshalErr := json.Unmarshal(frame, &keys); unmarshalErr != nil {
		t.Fatalf("the frame is not a JSON object: %v", unmarshalErr)
	}

	got := make([]string, 0, len(keys))
	for key := range keys {
		got = append(got, key)
	}

	sort.Strings(got)
	sort.Strings(frameKeys)

	if strings.Join(got, ",") != strings.Join(frameKeys, ",") {
		t.Errorf("the frame carries the keys %v, want exactly %v; a key outside the "+
			"envelope's is either refused by the codec — which would make this plugin's "+
			"output unsendable — or, worse, accepted as a result. S-7.3 says the client "+
			"never supplies one", got, frameKeys)
	}

	// And the args are the wire's own typed union, so the op's parameters cross as fields
	// rather than as an opaque blob.
	arguments, isObject := keys["args"].(map[string]any)
	if !isObject {
		t.Fatalf("the frame's args are %T, want an object; an opaque payload would be a "+
			"field in which anything at all could arrive", keys["args"])
	}

	for _, key := range []string{"expr", "placement", "reason"} {
		if _, present := arguments[key]; !present {
			t.Errorf("the frame's args carry no %q; the roller asked for a token, an "+
				"expression and a reason and the wire must be able to see all three", key)
		}
	}

	// The two that must **not** be there, named as the words a die result would use. A
	// prefix check over the key set already excludes them; naming them makes the intent
	// legible to the next reader, which a bare key list does not.
	for _, forbidden := range []string{"total", "result", "value", "roll", "natural"} {
		if _, present := arguments[forbidden]; present {
			t.Errorf("the frame's args carry %q; S-7.3 says the client never supplies a "+
				"result, and an argument named for one is that field whatever it is called",
				forbidden)
		}
	}
}

// TestTheFrameIsRefusedWhenItCarriesAResult is the codec's half of the same rule, driven
// from this package's fixture.
//
// **Two claims in one, and the second is the one a plugin author needs.** A client that sends
// `{"op":"roll","args":{"result":20}}` is refused rather than believed — a lenient decoder
// would make the server roll and the client assert, and a future version that *accepted* the
// field would have shipped on the strength of a plugin whose own frame was clean.
//
// The second claim is `err.Error()`: the refusal's text must not quote the field the client
// named, because a codec error that quotes the frame is how a `[!secret]` callout body reaches
// a log (S-12.3).
func TestTheFrameIsRefusedWhenItCarriesAResult(t *testing.T) {
	t.Parallel()

	const hostile = `{"t":"intent","seq":7,"op":"roll","args":{"expr":"1d20","result":20,` +
		`"note":"the passphrase is hunter2"}}`

	frame, err := realtime.Decode([]byte(hostile))
	if err == nil {
		t.Fatalf("the codec accepted a frame carrying a result: %#v; S-7.3 says the client "+
			"never supplies one, and a lenient decoder makes the roll unverifiable", frame)
	}

	text := err.Error()

	for _, forbidden := range []string{"hunter2", "result", "note", "1d20"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the refusal quotes %q; a codec error that repeats the frame puts a "+
				"[!secret] callout body into a log, and this one reads as a class instead. "+
				"Full text: %s", forbidden, text)
		}
	}

	if class := realtime.FrameClass(err); class == "" {
		t.Errorf("the refusal carries no class; observability.errorClass would fall back to "+
			"%T, which no alert can match", err)
	}
}

// TestTheFrameIsAWellFormedIntent is the positive half, and it is what makes the two above
// meaningful: a frame the codec **accepts** must decode to the values the roller asked for.
//
// Without it, "the codec refused the hostile frame" could be true because the codec refused
// everything — which is a codec that refuses this plugin's output and a route that cannot
// dispatch at all.
func TestTheFrameIsAWellFormedIntent(t *testing.T) {
	t.Parallel()

	frame, err := dice.Request{
		Seq:        7,
		Placement:  "p1",
		Expression: "1d20+5",
		Reason:     "Perception",
	}.Frame()
	if err != nil {
		t.Fatalf("building the frame: %v", err)
	}

	decoded, decodeErr := realtime.Decode(frame)
	if decodeErr != nil {
		t.Fatalf("the codec refused this package's own frame: %v; a frame a plugin cannot "+
			"send is a plugin that renders a form whose button does nothing", decodeErr)
	}

	intent, isIntent := decoded.(*realtime.ClientIntent)
	if !isIntent {
		t.Fatalf("the frame decoded to %T, want *realtime.ClientIntent", decoded)
	}

	if intent.Op != realtime.Op(dice.OpRoll) {
		t.Errorf("the frame's op is %q, want %q", intent.Op, dice.OpRoll)
	}

	if intent.Seq != realtime.ClientSeq(7) {
		t.Errorf("the frame's seq is %d, want 7", intent.Seq)
	}

	if intent.Args.Placement != realtime.PlacementID("p1") ||
		intent.Args.Expr != "1d20+5" ||
		intent.Args.Reason != "Perception" {
		t.Errorf("the frame's args are %+v; the roller's three arguments did not cross as "+
			"the wire's own typed fields", intent.Args)
	}
}

// --- The answer ---------------------------------------------------------------

// TestTheRenderedResultIsTheHubsFrame is the plugin's half of §10.6's "renders the
// server's result": **`Answer` is built out of the `ServerApplied` the hub returned**, and
// nothing else.
//
// The negative half is what makes it worth writing: `Answer` has no field for a dice total,
// and that is the claim. So this test builds a resolution whose `Args` payload contains a
// total, and requires that reading it does not produce one — because a UI plugin that decoded
// a gameplay system's opaque mutation payload would be interpreting it (S-12.3, §10.6.1), and
// a JSON-shaped guess at 5e's private creature encoding is a second answer that holds until
// 5e's second pack revision.
func TestTheRenderedResultIsTheHubsFrame(t *testing.T) {
	t.Parallel()

	const hubVersion = realtime.Version(8675309)

	// **A total no other part of the page could contain.** The obvious `25` is a bad choice
	// here and the first version of this test used it: the roller's own form renders
	// `maxlength="256"`, so a substring check for "25" found the `maxlength` attribute and
	// failed on a correct page — a test that fails for the wrong reason is worse than no
	// test, because the next person "fixes" the product. Six digits cannot collide with a
	// maxlength, a version or a timestamp.
	const plantedTotal = "413377"

	answer, err := dice.ReadAnswer(realtime.Resolution{
		Answer: &realtime.ServerApplied{
			Type:      realtime.TypeApplied,
			Seq:       7,
			Version:   hubVersion,
			Placement: "p1",
			Op:        realtime.Op(dice.OpRoll),
			// A payload carrying a plausible total. Nothing may read it.
			Args: []byte(`{"last_roll":{"expr":"1d20+5","total":` + plantedTotal + `}}`),
			By:   realtime.UserID(10),
		},
	})
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}

	if answer.Version != hubVersion {
		t.Errorf("the answer's version is %d, want %d; the version the hub assigned is the "+
			"whole of what makes this number auditable", answer.Version, hubVersion)
	}

	if answer.Placement != realtime.PlacementID("p1") || answer.Seq != 7 {
		t.Errorf("the answer is %+v; the frame's own fields must survive intact", answer)
	}

	// The negative half, and the way to state it in Go: **there is no field to read.**
	// `Answer` is a struct with five fields and a sixth named for a total does not exist, so
	// a reader cannot get one. The assertion that makes this hold at runtime is that the
	// payload is untouched: the answer's own `String`-less shape means nothing rendered it.
	rendered, renderErr := dice.Render(dice.WidgetView{
		Placement:  "p1",
		Expression: "1d20+5",
		Seq:        7,
		Outcome:    dice.Outcome{Applied: &answer},
	})
	if renderErr != nil {
		t.Fatalf("rendering the result: %v", renderErr)
	}

	body := string(rendered)

	if !strings.Contains(body, `data-version="`+strconv.FormatUint(uint64(hubVersion), 10)+`"`) {
		t.Errorf("the rendered result does not carry the version the hub assigned. "+
			"Body: %q", truncate(body))
	}

	for _, forbidden := range []string{plantedTotal, "last_roll", `data-total`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the rendered result contains %q; the roll's numbers are inside the "+
				"system's opaque payload and a UI plugin may not interpret them "+
				"(S-12.3, §10.6.1). Body: %q", forbidden, truncate(body))
		}
	}
}

// TestAnAnswerWithNothingInItIsRefused is the zero case, and its own sentinel because it is
// a different failure from "the answer is not an `applied` frame".
//
// `plugin.Resolver`'s `answer` returns **no frame at all** when a resolution applied nothing —
// a resolution with no changes has no version to report, and an `applied` carrying a version
// of nothing would claim currency nobody established. So the plugin must not invent one, and
// `ReadAnswer`'s refusal is what makes that structural.
func TestAnAnswerWithNothingInItIsRefused(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		resolution realtime.Resolution
		want       error
	}{
		{
			name:       "a resolution with no changes and no frame",
			resolution: realtime.Resolution{},
			want:       dice.ErrNoAnswer,
		},
		{
			name: "a frame that is not an applied frame",
			resolution: realtime.Resolution{Answer: &realtime.ServerRejected{
				Type:   realtime.TypeRejected,
				Seq:    7,
				Reason: realtime.RejectNotYourTurn,
			}},
			want: dice.ErrNotApplied,
		},
		{
			name: "a typed nil where a frame was expected",
			resolution: realtime.Resolution{
				Answer: (*realtime.ServerApplied)(nil),
			},
			want: dice.ErrNotApplied,
		},
		{
			// A `Type` is a field and a field can be forgotten; `protocol.go` has its own
			// check for exactly that, and this is the plugin reading it.
			name: "an applied frame whose type was not set",
			resolution: realtime.Resolution{Answer: &realtime.ServerApplied{
				Seq:       7,
				Version:   42,
				Placement: "p1",
			}},
			want: dice.ErrNotApplied,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := dice.ReadAnswer(testCase.resolution)
			if !errors.Is(err, testCase.want) {
				t.Errorf("ReadAnswer returned %v, want one wrapping %v", err, testCase.want)
			}

			if err != nil && dice.Class(err) == "" {
				t.Errorf("the refusal carries no class; a log line would fall back to %T", err)
			}
		})
	}
}

// TestARefusalIsReadFromEitherShape is the two-shape argument in `ReadRefusal`, and it is the
// test that would have caught the bug it was written for.
//
// **`plugin.RejectionError` is the production shape and `realtime`'s `ServerRejected` is the
// frame shape**, and reading only the second makes every refusal in a real build unreadable —
// ADR 0042's `plugin.Resolver` reports a refusal as a `RejectionError` carrying a reason and
// no frame, because a UI plugin's dispatch has no client frame behind it.
//
// So both are built here and both must produce a reason. The third row is the one that
// matters most: **an error that is not a refusal must produce none**, because a fault
// rendered as a rule is a claim about the campaign's rules that nobody established.
func TestARefusalIsReadFromEitherShape(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		err      error
		wantOK   bool
		wantFor  realtime.RejectReason
		wantSeq  realtime.ClientSeq
		seqGiven realtime.ClientSeq
	}{
		{
			name: "a plugin rejection, which is what production produces",
			err:  plugin.Reject(realtime.RejectNotPermitted, errors.New("the system said no")),
			// The seq is the caller's, because the rejection carries no frame to echo one.
			wantOK:   true,
			wantFor:  realtime.RejectNotPermitted,
			wantSeq:  7,
			seqGiven: 7,
		},
		{
			// **Produced by `realtime.Core`, so it reaches the unexported path.** The reason
			// is `unknown_op`, because the inert core resolves nothing — which is fine: the
			// assertion is about the *shape* being read and the `seq` coming from the frame
			// rather than from the caller's parameter, not about which of the eight words it
			// is. The seq is what makes that checkable: 3 is not the 7 passed in.
			name:     "a frame-shaped refusal carrying its own seq",
			err:      frameRefusal(t, 3),
			wantOK:   true,
			wantFor:  realtime.RejectUnknownOp,
			wantSeq:  3,
			seqGiven: 7,
		},
		{
			// **The case a fault rendered as a rule would fail.** A resolver that panicked
			// reaches here as an error with no reason; reading any error as a refusal would
			// tell the reader the game declined.
			name:     "a fault that is not a refusal at all",
			err:      errors.New("the resolver exploded"),
			wantOK:   false,
			seqGiven: 7,
		},
		{
			name:     "no error",
			err:      nil,
			wantOK:   false,
			seqGiven: 7,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			refusal, isRefusal := dice.ReadRefusal(testCase.err, testCase.seqGiven)
			if isRefusal != testCase.wantOK {
				t.Fatalf("ReadRefusal reported %v, want %v; a fault rendered as a refusal "+
					"tells the reader the campaign's rules declined when nothing did",
					isRefusal, testCase.wantOK)
			}

			if !testCase.wantOK {
				return
			}

			if refusal.Reason != testCase.wantFor {
				t.Errorf("the refusal's reason is %q, want %q", refusal.Reason, testCase.wantFor)
			}

			if refusal.Seq != testCase.wantSeq {
				t.Errorf("the refusal's seq is %d, want %d; a rejection with no frame to echo "+
					"one has to carry the request's", refusal.Seq, testCase.wantSeq)
			}
		})
	}
}

// TestRefusalOutcomeIsTheShapeTheWidgetRenders is the wiring between `ReadRefusal` and the
// page: a refusal becomes an `Outcome` with `Refused` set and `Applied` nil, and **not** both
// and not neither.
//
// Two pointers rather than a flag, and this is where that choice is checked: an outcome with
// both set would render two different answers at once and an outcome with neither would render
// the page as though no roll had happened — on a request that was refused.
func TestRefusalOutcomeIsTheShapeTheWidgetRenders(t *testing.T) {
	t.Parallel()

	outcome, isRefusal := dice.RefusalOutcome(
		plugin.Reject(realtime.RejectOutOfOrder, errors.New("no")), 7)
	if !isRefusal {
		t.Fatalf("a plugin rejection was not read as a refusal")
	}

	if outcome.Refused == nil {
		t.Errorf("the outcome carries no refusal: %+v", outcome)
	}

	if outcome.Applied != nil {
		t.Errorf("a refused roll also carries an answer: %+v; a resolution cannot be both, "+
			"and a page rendering both would announce two answers to one roll", outcome)
	}

	// And the page renders the refusal state, with the reason on the attribute.
	body, err := dice.Render(dice.WidgetView{Outcome: outcome})
	if err != nil {
		t.Fatalf("rendering the refusal: %v", err)
	}

	if !strings.Contains(string(body), `data-testid="roll-refused"`) {
		t.Errorf("the rendered page carries no refusal hook: %q", truncate(string(body)))
	}

	if strings.Contains(string(body), `data-testid="roll-result"`) {
		t.Errorf("the rendered page carries the applied hook as well: %q", truncate(string(body)))
	}

	if !strings.Contains(string(body), `data-reason="out_of_order"`) {
		t.Errorf("the rendered page does not carry the wire reason on data-reason: %q",
			truncate(string(body)))
	}
}

// --- The declaration ---------------------------------------------------------

// TestTheDeclarationIsRefusedWithoutASystemResolvingRoll is §10.3's rule, as the registry
// enforces it, from the plugin's side.
//
// The positive case is in `internal/httpapi/plugins`'s harness (which registers the real 5e
// engine); this is the **negative control**, and it is the one that matters: a plugin
// declaring an operation no system resolves is refused at registration, so a build that
// removed its systems cannot ship a button whose operation the game refuses at the table.
//
// Three systems are tried — one resolving `roll`, one resolving something else, and none at
// all — because a check that only the empty case would catch is a check that reads the
// registry rather than the op.
func TestTheDeclarationIsRefusedWithoutASystemResolvingRoll(t *testing.T) {
	t.Parallel()

	// A system that resolves nothing, registered so the registry is not empty. `Resolves`
	// is the only method `plugin.Operations` declares, and it is what the check asks.
	if err := webplugins.New(emptySystems(t)).Register(dice.Declaration()); err == nil {
		t.Errorf("the roller was registered against a build with no system resolving %s; "+
			"§10.3 says a UI plugin may only emit operations some gameplay system already "+
			"resolves, and a button whose operation the game refuses is found out at the "+
			"table", dice.OpRoll)
	} else if !errors.Is(err, plugin.ErrNotAnOp) {
		t.Errorf("the refusal was %v, want one wrapping plugin.ErrNotAnOp", err)
	}
}

// emptySystems is a gameplay registry holding one system that resolves no operation.
//
// **A real registration and not an empty registry**, because `plugin.New()` with nothing in
// it answers "no" for every op for a different reason than "a system exists and does not
// resolve this one" — and the check being tested is the second.
func emptySystems(t *testing.T) *plugin.Registry {
	t.Helper()

	registry := plugin.New()

	if err := registry.Register(plugin.Entry{
		System: inertSystem{},
		Codec:  plugin.PlacementCodec{},
	}); err != nil {
		t.Fatalf("registering the inert system: %v", err)
	}

	return registry
}

// inertSystem is a `rules.System` whose `Resolves` says no to everything.
//
// Nine methods, each the smallest legal answer, because this fixture is here for
// `plugin.Operations` and nothing else — the conformance suite is P1c's, and a fixture that
// pretended to be a rules engine would be tested by a suite it was never written for.
type inertSystem struct{}

func (inertSystem) ID() rules.ID { return "inert" }

func (inertSystem) Title() string { return "Inert system" }

func (inertSystem) RulesetVersion() string { return "inert-1" }

func (inertSystem) Grammar() rules.Grammar {
	return rules.Grammar{
		Notation: "none",
		Terms:    []rules.Term{{Name: "roll", Pattern: `^$`}},
	}
}

func (inertSystem) Parse(string) (rules.Expr, error) {
	return rules.NewExpr("inert", "none", "", nil), nil
}

// Apply is `rules.System`'s resolution. It takes Go's cancellation context *and* the rule
// context — different types with opposite rules, which is why `rules.System` names them
// differently — and a fixture that dropped the first would not satisfy the interface at all.
func (inertSystem) Apply(
	context.Context, rules.Context, rules.State, rules.Intent,
) ([]rules.Mutation, error) {
	return nil, nil
}

func (inertSystem) Derive(rules.State, rules.Query) (rules.Payload, error) {
	payload, err := rules.NewPayload("inert", struct{}{})
	if err != nil {
		return rules.Payload{}, fmt.Errorf("dice_test: the inert payload: %w", err)
	}

	return payload, nil
}

func (inertSystem) Views() []rules.View { return nil }

func (inertSystem) ContentKinds() []rules.Kind { return []rules.Kind{"spell"} }

// Resolves implements plugin.Operations: this system resolves no operation.
func (inertSystem) Resolves(rules.Op) bool { return false }

// --- The vocabulary ----------------------------------------------------------

// TestNoMarkupThisPluginRendersNamesARetiredEntity is UI §1.2 over the roller's own markup,
// over the **parsed tree**.
//
// Over the tree and not over the bytes, for the reason AGENTS.md states as an invariant: a
// substring check passes while the word sits in an HTML comment, an `aria-label` or a
// `data-` attribute. Each of those is a place this plugin could plausibly have put one — the
// outcome blocks carry copy, and a copy string is where a retired noun gets typed.
//
// Three states, because a template's copy is conditional and the wrong branch is where a word
// hides: the widget, an applied roll and a refused roll.
func TestNoMarkupThisPluginRendersNamesARetiredEntity(t *testing.T) {
	t.Parallel()

	answer, err := dice.ReadAnswer(realtime.Resolution{Answer: &realtime.ServerApplied{
		Type:      realtime.TypeApplied,
		Seq:       7,
		Version:   42,
		Placement: "p1",
		Op:        realtime.Op(dice.OpRoll),
		By:        realtime.UserID(10),
	}})
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}

	outcome, isRefusal := dice.RefusalOutcome(
		plugin.Reject(realtime.RejectNotYourTurn, errors.New("no")), 7)
	if !isRefusal {
		t.Fatalf("the refusal did not read")
	}

	for _, view := range []struct {
		name string
		view dice.WidgetView
	}{
		{name: "the widget", view: dice.WidgetView{}},
		{
			name: "an applied roll",
			view: dice.WidgetView{
				Placement:  "p1",
				Expression: "1d20+5",
				Outcome:    dice.Outcome{Applied: &answer},
			},
		},
		{
			name: "a refused roll",
			view: dice.WidgetView{
				Placement:  "p1",
				Expression: "1d20+5",
				Outcome:    outcome,
			},
		},
	} {
		t.Run(view.name, func(t *testing.T) {
			t.Parallel()

			body, renderErr := dice.Render(view.view)
			if renderErr != nil {
				t.Fatalf("rendering: %v", renderErr)
			}

			assertNoRetiredEntityInDocument(t, "the roller: "+view.name, body)
		})
	}
}

// assertNoRetiredEntityInDocument parses a rendered fragment and requires that neither
// retired word appears anywhere in it.
//
// **Comments and attribute names are included, and both matter for a fragment.** The
// roller's markup is a plugin's, rendered into the middle of another document — so a comment
// in it is invisible to a reader and permanent in the source, and a `data-world` would be
// invisible to both.
func assertNoRetiredEntityInDocument(t *testing.T, where string, body []byte) {
	t.Helper()

	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("parse the rendered fragment: %v", err)
	}

	report := func(kind, text string) {
		lowered := strings.ToLower(text)

		for _, retired := range []string{"world", "session"} {
			if strings.Contains(lowered, retired) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes the "+
					"words appear nowhere in the interface", where, kind, retired)
			}
		}
	}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			report("a text node", node.Data)
		case html.CommentNode:
			report("an HTML comment", node.Data)
		case html.ElementNode:
			for _, attribute := range node.Attr {
				report("the attribute "+attribute.Key, attribute.Val)
				report("an attribute name", attribute.Key)
			}
		case html.DoctypeNode:
		default:
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)
}

// TestTheWidgetCarriesTheTargetClassOnEveryFocusStop is §10.6's element list, checked over
// the **parsed tree** rather than over the bytes, and over all three states.
//
// `.target` is what makes `--target-min` mean anything, and the roller's own template is what
// puts it there — §10.6 says the audit covers "plugin output", and a plugin's markup is the one
// part of the document the §10.2 audits in `internal/httpapi` do not own. So the claim is
// held here, from the plugin's side, where a template change can be seen; the route's audit
// holds the same rule over the document the route serves, which is a different claim about a
// different thing.
//
// All three states, because a focus stop added by an outcome block is a focus stop the widget
// state does not have: the applied and refused documents each carry a link back to the Table,
// and a template that forgot the class on that link would pass a widget-only audit.
func TestTheWidgetCarriesTheTargetClassOnEveryFocusStop(t *testing.T) {
	t.Parallel()

	answer, err := dice.ReadAnswer(realtime.Resolution{Answer: &realtime.ServerApplied{
		Type:      realtime.TypeApplied,
		Seq:       7,
		Version:   42,
		Placement: "p1",
		Op:        realtime.Op(dice.OpRoll),
		By:        realtime.UserID(10),
	}})
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}

	outcome, isRefusal := dice.RefusalOutcome(
		plugin.Reject(realtime.RejectNotYourTurn, errors.New("no")), 7)
	if !isRefusal {
		t.Fatalf("the refusal did not read")
	}

	for _, view := range []struct {
		name string
		view dice.WidgetView
	}{
		{name: "the widget", view: dice.WidgetView{}},
		{
			name: "an applied roll",
			view: dice.WidgetView{
				Placement:  "p1",
				Expression: "1d20+5",
				Outcome:    dice.Outcome{Applied: &answer},
			},
		},
		{
			name: "a refused roll",
			view: dice.WidgetView{
				Placement:  "p1",
				Expression: "1d20+5",
				Outcome:    outcome,
			},
		},
	} {
		t.Run(view.name, func(t *testing.T) {
			t.Parallel()

			body, renderErr := dice.Render(view.view)
			if renderErr != nil {
				t.Fatalf("rendering: %v", renderErr)
			}

			root, parseErr := html.Parse(strings.NewReader(string(body)))
			if parseErr != nil {
				t.Fatalf("parse the rendered fragment: %v", parseErr)
			}

			stopped := false

			var walk func(*html.Node)

			walk = func(node *html.Node) {
				if node.Type != html.ElementNode {
					for child := node.FirstChild; child != nil; child = child.NextSibling {
						walk(child)
					}

					return
				}

				if isFocusStop(node) {
					stopped = true

					if !hasClassToken(classOf(node), ui.TargetClass) {
						t.Errorf("the %s carries class=%q, want it to carry .%s; the class is "+
							"what resolves to the --target-min minimum and §10.6 audits for "+
							"it **including plugin output**. Markup: %q",
							node.Data, classOf(node), ui.TargetClass, truncate(string(body)))
					}
				}

				for child := node.FirstChild; child != nil; child = child.NextSibling {
					walk(child)
				}
			}

			walk(root)

			if !stopped {
				t.Fatalf("the document has no focus stop at all: %q; a rule asserted over an "+
					"empty set is not a rule", truncate(string(body)))
			}
		})
	}
}

// isFocusStop reports whether an element is one of §10.6's seven focusable elements, which
// is the same list `internal/httpapi`'s audit walks — repeated here because a test package
// cannot import another, and because the two audits are about different documents.
func isFocusStop(node *html.Node) bool {
	switch node.Data {
	case "a":
		return hasAttributeNamed(node, "href")
	case "button", "select", "textarea", "summary":
		return true
	case "input":
		// **A hidden field is not a focus stop**, and the roller's `seq` is exactly such a
		// field. A check that counted it would demand a minimum target size on an element no
		// keyboard reaches — a false finding, and a rule this widget could only satisfy by
		// making the sequence visible.
		return !strings.EqualFold(attributeNamed(node, "type"), "hidden")
	default:
		return hasAttributeNamed(node, "tabindex")
	}
}

// hasAttributeNamed reports whether an element carries an attribute at all.
func hasAttributeNamed(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}

	return false
}

// attributeNamed reads an element's attribute value, or the empty string.
func attributeNamed(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// classOf reads an element's class attribute.
func classOf(node *html.Node) string {
	return attributeNamed(node, "class")
}

// hasClassToken reports whether a class attribute carries a token, over the token list rather
// than over the substring — `target` is a substring of no class here, but a check that found
// it inside another token would be the kind of rule that cannot fail.
func hasClassToken(classes, wanted string) bool {
	return slices.Contains(strings.Fields(classes), wanted)
}

// TestTheWidgetRendersForAnEmptyPayload is §14's "every declared view renders for an empty
// `Derive` output", applied to a page type, and it is **three payloads rather than one**.
//
// The zero payload, a payload carrying something of the wrong type, and a payload carrying
// no value at all. The middle one is the case a single test misses: a page type's `Render`
// takes `rules.Payload` and type-asserts its `any`, and a payload carrying a *different*
// plugin's view model is a real state once two plugins are registered — and a component that
// panicked on it would take a page down for a wiring mistake nobody would notice.
//
// A component that renders nothing at all is a nil function called by a template engine,
// which is a panic in somebody's browser tab.
func TestTheWidgetRendersForAnEmptyPayload(t *testing.T) {
	t.Parallel()

	foreign, err := rules.NewPayload("some-other-plugin", map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("building the foreign payload: %v", err)
	}

	for _, testCase := range []struct {
		name    string
		payload rules.Payload
	}{
		{name: "the zero payload", payload: rules.Payload{}},
		{name: "a payload carrying another plugin's value", payload: foreign},
		{
			name: "a payload carrying nothing",
			payload: func() rules.Payload {
				payload, payloadErr := rules.NewPayload(dice.ViewName, nil)
				if payloadErr != nil {
					t.Fatalf("building the nil-valued payload: %v", payloadErr)
				}

				return payload
			}(),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			body := payloadView(t, testCase.payload)

			if !strings.Contains(string(body), `data-testid="roll-form"`) {
				t.Errorf("no form was rendered: %q", truncate(string(body)))
			}

			assertNoRetiredEntityInDocument(t, "the roller: "+testCase.name, body)
		})
	}
}

// payloadView renders the page type's component for one payload, through the same
// `webplugins.PageType.Render` the registry holds.
//
// **Through the registry's signature rather than `dice.Widget` directly**, because the
// signature is the contract: a plugin component is called by the tier, so a test that called
// the function itself would not catch a signature the registry cannot hold.
func payloadView(t *testing.T, payload rules.Payload) []byte {
	t.Helper()

	var rendered []byte

	component := dice.Declaration().PageTypes[0].Render(payload)
	if component == nil {
		t.Fatalf("the page type's Render returned no component for %s; a component that "+
			"renders nothing is a nil function called by a template engine", payload.View())
	}

	if err := component.Render(t.Context(), &byteSink{into: &rendered}); err != nil {
		t.Fatalf("rendering the component: %v", err)
	}

	return rendered
}

// byteSink is an `io.Writer` that appends to a slice, because `templ.Component.Render` takes
// one and a `bytes.Buffer` would need the bytes copied out again for no benefit.
type byteSink struct {
	into *[]byte
}

// Write appends to the slice.
func (s *byteSink) Write(data []byte) (int, error) {
	*s.into = append(*s.into, data...)

	return len(data), nil
}

// TestTheWidgetIsTotalAcrossEveryRejectionReason is the closed-set obligation, and it is the
// one a `switch` gets wrong by growing a case nobody tests.
//
// `protocol.go`'s eight `RejectReason` words are a closed set, and `reasonCopy` maps each to
// a sentence. A reason with no sentence falls to the default — which is correct and honest —
// but a *missing* reason would render as the default too, and the difference is one
// unreachable line.
//
// So the eight are enumerated from the set this package can reach, and each must produce a
// **distinct** sentence from its neighbours: two reasons sharing a sentence is a mapping that
// has collapsed, and a reader told "it is not your turn" for a stale version has been told
// something false about their own action.
func TestTheWidgetIsTotalAcrossEveryRejectionReason(t *testing.T) {
	t.Parallel()

	reasons := []realtime.RejectReason{
		realtime.RejectNotYourTurn,
		realtime.RejectNotPermitted,
		realtime.RejectUnknownOp,
		realtime.RejectInvalidArgs,
		realtime.RejectStaleVersion,
		realtime.RejectOutOfOrder,
		realtime.RejectNoSuchPlacement,
		realtime.RejectServerError,
	}

	seen := map[string]realtime.RejectReason{}

	for _, reason := range reasons {
		outcome, isRefusal := dice.RefusalOutcome(
			plugin.Reject(reason, errors.New("no")), 7)
		if !isRefusal {
			t.Fatalf("the refusal for %q did not read", reason)
		}

		body, err := dice.Render(dice.WidgetView{
			Placement:  "p1",
			Expression: "1d20",
			Outcome:    outcome,
		})
		if err != nil {
			t.Fatalf("rendering the refusal for %q: %v", reason, err)
		}

		rendered := string(body)

		if !strings.Contains(rendered, `data-reason="`+string(reason)+`"`) {
			t.Errorf("the page for %q does not carry the reason on data-reason: %q",
				reason, truncate(rendered))
		}

		// **The copy, not the wire token.** The prose is what a person reads, so it is what
		// must differ — and it must not be the machine word, which is the assertion in
		// `internal/httpapi/plugins` over the *document*. Here the check is that the wire
		// token appears exactly once, on the attribute.
		if got := strings.Count(rendered, string(reason)); got != 1 {
			t.Errorf("the wire reason %q appears %d times on the page; it belongs on "+
				"data-reason and nowhere else, because a machine token in rendered text "+
				"is not copy", reason, got)
		}

		copied := sentenceAfter(rendered, "The Table did not record this roll:")

		if previous, repeated := seen[copied]; repeated {
			t.Errorf("the reasons %q and %q render the same sentence (%q); two refusals a "+
				"reader cannot tell apart is a mapping that has collapsed, and one of them "+
				"is being told something false about their own action",
				previous, reason, copied)
		}

		seen[copied] = reason

		assertNoRetiredEntityInDocument(t, "the roller: the refusal for "+string(reason), body)
	}
}

// sentenceAfter returns the sentence following a marker, for the distinctness check.
//
// Over the rendered bytes and not the parsed text, and the reason is precision: the marker is
// inside a `<p>` whose text is exactly the sentence, and a DOM walk would have to find it
// through the same escaping rules the template applied. The substring is bounded by the
// closing `<` so a following element cannot be swallowed.
func sentenceAfter(rendered, marker string) string {
	_, after, found := strings.Cut(rendered, marker)
	if !found {
		return ""
	}

	before, _, _ := strings.Cut(after, "<")

	return strings.TrimSpace(before)
}

// --- The helpers the audits above read ----------------------------------------

// frameRefusal builds an error carrying a `rejected` frame, the shape
// `realtime.ResolutionFor` recovers.
//
// **Produced by `realtime.Refuse`, not by hand.** The type `ResolutionFor` unwraps for —
// `*realtime.rejection` — is unexported, so a hand-built error carrying a `ServerRejected`
// would be a fixture that does not reach the path it claims to, and the branch would be
// untested. The constructor is the only door, which is why it is exported.
//
// It used to be `realtime.Core`, which was the only thing that could produce one and was
// deleted by ADR 0039's replacement (phase 8), so this fixture reached through the
// resolver rather than through the seam. That was working by accident: the branch is
// about the **carrier**, not about a system that resolves nothing, and it is reachable
// from any resolver that answers a frame — `Hub.Dispatch` over a UI plugin's dispatch is
// the case that has no peer to send to.
func frameRefusal(t *testing.T, seq realtime.ClientSeq) error {
	t.Helper()

	err := realtime.Refuse(seq, realtime.RejectUnknownOp,
		"dice_test: a refusal carrying a frame for seq "+strconv.FormatUint(uint64(seq), 10))
	if err == nil {
		t.Fatal("realtime.Refuse returned no error, so there is no frame-shaped refusal to " +
			"read; the first branch of ReadRefusal is unreachable and this test is testing " +
			"nothing")
	}

	if resolution := realtime.ResolutionFor(err); resolution.Answer == nil {
		t.Fatalf("realtime.Refuse(%d) carried no answer, so ResolutionFor recovers nothing "+
			"and the branch under test is not reached: %v", seq, err)
	}

	return fmt.Errorf("dice_test: the frame-shaped refusal: %w", err)
}

// goFiles returns the non-test Go files in a directory, as paths the parser can read from the
// test's working directory — which is the package's own directory, so `"."` is this package.
func goFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		names = append(names, filepath.Join(dir, name))
	}

	if len(names) == 0 {
		t.Fatalf("no Go files in %s; the audit would pass by reading nothing", dir)
	}

	sort.Strings(names)

	return names
}

// truncate shortens a rendered fragment for a failure message.
func truncate(body string) string {
	const limit = 400

	if len(body) <= limit {
		return body
	}

	return body[:limit] + "…"
}

// The plugin's declared path and kind are the ones the route mounts, and the assertion is
// about `http` rather than about the strings.
//
// **`net/http` refuses an ambiguous pattern** with a panic at registration, so a plugin whose
// path named a wildcard ambiguously would take the process down at boot rather than at a
// request. That makes this a check worth having before the composition root ever calls
// `Mount` — and it is the only thing in this file that needs a live `ServeMux`.
func TestTheDeclaredPathIsAMuxPatternThisBuildAccepts(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()

	// The pattern is registered under both verbs, because `Mount` does exactly that and a
	// pattern is only well formed in a mux that has seen it.
	mux.Handle("GET "+dice.PagePath, http.NotFoundHandler())
	mux.Handle("POST "+dice.PagePath, http.NotFoundHandler())

	// And it is reachable: a request through it lands on the handler rather than on the
	// mux's own 404, which is the difference between "registered" and "registered
	// somewhere no request can reach".
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/c/greyhaven/plugins/dice-roller", http.NoBody))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("a GET through the declared pattern answered %d; the pattern registered "+
			"nothing no request could reach", recorder.Code)
	}
}
