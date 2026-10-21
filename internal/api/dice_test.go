package api

import (
	"net/http"
	"testing"

	"github.com/PopinjayJohn/semiplane/internal/dice"
)

func TestDiceRollSeededDeterministic(t *testing.T) {
	t.Setenv("SEMIPLANE_TEST_SEED", "42")
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	roll := func() (int, []int, int64) {
		resp := postJSON(t, srv.URL+"/api/v1/dice/roll?breakdown=1",
			map[string]any{"formula": "2d20kh1+5"})
		var out struct {
			Formula   string   `json:"formula"`
			Total     int      `json:"total"`
			Faces     []int    `json:"faces"`
			Seed      int64    `json:"seed"`
			Kept      []int    `json:"kept"`
			Discarded []int    `json:"discarded"`
			Breakdown []string `json:"breakdown"`
		}
		decodeBody(t, resp, &out)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		return out.Total, out.Faces, out.Seed
	}
	total1, faces1, seed1 := roll()
	total2, faces2, seed2 := roll()
	if seed1 != 42 || seed2 != 42 {
		t.Fatalf("seeds = %d, %d; SEMIPLANE_TEST_SEED must win", seed1, seed2)
	}
	if total1 != total2 || len(faces1) != len(faces2) {
		t.Fatal("same seed gave different results")
	}
	for i := range faces1 {
		if faces1[i] != faces2[i] {
			t.Fatal("same seed gave different faces")
		}
	}
	// Cross-check against the engine directly: same formula + seed = same.
	res, err := dice.Evaluator{}.Roll(t.Context(), "2d20kh1+5", 42)
	if err != nil {
		t.Fatal(err)
	}
	if total1 != res.Total {
		t.Fatalf("handler total %d != engine total %d", total1, res.Total)
	}
}

func TestDiceRollDefaultOmitsBreakdown(t *testing.T) {
	t.Setenv("SEMIPLANE_TEST_SEED", "7")
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/dice/roll", map[string]any{"formula": "1d6"})
	var out map[string]any
	decodeBody(t, resp, &out)
	for _, k := range []string{"formula", "total", "faces", "seed"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("default response missing %q: %v", k, out)
		}
	}
	for _, k := range []string{"kept", "discarded", "breakdown"} {
		if _, ok := out[k]; ok {
			t.Fatalf("default response must omit %q (needs ?breakdown=1)", k)
		}
	}
}

func TestDiceRollBreakdownShape(t *testing.T) {
	t.Setenv("SEMIPLANE_TEST_SEED", "7")
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/dice/roll?breakdown=1", map[string]any{"formula": "2d20kh1"})
	var out struct {
		Kept      []int    `json:"kept"`
		Discarded []int    `json:"discarded"`
		Breakdown []string `json:"breakdown"`
	}
	decodeBody(t, resp, &out)
	if len(out.Kept) != 1 || len(out.Discarded) != 1 {
		t.Fatalf("2d20kh1 kept/discarded = %v/%v, want 1/1", out.Kept, out.Discarded)
	}
	if len(out.Breakdown) == 0 {
		t.Fatal("breakdown=1 must return per-tier lines")
	}
}

func TestDiceRollInvalidFormula400(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/dice/roll", map[string]any{"formula": "not dice at all (((("})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q, want problem+json", ct)
	}
}

func TestDiceRollRequestSeedHonoredWithoutEnv(t *testing.T) {
	t.Setenv("SEMIPLANE_TEST_SEED", "")
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	first := postJSON(t, srv.URL+"/api/v1/dice/roll", map[string]any{"formula": "1d20", "seed": 1234})
	var a struct {
		Total int   `json:"total"`
		Seed  int64 `json:"seed"`
	}
	decodeBody(t, first, &a)
	second := postJSON(t, srv.URL+"/api/v1/dice/roll", map[string]any{"formula": "1d20", "seed": 1234})
	var b struct {
		Total int   `json:"total"`
		Seed  int64 `json:"seed"`
	}
	decodeBody(t, second, &b)
	if a.Seed != 1234 || b.Seed != 1234 {
		t.Fatalf("request seed not echoed: %d, %d", a.Seed, b.Seed)
	}
	if a.Total != b.Total {
		t.Fatal("same request seed gave different totals")
	}
}

func TestDiceRollWithVariables(t *testing.T) {
	t.Setenv("SEMIPLANE_TEST_SEED", "99")
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	// Test variable substitution via API
	resp := postJSON(t, srv.URL+"/api/v1/dice/roll?breakdown=1",
		map[string]any{"formula": "1d12 + @con", "vars": map[string]int{"con": 3}})
	var out struct {
		Formula   string   `json:"formula"`
		Total     int      `json:"total"`
		Faces     []int    `json:"faces"`
		Seed      int64    `json:"seed"`
		Kept      []int    `json:"kept"`
		Discarded []int    `json:"discarded"`
		Breakdown []string `json:"breakdown"`
	}
	decodeBody(t, resp, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if out.Total < 4 || out.Total > 15 {
		t.Errorf("Total = %d, want in [4,15] (1d12 + 3)", out.Total)
	}
	if len(out.Faces) != 1 {
		t.Errorf("Faces = %v, want 1 face", out.Faces)
	}
	if out.Formula != "1d12 + @con" {
		t.Errorf("Formula echoed back = %q, want %q", out.Formula, "1d12 + @con")
	}
}

func TestDiceRollWithVariablesInKeep(t *testing.T) {
	t.Setenv("SEMIPLANE_TEST_SEED", "100")
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	// Test variable in keep expression: 2d20kh1 + @mod
	resp := postJSON(t, srv.URL+"/api/v1/dice/roll?breakdown=1",
		map[string]any{"formula": "2d20kh1 + @mod", "vars": map[string]int{"mod": 5}})
	var out struct {
		Total     int   `json:"total"`
		Faces     []int `json:"faces"`
		Kept      []int `json:"kept"`
		Discarded []int `json:"discarded"`
	}
	decodeBody(t, resp, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(out.Faces) != 2 {
		t.Errorf("Faces = %v, want 2 faces", out.Faces)
	}
	if len(out.Kept) != 1 || len(out.Discarded) != 1 {
		t.Errorf("Kept/Discarded = %v/%v, want 1/1", out.Kept, out.Discarded)
	}
	// Total = kept face + mod (5)
	if out.Total != out.Kept[0]+5 {
		t.Errorf("Total = %d, want kept(%d) + 5 = %d", out.Total, out.Kept[0], out.Kept[0]+5)
	}
}

func TestDiceRollUnknownVariable400(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/dice/roll",
		map[string]any{"formula": "1d20 + @unknown", "vars": map[string]int{"known": 5}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q, want problem+json", ct)
	}
}

func TestDiceRollNegativeVariable(t *testing.T) {
	t.Setenv("SEMIPLANE_TEST_SEED", "101")
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	// Test negative variable: 1d20 - @penalty
	resp := postJSON(t, srv.URL+"/api/v1/dice/roll",
		map[string]any{"formula": "1d20 - @penalty", "vars": map[string]int{"penalty": 2}})
	var out struct {
		Total int   `json:"total"`
		Faces []int `json:"faces"`
	}
	decodeBody(t, resp, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(out.Faces) != 1 {
		t.Errorf("Faces = %v, want 1 face", out.Faces)
	}
	// Total = face - 2
	if out.Total != out.Faces[0]-2 {
		t.Errorf("Total = %d, want face(%d) - 2 = %d", out.Total, out.Faces[0], out.Faces[0]-2)
	}
}
