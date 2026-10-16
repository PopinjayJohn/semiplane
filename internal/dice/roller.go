package dice

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Roller is a deterministic source of die faces. The zero value is not
// usable; construct with NewQueueRoller or NewSeededRoller.
type Roller struct {
	mu     sync.Mutex
	queued []int
	next   int
	rng    *rand.Rand
	lcg    uint64
}

// NewQueueRoller returns a Roller that yields faces in order. Faces within
// 1..size are used exactly; out-of-range faces are reduced deterministically
// via modFace so a roll never fails. When the queue is exhausted, a
// deterministic LCG stream (fixed start constant, no wall clock, no global
// RNG) takes over, so two queue rollers with equal faces produce equal
// sequences no matter how far past exhaustion they roll.
func NewQueueRoller(faces ...int) *Roller {
	return &Roller{
		queued: append([]int(nil), faces...),
		lcg:    0x5851F42D4C957F2D,
	}
}

// NewSeededRoller returns a Roller backed by a deterministic math/rand/v2
// PCG stream derived from seed via splitmix64. Equal seeds yield equal
// sequences.
func NewSeededRoller(seed int64) *Roller {
	u := uint64(seed)
	s1 := splitmix64(&u)
	s2 := splitmix64(&u)
	return &Roller{rng: rand.New(rand.NewPCG(s1, s2|1))}
}

func splitmix64(state *uint64) uint64 {
	*state += 0x9E3779B97F4A7C15
	z := *state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Roll returns a face in 1..size. It panics if size < 1 (die sizes are
// validated against MaxSides during parsing, so this is a programmer
// error, not user input).
func (r *Roller) Roll(size int) int {
	if size < 1 {
		panic(fmt.Sprintf("dice: Roll size %d < 1", size))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rng != nil {
		return r.rng.IntN(size) + 1
	}
	if r.next < len(r.queued) {
		v := r.queued[r.next]
		r.next++
		if v >= 1 && v <= size {
			return v
		}
		return modFace(v, size)
	}
	r.lcg = r.lcg*6364136223846793005 + 1442695040888963407
	return int((r.lcg>>33)%uint64(size)) + 1
}

// modFace deterministically maps an out-of-range queued face into 1..size.
func modFace(v, size int) int {
	m := (v - 1) % size
	if m < 0 {
		m += size
	}
	return m + 1
}

// SeedFromEnv returns the SEMIPLANE_TEST_SEED environment value, or def
// when unset, blank, or unparsable. Tests use it so a fixed seed can
// override the default deterministically from the outside.
func SeedFromEnv(def int64) int64 {
	s, ok := os.LookupEnv("SEMIPLANE_TEST_SEED")
	if !ok || strings.TrimSpace(s) == "" {
		return def
	}
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return def
	}
	return v
}
