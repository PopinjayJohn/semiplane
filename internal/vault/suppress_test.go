package vault

import "testing"

func TestSuppressEcho(t *testing.T) {
	s := NewSuppress()
	if s.IsEcho("a.md", "abc") {
		t.Fatal("unknown path must not be echo")
	}
	s.Note("a.md", "abc")
	if !s.IsEcho("a.md", "abc") {
		t.Fatal("matching hash must be echo")
	}
	if s.IsEcho("a.md", "abc") {
		t.Fatal("entries are single-use")
	}
	s.Note("b.md", "abc")
	if s.IsEcho("b.md", "xyz") {
		t.Fatal("mismatched hash must not be echo (concurrent external edit)")
	}
}
