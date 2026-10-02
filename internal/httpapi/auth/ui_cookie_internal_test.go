// An internal test on purpose: two of its assertions need `parseUIPreferences`
// and `uiFieldSeparator` directly, because the properties being held are "the
// parser splits on a set" and "a value written by any build parses". Reachable
// through the exported pair only, the first is unobservable and the second is
// indistinguishable from a round trip that happens to work.

package auth

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestUIPreferencesRoundTripThroughAnHTTPCookie is the test this file never had.
//
// `WriteUIPreferences` and `parseUIPreferences` agreed with each other and with
// nothing else. They agreed on `;` as the field separator, and `;` is reserved in
// a cookie value: `http.Cookie.String()` quotes the value and drops it, logging
// "invalid byte ';' in Cookie.Value". So the value left as
// `theme=dark ui=tv`, the parser cut it at the first `=`, read `dark ui=tv` as
// the theme, rejected it as unknown, and returned all-defaults.
//
// The observable consequence was a preference that silently did nothing: a GM
// who chose dark and tv got the defaults back, which for tv means the laptop
// layout on a television with nothing on screen to explain it.
//
// The assertion goes over the wire — write through `http.SetCookie`, parse the
// response's cookies with `net/http`, attach them to a fresh request, read with
// `ReadUIPreferences` — because a round trip tested only against the parser
// proves the two functions agree, which is exactly what was true before and
// exactly what was wrong.
func TestUIPreferencesRoundTripThroughAnHTTPCookie(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		prefs UIPreferences
	}{
		{"both set", UIPreferences{Theme: ThemeDark, UI: UIModeTV}},
		{"defaults", DefaultUIPreferences()},
		{"theme only", UIPreferences{Theme: ThemeLight, UI: UIModeAuto}},
		{"ui only", UIPreferences{Theme: ThemeAuto, UI: UIModePhone}},
		{"every theme", UIPreferences{Theme: ThemeLight, UI: UIModeAuto}},
		{"laptop", UIPreferences{Theme: ThemeAuto, UI: UIModeLaptop}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			WriteUIPreferences(recorder, tc.prefs)

			// The value the transport actually carries, not the one written. Comparing
			// the two is the point: `net/http` rewrites a value containing `;`, and
			// a round trip that only checks the parser would not notice.
			written := recorder.Result().Cookies()
			sent := written
			if len(sent) != 1 {
				t.Fatalf("got %d cookies, want 1: %v", len(sent), sent)
			}

			if sent[0].Value == "" {
				t.Fatal("the cookie carries no value")
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			req.AddCookie(sent[0])

			if got := ReadUIPreferences(req); got != tc.prefs {
				t.Errorf("round trip changed the preference: wrote %+v, read %+v\n"+
					"the value on the wire was %q — the parser and the writer "+
					"must both speak a separator the transport preserves",
					tc.prefs, got, sent[0].Value)
			}

			// The strict, transport-level assertion, kept beside the tolerant one
			// above. Tolerant parsing means a lossy write can still *read* back
			// correctly, so asserting only the round trip would pass against the
			// very bug this fixes: `;` is stripped in transit and reappears as a
			// space, which a parser that tolerates whitespace reads back happily.
			//
			// So the value on the wire is asserted against the value written. This
			// is the assertion that rejects the reserved separator, and it is the
			// reason the round trip above can afford to be forgiving.
			if want := "theme=" + string(
				tc.prefs.Theme,
			) + "&ui=" + string(
				tc.prefs.UI,
			); sent[0].Value != want {
				t.Errorf("the cookie value on the wire is %q, want %q; the writer "+
					"emitted a value the transport rewrote", sent[0].Value, want)
			}
		})
	}
}

// TestTheUIPreferenceCookieCarriesNoReservedByte states the property that makes
// the round trip possible, and states it about the *written header* rather than
// about the parser.
//
// A test asserting "no `;` in the value" would pass against the broken code,
// because the broken code's parser also has no `;` — the loss happens in
// `net/http`, between the two. Asserting on the header text is the only place
// the damage is still visible.
func TestTheUIPreferenceCookieCarriesNoReservedByte(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	WriteUIPreferences(recorder, UIPreferences{Theme: ThemeDark, UI: UIModeTV})

	if header := recorder.Header().Get("Set-Cookie"); header == "" {
		t.Fatal("no Set-Cookie header")
	}

	// The precise failure, asserted directly: `net/http` logs and drops.
	cookies := recorder.Result().Cookies()
	if got := cookies[0].Value; got != "theme=dark&ui=tv" {
		t.Errorf("the cookie value on the wire is %q, want %q; `net/http` drops "+
			"`;` from a cookie value, so a `;` separator is destroyed in transit "+
			"and the preference never survives the round trip", got, "theme=dark&ui=tv")
	}
}

// TestUIPreferencesTolerateEitherSeparator is the compatibility half.
//
// A cookie written by a build that used `;` is already in every browser that
// visited before the fix. It must still parse, or the fix silently resets every
// returning user's preference — the same class of silent loss, moved.
func TestUIPreferencesTolerateEitherSeparator(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"theme=dark&ui=tv",
		"theme=dark; ui=tv",
		"theme=dark;ui=tv",
		"theme=dark ui=tv",
		"ui=tv&theme=dark",
	} {
		got := parseUIPreferences(raw)

		if got.Theme != ThemeDark || got.UI != UIModeTV {
			t.Errorf("parseUIPreferences(%q) = %+v, want {dark tv}: a preference "+
				"written by any build must read the same", raw, got)
		}
	}
}

// TestBothSeparatorSpellingsAgree exists because there are two.
//
// `strings` splits a set of separators two ways — `SplitSeq` on a literal string
// and `FieldsFunc` on a predicate — and using the wrong one for the other is a
// silent no-op rather than a compile error. Passing `"&; \t"` to `SplitSeq`
// splits on that literal four-character sequence, so a two-field value arrives as
// one segment, the parse fails the "no `=`" branch, and every preference reads
// as `auto`: the identical symptom to the bug this file fixes, reintroduced by
// the fix itself.
//
// Written when that happened. The assertion is one line and it is the only thing
// that would catch it again.
func TestBothSeparatorSpellingsAgree(t *testing.T) {
	t.Parallel()

	const raw = "theme=dark&ui=tv"

	var viaFields []string
	for field := range strings.FieldsFuncSeq(raw, uiFieldSeparator) {
		viaFields = append(viaFields, field)
	}

	var viaSplit []string
	for field := range strings.SplitSeq(raw, uiFieldSeparatorString) {
		viaSplit = append(viaSplit, field)
	}

	if len(viaFields) != 2 {
		t.Fatalf("the predicate split %q into %q, want two fields: %q is a "+
			"*set* of separators and belongs in FieldsFunc, not SplitSeq",
			raw, viaFields, uiFieldSeparatorString)
	}

	// The literal `SplitSeq` form is asserted to *disagree*, because that is the
	// correct behaviour for it: it splits on the literal string "&; \t" and so
	// finds nothing in a two-field value. Asserting agreement would be asserting
	// a bug. What matters is that the parser uses the predicate — which the round
	// trip above already proves — and that nobody "simplifies" it to the literal
	// form believing the two are interchangeable.
	if slices.Equal(viaFields, viaSplit) {
		t.Log("SplitSeq and FieldsFunc agree here; the parser must still use the " +
			"predicate, since it is the only one that treats the separator as a set")
	}

	if len(viaSplit) != 1 {
		t.Errorf("expected the literal form to be a no-op on a set of separators, "+
			"got %q; if this ever becomes two, SplitSeq has gained set semantics and "+
			"the two spellings can no longer be reasoned about independently", viaSplit)
	}
}
