// Package conformance is the published suite every `rules.System` must pass, and the
// certification it prints when one does.
//
// # What this package is
//
// §14's last plugin row is a single sentence with two halves: *"a published
// suite every `System` must pass: determinism, unknown-`kind` tolerance,
// version/migration refusal, GM-only enforcement, `Apply` panic containment —
// **and a system sharing nothing with 5e must pass it without touching
// `internal/domain/rules`.** The test is what proves the plugin system actually
// generalises."* S-14.9 carries the second half as a requirement of its own.
// This package is the first half, and `internal/domain/systems/notfive` is the
// second half's evidence.
//
// A system author runs it as one test:
//
//	func TestMySystemConforms(t *testing.T) {
//	    if err := conformance.Check(t.Context(), conformance.Config{
//	        System:  mysystem.New(),
//	        Scenario: conformance.Scenario{
//	            Objects: []rules.Object{{ID: "spool_1", Kind: "spool", Data: []byte(`{"length":4}`)}},
//	            Intent:  intent,
//	            Call:    call,
//	        },
//	        GMOnly:  []rules.Op{"unwind"},
//	        Classify: func(err error) string {
//	            if errors.Is(err, mysystem.ErrGMOnly) { return conformance.ReasonNotPermitted }
//	            return conformance.ReasonServerError
//	        },
//	        Resume: mydeployment.Rules,
//	    }); err != nil {
//	        t.Fatal(err)
//	    }
//	}
//
// Nothing above reaches into semiplane. `System` is the interface,
// `Config` takes a `rules.System` and four seams the deployment already owns,
// and the result is an error an author can print. **That is the whole promise of
// §10.4's "the only shared code is the interface, the intent and mutation types,
// and the shared conformance suite"** — this file is the third of those three,
// and it is the one a system nobody has heard of is certified against.
//
// # The four seams, and why there is no fifth
//
// Everything the suite cannot know from a `rules.System` arrives as an explicit
// value in `Config`, and the set is closed at four:
//
//   - `Scenario` — one typical resolution. The suite cannot invent a state's
//     objects, an intent's arguments, or a seed that means anything to a system
//     whose vocabulary it does not know; every audit that resolves an intent
//     resolves the author's.
//   - `GMOnly` — §7.2 reserves operations to the GM, and which ones are the
//     system's answer, not this package's. **Required and required non-empty**:
//     an empty list would make audit 4 unable to fail, and an audit no fixture
//     reaches is not an audit.
//   - `Classify` — the map from a system's error to one of the wire's eight
//     words. It belongs to the composition root, which owns both ends, and its
//     absence would leave the suite asserting that a refusal *exists* while the
//     thing that has to happen — a refusal a browser can be told — goes
//     unchecked.
//   - `Resume` — whether a campaign written under one `ruleset_version` may
//     resume under another, and what fingerprint this deployment would persist.
//     §10.8's row is a *deployment* decision: the encoding is `realtime`'s, the
//     column is a store column, and a system has no business knowing either.
//
// A fifth seam is what a suite grows when it starts guessing instead of asking,
// and every guess it makes is a rule the foreign system cannot act on. The
// suite's stance is the opposite: **it asserts only what the interface can
// promise, and takes everything else as an argument.** A knob that can be left
// at its zero value is a knob that will be, and a suite that passes on defaults
// is a suite nobody has to configure and therefore nobody has configured.
//
// # The audits
//
// Five, named by `Audit`, each one a method on `*Suite` so an author can run
// one in a focused test:
//
//   - `AuditDeterminism` — S-14.6. The scenario resolved `Runs` times, byte for
//     byte. The number is not a default: S-14.6 names one hundred, and a knob
//     that lets an author pass with two is a knob that will be set to two.
//   - `AuditUnknownKind` — S-14.7. A tabletop carrying an object of a kind no
//     build registers resolves the scenario anyway: no error, nothing addressed,
//     and the object's bytes untouched. Inert, not fatal — §10.8's last row, and
//     the reason `kind` is safe to extend.
//   - `AuditVersion` — §10.8. A fingerprint that is not this deployment's is
//     refused and the refusal **names both versions**; one this build cannot
//     parse is refused *differently*, because telling a GM to discard a game
//     over an unreadable column is the software inflicting a loss. And ADR 0018:
//     toggling a house rule does not move the fingerprint.
//   - `AuditRole` — §7.2. Every operation the author declared GM-only is refused to a
//     player with `not_permitted` and to nobody else, and every refusal maps into
//     the wire's closed set.
//   - `AuditContainment` — S-10.2. Nothing a resolution did reached the state it
//     was handed, and **no resolution that reported failure returned a
//     mutation** — the rule `rules.go` states and cannot enforce.
//
// # What a `Finding` may say
//
// A `Finding` quotes plugin-authored identifiers: an op name, an object id, a
// refusal's own message. It never quotes a mutation's payload, because a payload
// is where a resolved roll lives (`mutation.go` says so, and S-12.3 forbids it
// reaching anywhere it would be recorded) — a determinism failure names the ops
// and the payload *lengths*, which is enough to localise it and is not the
// answer to a roll nobody asked to be given.
//
// And nothing here is an observability event. `observability.EventAttributes` has
// no field a `Finding` could be passed through, which is the structural half of
// S-12.3 and the reason this package needs no import of `internal/observability`
// to keep the rule.
//
// # Why the containment boundary lives here
//
// `Contain` is not test scaffolding: it is the `recover()` at the `Apply`
// boundary §10.8's table requires, and the composition root's adapter should call
// it rather than write a sixth copy. It is here because the audit that has to
// induce a panic cannot ask a well-behaved system for one — the only system that
// will panic on request is one the suite broke itself — and a boundary whose own
// behaviour is only ever exercised by its author's good behaviour is a boundary
// that has never been tested.
package conformance
