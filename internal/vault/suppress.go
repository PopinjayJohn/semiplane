// Echo suppression for the file watcher (tech-stack §5: hash-based change
// detection suppresses fsnotify echo loops).
//
// The flow: Vault.Put records the hash it just wrote via Suppress.Note.
// When the watcher's debounced event for that path arrives, the consumer
// re-hashes the file and calls Suppress.IsEcho: a match means the change
// is our own write (skip the reindex), a mismatch means a concurrent
// external edit landed after our write (reindex anyway). Entries are
// single-use: consumed on first check so a later external edit with
// identical content still re-verifies through the normal path.
package vault

import "sync"

// Suppress tracks last-written content hashes per vault-relative path so
// watch consumers can tell own writes apart from external edits.
type Suppress struct {
	mu sync.Mutex
	m  map[string]string
}

// NewSuppress returns an empty Suppress map.
func NewSuppress() *Suppress {
	return &Suppress{m: make(map[string]string)}
}

// Note records path's just-written content hash (as returned by Put).
func (s *Suppress) Note(path, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[path] = hash
}

// IsEcho reports whether hash matches the recorded write for path,
// consuming the entry. Unknown paths report false.
func (s *Suppress) IsEcho(path, hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want, ok := s.m[path]
	if !ok {
		return false
	}
	delete(s.m, path)
	return want == hash
}
