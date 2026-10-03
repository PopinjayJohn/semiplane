package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// Fingerprint renders everything a snapshot exposes, through its exported
// accessors and nothing else.
//
// It exists because "the state is byte-identical" is not a question a `rules.State`
// can answer about itself: its fields are unexported by design, so there is no
// comparison to make, and `reflect.DeepEqual` over a value would compare a slice
// header — which is the very thing that must never be shared. This reads the
// state the only way a resolver can, through copies, and prints what they say.
//
// **Deliberately not an encoder this package owns.** §14 asks whether a panic
// inside `Apply` leaves `campaign_state` byte-identical, and the honest reading is
// that the bytes are the *hub's* document — `realtime.Document`, which owns
// persistence and its `documentMagic`. A second encoder here would be a second
// answer to "what is a campaign's state", and the two would drift the first time a
// field was added. A rendering is not an encoding: it is a comparison built where
// the comparison is made, out of the accessors, so adding a field to
// `rules.Object` changes this string without changing anything persisted.
//
// Printable, unlike `fingerprintMutations`, because a `rules.Object`'s data is a
// placement's runtime facts — a position, a hit-point count, whatever this system's
// body holds — and the suite already knows all of them from the scenario it built
// them out of. There is no secret here to withhold, only an amount of noise.
func Fingerprint(state rules.State) string {
	var out strings.Builder

	fmt.Fprintf(&out, "revision %d, %d objects\n", state.Revision(), state.Len())

	// `Objects` is in id order by construction — `NewState` sorts before it returns
	// and `slices.Clone` preserves order — so this walk is in a fixed order and the
	// string is comparable. A rendering that iterated a map would produce a different
	// string for the same state on different runs, and the containment audit would
	// then fail a system that mutated nothing.
	for _, object := range state.Objects() {
		fmt.Fprintf(&out, "%s %s %q\n", object.ID, object.Kind, object.Data)
	}

	return out.String()
}

// fingerprintMutations reduces a resolution's answer to one comparable digest.
//
// A digest rather than a rendering, and the difference is the whole reason this is
// a separate function: a payload is where a **resolved roll** lives (S-12.3, and
// `mutation.go` on `Mutation.String`), so the bytes have to be compared without
// being printed. A rendering that printed them would be the one place in this
// package where a dice result reached something a CI log kept. Comparing them is
// not printing them, so the audit can be exact and quiet at the same time.
//
// The pre-image is length-prefixed and separator-terminated so that two different
// lists cannot hash to the same digest: `("ab", "")` and `("a", "b")` are different
// answers and must not collide.
func fingerprintMutations(mutations []rules.Mutation) string {
	var preimage strings.Builder

	preimage.WriteString(strconv.Itoa(len(mutations)))
	preimage.WriteByte(0)

	for _, mutation := range mutations {
		preimage.WriteString(mutation.Target.String())
		preimage.WriteByte(0)
		preimage.WriteString(mutation.Op.String())
		preimage.WriteByte(0)
		preimage.WriteString(strconv.FormatBool(mutation.Remove))
		preimage.WriteByte(0)
		preimage.WriteString(strconv.Itoa(len(mutation.Args)))
		preimage.WriteByte(0)
		preimage.Write(mutation.Args)
		preimage.WriteByte(0)
	}

	digest := sha256.Sum256([]byte(preimage.String()))

	// Sixteen hex characters: eight bytes, which is more than enough to notice that
	// two runs differ and few enough that a failing audit's message stays readable.
	return hex.EncodeToString(digest[:8])
}
