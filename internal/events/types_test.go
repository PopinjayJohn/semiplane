package events_test

import (
	"testing"

	"github.com/PopinjayJohn/semiplane/internal/events"
)

func TestKnownKindsCoversRows1To16(t *testing.T) {
	// §1 rows 1–16; row 11 is the turn_start/turn_end pair → 17 kinds.
	if len(events.KnownKinds) != 17 {
		t.Fatalf("len(KnownKinds) = %d, want 17 (16 rows, turn pair split)", len(events.KnownKinds))
	}
	seen := map[events.Kind]bool{}
	for _, k := range events.KnownKinds {
		if seen[k] {
			t.Fatalf("duplicate kind %q", k)
		}
		seen[k] = true
		if !events.ValidKind(k) {
			t.Fatalf("ValidKind(%q) = false, want true", k)
		}
	}
	for _, k := range []events.Kind{"attack", "cast", "turn_start", "turn_end", "area", "fudge"} {
		if events.ValidKind(k) != (k != "fudge") {
			t.Fatalf("ValidKind(%q) wrong", k)
		}
	}
}

func TestAdvDiceCancelsNeverStacks(t *testing.T) {
	cases := []struct {
		name     string
		adv, dis bool
		want     events.KeepSpec
	}{
		{"straight", false, false, events.KeepSpec{Count: 1, Keep: 1, KeepHigh: true}},
		{"advantage", true, false, events.KeepSpec{Count: 2, Keep: 1, KeepHigh: true}},
		{"disadvantage", false, true, events.KeepSpec{Count: 2, Keep: 1, KeepHigh: false}},
		{"cancel", true, true, events.KeepSpec{Count: 1, Keep: 1, KeepHigh: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := events.AdvDice(tc.adv, tc.dis); got != tc.want {
				t.Fatalf("AdvDice(%v, %v) = %+v, want %+v", tc.adv, tc.dis, got, tc.want)
			}
		})
	}
}

func TestDamageSlotsDistinct(t *testing.T) {
	if events.SlotDice == events.SlotFlat {
		t.Fatal("SlotDice == SlotFlat, want distinct slots")
	}
	p := events.DamagePayload{
		Source:   "weapon_melee",
		Attacker: "throg",
		Target:   "goblin",
		BaseDice: "1d12",
		Contributions: []events.DamageContribution{
			{Slot: events.SlotDice, Tier: "inherent", Label: "Greataxe", DiceN: 1, DiceSize: 12},
			{Slot: events.SlotFlat, Tier: "class", Label: "Rage +2", Amount: 2},
		},
		Total: 9,
	}
	if len(p.Contributions) != 2 {
		t.Fatalf("contributions = %d, want dice and flat lines separate", len(p.Contributions))
	}
}

func TestStateMinimumShape(t *testing.T) {
	s := events.State{
		Actors: map[string]events.ActorState{
			"throg": {
				ID:     "throg",
				Name:   "Throg",
				Traits: map[string]any{"str": 18},
				Pools:  map[string]int{"hp": 55},
				Conditions: []events.Condition{
					{Key: "rage", Until: events.UntilManual, Source: "class:barbarian:rage"},
				},
				Position: nil, // theatre-of-mind: absent position is valid
			},
		},
		Turn: events.TurnState{Round: 3, ActiveActor: "throg"},
	}
	if s.Turn.ActiveActor != "throg" || s.Actors["throg"].Pools["hp"] != 55 {
		t.Fatalf("state minimum shape wrong: %+v", s)
	}
}

func TestEventEnvelopeCausalLink(t *testing.T) {
	atk := events.Event{
		ID:        "evt_attack",
		Kind:      events.KindAttack,
		Round:     3,
		TurnActor: "throg",
		Payload:   events.AttackPayload{Attacker: "throg", Hit: true, Crit: false},
	}
	dmg := events.Event{
		ID:        "evt_damage",
		Kind:      events.KindDamage,
		Round:     3,
		TurnActor: "throg",
		CausedBy:  atk.ID,
		Payload:   events.DamagePayload{Attacker: "throg", Target: "goblin"},
	}
	if dmg.CausedBy != atk.ID {
		t.Fatalf("damage.caused_by = %q, want attack id %q", dmg.CausedBy, atk.ID)
	}
	if atk.Payload.(events.AttackPayload).Hit != true {
		t.Fatal("attack payload type assertion failed")
	}
}
