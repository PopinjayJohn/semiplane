package rules

import (
	"cmp"
	"fmt"
	"slices"
	"unicode"
)

// maxObjectIDLen mirrors `realtime`'s placement-id bound of 128 bytes.
//
// Mirrored **on purpose and to the letter**, including the absence of a character
// restriction beyond control characters. `ObjectID` is the same value as
// `realtime.PlacementID` — a game object instance's id within one campaign — and
// this package cannot import the one that mints it, so the two rules have to agree
// by construction and by comment. They could have disagreed: a stricter rule here
// would have meant a state the hub happily accepted and the system refused, and that
// refusal would have looked like a plugin bug rather than a disagreement between
// two packages about a string.
//
// The characters this allows are therefore the characters a placement id already has
// everywhere else in this project. Whether that set is the right one is an argument
// about `realtime.PlacementID`, and this package is not the place to have it.
const maxObjectIDLen = 128

// ObjectID names one game object instance inside one campaign: a token, a marker,
// a scene.
//
// It is **not** a page path. §4.2 forbids writing a placement to a file, so this
// string never becomes a filename and never reaches `os.Root`: the game object is a
// page, and the placement is that page's instance. The two are related by a field a
// system interprets in its own `Data`, never by this value.
//
// A distinct type from `realtime.PlacementID` so that converting between them is a
// line somebody wrote and reviewed, in one place, rather than an assignment that
// compiles because both happen to be strings.
type ObjectID string

// ErrInvalidObject is returned for an object id this build will not carry.
var ErrInvalidObject = fmt.Errorf(
	"%w: the game object id is not a usable identifier",
	ErrMalformedSystem,
)

// ErrDuplicateObject is returned by `NewState` when the input named one object id
// twice.
//
// Kept rather than deduplicated, because there is no defensible winner. Keeping the
// last is a state that resolves against whichever object the caller happened to
// collect last, keeping the first is the same question with the other answer, and
// "first" is not a rule anyone could remember while debugging a token whose HP is
// wrong.
var ErrDuplicateObject = fmt.Errorf("%w: two game objects share one id", ErrMalformedSystem)

// String returns the stored text, verbatim.
//
// Identity and not a validity check, as with `ID.String`.
func (id ObjectID) String() string {
	return string(id)
}

// Valid reports whether id is usable as a game object id.
//
// Non-empty, at most 128 bytes, and free of control characters — the rule
// `realtime.PlacementID.valid` enforces, restated for the reason named at
// `maxObjectIDLen`.
//
// The control character is the one that is not obvious. An object id is the
// `placement` field of every `delta` and `applied` frame and it is a value a log
// line will quote, so an id carrying a newline produces a line that lies about where
// it breaks and one carrying any other control character cannot be read back out of
// a terminal at all.
func (id ObjectID) Valid() bool {
	if id == "" || len(id) > maxObjectIDLen {
		return false
	}

	for _, char := range string(id) {
		if unicode.IsControl(char) {
			return false
		}
	}

	return true
}

// Object is one game object instance as a system sees it.
//
// **`Data` is opaque to semiplane, and that is the boundary this type draws.** A
// placement's position and hit points are runtime facts §4.2 forbids writing to a
// file, and what else belongs in one is a rules question: a 5e creature's action
// economy and a Starfinder's are not the same shape, and a union type here would be
// a list every new system has to edit. So semiplane owns the envelope — the id, the
// kind, and the ordering — and the system owns the body, which it encodes however
// it likes and decodes itself.
//
// The `Kind` is in the envelope rather than inside `Data` for the reason §10.2.1
// gives: the kind is what the hub renders a token or a scene from, and it is what a
// page's front matter declares, so it has to be readable without the system's
// cooperation. `KindToken` and `KindScene` are the two the client reads on every
// frame.
type Object struct {
	// ID names the object within its campaign. Unique within a `State`, and
	// `NewState` refuses an input where it is not.
	ID ObjectID

	// Kind is what the object is. Semiplane's own kinds are the ones the hub can
	// render without asking; a rules-content kind is the system's, and semiplane
	// only knows it as a name.
	Kind Kind

	// Data is the system's own encoding of this object's runtime state.
	//
	// Read-only by convention and by documentation, and the reason a `State` copies
	// rather than aliases: every accessor hands out a fresh `Object` with a fresh
	// `Data`, so writing to one is invisible to every other holder. An empty
	// `Data` is legal and means the object carries nothing the system needs beyond
	// its identity and kind.
	Data []byte
}

// State is one read-only snapshot of one campaign's tabletop.
//
// **Both fields are unexported, and that is the load-bearing decision in this
// file.** S-10.2 wants a panic inside `Apply` to leave `campaign_state`
// byte-identical, and the half of that a type can guarantee is the absence of a
// write path. A `State` with an exported `[]Object` would be handed to a plugin by
// value and would still share its backing array: the plugin writes one byte into
// `Objects[0].Data[0]` and the hub's own copy of the state is corrupted, with no
// compiler complaint and no lock involved. With the slice unexported and every
// accessor returning a copy, there is no field to write to.
//
// The cost is a small allocation per read, which is the right trade at this scale —
// a tabletop holds tens of placements, not tens of thousands — and the alternative's
// cost is a correctness property held by a comment.
//
// A zero `State` is a valid, empty snapshot at revision 0, which is what a campaign
// whose game has never started should hand a system.
type State struct {
	revision uint64
	objects  []Object
}

// NewState returns a snapshot at revision carrying objects, sorted by id.
//
// It **sorts** rather than trusting the caller's order, and that is not
// convenience. The hub collects objects from a map — `realtime.CampaignState` holds
// its placements in one — and Go randomises map iteration, so a snapshot built in
// map order is a snapshot whose order differs per process. A system that iterates
// its state and picks the first match would then resolve the same intent to
// different mutations on different machines, which is precisely what S-14.6 forbids
// and precisely what "no map iteration" in S-10.4 is about. Sorting here makes the
// order a property of the *contents* rather than of the collection.
//
// It also **copies**: the input slice and every `Object.Data` within it, so a caller
// that keeps its collection and mutates it afterwards cannot change a snapshot it
// has already handed out.
//
// Two objects with one id are refused. Keeping the last, or the first, is a state
// that resolves against whichever object the caller happened to collect in which
// order — and there is no order to appeal to.
func NewState(revision uint64, objects []Object) (State, error) {
	copied := slices.Clone(objects)

	for idx := range copied {
		copied[idx].Data = slices.Clone(copied[idx].Data)
	}

	// Sort before the duplicate check so the refusal names the neighbours either
	// way, and so the check does not depend on the caller's order.
	slices.SortFunc(copied, compareObject)

	for idx := 1; idx < len(copied); idx++ {
		if copied[idx].ID == copied[idx-1].ID {
			return State{}, fmt.Errorf("%w: %q", ErrDuplicateObject, copied[idx].ID)
		}
	}

	return State{revision: revision, objects: copied}, nil
}

// compareObject orders objects by id, for `slices.SortFunc`.
//
// `cmp.Compare` rather than `<` so the ordering is the byte-wise one every other
// ordered walk in this project uses and cannot drift from it.
func compareObject(a, b Object) int {
	return cmp.Compare(a.ID, b.ID)
}

// compareObjectID orders an object against a wanted id, for `slices.BinarySearch`.
func compareObjectID(object Object, want ObjectID) int {
	return cmp.Compare(object.ID, want)
}

// Revision is the revision of the campaign's state document this snapshot was
// taken at.
//
// The hub's counter, and **not** per-object currency: S-7.2 makes the version the
// ordering authority and versions are per placement, so no single number describes
// how current a tabletop is. A system that wants to know whether two intents landed
// in the same snapshot reads this; a system that wants a placement's currency
// reads it from the version the hub checks the intent's claim against, which is not
// this field and not reachable from here.
func (s State) Revision() uint64 {
	return s.revision
}

// Len reports how many objects the snapshot carries.
func (s State) Len() int {
	return len(s.objects)
}

// Lookup returns the object with this id, and whether there was one.
//
// A **copy**, always. See the type comment.
func (s State) Lookup(id ObjectID) (Object, bool) {
	at, found := slices.BinarySearchFunc(s.objects, id, compareObjectID)
	if !found {
		return Object{}, false
	}

	return cloneObject(s.objects[at]), true
}

// Objects returns every object, in id order.
//
// A **copy of the slice and of every `Data` within it**, always. See the type
// comment.
func (s State) Objects() []Object {
	return cloneObjects(s.objects)
}

// OfKind returns the objects of one kind, in id order.
//
// A **copy**, always, and a fresh slice rather than a view: the result is a
// projection built here, so there is nothing of the caller's to alias.
//
// Unknown kinds return nothing rather than an error. S-3.3 and §10.8 make an unknown
// kind inert — a page whose kind no build registers degrades to prose, content
// intact — and a system asking for a kind this campaign has none of is the same
// question asked in the other order.
func (s State) OfKind(kind Kind) []Object {
	matching := make([]Object, 0, len(s.objects))

	for idx := range s.objects {
		if s.objects[idx].Kind == kind {
			matching = append(matching, cloneObject(s.objects[idx]))
		}
	}

	return matching
}

// cloneObjects returns a slice of copies of every object.
func cloneObjects(objects []Object) []Object {
	copied := make([]Object, len(objects))

	for idx := range objects {
		copied[idx] = cloneObject(objects[idx])
	}

	return copied
}

// cloneObject returns a copy of one object, sharing no mutable state with it.
//
// `Data` is the only mutable field, and the reason this exists is that a value copy
// of a struct containing a slice shares its backing array — the argument
// `realtime.Placement.clone` makes for the same field.
func cloneObject(object Object) Object {
	object.Data = slices.Clone(object.Data)

	return object
}
