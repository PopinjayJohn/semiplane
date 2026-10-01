package ext

// Dice returns the definition for `{{dice:1d20+5}}`.
//
// **The rendered element is a slot and never a number.** That is the single most
// important thing about this extension, and the reason is the audit trail. The
// architecture puts dice on the server: the client sends an *intent* to roll, the
// server validates it, applies it, increments `version` and broadcasts the result
// (S-7.1), and no event carries a dice result the server did not produce. A roll
// computed during rendering would be a number produced by whatever process happened
// to serve the page — different on every request, uncountable, unauditable — and
// the render cache would then be caching a random number, which breaks S-5.2's
// content-hash key as well as the audit trail.
//
// The argument is carried in `data-arg` and the element's text is empty, so:
//
//   - There is no result anywhere in the output. A reader who inspects the DOM, or
//     a screen reader, or the page source, gets no number to mistake for a roll.
//   - Two renders of the same bytes are byte-identical, which is what makes
//     S-5.2's content-hash cache key meaningful and the salted `ETag` (0016) stable.
//   - The live layer (phase 7) replaces the slot's contents with the result of a
//     server-rolled intent, keyed on `data-ref-index`. It does not compute one, and
//     it does not display an optimistic result before the server's `applied` frame
//     arrives.
//
// The expression grammar belongs to the gameplay plugin, not here: a d20 system and
// a 2d6-pool system describe different grammars, and the protocol never assumes d20.
// So the argument is carried verbatim and never parsed — which is also why
// `{{dice:1d20+5}}` and `{{dice:4d6 drop lowest}}` are both accepted without this
// package having an opinion about either.
func Dice() Definition {
	return Definition{
		Kind:    KindDice,
		Trigger: openBrace,
		Name:    "dice",
		Write:   OperandSlot("span"),
	}
}
