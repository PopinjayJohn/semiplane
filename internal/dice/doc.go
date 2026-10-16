// Package dice parses and evaluates tabletop dice formulas such as
// "2d20kh1+5", "4d6kh3", "1d12+4" or "(2d6+3)*2".
//
// Grammar (see grammar.go): integer arithmetic with + - * / and
// parentheses, plus dice terms of the form [count]d sides [kh|kl keep]
// (count defaults to 1, e.g. "d20"). Integer division truncates toward
// zero. All rolls are deterministic: evaluation consumes faces from a
// *Roller (see roller.go), never from a global RNG or wall-clock source.
//
// Event-level dice convention: advantage/disadvantage lives outside this
// package. Callers translate the event-level keep spec (internal/events
// KeepSpec, computed by AdvDice: advantage XOR disadvantage rolls 2 dice
// and keeps the highest/lowest, otherwise a straight roll) into a formula
// string using the active pack's die size — e.g. AdvDice(true, false)
// {Count:2, Keep:1, KeepHigh:true} becomes "2d20kh1" for a d20 pack — and
// evaluate it here. This package names no die sizes and parses no prose.
//
// Result.Successes is len(Kept): the numeric-only symbolic-dice seam.
// Symbolic (non-numeric) dice faces are unmodelled; Successes is the int
// placeholder that seam will grow into.
//
// Plugin boundary: per repo-layout rules, internal packages must not import
// pkg/pluginapi. Evaluator therefore does not implement a pluginapi
// interface; it keeps the structurally compatible method shape
// Roll(context.Context, formula string, seed int64) (Result, error) so a
// thin adapter outside internal/ can satisfy the plugin API without this
// package importing it.
//
// Safety caps (named constants in grammar.go): MaxInputLen=64 rejects long
// input before parsing; MaxCount=100, MaxSides=1000 bound each dice term;
// MaxParenDepth=4 bounds parenthesis nesting (checked on the AST after
// parsing); keep is bounded by MaxKeep<=count per term. Every rejection
// names the violated cap.
package dice
