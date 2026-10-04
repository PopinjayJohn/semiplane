package demo

import (
	"fmt"
	"maps"
	"slices"

	"github.com/semiplane/semiplane/internal/plugin"
)

// Fingerprints is the ruleset fingerprint of every gameplay system this build
// registers, keyed by system id.
//
// **Built by the composition root and handed in, because that is the only place
// that can build one.** `realtime.FingerprintOf` takes a `realtime.Descriptor`, and
// the four components of a descriptor come from `dnd5e.Engine.Versions()` — a
// method on a concrete engine, deliberately not on `rules.System`, because the
// pack versions are the system's own and a foreign system has its own shape. So
// `cmd/server`'s `plugins.fingerprint()` is the only code in the tree that can
// answer "what does this build resolve under", and this package cannot reach it.
//
// Which is why the seed **never writes a `ruleset_version` of its own**: it looks
// the campaign's `system` up here, and a system with no entry gets the empty
// string. A hardcoded fingerprint is the specific failure the delivery plan names
// — the resume path refuses to start and the demo breaks on first load — and the
// only defence is that the string is not in this repository at all.
type Fingerprints map[string]string

// Resolved reports the fingerprint a campaign with this system id is seeded under,
// and whether this build resolves it at all.
//
// **The empty string and `false` are the answer for an unregistered system, and
// they are a real answer rather than an absence.** Migration 0005 defines the empty
// `ruleset_version` as "state written under no particular ruleset" and
// `realtime.Gate.Inspect` reports it as `Unfingerprinted` without logging an
// error. That is exactly `forgotten-realm` in the shipped demo: it names
// `pathfinder-2e`, its wiki answers 200, and its game refuses by name (§10.8,
// S-14.8). Writing a fingerprint for a system this build does not have would be a
// fiction, and a fiction in this column is what the resume gate compares.
func (f Fingerprints) Resolved(systemID string) (string, bool) {
	fingerprint, known := f[systemID]

	return fingerprint, known
}

// Systems returns the ids this build resolves, in order.
//
// Sorted rather than in map order, for the reason `houserules.Registry.IDs` sorts
// and `observability.Registry.SortedNames` does: a listing printed in map order
// reorders between two runs with nothing having changed, which is a listing nobody
// can diff.
func (f Fingerprints) Systems() []string { return slices.Sorted(maps.Keys(f)) }

// BuildFingerprints answers what this build resolves each registered system under.
//
// `thisBuild` is the fingerprint the composition root computed for the engine it
// registered. **It is applied to every system the registry holds, and the registry
// is required to hold exactly one for that to be true** — see the refusal.
//
// That requirement is the interesting part, and it is a refusal rather than a
// best-effort answer on purpose. The composition root exposes one fingerprint,
// because it holds one engine; a build that registered a second gameplay system
// would need a second, and answering both with the first would write 5e's
// fingerprint into a campaign's row that plays under something else. The gate
// would then compare the wrong semantics and refuse — or worse, not refuse. So a
// second system turns the demo seed red with a message that says exactly this, and
// the fix is in `cmd/server/systems.go`: give each system its own `Versions()`
// lookup and build the map per entry. It is the day this is needed that the shape
// should move, and a seed that quietly guessed would be the alternative that lets
// it go unnoticed until then.
//
// An **empty** registry is not a refusal and produces an **empty** map, because
// that is a build with no gameplay system: every campaign's `system` then resolves
// to nothing, every campaign gets an empty `ruleset_version`, and every campaign's
// wiki still serves. That is S-10.6's survivable case and the demo is correct in
// it.
func BuildFingerprints(systems *plugin.Registry, thisBuild string) (Fingerprints, error) {
	if systems == nil || systems.Empty() {
		return Fingerprints{}, nil
	}

	entries := systems.Systems()
	if len(entries) != 1 {
		ids := make([]string, 0, len(entries))

		for _, entry := range entries {
			ids = append(ids, entry.System.ID().String())
		}

		slices.Sort(ids)

		return nil, fmt.Errorf(
			"%w: this build registers %d gameplay systems (%v) and `demo seed` was handed "+
				"one fingerprint for all of them, which would write the wrong resolution "+
				"semantics into a campaign's ruleset_version; `cmd/server/systems.go` must "+
				"build one fingerprint per registered system",
			ErrIncompleteManifest, len(entries), ids,
		)
	}

	return Fingerprints{entries[0].System.ID().String(): thisBuild}, nil
}
