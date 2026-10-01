package ext

// Statblock returns the definition for `{{statblock:Name}}`.
//
// The argument is the *name* of a game object, and it lands in a slot rather than
// in a rendered stat block. The stat block itself is a rules question: which
// fields it has, in what order, whether an ability score is shown as a modifier
// or as a number, is the gameplay plugin's business and phase 8's (S-2.4, S-2.5).
// A renderer that emitted a stat block would be a second implementation of a rules
// system, and the one that would be wrong: a d20 system and a 2d6-pool system
// describe different objects, and this package knows about neither.
//
// So the slot carries the argument in `data-arg` and nothing else. The visible text
// is empty, because a stat block that has not been rendered has nothing truthful to
// display — and an empty slot is also the permission-neutral answer, since a slot
// showing a looked-up title would make the page's bytes depend on another document
// (ADR 0017).
//
// A `{{statblock}}` with no argument is a directive with no operand and is
// rendered as such rather than as an empty slot: the scanner refuses it, so the
// author's braces reach the page as the braces they typed. S-3.3's inertness,
// applied to a directive.
func Statblock() Definition {
	return Definition{
		Kind:    KindStatblock,
		Trigger: openBrace,
		Name:    "statblock",
		Write:   OperandSlot("span"),
	}
}
