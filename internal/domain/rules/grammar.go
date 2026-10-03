package rules

// Grammar is one system's expression notation, as **data**.
//
// It exists so a client can preview and validate an expression *before* sending it,
// and it is data rather than a parser for one reason, which is the sentence §10.3
// puts first: a d20 system and a 2d6-pool system describe different grammars, and
// **the protocol never assumes d20**. A shared parser here would make every system
// express itself in whichever notation was implemented first, and the alternative —
// no grammar at all — leaves a client with nothing to validate against and a server
// with an error it can only report after the fact.
//
// So the client receives this struct as JSON, tests the text against `Terms`, and
// shows `Example` next to the input; the server still calls `Parse`, which is the
// authority (S-7.3: the client may ask for a roll, it never says what the roll was).
// The pattern is a convenience that can be wrong. `Parse` cannot.
//
// There is no `Notation`-shaped grammar here beyond "a name, some patterns and an
// example" because a notation a client cannot validate is a notation only semiplane
// would have to render. Anything richer belongs in the system's own `Parse`.
type Grammar struct {
	// Notation names the notation, e.g. `d20` or `2d6-pool`. It is a label, not a
	// selector: nothing in this package switches on it, because a name that selects
	// behaviour is a name the contract would have to know.
	Notation string

	// Summary is one sentence describing the notation, shown beside the input. It is
	// display copy and is never parsed.
	Summary string

	// Example is a worked expression in this notation, rendered next to the input
	// so a player can see the shape before typing one.
	//
	// It is documentation, and a system that ships an example its own `Parse`
	// refuses has shipped a broken tooltip. Nothing here can check that without
	// calling into the system, which is why the conformance suite (P1c) does it
	// rather than `Validate`.
	Example string

	// Terms are the shapes a valid expression may take, in declaration order. Order
	// is the system's, so a client can present them in the order the system thinks
	// about them rather than an order derived from a map.
	Terms []Term
}

// Term is one shape a valid expression may take, and the pattern that recognises
// it.
type Term struct {
	// Name identifies the term within the notation, e.g. `roll` or `pool`. It is
	// what a client shows and what an error message can quote.
	Name string

	// Summary is one sentence describing the term, for the same place `Grammar.Summary`
	// is used.
	Summary string

	// Pattern is an RE2 expression that matches the term's **whole** text.
	//
	// Two properties make this safe to ship to a browser. RE2 is linear-time, so a
	// hostile input cannot make a client hang; and a pattern comes from a
	// compiled-in plugin rather than from a vault, so there is nothing here an
	// attacker chose. It is still compiled once at registration (`checkGrammar`),
	// because a pattern that does not compile is a preview that accepts everything —
	// and a client that believes it validated an expression has validated nothing.
	//
	// **Use the JavaScript-compatible subset.** Go's regexp is RE2 and a browser's
	// is not the same language: lookaround and backreferences compile here and
	// behave differently there, and the failure mode is a client that accepts an
	// expression its own engine rejects — or worse, one whose two disagree about what
	// was validated. `(?i)`, character classes, anchors and repetition are the
	// portable core.
	Pattern string
}

// Valid reports whether the grammar declares something a client could validate
// against.
//
// A predicate, as with `ID.Valid`; `checkGrammar` is what registration asks, and it
// also compiles the patterns.
func (g Grammar) Valid() bool {
	return g.Notation != "" && len(g.Terms) > 0
}

// Expr is one parsed expression in a system's own notation.
//
// **The three payload fields are the system's and semiplane's is the first.** `Owner`
// is recorded at parse time so a hub can refuse to hand a 2d6-pool expression to a
// d20 system, which is S-10.3's rule ("a UI plugin may only emit operations some
// gameplay system already resolves") applied to the notation rather than the
// operation — the same accident one level down, since an expression parsed by one
// system's grammar and resolved by another's is a roll whose meaning changed in
// flight.
//
// `Node` is `any` on purpose and the temptation to type it is the mistake to avoid.
// A concrete expression tree in this package would be a d20 tree with a d20's node
// kinds, and every system that did not fit would either wrap it or fork it — which
// is the hardcoded-parser outcome §10.3 rules out. `any` says what is true: the
// value is a system value, semiplane routes it back to the same system, and nobody
// else may read it. A `json.RawMessage` would have been the alternative and would
// have forced every system to serialise a tree it then had to parse again.
type Expr struct {
	// owner is the system that parsed it. Unexported so the only way to build one is
	// `NewExpr`, which means every `Expr` names the grammar it came from.
	owner ID

	// Notation is the `Grammar.Notation` it was parsed under, so a mismatch between
	// a system's declared notation and the one it actually used is visible in a
	// value rather than inferred.
	Notation string

	// Source is the text exactly as the client wrote it.
	//
	// Kept because it is the only part of an expression that can be shown back to a
	// player, logged in an audit trail, or re-parsed after a system upgrade — and
	// because a parse that could not reproduce its own input is a parse nobody can
	// audit (§16.3's roll log).
	Source string

	// Node is the system's parse tree, opaque to semiplane. See the type comment.
	Node any
}

// NewExpr returns a parsed expression for the system that parsed it.
//
// The one constructor, exported so a system's `Parse` can build what its own
// signature returns. A system that needed to fabricate an expression it did not parse
// would be a system forging its own input, and keeping the owner unexported means
// the `Owner` a hub routes by always names whoever really parsed it.
func NewExpr(owner ID, notation, source string, node any) Expr {
	return Expr{owner: owner, Notation: notation, Source: source, Node: node}
}

// Owner reports which system's grammar parsed this expression.
//
// Routing by this rather than by "the system the hub happened to call" is what makes
// the refusal above expressible: a hub that resolves an intent carrying an expression
// compares `Expr.Owner()` with the system it is about to call, and refuses a
// mismatch rather than resolving one system's notation with another's rules.
func (e Expr) Owner() ID {
	return e.owner
}

// Valid reports whether the expression is one a resolver can act on.
//
// An expression needs an owner, a notation and a source. The zero `Expr` is
// therefore **invalid**, which is the right answer twice over: a caller that forgot
// to parse has nothing to resolve, and an `Expr` with no owner is precisely the
// forger's — it would be the value a plugin hands the hub claiming to be its own
// parse of someone else's notation.
func (e Expr) Valid() bool {
	return e.owner.Valid() && e.Notation != "" && e.Source != ""
}

// String renders the source, which is what a player typed and what an audit trail
// records.
//
// **Never the tree.** `Node` is a system's parse of attacker-influenceable text, and
// S-12.3 forbids an event carrying dice results: a tree printed into a log is
// exactly where a resolved roll would leak from. The source is the text the client
// already holds, and re-deriving the result from it needs the seed — which is in the
// `Context`, and is printed in full precisely so this audit is possible.
func (e Expr) String() string {
	return e.Source
}
