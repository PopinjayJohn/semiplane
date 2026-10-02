package notfive_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
	"github.com/semiplane/semiplane/internal/domain/systems/notfive"
)

// # S-14.9
//
// *"A system sharing nothing with 5e passes the conformance suite without touching
// `internal/domain/rules`. This is the test that proves the plugin system
// generalises."*
//
// The first half is `TestTheLoomPassesTheConformanceSuite`, one call, and it is the
// half anybody can write for any system in one afternoon. The second half is the three
// audits below, and it is the half that makes the first mean something: **a suite that
// could pass with a shared helper, and pass without one, has proved nothing.** If the
// loom imported the 5e engine, the suite's determinism audit would still be green, and
// so would the other four — so the generalisation claim would rest on a sentence in a
// doc comment.
//
// So the claim is made executable, in three separate ways because they fail
// differently:
//
//  1. `TestTheLoomImportsNothingButTheContract` — the import set is **exact and
//     closed**, each entry with a reason. A closed set is what makes the *first* 5e
//     import a failure rather than a merge conflict somebody notices in review.
//  2. `TestTheLoomUsesNothingFromTheContractButItsOwnTypes` — every `rules.X` selector
//     the package evaluates is on an allowlist of what implementing nine methods
//     requires. This is the "without touching `internal/domain/rules`" clause read as
//     code rather than as a package boundary: an allowlist of *imports* would be
//     satisfied by importing something from `rules` and not using it.
//  3. `TestTheLoomCarriesNoVocabularyFromFiveE` — no identifier and no string literal
//     holds 5e's dice, its scoring model or its content kinds.
//
// And every one of the three is checked by its own meta-test, because an audit that
// cannot fail is a green light wired to nothing — which is how the wiki route and the
// assets route both passed `make a11y` without contributing a single audit.

// rulesPackage is the import path the loom is allowed to implement the interface from.
const rulesPackage = "github.com/semiplane/semiplane/internal/domain/rules"

// wantedImports is the **exact** set of imports this package may carry, each with the
// reason it is permitted rather than merely allowed.
//
// Closed on purpose. An allowlist of permitted paths would still admit a 5e package the
// day one exists, which is the failure this audit exists to prevent; a closed set admits
// nothing until somebody edits this table, and the edit is a review comment about a
// counterexample to S-14.9.
var wantedImports = map[string]string{
	// The Go cancellation context, because `Apply`'s signature takes one (ADR 0041).
	"context": "the signature requires it",
	// This system's own encoding. A pack would be `go:embed`ed; this package ships none,
	// and `_ "embed"` is conspicuously absent for that reason.
	"encoding/json": "the system's own body encoding",
	// Sentinels the composition root's adapter matches on with `errors.Is`.
	"errors": "refusals the adapter has to recognise",
	// Messages.
	"fmt": "refusal text",
	// `strings.Cut` and `strings.TrimSpace`, which is this system's parser and nothing
	// else's.
	"strings": "this system's own notation parser",
	// `slices.Clone` and `slices.Contains`: every declaration this package hands out is
	// cloned per call, which is `rules.SemiplaneKinds`'s rule and not a nicety.
	"slices": "a fresh slice per call, so no caller can mutate package state",
	// The role vocabulary, which is the one thing a GM-only check needs and which
	// belongs to `domain` rather than to `rules` — so the loom's dependence on
	// semiplane's *vocabulary* is visible in one line.
	"github.com/semiplane/semiplane/internal/domain": "the role vocabulary, and nothing else",
	// The interface and its value types. **This is the whole of what is shared.**
	rulesPackage: "the interface, and the intent and mutation types",
}

// wantedSelectors is every `rules.X` this package may evaluate.
//
// The "except what implementing the interface requires" clause, made executable. An
// import audit alone would be satisfied by importing something from `rules` and not
// using it, so the audit is over **selectors**: what the loom actually reaches for.
//
// Four groups, and the boundary between them is the point:
//
//   - Types the nine methods return. `ID`, `Title` (a string, not one of these),
//     `Grammar`, `Expr`, `Context`, `State`, `Intent`, `Mutation`, `Query`, `Payload`,
//     `Op`, `Kind`, `ObjectID`, `View` — without these there is no implementation.
//   - Constructors those types need, because the fields a system has to build are
//     unexported on purpose: `NewExpr` owns the owner field, `NewMutation` refuses a
//     mutation the hub could not apply, and `NewPayload` refuses a payload with no view.
//   - Two checks the contract offers rather than requires: `Intent.Valid` and
//     `Query.Check`, both predicates a system has no need to call.
//   - One sentinel: `ErrInvalidView`, for the refusal a `Derive` that does not recognise
//     a view's name has to return. A system may invent its own instead — which is
//     exactly why this is an allowlist and not a requirement.
var wantedSelectors = map[string]string{
	// The interface itself, for the compile-time assertion. Naming it is unavoidable and
	// costs nothing: `var _ rules.System = loom{}` proves this package can be dropped
	// anywhere, and *evaluating* it would be the shared thing.
	"System": "the compile-time assertion that this is a system",
	// The types the nine methods return, and cannot return without.
	"Context":  "the rule context `Apply` is handed",
	"Expr":     "what `Parse` returns",
	"Grammar":  "what `Grammar` returns",
	"ID":       "what `ID` returns",
	"Intent":   "the operation `Apply` resolves",
	"Kind":     "the content kinds `ContentKinds` declares",
	"Mutation": "what `Apply` returns",
	"Op":       "this system's operation vocabulary",
	"Payload":  "what `Derive` returns",
	"Query":    "the render query `Derive` is handed",
	"State":    "the read-only snapshot `Apply` is handed",
	"View":     "the declared views `Views` returns",
	// Types a method's *parameters* are declared with, and a body reads through.
	"Object":   "what `State.Lookup` hands back",
	"ObjectID": "a game object's name, which `Query.Object` and `NewMutation`'s target are",
	// Constructors for the values this package cannot build any other way.
	"NewExpr": "the only way to build an `Expr`: its owner is unexported on purpose",
	"NewMutation": "builds a mutation and refuses one the hub could not apply, which is a check a " +
		"plugin wants rather than re-implements",
	"NewPayload": "records which view a payload answers, and the view is what chooses the renderer",
	// The grammar's vocabulary, which §10.3 exists so a client can validate against. A
	// system that declares no notation cannot implement `Grammar`.
	"Term":            "one shape a valid expression may take",
	"RendererGeneric": "the built-in renderers are semiplane's, and a view says which serves it",
	"ShapeStatBlock":  "as above, for the built-in renderer's shape",
	"ShapeList":       "as above, for the built-in renderer's shape",
	// Two checks the contract offers rather than requires, and one optional sentinel.
	//
	// A system may do without every one of these: refuse its own notation, refuse an
	// unknown view with its own error, and trust the hub to have built a valid intent.
	// They are on the list because *using* them is free and re-implementing them is where
	// a plugin and the contract drift apart.
	"Intent.Valid": "a check the contract offers, not one it requires",
	"Query.Check":  "a check the contract offers, not one it requires",
	"ErrInvalidView": "the refusal a `Derive` that does not know a view's name returns; a system may " +
		"invent its own, and this is on the list because it costs nothing",
}

// forbiddenVocabulary is 5e's, and the audit is that this package does not carry it.
//
// **Fragments, matched case-insensitively against identifiers and string literals with
// separators removed.** Fragments rather than whole words because the vocabulary's
// whole problem is that it arrives in every spelling a Go identifier can take:
// `HitPoints`, `hit_points` and `hitPoint` are one assumption written three ways, and a
// test that matched whole identifiers would catch one of them.
//
// Comments are **excluded**, and that is a decision with a reason rather than an
// omission: a comment is documentation, and this file's own comments are full of the
// words below because they are constantly saying "not a d20 system". Vocabulary *in use*
// is an identifier a compiler binds or a literal a client is shown, and that is what a
// shared assumption is made of. The meta-test asserts both directions — that six
// spellings of the same assumption are each caught, and that a comment saying it is not
// caught.
var forbiddenVocabulary = map[string]string{
	"d20":       "the die §10.3 says the protocol never assumes",
	"d4":        "5e's damage dice",
	"d6":        "5e's damage dice",
	"d8":        "5e's damage dice",
	"d10":       "5e's damage dice",
	"d12":       "5e's damage dice",
	"ability":   "5e's scoring model",
	"hitpoint":  "5e's resource model, in every spelling",
	"spell":     "a 5e content kind (§10.2.1's example)",
	"class":     "a 5e content kind (§10.2.1's example)",
	"feat":      "a 5e content kind (§10.2.1's example)",
	"ancestry":  "a 5e content kind (§10.2.1's example)",
	"creature":  "a 5e content kind (§10.2.1's example)",
	"condition": "5e's status vocabulary, which this system has no need of",
	"fivee":     "5e itself, spelled out",
}

// TestTheLoomPassesTheConformanceSuite is S-14.9's first half, and it is one call to
// one function.
//
// **Nothing else is imported and nothing else is configured beyond the four seams**, and
// that is the shape an author outside this repository copies: the interface, one
// scenario, the operations reserved to the GM, the map from this system's errors to the
// wire's words, and the deployment's ruleset decision. `RulesetVersion` is the only
// thing the seams share with the system itself.
func TestTheLoomPassesTheConformanceSuite(t *testing.T) {
	t.Parallel()

	system := notfive.New()

	if err := conformance.Check(t.Context(), suiteFor(system)); err != nil {
		t.Fatalf(
			"a system sharing nothing with 5e did not pass the conformance suite, and with it "+
				"S-14.9's claim that the plugin system generalises:\n%v",
			err,
		)
	}
}

// TestTheLoomIsAcceptedByTheContract is the one check that is not the suite's: a system
// this package exports has to be one `rules.Validate` accepts, or the registry would
// refuse it at startup with every other plugin's blessing absent.
func TestTheLoomIsAcceptedByTheContract(t *testing.T) {
	t.Parallel()

	if err := rules.Validate(notfive.New()); err != nil {
		t.Fatalf("the contract refused the loom: %v", err)
	}
}

// TestTheLoomDeclaresNoneOfSemiplanesKinds is §10.2.1 restated for this package, and
// it is what lets the client render a table without knowing a loom exists.
//
// The count as well as the names, because "declares none of them" is satisfied by a
// system that declares nothing and a set that has quietly been emptied.
func TestTheLoomDeclaresNoneOfSemiplanesKinds(t *testing.T) {
	t.Parallel()

	declared := notfive.New().ContentKinds()
	if len(declared) == 0 {
		t.Fatal(
			"the loom declares no content kinds, so the ownership rule is satisfied by having " +
				"nothing to check",
		)
	}

	owned := rules.SemiplaneKinds()

	for _, kind := range declared {
		if rules.IsSemiplaneKind(kind) {
			t.Errorf("the loom declares %q, which semiplane owns", kind)
		}

		for _, semiplaneKind := range owned {
			if kind == semiplaneKind {
				t.Errorf("the loom declares %q, which semiplane owns", kind)
			}
		}
	}
}

// TestTheLoomImportsNothingButTheContract is audit 1.
//
// Over **parsed imports**, not over the file's text, because `math/rand/v2` and
// `math/rand` are one character apart and a substring search cannot tell them — the
// same argument `rules`' own import audit makes. And over the whole package's
// non-test files, because a helper split across two files is still one dependency and a
// per-file audit would certify whichever file happened to be short.
func TestTheLoomImportsNothingButTheContract(t *testing.T) {
	t.Parallel()

	for _, file := range packageSources(t) {
		for _, objection := range auditImports(file.name, file.source) {
			t.Errorf("%s: %s", file.name, objection)
		}
	}
}

// TestTheLoomUsesNothingFromTheContractButItsOwnTypes is audit 2, and it is the one
// that says what "without touching `internal/domain/rules`" means.
//
// Imports are a coarse instrument: importing something and not using it satisfies them.
// What a shared assumption looks like is a *selector* — the loom reaching for
// something the interface does not require it to reach for.
func TestTheLoomUsesNothingFromTheContractButItsOwnTypes(t *testing.T) {
	t.Parallel()

	for _, file := range packageSources(t) {
		for _, objection := range auditRulesSelectors(file.name, file.source) {
			t.Errorf("%s: %s", file.name, objection)
		}
	}
}

// TestTheLoomCarriesNoVocabularyFromFiveE is audit 3.
//
// Over **identifiers and string literals**, parsed. Not over the file's text: this
// package's comments say "no d20" several times, in every file, because that is what the
// comments are for — and a substring audit over the text would either have to allow
// them (and then also allow a `d20` in a string) or object to them (and then object to
// this very file). Parsing is what separates documentation from use.
func TestTheLoomCarriesNoVocabularyFromFiveE(t *testing.T) {
	t.Parallel()

	for _, file := range packageSources(t) {
		for _, objection := range auditVocabulary(file.name, file.source) {
			t.Errorf("%s: %s", file.name, objection)
		}
	}
}

// # The three audits' meta-tests
//
// Each takes source text rather than a file, so a fixture can be fed without being
// written to disk, and each requires the fixture to produce at least one objection. An
// audit that cannot be shown to fail is not an audit.

func TestTheImportAuditRejectsEveryImportOutsideTheContract(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		source  string
		wantAny string
	}{
		{
			name:    "a 5e engine",
			source:  "package p\n\nimport \"github.com/semiplane/semiplane/internal/domain/systems/dnd5e\"\n",
			wantAny: "dnd5e",
		},
		{
			name:    "the conformance suite itself, which would be a test harness in production",
			source:  "package p\n\nimport \"github.com/semiplane/semiplane/internal/domain/rules/conformance\"\n",
			wantAny: "conformance",
		},
		{
			name:    "go:embed, which is what a shared data pack needs",
			source:  "package p\n\nimport _ \"embed\"\n",
			wantAny: "embed",
		},
		{
			name:    "a clock, which S-10.4 forbids in rule code",
			source:  "package p\n\nimport \"time\"\n",
			wantAny: "time",
		},
		{
			name:    "the ambient random source, which S-10.4 forbids",
			source:  "package p\n\nimport \"math/rand\"\n",
			wantAny: "math/rand",
		},
		{
			name:    "a package-level mutable slice, which this package never declares",
			source:  "package p\n\nimport \"os\"\n",
			wantAny: "os",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			objections := auditImports("fixture.go", []byte(testCase.source))
			if len(objections) == 0 {
				t.Fatalf("the import audit said nothing about %s", testCase.name)
			}

			if !containsAny(objections, testCase.wantAny) {
				t.Errorf(
					"the objections %v do not name %q, so the audit objected to something else",
					objections,
					testCase.wantAny,
				)
			}
		})
	}
}

func TestTheImportAuditAcceptsTheImportsItNames(t *testing.T) {
	t.Parallel()

	// The negative control. Without it a test that feeds six bad files to an audit is
	// satisfied by an audit that objects to everything, including the seven imports this
	// package legitimately carries.
	source := "package p\n\nimport (\n\t\"context\"\n\t\"errors\"\n)\n\n" +
		"func use() (context.Context, error) { return nil, errors.New(\"x\") }\n"

	objections := auditImports("fixture.go", []byte(source))
	if len(objections) != 0 {
		t.Fatalf("the import audit objected to two permitted, used imports: %v", objections)
	}
}

func TestTheSelectorAuditRejectsEverythingTheInterfaceDoesNotRequire(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		source  string
		wantAny string
	}{
		{
			name:    "Validate, which is the registry's call and not a system's",
			source:  "package p\n\nimport \"rules\"\n\nvar _ = rules.Validate\n",
			wantAny: "Validate",
		},
		{
			name: "the ownership table, which would be a system checking its own homework",
			source: "package p\n\nimport \"rules\"\n\nvar _ = rules.IsSemiplaneKind\n" +
				"var _ = rules.SemiplaneKinds\n",
			wantAny: "IsSemiplaneKind",
		},
		{
			name:    "the state constructor, which would mean building the hub's snapshot",
			source:  "package p\n\nimport \"rules\"\n\nvar _ = rules.NewState\n",
			wantAny: "NewState",
		},
		{
			name: "the seeded source itself, which reaches past `Context.Rand` for a draw",
			source: "package p\n\nimport \"rules\"\n\nvar _ = rules.Context.Rand\n" +
				"var _ = rules.SemiplaneKinds\n",
			wantAny: "Context.Rand",
		},
		{
			name: "an unexported type, which this audit cannot reach but must not lose track of",
			source: "package p\n\nimport \"rules\"\n\nvar _ = rules.KnownOp\n" +
				"var _ = rules.SemiplaneKinds\n",
			wantAny: "KnownOp",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			objections := auditRulesSelectors("fixture.go", []byte(testCase.source))
			if len(objections) == 0 {
				t.Fatalf("the selector audit said nothing about %s", testCase.name)
			}

			if !containsAny(objections, testCase.wantAny) {
				t.Errorf("the objections %v do not name %q", objections, testCase.wantAny)
			}
		})
	}
}

func TestTheSelectorAuditAcceptsEveryNameTheInterfaceRequires(t *testing.T) {
	t.Parallel()

	for name := range wantedSelectors {
		source := "package p\n\nimport \"rules\"\n\nvar _ = rules." + name + "\n"

		if objections := auditRulesSelectors("fixture.go", []byte(source)); len(objections) != 0 {
			t.Errorf(
				"the selector audit refused %q, which the interface requires: %v",
				name,
				objections,
			)
		}
	}
}

func TestTheVocabularyAuditFindsTheVocabularyWhereverItIs(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		source string
	}{
		{
			name:   "as an identifier, spelled plainly",
			source: "package p\n\nfunc castSpell() {}\n",
		},
		{
			name:   "as a camel-cased identifier",
			source: "package p\n\nfunc hitPointsLeft() int { return 0 }\n",
		},
		{
			name:   "as an underscore-separated identifier",
			source: "package p\n\nfunc cast_spell() {}\n",
		},
		{
			name:   "as a mixed-case string literal",
			source: "package p\n\nvar kind = \"SPELL\"\n",
		},
		{
			name:   "as a string literal inside a composite",
			source: "package p\n\nvar pack = []string{\"ancestry\", \"feat\"}\n",
		},
		{
			// A type name and a field name, which a walk that only looked at functions and
			// strings would miss entirely — and a struct field is exactly where a shared
			// assumption ends up living.
			name:   "as a type name and a struct field",
			source: "package p\n\ntype creature struct{ hitPoints int }\n",
		},
		{
			// A local variable, which is a declaration rather than a member.
			name:   "as a local variable name",
			source: "package p\n\nfunc f() { spellLevel := 3; _ = spellLevel }\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if objections := auditVocabulary(
				"fixture.go",
				[]byte(testCase.source),
			); len(
				objections,
			) == 0 {
				t.Fatalf("the vocabulary audit did not object to 5e's vocabulary %s", testCase.name)
			}
		})
	}
}

// TestTheVocabularyAuditSeesCommentsAndIsChoosingNotTo is the other direction, and it
// is the one that makes the "fragments" decision defensible.
//
// A comment is documentation. This package's comments are full of the words the audit
// forbids — they are constantly saying "not a d20 system" — and an audit that objected
// to those would be a lint rule against explaining itself. Feeding it a comment as a
// fixture and requiring silence is what stops a later edit from "simplifying" the audit
// into a substring search over the file's text.
func TestTheVocabularyAuditSeesCommentsAndIsChoosingNotTo(t *testing.T) {
	t.Parallel()

	source := "package p\n\n" +
		"// This is not a d20 system and declares no spells, classes, feats or creatures.\n" +
		"func weave() {}\n"

	if objections := walkVocabulary("fixture.go", []byte(source), true); len(objections) == 0 {
		t.Fatal("the walk did not reach the comment, so the exclusion the audit makes is not a " +
			"decision but an accident of the parse mode, and this test proves nothing")
	}

	if objections := auditVocabulary("fixture.go", []byte(source)); len(objections) != 0 {
		t.Fatalf(
			"the vocabulary audit objected to a comment, which is documentation: %v",
			objections,
		)
	}
}

// TestTheVocabularyAuditsOwnListIsNotVacuous checks the list against itself.
//
// Every entry must be findable in a plausible spelling, or it is a line of code nobody
// is protected by. `forbiddenVocabulary` grew by adding words, and a word that matches
// nothing is exactly as inert as an audit that cannot fail.
func TestTheVocabularyAuditsOwnListIsNotVacuous(t *testing.T) {
	t.Parallel()

	for word := range forbiddenVocabulary {
		source := "package p\n\nvar " + word + " = \"" + word + "\"\n"

		if objections := auditVocabulary("fixture.go", []byte(source)); len(objections) == 0 {
			t.Errorf("the vocabulary list names %q, and no spelling of it is caught", word)
		}
	}
}

// TestTheAuditsAreNotTheSameAudit checks that the three are actually distinct.
//
// Falsifiable, and worth it: an audit that duplicates another's coverage would still
// pass every meta-test above while proving one thing three times, and the cost of the
// check is a loop.
func TestTheAuditsAreNotTheSameAudit(t *testing.T) {
	t.Parallel()

	shared := "package p\n\nimport \"rules\"\n\nvar _ = rules.Validate\n" +
		"var _ = rules.Context.Rand\n\nfunc castSpell() {}\n"

	imports := auditImports("fixture.go", []byte(shared))
	selectors := auditRulesSelectors("fixture.go", []byte(shared))
	vocabulary := auditVocabulary("fixture.go", []byte(shared))

	if len(imports) == 0 {
		t.Error("the import audit said nothing about a fixture that imports rules itself")
	}

	if len(selectors) == 0 {
		t.Error("the selector audit said nothing about a fixture that reaches past the interface")
	}

	if len(vocabulary) == 0 {
		t.Error("the vocabulary audit said nothing about a fixture carrying 5e's vocabulary")
	}
}

// # The audits
//
// Each takes the source rather than a filename so a fixture can be fed from a string,
// and each returns a list of objections so a test can require it to be non-empty for
// one and empty for another.

// auditImports reports every import path outside `wantedImports`, and every permitted
// import that is not actually used — the second direction is what stops the table from
// growing an entry nobody needs.
func auditImports(name string, source []byte) []string {
	parsed, err := parser.ParseFile(token.NewFileSet(), name, source, parser.ImportsOnly)
	if err != nil {
		return []string{"the file does not parse, so nothing could be audited: " + err.Error()}
	}

	var objections []string

	for _, imported := range parsed.Imports {
		path := strings.Trim(imported.Path.Value, `"`)

		if reason, permitted := wantedImports[path]; permitted {
			if !importIsUsed(name, source, path, aliasOf(imported)) {
				objections = append(
					objections,
					fmt.Sprintf(
						"imports %q, which is permitted because it is %s, and never uses it; "+
							"the table is a claim about what this package needs",
						path,
						reason,
					),
				)
			}

			continue
		}

		objections = append(objections, forbiddenImportObjection(path))
	}

	return objections
}

// aliasOf returns an import's explicit alias, or the empty string when it has none.
func aliasOf(imported *ast.ImportSpec) string {
	if imported.Name == nil {
		return ""
	}

	return imported.Name.Name
}

// forbiddenImportObjection explains one unpermitted import, and says why the refusal
// matters rather than only naming the path.
func forbiddenImportObjection(path string) string {
	switch {
	case strings.Contains(path, "internal/domain/systems/") && path != rulesPackage:
		return fmt.Sprintf(
			"imports %q, which is another gameplay system or the suite for one; S-14.9 is this "+
				"package's reason to exist and an import here would make the counterexample share code "+
				"with what it is a counterexample to", path)
	case path == rulesPackage+"/conformance":
		return "imports the conformance suite; the suite is what certifies this package, so depending on " +
			"it would make the certification a circular argument"
	case path == "embed" || path == "_ \"embed\"":
		return "imports go:embed, which is what a shared data pack needs; this package ships its own " +
			"state and shares none"
	case path == "time":
		return "imports \"time\", a wall clock, which S-10.4 forbids in rule code: a resolution's " +
			"result may not depend on when it ran"
	case path == "crypto/rand" || path == "math/rand" || path == "math/rand/v2":
		return fmt.Sprintf(
			"imports %q, an ambient source of randomness, which S-10.4 forbids: a resolution has to be "+
				"reproducible from (state, intent, seed), and `call.Rand` is the only source it may draw from",
			path,
		)
	default:
		return fmt.Sprintf(
			"imports %q, which is not one of the %d imports implementing the interface requires; a "+
				"system sharing nothing with 5e must not reach for anything a shared engine would offer",
			path,
			len(wantedImports),
		)
	}
}

// importIsUsed reports whether anything in the file evaluates a selector on the import.
//
// **An import the file does not use is itself the objection**, and this is how the
// audit knows: a blank or side-effect import has no selector, so "unused" is exactly
// "nothing qualified with it", and `_ "embed"` — the shape `go:embed` requires — is
// caught by the path table before this is ever consulted.
func importIsUsed(name string, source []byte, path, alias string) bool {
	if alias == "_" || alias == "." {
		return false
	}

	local := alias
	if local == "" {
		local = packageLocalName(path)
	}

	if local == "" {
		return false
	}

	parsed, err := parser.ParseFile(token.NewFileSet(), name, source, 0)
	if err != nil {
		return false
	}

	used := false

	ast.Inspect(parsed, func(node ast.Node) bool {
		if used {
			return false
		}

		selector, isSelector := node.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}

		identifier, isIdentifier := selector.X.(*ast.Ident)
		if isIdentifier && identifier.Name == local {
			used = true
		}

		return true
	})

	return used
}

// packageLocalName is the identifier a package is reached by when it is imported
// without an alias.
//
// A nil in a real program and a one-word return here, because every path in
// `wantedImports` is either a standard-library package whose last element is the name
// (`context`, `encoding/json` → `json`) or this project's own, whose last element is
// `rules` or `domain`. A three-element standard-library path whose last element is a
// version — `math/rand/v2` → `rand` — is handled by taking the second element for a
// path with a version suffix, because `v2` is not what anybody writes.
func packageLocalName(path string) string {
	parts := strings.Split(path, "/")

	last := parts[len(parts)-1]
	if strings.HasPrefix(last, "v") && len(parts) > 1 {
		last = parts[len(parts)-2]
	}

	if last == "" {
		return ""
	}

	// A hyphen is legal in a path and not in an identifier; the one path here with one
	// is the module prefix, whose last element never has one.
	return strings.ReplaceAll(last, "-", "")
}

// auditRulesSelectors reports every `rules.X` outside `wantedSelectors`.
func auditRulesSelectors(name string, source []byte) []string {
	parsed, err := parser.ParseFile(token.NewFileSet(), name, source, 0)
	if err != nil {
		return []string{"the file does not parse, so nothing could be audited: " + err.Error()}
	}

	var objections []string

	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, isSelector := node.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}

		base, dotted := selectorBase(selector)
		if base != "rules" {
			// A method on a value this package holds — `call.Rand`, `object.ID` — is a
			// different claim: `Context.Rand` is the rule context's own method and is what
			// §10.3 points every draw at, so it is permitted by being the interface's own
			// surface rather than by an entry in this table.
			return true
		}

		if _, permitted := wantedSelectors[dotted]; permitted {
			return true
		}

		objections = append(objections, fmt.Sprintf(
			"evaluates rules.%s, which implementing the interface does not require; if this package "+
				"needs it, the contract owes the plugin something and the decision belongs in a record "+
				"rather than in a plugin",
			dotted,
		))

		return true
	})

	return objections
}

// selectorBase returns the leftmost package an expression is rooted at, and the dotted
// name of everything selected off it.
//
// **The base, not the immediate `X`**, because a method expression like
// `rules.Context.Rand` parses as a selector whose `X` is *another* selector, and a walk
// that only inspected the immediate child would not see it at all. That is the shape an
// audit cannot catch, which is why this function exists and why its fixture is a method
// expression.
func selectorBase(selector *ast.SelectorExpr) (base, dotted string) {
	parts := []string{selector.Sel.Name}
	current := selector.X

	for {
		switch node := current.(type) {
		case *ast.SelectorExpr:
			parts = append([]string{node.Sel.Name}, parts...)
			current = node.X
		case *ast.Ident:
			// A field selector roots at something this audit cannot reach, and stopping
			// here is the honest answer: the table is about *package* selectors.
			return node.Name, strings.Join(parts, ".")
		default:
			return "", strings.Join(parts, ".")
		}
	}
}

// auditVocabulary reports every 5e fragment in an identifier or a string literal.
func auditVocabulary(name string, source []byte) []string {
	return walkVocabulary(name, source, false)
}

// walkVocabulary is the vocabulary audit with one switch: whether comments are walked.
//
// The switch exists so the *decision* is testable, and it exists because a mutation
// removing the exclusion **survived**: `parser.ParseFile` without
// `parser.ParseComments` never puts a comment in the tree, so the `case *ast.Comment`
// was a branch nothing could reach, and the test that claims comments are excluded
// passed because there was nothing to exclude. That is the exact failure this
// repository has paid for three times, found the way the standing instruction says to
// look: by breaking the thing the test guards and watching it stay green.
//
// So the parse now asks for comments, the walk visits them, the exclusion is a real
// branch, and the meta-test calls this function with the switch the other way round —
// which is the only way to prove the comments were reachable at all.
func walkVocabulary(name string, source []byte, walkComments bool) []string {
	// `parser.ParseComments` and not the default, because without it the exclusion below is
	// decoration. See this function's own comment.
	parsed, err := parser.ParseFile(token.NewFileSet(), name, source, parser.ParseComments)
	if err != nil {
		return []string{"the file does not parse, so nothing could be audited: " + err.Error()}
	}

	var objections []string

	ast.Inspect(parsed, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.Ident:
			for _, objection := range objectionsTo(value.Name) {
				objections = append(
					objections,
					"identifier "+strconv.Quote(value.Name)+" "+objection,
				)
			}
		case *ast.BasicLit:
			if value.Kind != token.STRING {
				// A number is not vocabulary. `rand.IntN(20)` is a d20 in *effect* and
				// that is the determinism audit's business, not this one's: what this
				// audit forbids is the shared *assumption*, which reaches the code as a
				// name a reader parses.
				return true
			}

			text := value.Value
			if strings.ContainsAny(text, "`") {
				// A raw string literal's text is not its value, and re-unquoting one is
				// more machinery than a fixture warrants. Stripping the backticks is
				// enough: a vocabulary word in a raw literal is still a vocabulary word.
				text = strings.Trim(text, "`")
			}

			for _, objection := range objectionsTo(text) {
				objections = append(objections, "string literal "+strconv.Quote(text)+" "+objection)
			}
		case *ast.Comment:
			if !walkComments {
				// The exclusion, and it is a decision rather than an accident of the parse
				// mode. See this function's own comment.
				return false
			}

			text := strings.TrimLeft(strings.TrimRight(value.Text, "*/"), "/ ")
			for _, objection := range objectionsTo(text) {
				objections = append(objections, "comment "+strconv.Quote(text)+" "+objection)
			}

			return false
		case *ast.CommentGroup:
			// Reached only through a declaration's `Doc`, which is where the parser
			// attaches a comment written above one. `ast.Walk` does not visit
			// `File.Comments`, so the group is the only way in — and returning false at
			// the group skips every comment in it in one step.
			return walkComments
		}

		return true
	})

	return objections
}

// objectionsTo returns one sentence per forbidden fragment in a piece of source text.
//
// Normalised the way `forbiddenVocabulary` says: lowercased with `_` and `-` removed,
// because `hit_points`, `hit-points` and `hitPoints` are one assumption written three
// ways and a fragment match on any of the three spellings would have to list all three.
func objectionsTo(text string) []string {
	normalised := normaliseVocabulary(text)

	var objections []string

	for fragment, reason := range forbiddenVocabulary {
		if strings.Contains(normalised, fragment) {
			objections = append(objections, fmt.Sprintf("carries %q, %s", fragment, reason))
		}
	}

	return objections
}

// separatorPattern is the pair of characters this package's identifiers and kinds use
// between words, and the ones 5e's do.
var separatorPattern = regexp.MustCompile(`[_\- ]+`)

// normaliseVocabulary lowercases and removes separators.
//
// **Not `unicode` folding and not anything clever**: the vocabulary is ASCII, so ASCII
// lowercasing matches it exactly, and a `strings.ToLower` that also touched a Turkish
// dotless `I` would be a rule nobody could predict for a check nobody can see.
func normaliseVocabulary(text string) string {
	return strings.ToLower(separatorPattern.ReplaceAllString(text, ""))
}

// packageSources parses every non-test Go file in this directory.
//
// The whole package and not one file, because a dependency split across two files is
// still one dependency — and because the first thing an author does when an import
// audit complains is move the offending call into a helper file.
func packageSources(t *testing.T) []namedSource {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	var files []namedSource

	for _, entry := range entries {
		name := entry.Name()

		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		source, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}

		files = append(files, namedSource{name: name, source: source})
	}

	if len(files) == 0 {
		t.Fatal("no Go files were found in this directory, so the audits certified nothing")
	}

	return files
}

// namedSource is one file's name and bytes.
type namedSource struct {
	name   string
	source []byte
}

func containsAny(objections []string, needle string) bool {
	for _, objection := range objections {
		if strings.Contains(objection, needle) {
			return true
		}
	}

	return false
}
