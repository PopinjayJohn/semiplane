package index

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// The authored charcreation system note is the creator-wizard's attribute
// source (tech-stack §3.4: point-buy rules come from Markdown). These
// invariants keep the handwritten namespace honest: no extractor regenerates
// it, so drift would otherwise go unnoticed.

func loadCharcreation(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vaults", "facets", "rules", "systems", "dnd5e-2024", "charcreation.md"))
	if err != nil {
		t.Fatalf("read authored note: %v", err)
	}
	fm := parseMeta(string(raw))
	ns, ok := fm["semiplane"].(map[string]any)
	if !ok {
		t.Fatalf("frontmatter carries no semiplane namespace (keys: %v)", keysOf(fm))
	}
	cc, ok := ns["charcreation"].(map[string]any)
	if !ok {
		t.Fatalf("semiplane carries no charcreation block (keys: %v)", keysOf(ns))
	}
	return cc
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCharcreationPointBuyInvariants(t *testing.T) {
	cc := loadCharcreation(t)

	rawMethods, ok := cc["methods"].([]any)
	if !ok || len(rawMethods) == 0 {
		t.Fatalf("charcreation.methods missing or empty: %v", cc["methods"])
	}

	var costs map[string]int
	var budget, scoreMin, scoreMax int
	havePointBuy := false
	var array []int
	haveArray := false

	for _, m := range rawMethods {
		method, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("method is not a map: %v", m)
		}
		switch method["id"] {
		case "point-buy":
			havePointBuy = true
			budget = method["budget"].(int)
			scoreMin, scoreMax = method["scoreMin"].(int), method["scoreMax"].(int)
			costs = map[string]int{}
			for k, v := range method["costs"].(map[string]any) {
				costs[k] = v.(int)
			}
		case "standard-array":
			haveArray = true
			for _, v := range method["values"].([]any) {
				array = append(array, v.(int))
			}
		default:
			t.Fatalf("unknown creation method %q — fail loudly rather than ignore", method["id"])
		}
	}
	if !havePointBuy || !haveArray {
		t.Fatalf("want both point-buy and standard-array methods")
	}

	if budget != 27 {
		t.Errorf("point-buy budget = %d, want 27", budget)
	}
	if scoreMin != 8 || scoreMax != 15 {
		t.Errorf("score range [%d,%d], want [8,15]", scoreMin, scoreMax)
	}

	// Costs must cover the whole score range with the canonical SRD 5.2
	// table: 8=0 9=1 10=2 11=3 12=4 13=5 14=7 15=9.
	want := map[string]int{"8": 0, "9": 1, "10": 2, "11": 3, "12": 4, "13": 5, "14": 7, "15": 9}
	if !reflect.DeepEqual(costs, want) {
		t.Errorf("costs = %v, want canonical %v", costs, want)
	}

	if len(array) != 6 {
		t.Fatalf("standard array length = %d, want 6", len(array))
	}
	if !reflect.DeepEqual(array, []int{15, 14, 13, 12, 10, 8}) {
		t.Errorf("standard array = %v, want [15 14 13 12 10 8]", array)
	}

	// The standard array must be affordable under point buy — a cross-method
	// consistency check (the canonical spread {15,14,13,12,10,8} spends
	// 9+7+5+4+2+0 = 27, exactly the budget).
	sum := 0
	for _, score := range array {
		sum += costs[strconv.Itoa(score)]
	}
	if sum != budget {
		t.Errorf("standard array costs %d under point buy, want %d (budget)", sum, budget)
	}
}

// Increases: background grants +2/+1 under the 2024 rules — species grant
// nothing (the extractor's XPHB species notes carry no ability field, so a
// species-side increase here would contradict the corpus).
func TestCharcreationIncreaseInvariants(t *testing.T) {
	cc := loadCharcreation(t)
	inc, ok := cc["increases"].(map[string]any)
	if !ok {
		t.Fatalf("charcreation.increases missing: %v", cc["increases"])
	}
	if src, _ := inc["from"].(string); src != "background" {
		t.Errorf("increases.from = %v, want background", inc["from"])
	}
	weights := inc["weights"].([]any)
	if len(weights) != 2 || weights[0].(int) != 2 || weights[1].(int) != 1 {
		t.Errorf("increases.weights = %v, want [2,1] (+2/+1)", weights)
	}
}
