package conformance

import (
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// upgraded is a system whose `RulesetVersion` differs from the one it was wrapped
// from, and which is otherwise that system.
//
// **A decorator rather than a copy.** Every method but `RulesetVersion` is promoted,
// so the "some other build" the version audit asks about still resolves, derives,
// parses and declares exactly as the author's system does — which is what makes the
// fingerprint it produces a *plausible* version of the same system rather than a
// different thing entirely. A fingerprint for a system with no grammar, no views and
// no kinds would be refused by the deployment's own encoder for reasons that have
// nothing to do with drift, and the audit would then be measuring its own fixture.
type upgraded struct {
	rules.System

	version string
}

// RulesetVersion returns the wrapped version, which is the whole of this type.
func (u upgraded) RulesetVersion() string { return u.version }

// upgradeVersion returns the system as some later build would report it.
func upgradeVersion(system rules.System) rules.System {
	return upgraded{
		System:  system,
		version: system.RulesetVersion() + upgradeSuffix,
	}
}

// The compile-time assertion that a decorator is still a system. Cheap, and the
// thing whose absence would be a very quiet failure: a `rules.System` that gained a
// tenth method would make `upgraded` stop implementing it, and the version audit
// would then fail with a build error pointing at a file whose name does not suggest
// it is where the interface is.
var _ rules.System = upgraded{}
