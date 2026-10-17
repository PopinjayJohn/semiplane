// Package rules is the server-authoritative rules engine graduated from the
// spike (spike/engine, never merged): structural hook triggers over the
// closed event/state ontology in docs/rules-ontology.md Part I, with every
// game rule arriving via a *Pack (pack.go) loaded from vault data.
//
// Pipeline per roll:
//
//	pack dice spec (count/size/keep) -> seeded Roller -> hook reducers
//	(priority order, lower first, id tiebreak) -> tier-ordered breakdown
//
// Non-negotiables (ontology Part I §0), enforced here:
//
//   - Structural triggers only: a hook fires iff its On event kind matches
//     and its When expression evaluates true over a typed Ctx. No prose,
//     regex, or NLP at roll time, ever.
//   - Pack-relative refusal: unknown event kind, ctx path, verb, slot, tier,
//     condition key, attribute, source, damage type, or resource is a hard
//     authoring-time error from ValidateHook, judged against the active pack.
//     Never guessed at roll time.
//   - Damage slots: every contribution declares dice|flat (events.Slot). On a
//     crit only dice roll twice; flats never double. Display order comes from
//     pack precedence; every contribution renders one breakdown line, so the
//     GM toggle (?breakdown=1) stays auditable.
//   - Pure reducers: hooks are (state, event) -> (state, effects). The only
//     nondeterminism is the threaded *Roller (shared with internal/dice:
//     face queue in tests, deterministic fallback otherwise; explicitly
//     seeded rollers honor SEMIPLANE_TEST_SEED via dice.SeedFromEnv).
//     No wall-clock, no I/O: the expr sandbox disables now/date/duration
//     and caps AST nodes.
//   - Event-level dice: advantage/disadvantage cancel to a straight roll and
//     never stack. Hooks add contributions; they never touch the dice. The
//     grant_advantage/grant_disadvantage verbs are therefore REJECTED at
//     validation with a pointer to the adv/dis roll-request fields.
//
// Phases: hooks with On∋attack run pre-roll (expand_crit_range widens the
// pack crit threshold to the minimum of all firing `to` values;
// spend_resource/apply_condition resolve before the roll). Hooks with
// On∋damage run post-hit (add_damage, redirect_damage, spend_resource,
// apply_condition). add_damage/redirect_damage on an attack-phase hook and
// expand_crit_range on a damage-phase hook are ignored; expand_crit_range is
// additionally rejected at validation unless On∋attack.
//
// Redirects (hook redirect_damage ops plus request-level DamageMod entries)
// apply AFTER the tier sum: immunity zeroes, else resistance halves (floor)
// then vulnerability doubles. Multiple same-kind redirects do not stack.
//
// once_per_turn latches key on (hook id, turn actor, round) per tech-stack
// §3.8; the caller threads the fired map through attack+damage so one map
// covers a full roll action.
//
// spend_resource debits events.State pools and fails closed (error, no
// partial write) when the pool is short. Pool names are validated against
// pack resources.
//
// Reserved v1 order (NOT implemented): concentration -> pools.temp_hp ->
// death saves -> exhaustion. The pools.temp_hp name is reserved (see the
// debit helper in resolve.go); temp HP itself is absent from v0 state by
// design. A generic `reroll` verb does not exist: it is refused as an
// unknown verb (v1 candidate, cf. implementation-plan Phase 1/D).
//
// Versioning is two-level: core ontology vN (events.OntologyVersion) plus
// the pack id; replay needs seed, faces, contributions, conditions, pack,
// and ontology version (see implementation-plan Phase 1/D).
package rules
