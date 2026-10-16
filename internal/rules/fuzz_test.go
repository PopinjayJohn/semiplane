package rules

import (
	"testing"
)

// FuzzCompileHook feeds arbitrary strings to the `when` compiler: it must
// never panic, and every input either fails to compile or yields a runnable
// bool program over the struct Env.
func FuzzCompileHook(f *testing.F) {
	seeds := []string{
		"ctx.hit == true",
		"ctx.used_ability == 'str' and ctx.source in ['weapon_melee','unarmed']",
		"ctx.kept_d20 >= 19",
		"true",
		"ctx.vibes == 'strong'",
		"ctx.hit == ",
		"now()",
		"",
		"ctx.damage_type == 'fire' or (ctx.crit and not ctx.hit)",
		"((((((((((ctx.hit))))))))))",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	ctxs := []Ctx{
		{},
		{Hit: true, Source: "weapon_melee", UsedAbility: "str", DamageType: "slashing", TargetAC: 15, KeptD20: 17, Attacker: "a", Target: "b"},
		{Crit: true, Hit: true, KeptD20: 20, DC: 15, Mode: "reckless", Skill: "stealth", Ability: "dex", Provoked: true, AttackerVisible: true},
	}
	f.Fuzz(func(t *testing.T, s string) {
		prog, err := CompileHook(s)
		if err != nil {
			return // refused: the only other legal outcome
		}
		if prog == nil {
			t.Fatalf("CompileHook(%q) returned nil program with nil error", s)
		}
		for _, ctx := range ctxs {
			// Must not panic; returns a bool or a run-time error.
			_, _ = evalWhen(prog, ctx)
		}
	})
}
