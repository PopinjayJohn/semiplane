package api

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/PopinjayJohn/semiplane/internal/dice"
	"github.com/PopinjayJohn/semiplane/internal/events"
	"github.com/PopinjayJohn/semiplane/internal/oplog"
	"github.com/PopinjayJohn/semiplane/internal/sse"
)

// rollBody is the dice-roll request. seed is advisory: when
// SEMIPLANE_TEST_SEED is set the env value wins (deterministic E2E + exact
// goldens); otherwise the request seed wins when non-zero; otherwise the
// server draws crypto-random.
// vars is an optional map of @-variable substitutions (e.g. {"con": 3}).
type rollBody struct {
	Formula string         `json:"formula"`
	Seed    int64          `json:"seed"`
	Vars    map[string]int `json:"vars,omitempty"`
}

// handleDiceRoll resolves one formula server-authoritatively (D's engine,
// same engine that later serves /ws/vtt per tech-stack §3.8) and journals
// the v0 replay fields to the oplog: seed, faces, contributions,
// conditions, pack, ontology version. ?breakdown=1 adds the GM-toggle lines
// — kept/discarded d20s plus per-tier dice/flat lines, so
// not-doubled-on-crit stays auditable (tech-stack §3.8).
func (d *Deps) handleDiceRoll(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body rollBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, http.StatusBadRequest, "bad request", "body must be JSON {formula, seed?}: "+err.Error())
		return
	}
	formula := strings.TrimSpace(body.Formula)
	if formula == "" {
		WriteProblem(w, http.StatusBadRequest, "bad request", "formula is required")
		return
	}
	seed := body.Seed
	if env, ok := envSeed(); ok {
		seed = env
	} else if seed == 0 {
		seed = cryptoSeed()
	}
	res, err := dice.Evaluator{}.RollWithVars(r.Context(), formula, seed, body.Vars)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "bad request", "invalid formula: "+err.Error())
		return
	}
	pack := d.PackID
	if pack == "" {
		pack = oplog.DefaultPack
	}
	if d.Oplog != nil {
		_, _ = d.Oplog.Append(oplog.Entry{
			Kind:          "dice_roll",
			Formula:       formula,
			Seed:          seed,
			Faces:         append([]int(nil), res.Faces...),
			Contributions: append([]string(nil), res.Breakdown...),
			Conditions:    []string{},
			Pack:          pack,
			Ontology:      events.OntologyVersion,
			Total:         res.Total,
		})
	}
	if d.Hub != nil {
		payload, _ := json.Marshal(map[string]any{
			"formula": formula, "total": res.Total, "faces": res.Faces, "seed": seed,
		})
		d.Hub.Publish(sse.Event{Type: "dice_rolled", Data: string(payload)})
	}
	resp := map[string]any{
		"formula": formula,
		"total":   res.Total,
		"faces":   res.Faces,
		"seed":    seed,
	}
	if r.URL.Query().Get("breakdown") == "1" {
		resp["kept"] = res.Kept
		resp["discarded"] = res.Discarded
		resp["breakdown"] = res.Breakdown
	}
	writeJSON(w, http.StatusOK, resp)
}

// envSeed reports SEMIPLANE_TEST_SEED when set and parseable: the
// determinism switch for E2E + httptest exact assertions (seeded iff set).
func envSeed() (int64, bool) {
	raw, ok := os.LookupEnv("SEMIPLANE_TEST_SEED")
	if !ok || strings.TrimSpace(raw) == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func cryptoSeed() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b[:]))
}
