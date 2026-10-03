package houserules_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/domain/rules/houserules"
)

// TestTheConflictLineNamesBothModulesAndCarriesNoConfiguration is S-12.3, asserted on
// the bytes that reach a log handler.
//
// §10.5 and ADR 0018 both require a conflict to be "logged with both module IDs". The
// half of that which is easy to get right is naming them; the half that is not is
// naming *nothing else*. S-12.3 says no event carries secret content, file contents or
// dice results, and the finding this repository keeps re-making is putting the thing
// that failed into a log aggregator: a `slog.Any("config", module.Config)` compiles,
// ships, and puts a document a GM's own browser wrote into whatever collects the
// lines.
//
// So the fixture is built to make that mistake *tempting*. Both modules' configurations
// are JSON objects carrying a free-text note that looks exactly like a secret callout
// body, each module **decodes that note** — so the value is in scope, is the kind of
// thing a module would put in a message when reporting a problem — and both modules
// conflict, so the line is definitely written. The assertions are then:
//
//   - both module ids appear, as their own attributes, so "logged with both module IDs"
//     is a claim about the rendered line and not about a struct field;
//   - the setting appears, so the line says *what* was contested;
//   - both positions appear, because a tie on `position` was decided by `module_id` and
//     a reader cannot see that from the ids alone;
//   - the note does **not** appear, in whole or in part — not the passphrase, not the
//     callout marker, not the surrounding sentence.
//
// The level is asserted too, because §10.5 asks for the conflict to be logged and not
// for the campaign to be refused: a conflict is a *defined* outcome, so a line at error
// would make a legal configuration look like a fault in every dashboard that alerts on
// error.
func TestTheConflictLineNamesBothModulesAndCarriesNoConfiguration(t *testing.T) {
	t.Parallel()

	// Written to look like a `[!secret]` callout body, because the value that finds
	// its way into a log line is nearly always the one that looks like it should.
	const secret = "the passphrase is hunter2"

	var logged bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))

	registry := registryOf(t,
		configuredToggle("flanking-optional"),
		configuredToggle("averaging"),
	)

	effective, err := registry.Apply(t.Context(), []determinism.Module{
		enabled(configured(module("flanking-optional", 0), secret)),
		enabled(configured(module("averaging", 3), secret)),
	}, logger)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	if len(effective.Conflicts()) != 1 {
		t.Fatalf("the fixture recorded %s, want exactly one conflict, or nothing is logged "+
			"and the assertions below would pass vacuously",
			renderedConflicts(effective.Conflicts()))
	}

	lines := nonEmptyLines(logged.String())
	if len(lines) != 1 {
		t.Fatalf(
			"the handler received %d lines, want one per conflict: %s",
			len(lines),
			logged.String(),
		)
	}

	line := lines[0]

	for _, wanted := range []string{
		`"houserules.conflict"`,
		`"toggle flanking_optional"`,
		`"flanking-optional"`,
		`"averaging"`,
		`"winner_position":0`,
		`"shadowed_position":3`,
		`"level":"WARN"`,
	} {
		if !strings.Contains(line, wanted) {
			t.Errorf("the conflict line\n%s\ndoes not contain %s: §10.5 and ADR 0018 both require "+
				"the conflict to be logged with both module ids, and a reader cannot see that a "+
				"tie on position was decided by module id without the positions", line, wanted)
		}
	}

	// The refusal, and the reason the fixture decodes the note: the value is in scope,
	// it is the natural thing to log when a module reports a problem, and it came from
	// a row a browser wrote.
	for _, forbidden := range []string{
		"hunter2",
		"passphrase",
		secret,
		"[!secret]",
		"\"note\"",
		"config",
	} {
		if strings.Contains(line, forbidden) {
			t.Errorf("the conflict line carries %q:\n%s\nS-12.3 forbids an event carrying secret "+
				"content, and a module's configuration is a document this package does not own "+
				"and has no business putting in a log", forbidden, line)
		}
	}
}

// TestTheConflictLineIsOneLinePerShadowedModule is the count the test above relies
// on, kept as its own assertion so a failure says which of the two broke.
//
// Three modules declaring one setting is two conflicts and therefore two lines. If a
// future change collapses them into one line naming a joined list of losers, the pair
// §10.5 asks for stops being visible on the line, and the only signal is this count.
func TestTheConflictLineIsOneLinePerShadowedModule(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))

	registry := registryOf(t,
		declaring("first", changeOfToggle("flanking_optional", true)),
		declaring("second", changeOfToggle("flanking_optional", false)),
		declaring("third", changeOfToggle("flanking_optional", false)),
	)

	if _, err := registry.Apply(t.Context(), []determinism.Module{
		enabled(module("first", 0)),
		enabled(module("second", 1)),
		enabled(module("third", 2)),
	}, logger); err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	lines := nonEmptyLines(logged.String())
	if len(lines) != 2 {
		t.Fatalf("the handler received %d lines, want two: three modules declaring one setting is "+
			"two pairs, and a line that names a winner and a list of losers cannot be read as "+
			"either pair.\n%s", len(lines), logged.String())
	}

	// Both lines name the same winner: neither the second nor the third module lost to
	// the module immediately before it, they both lost to the first.
	for index, line := range lines {
		if !strings.Contains(line, `"winner":"first"`) {
			t.Errorf("line %d does not name %q as the winner:\n%s", index, "first", line)
		}
	}

	if !strings.Contains(lines[0], `"shadowed":"second"`) ||
		!strings.Contains(lines[1], `"shadowed":"third"`) {
		t.Errorf("the two lines do not name %q and %q as the shadowed modules, in the order the "+
			"declarations reached the setting:\n%s\n%s", "second", "third", lines[0], lines[1])
	}
}

// configuredToggle returns a module that reads its configuration — the whole object —
// and toggles the given setting.
//
// **It decodes the free-text note on purpose.** A fixture whose module ignored its
// configuration would make "the note is not logged" true for the wrong reason: there
// would be no value in scope to log. Reading it is what makes
// `TestTheConflictLineNamesBothModulesAndCarriesNoConfiguration` a real constraint on
// this package rather than on a module that never looked.
func configuredToggle(id rules.ID) *houserules.Definition {
	return scoped(
		id,
		determinism.Scope{determinism.CapToggle},
		func(config json.RawMessage) ([]houserules.Change, error) {
			var declared struct {
				Value bool   `json:"value"`
				Note  string `json:"note"`
			}

			if err := json.Unmarshal(config, &declared); err != nil {
				return nil, err
			}

			// Touched, so a reader cannot tell from the code whether the note was used;
			// what matters is that it was available. The compiler keeps the read and the
			// package needs no statement that the note is deliberately ignored, because
			// what this test asserts is where the line does *not* carry it.
			_ = declared.Note

			return []houserules.Change{
				{Kind: determinism.CapToggle, Key: "flanking_optional", Toggle: &declared.Value},
			}, nil
		},
	)
}

// configured returns an enabled row carrying the given note in its configuration.
func configured(module determinism.Module, note string) determinism.Module {
	module.Enabled = true
	module.Config = json.RawMessage(`{"value":true,"note":"` + note + `"}`)

	return module
}

// nonEmptyLines splits rendered log output into the lines that carry something.
func nonEmptyLines(rendered string) []string {
	lines := make([]string, 0, 2)

	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}

	return lines
}
