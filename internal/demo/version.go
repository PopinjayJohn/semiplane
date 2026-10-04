package demo

import (
	"fmt"
	"strconv"
)

// SchemaVersion is the manifest schema this build reads.
//
// **A constant, and it moves when the keys move** rather than when the product
// does. `Manifest.Schema` and `Product` are two different questions — "does this
// build know what these fields mean" and "were these two built together" — and
// merging them is how a build that renamed a key goes on accepting manifests that
// name the old one. A change to the shape below is a change to this number, and
// the refusal `Manifest.check` produces names both.
const SchemaVersion = 1

// The two version refusals. Both name **both** versions, and that is the whole of
// what makes them actionable: the operator has an artefact and a binary and needs
// to know which pair they are holding, and neither number appears anywhere else —
// not in the rail's version line, which is the product's and not the artefact's.
var (
	// ErrVersionSkew is a manifest built for a different release than this binary.
	//
	// **A refusal and not a warning**, and the failure it prevents is the quiet
	// one: a vault populated by one release, rendering against another, with every
	// page answering 200 and nothing in any log to say so.
	ErrVersionSkew = fmt.Errorf(
		"%w: the demo artefact and this binary are different releases",
		errDemo,
	)

	// ErrSchemaMismatch is a manifest whose *shape* this build does not read.
	//
	// **A separate sentinel under the same umbrella, and the reason is that
	// `Reset` treats the two differently.** A release mismatch is a reason to reset,
	// so `Reset` reports it and continues. A schema mismatch means the campaign and
	// account names in this document may not be the ones the artefact created — the
	// parser may have read a renamed field — so a reset that continued would delete
	// whatever it *could* read, which is the worst possible outcome for a command
	// whose whole job is to remove exactly what the seed added.
	ErrSchemaMismatch = fmt.Errorf(
		"%w: this build reads a different manifest schema",
		ErrVersionSkew,
	)

	// ErrAlreadySeeded is an instance that already has what the manifest declares.
	ErrAlreadySeeded = fmt.Errorf("%w: this instance is already seeded", errDemo)
)

// UnversionedBinary is what a binary that stamps no version reports as its own.
//
// **`productVersion` in `cmd/server` is an empty string until a release workflow
// injects one**, and that is the answer rather than `0.0.0`. It changes this
// package's behaviour in exactly one way and the change is stated here rather than
// discovered: an artefact naming a version this binary cannot name is *not* a
// mismatch, because there is nothing to compare against, so the seed says so out
// loud and proceeds. Everything else — two releases that disagree, a schema this
// build does not read — is a refusal.
const UnversionedBinary = "(this build carries no version)"

// Check compares the manifest's declared release against the binary's own and
// refuses a disagreement.
//
// **Both numbers are in the message, always**, and neither is shortened away when
// one of them is `UnversionedBinary`: the operator holding an artefact and a binary
// has to be able to tell which two they are from one line, and a message that says
// only "these do not match" sends them to the release page to work out why.
//
// The third return value is the warning a versionless binary produces instead of a
// refusal. It is returned rather than logged because this package has no logger:
// the only thing that prints for an operator is the command, and a warning the
// command might drop is not a warning.
func Check(manifest Manifest, binaryVersion string) (warning string, err error) {
	if manifest.Schema != SchemaVersion {
		return "", fmt.Errorf(
			"%w: the artefact declares manifest schema %s, this build reads schema %d",
			ErrSchemaMismatch, strconv.Itoa(manifest.Schema), SchemaVersion,
		)
	}

	ours := binaryVersion
	if ours == "" {
		ours = UnversionedBinary
	}

	if manifest.Product == ours {
		return "", nil
	}

	if ours == UnversionedBinary {
		return fmt.Sprintf(
			"semiplane: the binary is %s, so the demo artefact built for %q cannot be checked "+
				"against it; proceeding, and pages will render against whatever this build "+
				"compiled in",
			ours, manifest.Product,
		), nil
	}

	return "", fmt.Errorf(
		"%w: the demo artefact declares product %q and this binary is %q; extract the "+
			"artefact published for this release, or point at the one this release published",
		ErrVersionSkew, manifest.Product, ours,
	)
}
