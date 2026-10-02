package dnd5e //nolint:testpackage // see the note below.
// This file asserts on what the package holds internally, and the internal form is
// the assertion: a parsed expression's dice and modifiers, a pack's compiled rows, the
// effect vocabulary's closed set, the merge by slug. Every one of those is
// unexported on purpose — a client validates against `Grammar` and `Parse`, never
// against `Expr`'s fields — so moving this file out of the package would mean
// exporting internals for a test's benefit, which is the opposite of what the
// boundary is for. The externally reachable behaviour is certified separately in
// `dnd5e_test.go`, through the `rules.System` interface.
import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// # The notation, and what these tests are actually asserting
//
// §10.3 says a grammar exists so a client can validate an expression *before* sending
// it, and that "**the protocol never assumes d20**". Both halves are met by one
// decision — the shapes are the pack's and the engine reads them — and the tests below
// are what hold it, in both directions and **asymmetrically**, because the two
// directions are not equally harmful.
//
// A pattern that accepts what the server refuses is a client that told a player their
// roll was fine and then refused it: the player cannot tell whether the roller or the
// rules are wrong, and §10.3's "the pattern is a convenience that can be wrong" is a
// sentence about this case. A pattern that refuses what the server accepts is a client
// blocking a roll the rules allow, which is an annoying tooltip and nothing worse. So
// the strict, exhaustive assertion runs in the direction that hurts, and the other
// direction is an **enumerated list** rather than a general claim.

// basePack compiles the embedded pack for a test.
//
// A constructor rather than a package-level value, for the reason S-10.1 and
// `notfive`'s own fixture give: a shared `*Engine` would be state every test in this
// package shares, and a package-level `var` initialised in `init()` is exactly what
// this repository forbids. A test that could not compile a pack would be a test
// reporting nothing, so the constructor's refusal is fatal here.
func basePack(t *testing.T) *Pack {
	t.Helper()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system over the embedded pack: %v", err)
	}

	return engine.Pack()
}

// TestTheExampleInTheGrammarParses is the tooltip check.
//
// `rules.Grammar.Example` is documentation, and nothing in `rules` can check it — that
// package does not call into the system — so a system shipping an example its own
// `Parse` refuses has shipped a broken tooltip and every other test still passes.
func TestTheExampleInTheGrammarParses(t *testing.T) {
	t.Parallel()

	pack := basePack(t)
	grammar := Grammar(pack)

	if grammar.Example == "" {
		t.Fatal("the grammar ships no example, so a client has nothing to show beside the input")
	}

	if _, err := parseExpr(pack, grammar.Example); err != nil {
		t.Errorf("the grammar's own example %q does not parse: %v", grammar.Example, err)
	}
}

// TestTheGrammarIsBuiltFromThePacksDice is "the protocol never assumes d20", held
// structurally: the patterns are **generated** from `dice.sizes`, so a pack that added
// a face changes the grammar with it and a pack whose primary die were not twenty would
// need two numbers changed rather than a parser rewritten.
func TestTheGrammarIsBuiltFromThePacksDice(t *testing.T) {
	t.Parallel()

	pack := basePack(t)
	grammar := Grammar(pack)

	for _, faces := range pack.DieSizes() {
		if !acceptsAnyTerm(t, grammar, "1d"+strconv.Itoa(faces)) {
			t.Errorf("no term accepts 1d%d, and the pack declares that die", faces)
		}
	}

	// And a die the pack does **not** declare is outside the grammar, so the client is
	// told before the server has to refuse it. `97` is chosen because it is not a
	// plausible d-something a pack would declare.
	if acceptsAnyTerm(t, grammar, "1d97") {
		t.Error("a term accepts 1d97, which this pack declares no die of")
	}

	// The count range too: `0d6` is refused by `parseExpr`, so a pattern accepting it
	// would be a preview that says yes to a roll the server then refuses.
	if acceptsAnyTerm(t, grammar, "0d6") {
		t.Error("a term accepts 0d6, which rolls no dice and is refused by Parse")
	}
}

// TestEveryExpressionTheGrammarAcceptsParses is the strict direction, exhaustively
// over generated strings rather than over a hand-written list.
//
// **Generated, because a hand-written list is a list of the cases somebody thought of.**
// Every combination this pack's dice admit of a count, a face and a modifier, over a
// range wide enough to cross the one-digit/two-digit boundary, is built and required to
// parse. A pattern that admitted something the parser refused would be caught here for
// any input in that space rather than for whichever three the author listed.
func TestEveryExpressionTheGrammarAcceptsParses(t *testing.T) {
	t.Parallel()

	pack := basePack(t)
	grammar := Grammar(pack)

	var candidates []string

	for count := 1; count <= 12; count++ {
		for _, faces := range pack.DieSizes() {
			group := strconv.Itoa(count) + "d" + strconv.Itoa(faces)

			candidates = append(candidates,
				group,
				group+"+0",
				group+"+7",
				group+"-3",
				group+"+99",
				group+"-1234",
			)
		}
	}

	candidates = append(candidates, "+1", "-1", "+0", "-0", "+12345")

	for _, text := range candidates {
		if !acceptsAnyTerm(t, grammar, text) {
			// Not a failure: the grammar is a preview and may be narrower. What matters
			// is that nothing it *accepts* is refused, so an unrepresented candidate is
			// simply not evidence either way.
			continue
		}

		if _, err := parseExpr(pack, text); err != nil {
			t.Errorf("the grammar accepts %q and Parse refuses it: %v", text, err)
		}
	}

	// The exhaustive half, as a count: if the grammar accepted nothing at all the loop
	// above would pass vacuously, and a grammar whose patterns match nothing is a
	// preview that accepts nothing and says nothing — the failure `rules.checkGrammar`
	// exists for, expressed differently.
	accepted := 0

	for _, text := range candidates {
		if acceptsAnyTerm(t, grammar, text) {
			accepted++
		}
	}

	if accepted == 0 {
		t.Fatal(
			"no generated expression was accepted by any term, so the assertions above passed " +
				"over a grammar that matches nothing",
		)
	}
}

// TestTheGrammarDivergesFromParseOnlyInTheFourNamedWays is the other direction, as a
// list rather than a claim.
//
// Each entry is a form `Parse` accepts and no pattern covers, with the reason it exists.
// A **fourth** entry is not a failure to fix but a change to record: it means either the
// language grew (and the patterns should follow) or the grammar was widened, and both
// are decisions about what a browser will let a player type.
func TestTheGrammarDivergesFromParseOnlyInTheFourNamedWays(t *testing.T) {
	t.Parallel()

	pack := basePack(t)
	grammar := Grammar(pack)

	// The four, each with what it is.
	named := []struct {
		what string
		text string
		why  string
	}{
		{
			what: "a bare number with no sign",
			text: "3",
			why: "a resistance and a flat bonus are legal notation, and the modifier term " +
				"requires a sign so a player cannot mistake one for a die",
		},
		{
			what: "leading zeros in a count or a magnitude",
			text: "02d6",
			why: "the scanner accepts them and there is no rule that says it should not; refusing " +
				"them would be a rule about typing rather than about rolling, and a preview " +
				"that blocked them would be a client disagreeing with the server",
		},
		{
			what: "two modifiers in one expression",
			text: "1d20+2+3",
			why: "the language is a sum, and a sum has as many addends as it likes; one modifier " +
				"is what the preview shows because it is the shape a player means",
		},
		{
			what: "two dice groups in one expression",
			text: "1d20+2d6",
			why:  "same answer, and a preview showing one group per term is the useful subset",
		},
	}

	for _, entry := range named {
		t.Run(entry.what, func(t *testing.T) {
			t.Parallel()

			if acceptsAnyTerm(t, grammar, entry.text) {
				t.Errorf("the grammar accepts %q, so it is no longer one of the named divergences",
					entry.text)
			}

			if _, err := parseExpr(pack, entry.text); err != nil {
				t.Fatalf("Parse refuses %q, so it is not a divergence to name: %v", entry.text, err)
			}

			_ = entry.why
		})
	}
}

// TestTheNotationRefusesWhatItDoesNotAccept is the parser's own table, and the strings
// are grouped by *why* they are refused rather than dumped in one list: a die the pack
// does not declare, no dice at all, a dangling sign, a letter where a term belongs,
// and two dice markers. Every one of those is a client typo or a hostile string, and
// each has to be a refusal rather than a repair — a parser that guesses what a player
// meant produces a result nobody can audit against `Expr.Source`, which is the whole of
// §16.3's roll log.
func TestTheNotationRefusesWhatItDoesNotAccept(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	refused := map[string]string{
		"":              "empty",
		"   ":           "whitespace only",
		"d6":            "no count",
		"0d6":           "no dice",
		"1d7":           "a die this pack does not declare",
		"1d":            "no faces",
		"2d6+":          "a trailing sign",
		"2d6-":          "a trailing sign",
		"+":             "a sign and nothing else",
		"-":             "a sign and nothing else",
		"perception":    "a word",
		"2d6+percepton": "a misspelled modifier",
		"2dd6":          "two dice markers",
		"1d20 2d6":      "a missing sign between two terms",
		"(1d20)+3":      "parentheses, which are a formula's and not a roll's",
		"1d20/2":        "division, which a formula refuses and so does this",
		"2D6":           "an upper-case marker",
		"2d6*3":         "multiplication",
	}

	for text, why := range refused {
		t.Run(strconv.Quote(text), func(t *testing.T) {
			t.Parallel()

			if _, err := parseExpr(pack, text); err == nil {
				t.Errorf("Parse accepted %q (%s)", text, why)
			} else if !strings.Contains(err.Error(), ErrRoll.Error()) {
				t.Errorf("%q was refused with %v, which does not satisfy %v",
					text, err, ErrRoll)
			}
		})
	}
}

// TestTheNotationNormalisesWhitespaceAndKeepsTheSource is the two-sides of one
// decision: whitespace is stripped so a player on a phone keyboard is not told the
// notation is hostile, and the parsed form re-renders canonically so a roll log reads
// the same expression in the table and in the payload.
func TestTheNotationNormalisesWhitespaceAndKeepsTheSource(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	spaced, err := parseExpr(pack, "  2d6  +  3 ")
	if err != nil {
		t.Fatalf("Parse refused a spaced expression: %v", err)
	}

	if got := spaced.String(); got != "2d6+3" {
		t.Errorf("the parsed expression re-renders as %q, want %q", got, "2d6+3")
	}
}

// TestADigestIsOfWhatWasRolledRatherThanHowItWasTyped is why `Digest` hashes
// `String()` and not `Source`.
//
// Two spellings of one expression are one roll, and if they drew from two streams then
// a table where one player typed `2d6+3` and the next typed `2d6 + 3` would produce two
// irreproducible rolls from one campaign decision. This is the property the labelled
// draw depends on.
func TestADigestIsOfWhatWasRolledRatherThanHowItWasTyped(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	first, err := parseExpr(pack, "2d6+3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	second, err := parseExpr(pack, "2d6 + 3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if first.Digest() != second.Digest() {
		t.Errorf("two spellings of one expression digest differently: %q and %q",
			first.Digest(), second.Digest())
	}

	other, err := parseExpr(pack, "2d6+4")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if other.Digest() == first.Digest() {
		t.Error("two different expressions digest the same, so a draw label cannot tell them apart")
	}
}

// packWithDie compiles a copy of the embedded pack with `faces` added to `dice.sizes`,
// and it is the strongest available evidence that the notation is **data**.
//
// The advantage assertions below need a die small enough to make "the kept die is
// always 2" an exact claim rather than a probable one, and this pack declares no d2. So
// the test *adds* one — and the fact that adding a line to a data file is all it takes is
// the claim §10.3's "the protocol never assumes d20" is making. A hardcoded parser
// would need its own rule changed, and a hardcoded **grammar** would need its pattern
// changed too; here the pattern is generated from the same list.
func packWithDie(t *testing.T, faces int) *Pack {
	t.Helper()

	source := strings.Replace(
		string(BasePackYAML()),
		"sizes: [4, 6, 8, 10, 12, 20, 100]",
		"sizes: [2, 4, 6, 8, 10, 12, 20, 100]",
		1,
	)
	if source == string(BasePackYAML()) {
		t.Fatal(
			"the embedded pack's dice line is not the one this test rewrites; a pack edit and " +
				"this fixture have parted, and the test is now measuring something else",
		)
	}

	pack, err := ParsePack([]byte(source))
	if err != nil {
		t.Fatalf("compiling a pack that declares a d%d: %v", faces, err)
	}

	return pack
}

// TestAdvantageAppliesToASingleDieAndToNothingElse is the limitation stated as a rule
// rather than approximated: 5e's advantage is a rule about one d20, and applying it to
// `2d6` would have to mean "roll 4d6 and keep the best 2", which is a different question
// under a different ruleset.
//
// **Asserted exactly rather than probably**, on a d2: the kept die of an advantageous
// roll is *always* 2 and of a disadvantageous roll *always* 1, whatever the seed. A
// test comparing two runs would pass one time in four by luck; this one cannot pass by
// luck at all, over two hundred seeds.
func TestAdvantageAppliesToASingleDieAndToNothingElse(t *testing.T) {
	t.Parallel()

	pack := packWithDie(t, 2)

	single, err := parseExpr(pack, "1d2")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	pair, err := parseExpr(pack, "2d2")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// `packWithDie` adds a d2, and the assertions below are all **per-seed identities**
	// rather than distributions: every claim is required to hold for two hundred seeds
	// individually, so none of them can pass by luck and none can pass by being right on
	// average. A test asserting "the advantageous total is usually higher" is a test that
	// a broken advantage passes half the time, and this repository does not ship those.
	for seed := range 200 {
		context := rules.Context{Seed: rules.Seed{byte(seed), byte(seed), byte(seed)}}

		// The two draws advantage is *supposed* to make, taken here independently, so
		// the assertion is about what `Roll` kept rather than about what it did.
		probe := context.Rand("single")
		first, second := 1+probe.IntN(2), 1+probe.IntN(2)

		kept, _, dice := single.Roll(context.Rand("single"), AdvGains)
		if kept != max(first, second) || len(dice) != 1 {
			t.Fatalf("seed %d: an advantageous 1d2 kept %d from %d and %d; advantage is "+
				"two draws and the better of them", seed, kept, first, second)
		}

		probe = context.Rand("single")
		first, second = 1+probe.IntN(2), 1+probe.IntN(2)

		kept, _, _ = single.Roll(context.Rand("single"), AdvLoses)
		if kept != min(first, second) {
			t.Fatalf("seed %d: a disadvantageous 1d2 kept %d from %d and %d; disadvantage is "+
				"two draws and the worse of them", seed, kept, first, second)
		}

		// The multi-die claim, and it needs the plain roll as its reference: two d2 sum
		// to 4 by chance one time in three, so "the total is not 4" cannot work. Compared
		// against the same draw taken plainly — a source freshly derived from the same
		// label — every one of these two hundred comparisons must agree, and a group of
		// two to which advantage reached would draw four and take the better two.
		plain, _, plainDice := pair.Roll(context.Rand("pair"), AdvNone)
		advantaged, _, advantagedDice := pair.Roll(context.Rand("pair"), AdvGains)

		if advantaged != plain || len(advantagedDice) != 2 || len(plainDice) != 2 {
			t.Fatalf("seed %d: advantage changed a 2d2 from %d to %d (%d dice against %d); "+
				"advantage is a rule about one die and applying it to two would mean rolling four",
				seed, plain, advantaged, len(plainDice), len(advantagedDice))
		}
	}
}

// TestTheBreakdownIsTheRollAndTheDiceCarryTheirOwnFaceCounts is what the 2024 critical
// rule reads.
//
// A greatsword's d6 showing 6 is a critical and a d12 showing 6 is not, so the crit hook
// needs each die's face count beside its face; a flat list of faces could not express it
// without the caller guessing a maximum.
func TestTheBreakdownIsTheRollAndTheDiceCarryTheirOwnFaceCounts(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	expression, err := parseExpr(pack, "2d12+3-1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	total, parts, dice := expression.Roll(
		rules.Context{Seed: rules.Seed{9}}.Rand("breakdown"),
		AdvNone,
	)

	if len(parts) != 4 || len(dice) != 2 {
		t.Fatalf(
			"the breakdown is %v and the dice %v; want two of each and two modifiers",
			parts,
			dice,
		)
	}

	for idx, die := range dice {
		if die.Faces != 12 {
			t.Errorf("die %d reports %d faces, want 12", idx, die.Faces)
		}

		if die.Face != parts[idx] {
			t.Errorf("die %d reported %d while the breakdown says %d", idx, die.Face, parts[idx])
		}

		if die.Face < 1 || die.Face > die.Faces {
			t.Errorf("die %d read %d, which no d%d can show", idx, die.Face, die.Faces)
		}
	}

	if parts[2] != 3 || parts[3] != -1 {
		t.Errorf("the modifiers are %v, want [3 -1]", parts[2:])
	}

	if want := dice[0].Face + dice[1].Face + 3 - 1; total != want {
		t.Errorf("the total is %d, want %d", total, want)
	}
}

// TestAnExpressionOfPureModifiersRollsNoNatural is why `HasDice` and `Natural` exist:
// `+5` is a legal expression, and recording a natural of 0 for it would put a figure in
// a roll log that reads as "the roll was a miss".
func TestAnExpressionOfPureModifiersRollsNoNatural(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	expression, err := parseExpr(pack, "-5")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if expression.HasDice() {
		t.Error("an expression of pure modifiers reports having dice")
	}

	total, parts, dice := expression.Roll(rules.Context{Seed: rules.Seed{1}}.Rand("flat"), AdvGains)
	if total != -5 || len(parts) != 1 || parts[0] != -5 || len(dice) != 0 {
		t.Errorf("an advantageous -5 read %v (total %d, dice %v); advantage has no die to apply to",
			parts, total, dice)
	}

	if _, ok := expression.Natural(rules.Context{Seed: rules.Seed{1}}.Rand("flat")); ok {
		t.Error("Natural reported a figure for an expression with no dice")
	}
}

// TestTheNotationIsNamedByItsPack is §10.3's second half — an expression parsed under
// one notation and resolved under another is a roll whose meaning changed in flight —
// asserted on the value rather than on the parser.
//
// The constant `Notation` and the pack's own `notation:` field are two spellings of one
// answer, and `TestTheKindsComeFromThePack` in `pack_test.go` holds that they agree.
func TestTheNotationIsNamedByItsPack(t *testing.T) {
	t.Parallel()

	pack := basePack(t)
	grammar := Grammar(pack)

	if pack.Notation() != Notation {
		t.Errorf("the pack declares the notation %q and the constant says %q",
			pack.Notation(), Notation)
	}

	if grammar.Notation != pack.Notation() {
		t.Errorf("the grammar reports %q and the pack %q; a client validating against one while "+
			"the server parses the other is the accident Expr.Owner exists to prevent",
			grammar.Notation, pack.Notation())
	}
}

// TestParseRecordsTheTextAsItWasWritten is §16.3's roll log in one assertion: an
// expression nobody can re-read cannot be audited, and the re-rendered form is a
// different thing from what the player typed.
func TestParseRecordsTheTextAsItWasWritten(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	const written = " 1d20 + 4 "

	expression, err := engine.Parse(written)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if expression.Source != written {
		t.Errorf(
			"Expr.Source is %q, want the text as it was written %q",
			expression.Source,
			written,
		)
	}

	if expression.Owner() != SystemID {
		t.Errorf("Expr.Owner is %q, want %q", expression.Owner(), SystemID)
	}

	if expression.Notation != Notation {
		t.Errorf("Expr.Notation is %q, want %q", expression.Notation, Notation)
	}
}

// acceptsAnyTerm reports whether any of a grammar's patterns matches the whole text.
//
// **Compiled per call rather than cached**, which is deliberate for a test: the
// grammar's patterns are the thing under test, so compiling them here means a pattern
// that does not compile is a *test failure* rather than a panic in the middle of an
// assertion. `rules.Validate` compiles them too, and that check runs at registration
// rather than in a system test.
func acceptsAnyTerm(t *testing.T, grammar rules.Grammar, text string) bool {
	t.Helper()

	for _, term := range grammar.Terms {
		compiled, err := regexp.Compile(term.Pattern)
		if err != nil {
			t.Fatalf("term %q has a pattern that does not compile: %v", term.Name, err)
		}

		if compiled.MatchString(text) {
			return true
		}
	}

	return false
}

// TestEveryDieThePackDeclaresIsASizeTheNotationAccepts holds `dice.sizes` and
// `parseExpr` together from the other side.
//
// `dieSizePattern` builds the grammar from this list and `parseExpr` checks against it,
// and the two are separate call sites over the same slice — so a list edited in one
// place and not the other would produce a grammar that accepts a roll the server
// refuses, which is the direction `TestEveryExpressionTheGrammarAcceptsParses` proves
// cannot happen. This test is the cheaper half: it needs no grammar at all.
func TestEveryDieThePackDeclaresIsASizeTheNotationAccepts(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	for _, faces := range pack.DieSizes() {
		text := "1d" + strconv.Itoa(faces)
		if _, err := parseExpr(pack, text); err != nil {
			t.Errorf("the pack declares a d%d and the notation refuses it: %v", faces, err)
		}

		if !slices.Contains(pack.DieSizes(), faces) {
			t.Errorf("a die the pack declares is missing from DieSizes: %d", faces)
		}
	}
}

// TestAdvantageScoresAndCancelAgree runs every combination of two advantages through both
// halves of the persisted form and requires the same answer.
//
// `rollRecord.Advantage` is a **small integer rather than the `Advantage` string**, because
// it is the one place the value is written onto a creature and a string would let a pack
// author put `advantage` into a creature's history by changing a row. That decision creates
// the risk this test removes: the score is a hand-written mapping, and a hand-written mapping
// is a second answer to a question `Cancel` already answers. Every pair of the three values,
// plus the empty fold and a three-way fold, is checked in both directions.
func TestAdvantageScoresAndCancelAgree(t *testing.T) {
	t.Parallel()

	every := []Advantage{AdvNone, AdvGains, AdvLoses}

	// Every ordered pair, so the mapping is checked from both sides.
	for _, first := range every {
		for _, second := range every {
			pair := [2]Advantage{first, second}

			cancelled := Cancel(pair[:]...)
			scored := advantageScore(cancelled)

			// The mapping, restated: the score is -1, 0 or 1 and names none, gains, loses.
			want := map[Advantage]int{AdvNone: -1, AdvGains: 0, AdvLoses: 1}

			if scored != want[cancelled] {
				t.Errorf("Cancel(%v, %v) is %q and scores %d, want %d",
					first, second, cancelled, scored, want[cancelled])
			}
		}
	}

	// The empty fold and a three-way fold, because "no conditions at all" is the commonest
	// case there is and three conditions is what a mid-battle token carries.
	if got := Cancel(); got != AdvNone || advantageScore(got) != -1 {
		t.Errorf("an empty fold is %q (%d), want none (-1)", got, advantageScore(got))
	}

	if got := Cancel(AdvGains, AdvNone, AdvLoses); got != AdvNone {
		t.Errorf("a three-way fold is %q, want none", got)
	}

	// And an unrecognised value is treated as none rather than as something else, which is
	// the only safe reading of a value no pack declared — a roll record must not render a
	// condition the pack does not have.
	if got := advantageScore("sideways"); got != advantageScore(AdvNone) {
		t.Errorf("an unrecognised advantage scores %d, want the score for none", got)
	}
}

// TestTheDiceCountMatchesWhatItRolls holds two answers to one question together.
//
// `DiceCount` is read by the resolver to decide how many dice the roll takes from the source
// under one label, and it is read by this test against the breakdown the roll actually
// produced. A count that disagreed with the roll would mean **the label a draw is filed
// under is wrong**, and a mislabelled draw is a draw a replay cannot reproduce — which is
// S-14.6 failing at the point it is least likely to be looked for.
func TestTheDiceCountMatchesWhatItRolls(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	for _, text := range []string{
		"1d20", "2d6+3", "1d20+2-1", "4d6", "2d4+3d8+2", "-5", "+1",
	} {
		t.Run(strconv.Quote(text), func(t *testing.T) {
			t.Parallel()

			expression, err := parseExpr(pack, text)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			// Three seeds: the counts are structural rather than drawn, so one seed would
			// do — but three makes a fixture that depends on a particular face a failure
			// rather than a mystery.
			for seed := range 3 {
				_, parts, dice := expression.Roll(
					rules.Context{Seed: rules.Seed{byte(seed)}}.Rand("count"), AdvNone,
				)

				if got := expression.DiceCount(); got != len(dice) {
					t.Errorf("seed %d: DiceCount is %d and the roll reported %d dice",
						seed, got, len(dice))
				}

				if got := expression.DiceCount() + countModifiers(expression); got != len(parts) {
					t.Errorf("seed %d: %d dice and %d modifiers made a breakdown of %d",
						seed, expression.DiceCount(), countModifiers(expression), got)
				}
			}

			if expression.HasDice() != (expression.DiceCount() > 0) {
				t.Errorf("%q reports HasDice %t with a dice count of %d",
					text, expression.HasDice(), expression.DiceCount())
			}
		})
	}
}
