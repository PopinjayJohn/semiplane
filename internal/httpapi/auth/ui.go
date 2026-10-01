package auth

import (
	"net/http"
	"strings"
	"time"
)

// UICookieName is the cookie carrying the two interface preferences.
//
// It is deliberately *not* HttpOnly: the shell's inline head resolver reads it
// before first paint, and a cookie the script cannot see is a cookie that cannot
// prevent the flash of the wrong theme. That is only safe because the cookie
// carries no sensitive data — which is precisely why it carries these two
// fields and no others. A preference cookie has to be readable by the client,
// so the rule is not "put nothing private in it" but "put nothing *at all* in
// it that you would mind being read". Theme and UI mode are not that.
const UICookieName = "sp_ui"

// UICookieMaxAge is one year, the value UI spec §6.6 fixes.
//
// Long on purpose. This cookie holds no session and no identifier; losing it
// costs a preference, and a preference that expires after a month is a
// preference that comes back on its own.
const UICookieMaxAge = 31536000 * time.Second

// Theme is the colour scheme preference.
type Theme string

// UIMode is the input-mode preference — the device class the layout adapts to,
// not a width band. A TV is a mode because a TV has a remote, not because it is
// wide.
type UIMode string

// The complete set of values each preference accepts. Anything else is not a
// preference this build understands and is dropped on read.
//
// They are named constants rather than literals at the parse sites so the wire
// values have exactly one definition in the package: the token layer in phase 5
// emits the same strings, and a value that appears twice is a value that will
// drift.
const (
	ThemeAuto  Theme = "auto"
	ThemeLight Theme = "light"
	ThemeDark  Theme = "dark"

	UIModeAuto   UIMode = "auto"
	UIModeLaptop UIMode = "laptop"
	UIModePhone  UIMode = "phone"
	UIModeTV     UIMode = "tv"
)

// UIPreferences is the entire contents of the `sp_ui` cookie.
//
// Two fields, and two on purpose. UI spec §6.6 lists exactly these; density was
// considered and not selected, and a `Density` field here would be a dangling
// token with no consumer — a third thing in the cookie, in the Go struct, and in
// the phase-5 token layer, none of it doing anything.
type UIPreferences struct {
	Theme Theme
	UI    UIMode
}

// DefaultUIPreferences returns the preferences a browser with no cookie gets:
// both fields `auto`, meaning "follow the OS and the viewport".
//
// `auto` is a real answer rather than a null: the shell's resolver has to
// branch on it, and an absent preference and an explicit auto must not be
// distinguishable once they reach it.
func DefaultUIPreferences() UIPreferences {
	return UIPreferences{Theme: ThemeAuto, UI: UIModeAuto}
}

// ReadUIPreferences parses the `sp_ui` cookie, falling back per field.
//
// The cookie is user-controlled input, so this is a strict parser: an unknown
// field, an unknown value, or a malformed pair yields the default for that
// field and never a guess. There is no partial credit and no fuzzy matching,
// because the failure mode of guessing is a layout that renders wrong for a
// reason nobody can see from the server's side.
//
// The two fields fall back independently. A `theme` that is nonsense costs the
// theme, not the input mode: one bad value is not evidence the whole cookie is
// corrupt.
//
// This function must not set Vary: Cookie and must not change a single byte of
// the response body. The document never varies by `sp_ui` — the shell resolves
// it client-side, in an inline script, before first paint (UI spec §6.6,
// requirement S-13.5) — so emitting Vary here would fragment every shared cache
// in front of the server on a value that changes nothing about the document, and
// would bury the content hash that the cache's invalidation signal actually
// rides on. A later phase that feels the urge to add it is missing the reason
// the spec says none is needed.
func ReadUIPreferences(r *http.Request) UIPreferences {
	cookie, err := r.Cookie(UICookieName)
	if err != nil {
		return DefaultUIPreferences()
	}

	return parseUIPreferences(cookie.Value)
}

// parseUIPreferences parses a raw `sp_ui` value.
//
// An unknown key is ignored rather than preserved. Ignoring is the only safe
// reading of a cookie carrying, say, `density=laptop`: this build has no density
// preference to hold, and inventing a field for it is how a preference written
// by a future version silently becomes a dangling one here.
//
// A *malformed* segment is a different case and is rejected outright rather than
// skipped, as is a repeated key. Both mean the cookie was not written by
// WriteUIPreferences, and a parser that keeps the fields it can read out of one
// that was not is a parser whose output depends on which half of the string
// happened to survive. All defaults is the one answer that cannot leak a partial
// preference into the layout.
//
// Per-field fallback still happens for the case it exists for: a cookie that is
// well-formed with two recognised keys, one of whose *values* this build does not
// know. `theme=neon; ui=tv` is a real cookie from a newer or older build, and it
// costs the theme only.
func parseUIPreferences(raw string) UIPreferences {
	prefs := DefaultUIPreferences()
	seen := make(map[string]bool, 2)

	for pair := range strings.SplitSeq(raw, ";") {
		if strings.TrimSpace(pair) == "" {
			// An empty segment carries nothing. Tolerated because a trailing
			// separator is the one malformation browsers add for free.
			continue
		}

		key, value, found := strings.Cut(pair, "=")
		if !found {
			return DefaultUIPreferences()
		}

		key = strings.TrimSpace(key)
		if seen[key] {
			return DefaultUIPreferences()
		}

		seen[key] = true

		value = strings.TrimSpace(value)

		switch key {
		case "theme":
			if theme, valid := parseTheme(value); valid {
				prefs.Theme = theme
			}
		case "ui":
			if mode, valid := parseUIMode(value); valid {
				prefs.UI = mode
			}
		}
	}

	return prefs
}

// parseTheme recognises the theme values, reporting false for anything else.
//
// The switch is over Theme rather than string so the accepted values are the
// constants above and not a second copy of them.
func parseTheme(value string) (Theme, bool) {
	switch Theme(value) {
	case ThemeAuto:
		return ThemeAuto, true
	case ThemeLight:
		return ThemeLight, true
	case ThemeDark:
		return ThemeDark, true
	default:
		return "", false
	}
}

// parseUIMode recognises the UI mode values, reporting false for anything else.
func parseUIMode(value string) (UIMode, bool) {
	switch UIMode(value) {
	case UIModeAuto:
		return UIModeAuto, true
	case UIModeLaptop:
		return UIModeLaptop, true
	case UIModePhone:
		return UIModePhone, true
	case UIModeTV:
		return UIModeTV, true
	default:
		return "", false
	}
}

// WriteUIPreferences writes the `sp_ui` cookie.
//
// Not HttpOnly, `SameSite=Lax`, `Path=/`, `Max-Age=31536000`, and no `Secure`:
// the preference is not a credential, and setting Secure on it would do nothing
// useful except mean a LAN deployment reads no preferences at all.
//
// Values this build does not recognise are normalised to `auto` before being
// written, so the cookie on disk is always one this build can read back.
func WriteUIPreferences(w http.ResponseWriter, prefs UIPreferences) {
	theme, valid := parseTheme(string(prefs.Theme))
	if !valid {
		theme = ThemeAuto
	}

	mode, valid := parseUIMode(string(prefs.UI))
	if !valid {
		mode = UIModeAuto
	}

	//nolint:gosec // G124 wants HttpOnly and Secure on every cookie. Neither is
	// correct here and the reason is the design, not an oversight: the shell's
	// inline head resolver reads this cookie before first paint to pick the
	// theme (S-13.5), so HttpOnly would make the value unreachable and defeat
	// the flash-of-wrong-theme it exists to prevent. It is safe precisely
	// because it holds two enum-valued preferences and nothing else — which is
	// why UIPreferences has two fields and why adding a third is a decision
	// rather than a convenience. Secure is omitted for the same reason as on the
	// session cookie: it would silently discard preferences on any self-hoster
	// serving plain HTTP on a LAN.
	http.SetCookie(w, &http.Cookie{
		Name:  UICookieName,
		Value: "theme=" + string(theme) + "; ui=" + string(mode),
		Path:  "/",
		// Left false deliberately, and asserted in a test. See UICookieName.
		HttpOnly: false,
		MaxAge:   int(UICookieMaxAge / time.Second),
		SameSite: http.SameSiteLaxMode,
	})
}
