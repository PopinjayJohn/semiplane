package shell_test

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/shell"
)

// The resolver's precedence is the part of the design record that is easiest to
// get subtly wrong and hardest to notice, so it is tested twice over.
//
// First, as a table over the four sources in §3.3's priority order, against a
// Go transcription of the script. The transcription is a *reference*, not the
// implementation: the implementation is `ResolverSource`, which runs in a
// browser. Nothing in this repository can execute that, so the transcription is
// where the order is actually asserted.
//
// Second, against the script itself — but structurally, not by string position.
// `TestTheScriptAndTheReferenceShareTheirConstants` parses the script's own
// declarations and compares the *sets*: the mode names, the theme names, the
// user-agent hints, the tier names and the breakpoint numbers. That is what
// makes the transcription trustworthy, because the thing most likely to drift
// between two copies of the same algorithm is the data, not the order.
//
// What is therefore asserted: every mode name, every hint, every tier name and
// every breakpoint the script uses, and the precedence order as the reference
// states it. What is not asserted: that the script's control flow evaluates the
// sources in that order. Nothing here runs JavaScript, and a test that grepped
// for the order of two substrings would be asserting a formatting detail while
// calling it a behavioural one.

// inputs is everything the resolver reads, injected.
//
// Every field is a thing the script reads from the environment it is handed —
// the query string, the cookie, the user agent, the viewport and the colour
// scheme — and nothing else. `CookieSet` is separate from `Cookie` because "no
// cookie" and "a cookie saying auto" are different inputs, and §3.3's third
// rule turns on the difference: the user-agent hint is a *first-visit* default
// and applies only before a cookie exists.
type inputs struct {
	Query       string
	Cookie      string
	CookieSet   bool
	UserAgent   string
	Width       int
	Height      int
	PrefersDark bool
}

// The values the script accepts. One definition, used by the reference and
// compared against the script's own regular expressions.
var (
	modeNames = []string{"auto", "laptop", "phone", "tv"}
	// The themes a reader can arrive with, and the two values `data-theme` can
	// take (§3.7's table). `auto` is deliberately not in the list: the
	// script validates a supplied theme against this list, and `auto` is the
	// default it installs when the cookie carries no theme at all — an absent
	// preference and an explicit automatic have to be the same value before the
	// media query reads them, or the cookie would pin the theme it happened to
	// resolve to. `TestTheScriptInstallsAutoAsTheAbsentTheme` checks that half.
	switcherThemeNames = []string{"light", "dark"}
	// §3.3's third rule, verbatim. Narrow on purpose: see
	// TestTheUserAgentHintListIsNotWidened.
	userAgentHints = []string{
		"SmartTV", "Tizen", "webOS", "HbbTV", "Web0S", "NetCast", "AppleTV",
	}
	// §3.7's `data-ui` values.
	tierNames = []string{
		"compact", "compact-short", "medium", "large", "wide", "tv", "tv-wide",
	}
	// The boundaries the script compares the viewport against: §3.4's tv-wide,
	// then §3.1's lg, §3.5's short, §3.1's xl and 2xl.
	viewportBoundaries = []int{2200, 1024, 480, 1280, 1536}
)

// resolve is a Go transcription of the resolver's decision, in §3.3's order.
//
// It exists to be wrong in one place at a time, which is the only way a
// precedence rule can be tested. Read it beside `ResolverSource` and the two
// should be recognisably the same function.
func resolve(in inputs) (ui, theme string) {
	fields := map[string]string{}

	if in.CookieSet {
		// The script's tolerant split: `&`, `;` or whitespace, because the
		// separator a cookie arrives with is not this build's choice.
		for _, pair := range strings.FieldsFunc(in.Cookie, func(r rune) bool {
			return r == '&' || r == ';' || r == ' ' || r == '\t'
		}) {
			key, value, found := strings.Cut(pair, "=")
			if !found {
				continue
			}

			fields[key] = value
		}
	}

	// (1) `?ui=` wins, and is bookmarkable.
	if contains(modeNames, queryValue(in.Query, "ui")) {
		fields["mode"] = queryValue(in.Query, "ui")
	}

	// (2) The cookie, the persisted choice.
	if fields["mode"] == "" && contains(modeNames, fields["ui"]) {
		fields["mode"] = fields["ui"]
	}

	// (3) The user-agent hint, and only before a cookie exists. A cookie that
	// says `auto` is a decision — it is the mode switcher set back to automatic —
	// and re-applying the hint over it would make the switcher's "automatic"
	// unreachable on a television.
	if fields["mode"] == "" && !in.CookieSet && matchesAnyHint(in.UserAgent) {
		fields["mode"] = "tv"
	}

	// (4) Otherwise, the media queries alone.
	ui = tierFor(fields["mode"], in.Width, in.Height)

	switch {
	case contains([]string{"light", "dark"}, fields["theme"]):
		theme = fields["theme"]
	case in.PrefersDark:
		theme = "dark"
	default:
		theme = "light"
	}

	return ui, theme
}

// tierFor maps a mode and a viewport to a tier.
func tierFor(mode string, width, height int) string {
	if mode == "tv" {
		if width >= 2200 {
			return "tv-wide"
		}

		return "tv"
	}

	// `phone` is a mode and not a width band (§3.3), and `data-ui` has no
	// `phone` value (§3.7), so it resolves to the tier a phone gets.
	if mode == "phone" || width < 1024 {
		if height <= 480 {
			return "compact-short"
		}

		return "compact"
	}

	switch {
	case width < 1280:
		return "medium"
	case width < 1536:
		return "large"
	default:
		return "wide"
	}
}

// queryValue reads one parameter out of a query string.
func queryValue(query, name string) string {
	for pair := range strings.SplitSeq(strings.TrimPrefix(query, "?"), "&") {
		key, value, found := strings.Cut(pair, "=")
		if found && key == name {
			return value
		}
	}

	return ""
}

// matchesAnyHint reports whether a user agent matches §3.3's allowlist.
func matchesAnyHint(agent string) bool {
	for _, hint := range userAgentHints {
		if strings.Contains(agent, hint) {
			return true
		}
	}

	return false
}

// contains reports membership in a small list.
//
// `slices.Contains` rather than a loop, so the answer cannot differ from the one
// a reader expects. The list is a package-level table of three or four names
// and the call is in a test, so the allocation a linear scan avoided is not worth
// a loop nobody has to check.
func contains(list []string, value string) bool {
	return slices.Contains(list, value)
}

// TestUIResolutionPrecedence is §3.3's four rules, one table, in the order the
// spec states them.
//
// The rows are chosen so that each one *fails* if its own rule is removed. A row
// that passes under every implementation proves nothing, so every row below has
// at least two sources that disagree.
func TestUIResolutionPrecedence(t *testing.T) {
	t.Parallel()

	const tvAgent = "Mozilla/5.0 (SMART-TV; Linux; Tizen 7.0) AppleWebKit/537.36"

	cases := []struct {
		name string
		in   inputs
		want string
		why  string
	}{
		{
			// Rule 1, the only row where the query and the cookie both have an
			// opinion and the query is the one that counts.
			name: "the query parameter wins over the cookie and the hint",
			in: inputs{
				Query: "?ui=phone", Cookie: "theme=auto&ui=tv", CookieSet: true,
				UserAgent: tvAgent, Width: 1920, Height: 1080,
			},
			want: "compact",
			why: "§3.3 rule 1: `?ui=` wins, and it is bookmarkable — a reader " +
				"who asked for the phone layout gets it on a 1920px television",
		},
		{
			name: "the query parameter wins over a UA hint on a first visit",
			in: inputs{
				Query: "?ui=laptop", UserAgent: tvAgent, Width: 1920, Height: 1080,
			},
			want: "wide",
			why: "§3.3 rule 1 over rule 3: `?ui=laptop` is the switcher's answer " +
				"to a television, and it resolves by media query because the " +
				"laptop layout *is* the media-query result",
		},
		{
			// Rule 2. The cookie is the persisted choice, and a TV that has been
			// switched to laptop once must stay switched.
			name: "the cookie wins over the hint when no query parameter is present",
			in: inputs{
				Cookie: "theme=auto&ui=laptop", CookieSet: true,
				UserAgent: tvAgent, Width: 1920, Height: 1080,
			},
			want: "wide",
			why:  "§3.3 rule 2 over rule 3: the cookie is the persisted choice",
		},
		{
			// The row that makes rule 3 mean what it says. "Before a cookie
			// exists" is not "before a mode is known": a reader who put the
			// switcher back to automatic has made a decision, and re-applying the
			// hint would make "automatic" unreachable on a television — which is
			// the one control that is supposed to undo the hint.
			name: "a cookie saying auto is a decision, and the hint does not reapply",
			in: inputs{
				Cookie: "theme=auto&ui=auto", CookieSet: true,
				UserAgent: tvAgent, Width: 1920, Height: 1080,
			},
			want: "wide",
			why: "§3.3 rule 3: the hint is for the first-visit default, before a " +
				"cookie exists. A cookie that exists and says auto is the " +
				"switcher set back to automatic",
		},
		{
			// Rule 3, the only source that is not a decision anybody made.
			name: "the hint applies on a first visit with no cookie",
			in:   inputs{UserAgent: tvAgent, Width: 1920, Height: 1080},
			want: "tv",
			why:  "§3.3 rule 3: a narrow allowlist, used only before a cookie exists",
		},
		{
			name: "the hint is a mode, so it does not need a wide viewport",
			in: inputs{
				UserAgent: "Mozilla/5.0 (Web0S; Linux/SmartTV) WebKit",
				Width:     900,
				Height:    700,
			},
			want: "tv",
			why: "§3.3: a TV is a mode because it has a remote, not because it is " +
				"wide. A 900px viewport is medium by width and tv by mode",
		},
		{
			name: "a hint at 4K CSS pixels is tv-wide, not wide",
			in:   inputs{UserAgent: tvAgent, Width: 3840, Height: 2160},
			want: "tv-wide",
			why: "§3.4: at 3840 CSS px the type scale and the rail both grow. " +
				"137.5rem is 2200px, not 3840, so a 4K TV at DPR 2 is unaffected",
		},
		{
			// Rule 4, and the row an unrecognised television lands on. §3.3 calls
			// this the accepted failure mode: a laptop layout until somebody
			// flips the switcher, one remote press, persisted thereafter.
			name: "an unrecognised browser gets the laptop layout",
			in: inputs{
				UserAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0",
				Width:     1920,
				Height:    1080,
			},
			want: "wide",
			why: "§3.3's accepted failure mode, and the right trade against a " +
				"stale user-agent list and against a tablet misclassified as a " +
				"television",
		},
		{
			name: "a query parameter this build does not know falls through",
			in: inputs{
				Query: "?ui=television", Cookie: "theme=auto&ui=tv", CookieSet: true,
				Width: 1366, Height: 768,
			},
			want: "tv",
			why: "an unrecognised mode is not a mode; the resolver falls to the " +
				"next source rather than setting an attribute no rule matches",
		},
		{
			name: "a query parameter that is not the mode at all is ignored",
			in:   inputs{Query: "?q=constructor&ui=auto", Width: 1366, Height: 768},
			want: "large",
			why: "`?ui=auto` is explicit automatic, and the tier comes from the " +
				"media queries. A second look at object keys here is a prototype " +
				"pollution hole, which is why the script tests a pattern",
		},
		{
			name: "a cookie naming an unknown mode falls through to the media queries",
			in: inputs{
				Cookie:    "theme=dark&ui=console",
				CookieSet: true,
				Width:     1366,
				Height:    768,
			},
			want: "large",
			why: "a preference this build does not understand is not a preference; " +
				"it must not become an attribute value no rule matches",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, _ := resolve(testCase.in)
			if got != testCase.want {
				t.Errorf("data-ui = %q, want %q. %s", got, testCase.want, testCase.why)
			}
		})
	}
}

// TestTierBoundariesFollowTheViewportMatrix is §2's verification matrix, one row
// per form factor, against the reference.
//
// §2 is the record of what the design was checked against, and the two
// derivations of a tier — a width comparison and a height comparison — are the
// ones a later edit breaks. The expected values are written out rather than
// computed, so a change to the comparison shows up as a changed expectation
// rather than as a changed answer.
func TestTierBoundariesFollowTheViewportMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		width, height int
		want          string
	}{
		// §2's matrix, in its own order.
		{"small phone portrait", 320, 568, "compact"},
		{"phone portrait", 360, 640, "compact"},
		{"phone portrait, large", 390, 844, "compact"},
		{"phone landscape", 844, 390, "compact-short"},
		{"tablet portrait", 768, 1024, "compact"},
		{"tablet landscape", 1024, 768, "medium"},
		{"14-inch laptop", 1366, 768, "large"},
		{"14-inch MacBook", 1512, 982, "large"},
		{"desktop", 1280, 800, "large"},
		{"desktop, wide", 1920, 1080, "wide"},
		{"ultrawide", 2560, 1080, "wide"},
		{"ultrawide, longer", 3440, 1440, "wide"},
		{"TV 1080p at DPR 1", 1920, 1080, "wide"},
		{"4K TV reporting native CSS px", 3840, 2160, "wide"},

		// The boundary rows, because a boundary is where a comparison is
		// written and a comparison is what breaks.
		{"one pixel below lg", 1023, 768, "compact"},
		{"exactly lg", 1024, 768, "medium"},
		{"one pixel below xl", 1279, 768, "medium"},
		{"exactly xl", 1280, 768, "large"},
		{"one pixel below 2xl", 1535, 768, "large"},
		{"exactly 2xl", 1536, 768, "wide"},
		{"one pixel above the short threshold", 844, 481, "compact"},
		{"exactly the short threshold", 844, 480, "compact-short"},
		{"a short desktop window stays a desktop tier", 1366, 400, "large"},

		// §7.9: 200% zoom on a 1366x768 laptop is a 683x384 viewport, and it must
		// drop a tier rather than clip. It drops two: 683 is below lg, and 384
		// is also short enough for §3.5's collapsed header, so a zoomed laptop
		// gets the short-landscape chrome. That is the rule as written and it is
		// the right answer — 56px of header on a 384px viewport is a fifth of
		// the screen.
		{"200% zoom on a 1366px laptop", 683, 384, "compact-short"},
		{"400% zoom on a 1366px laptop", 342, 192, "compact-short"},
		{"200% zoom on a 1440x900 laptop", 720, 450, "compact-short"},
		{"150% zoom on a 1440x900 laptop", 960, 600, "compact"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := tierFor("", testCase.width, testCase.height)
			if got != testCase.want {
				t.Errorf(
					"a %dx%d viewport resolved to %q, want %q (UI §2, §3.1)",
					testCase.width, testCase.height, got, testCase.want,
				)
			}
		})
	}
}

// TestThemeResolutionFollowsTheCookieThenTheSystem is the theme half of §3.7's
// table, and it is a table because the interesting rows are the ones where the
// two disagree.
func TestThemeResolutionFollowsTheCookieThenTheSystem(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   inputs
		want string
	}{
		{
			name: "no cookie and a light system",
			in:   inputs{Width: 1366, Height: 768},
			want: "light",
		},
		{
			name: "no cookie and a dark system",
			in:   inputs{Width: 1366, Height: 768, PrefersDark: true},
			want: "dark",
		},
		{
			// The row that matters: a cookie that says light on a dark system.
			// This is the whole reason the cookie is read at all, and the reason
			// the resolver runs before the first paint rather than after it.
			name: "a light cookie overrides a dark system",
			in: inputs{
				Cookie:      "theme=light&ui=auto",
				CookieSet:   true,
				PrefersDark: true,
				Width:       1366,
				Height:      768,
			},
			want: "light",
		},
		{
			name: "a dark cookie overrides a light system",
			in:   inputs{Cookie: "theme=dark&ui=auto", CookieSet: true, Width: 1366, Height: 768},
			want: "dark",
		},
		{
			// `auto` is a real answer and not an absent one, so a cookie that says
			// auto defers to the system rather than pinning the theme it happened
			// to resolve to when it was written.
			name: "an auto cookie follows the system",
			in: inputs{
				Cookie:      "theme=auto&ui=tv",
				CookieSet:   true,
				PrefersDark: true,
				Width:       1920,
				Height:      1080,
			},
			want: "dark",
		},
		{
			name: "a theme this build does not know follows the system",
			in: inputs{
				Cookie:      "theme=neon&ui=auto",
				CookieSet:   true,
				PrefersDark: true,
				Width:       1366,
				Height:      768,
			},
			want: "dark",
		},
		{
			// The separator the current server-side writer produces, with the `;`
			// already stripped by the cookie sanitiser. Reading the theme out of
			// it is the tolerance the script's split exists for.
			name: "a theme arriving with the sanitiser's separator",
			in:   inputs{Cookie: "theme=dark ui=tv", CookieSet: true, Width: 1920, Height: 1080},
			want: "dark",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, theme := resolve(testCase.in)
			if theme != testCase.want {
				t.Errorf(
					"data-theme = %q, want %q; the cookie wins and the system is "+
						"the fallback (UI §3.7, §6.6)",
					theme, testCase.want,
				)
			}
		})
	}
}

// TestTheScriptAndTheReferenceShareTheirConstants is what makes the reference
// above worth reading: it compares the script's own declarations to the
// reference's tables, as sets.
//
// Each value is extracted from the script by reading the declaration it lives in
// — `H=/…/` for the hint list, `M=/^(…)$/` for the mode names — rather than by
// looking for the value somewhere in the file. A transcription that drifted on
// its *data* while agreeing on its *order* is the failure this catches, and it
// is the failure a reader cannot see by reading the two side by side.
func TestTheScriptAndTheReferenceShareTheirConstants(t *testing.T) {
	t.Parallel()

	source := shell.ResolverSource()

	cases := []struct {
		name     string
		decl     string
		want     []string
		fromList string
	}{
		{
			// The hint list, verbatim from §3.3.
			name:     "the user-agent hint list",
			decl:     "H",
			want:     userAgentHints,
			fromList: "|",
		},
		{
			// `auto` is in the list, and it is not a mode that produces a tier: it
			// is the media queries alone.
			name:     "the accepted ui modes",
			decl:     "M",
			want:     modeNames,
			fromList: "|",
		},
		{
			name:     "the themes the switcher can set",
			decl:     "T",
			want:     switcherThemeNames,
			fromList: "|",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			literal := regexLiteral(t, source, testCase.decl)
			literal = strings.TrimSuffix(strings.TrimPrefix(literal, "^"), "$")
			literal = strings.TrimSuffix(strings.TrimPrefix(literal, "("), ")")

			got := strings.Split(literal, testCase.fromList)
			if len(got) != len(testCase.want) {
				t.Fatalf(
					"the script's %s has %d entries (%v), the reference has %d "+
						"(%v); one of the two has gained a value",
					testCase.name, len(got), got, len(testCase.want), testCase.want,
				)
			}

			for index := range got {
				if got[index] != testCase.want[index] {
					t.Errorf(
						"the script's %s entry %d is %q, the reference says %q; "+
							"§3.3 and §6.6 are the only places either list may "+
							"come from",
						testCase.name, index, got[index], testCase.want[index],
					)
				}
			}
		})
	}
}

// TestTheScriptEmitsExactlyTheTiersTheSpecNames reads the tier-producing function
// out of the script and compares the set of names it can return with §3.7's
// list.
//
// A tier the resolver can write and no rule matches is a document that renders
// at the compact default, which looks like a layout bug on a television and not
// like a resolver bug at all. A tier the spec names and the script cannot produce
// is a tier nobody can reach.
func TestTheScriptEmitsExactlyTheTiersTheSpecNames(t *testing.T) {
	t.Parallel()

	body := functionBody(t, shell.ResolverSource(), "t")

	quoted := regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(body, -1)

	mentioned := map[string]bool{}

	for _, match := range quoted {
		mentioned[match[1]] = true
	}

	// The tier function names the *modes* it branches on as well as the tiers it
	// returns — `f=="phone"` is a comparison, not a result — so the modes come
	// off the set first. What is left has to be exactly §3.7's values, and the
	// subtraction is itself the assertion that no other string is hiding in
	// there.
	emitted := map[string]bool{}

	for name := range mentioned {
		// `auto`, `laptop` and `phone` are modes that are not tiers, so they come
		// off. `tv` is both — it is a mode and a tier — and stays, which is
		// exactly why the comparison has to be "is this a mode that is not also a
		// tier" rather than "is this a mode".
		if contains(modeNames, name) && !contains(tierNames, name) {
			continue
		}

		emitted[name] = true
	}

	for _, tier := range tierNames {
		if !emitted[tier] {
			t.Errorf(
				"the script cannot produce data-ui=%q; §3.7's table names it, so a "+
					"tier the resolver cannot reach is a tier with no layout", tier,
			)
		}
	}

	for name := range emitted {
		if !contains(tierNames, name) {
			t.Errorf(
				"the script can produce data-ui=%q, which is not one of §3.7's "+
					"values (%v); an attribute no rule matches renders at the "+
					"compact default", name, tierNames,
			)
		}
	}
}

// TestTheScriptInstallsAutoAsTheAbsentTheme checks the other half of the theme
// resolution: what the script does when the cookie carries no theme at all.
//
// `auto` has to be a *value* in the parsed preferences before the media query is
// consulted, and not an absent field, for a reason the cookie reader in
// `internal/httpapi/auth` states: an absent preference and an explicit automatic
// must not be distinguishable once they reach the resolver. They are
// distinguishable in exactly one harmful way — the cookie write. A resolver
// that wrote back the theme it had just resolved would turn a reader who had
// chosen automatic into a reader pinned to whatever their system said that
// afternoon, and there would be no way back to following it.
func TestTheScriptInstallsAutoAsTheAbsentTheme(t *testing.T) {
	t.Parallel()

	source := shell.ResolverSource()

	if !strings.Contains(source, `{theme:"auto"}`) {
		t.Errorf(
			"the script does not install theme:auto as the default; without it an " +
				"absent preference and an explicit automatic are different values, " +
				"and the first thing that writes the cookie back pins the theme " +
				"(UI §6.6)",
		)
	}

	// And the write must use the stored preference, not the resolved one.
	if !strings.Contains(source, `"sp_ui=theme="+p.theme+"&ui="`) {
		t.Error(
			"the cookie write does not use the stored theme preference; writing " +
				"the resolved value would convert automatic into a fixed choice " +
				"(UI §6.6)",
		)
	}
}

// TestTheScriptComparesAgainstTheDeclaredBreakpoints is the same idea for the
// numbers, and it is the tie between the script and the stylesheet.
//
// The script cannot read a custom property, and the stylesheet cannot use one in
// a media query condition, so the two files each hold their own copy of 1024,
// 1280, 1536, 2200 and 480. Those copies have to agree or a tier boundary
// exists in one place and not the other: the resolver says `medium` and the
// stylesheet still draws a compact grid. The stylesheet's copy is checked
// against the same constants in `TestEveryMediaQueryMatchesADeclaredConstant`;
// this is the script's half.
func TestTheScriptComparesAgainstTheDeclaredBreakpoints(t *testing.T) {
	t.Parallel()

	body := functionBody(t, shell.ResolverSource(), "t")

	numbers := regexp.MustCompile(`\d+`).FindAllString(body, -1)

	seen := map[string]bool{}
	for _, number := range numbers {
		seen[number] = true
	}

	for _, boundary := range viewportBoundaries {
		if !seen[strconv.Itoa(boundary)] {
			t.Errorf(
				"the resolver does not compare against %d; the stylesheet declares "+
					"--breakpoint-* values that the script would then not agree "+
					"with (UI §3.1, §3.4, §3.5)", boundary,
			)
		}
	}

	declared := map[string]bool{"0": true}
	for _, boundary := range viewportBoundaries {
		declared[strconv.Itoa(boundary)] = true
	}

	for number := range seen {
		if declared[number] {
			continue
		}

		t.Errorf(
			"the resolver compares against %s, which is not one of the five "+
				"boundaries the stylesheet declares (%v) or a zero; a boundary in "+
				"one file and not the other is a tier that exists in one place",
			number, viewportBoundaries,
		)
	}
}

// TestTheUserAgentHintListIsNotWidened is a test about a decision not being
// improved.
//
// §3.3 lists seven tokens and accepts that an unrecognised television shows the
// laptop layout until somebody flips the switcher. The temptation is to widen
// the list — add `Linux`, `Android TV`, `CrKey`, a generic `TV` — and every
// addition is a false positive that puts 10-foot type on a tablet. So the list is
// asserted to be exactly §3.3's seven, and a substring that would catch a common
// non-television is asserted to be absent.
func TestTheUserAgentHintListIsNotWidened(t *testing.T) {
	t.Parallel()

	literal := regexLiteral(t, shell.ResolverSource(), "H")

	// Tokens that would match a device §3.3 does not mean to catch. `Web0S` is
	// already in the list and is a substring of nothing here; `TV` on its own
	// would match `SMART-TV`, `AppleTV` and a great many laptops whose user agent
	// mentions a TV feature; `Android` matches every phone.
	for _, widening := range []string{"Android", "Mobile", "CrKey", "Ubuntu"} {
		if strings.Contains(literal, widening) {
			t.Errorf(
				"the hint list contains %q; §3.3's list is seven tokens and every "+
					"addition is a device that is not a television showing 10-foot "+
					"type. The accepted failure mode — a laptop layout until "+
					"somebody flips the switcher — is the better trade", widening,
			)
		}
	}

	if strings.Count(literal, "|") != len(userAgentHints)-1 {
		t.Errorf(
			"the hint list has %d alternatives, §3.3 names %d: %s",
			strings.Count(literal, "|")+1, len(userAgentHints), literal,
		)
	}
}

// regexLiteral returns the body of the regular expression assigned to a
// one-letter variable in the script.
//
// Read out of the declaration rather than searched for as a value, because the
// point is to compare the script's *data* to the reference's and a search would
// find the tokens in the wrong place — a comment, a string, a second expression.
func regexLiteral(t *testing.T, source, name string) string {
	t.Helper()

	pattern := regexp.MustCompile(`\b` + name + `=/([^/\n]*)/`)

	match := pattern.FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("the script has no %s=/…/ declaration to compare", name)
	}

	return match[1]
}

// functionBody returns the body of a one-letter-named function in the script.
func functionBody(t *testing.T, source, name string) string {
	t.Helper()

	start := strings.Index(source, "function "+name+"(){")
	if start < 0 {
		t.Fatalf("the script has no function %s() to read", name)
	}

	rest := source[start:]

	open := strings.Index(rest, "{")
	depth := 0

	for offset, char := range rest[open:] {
		switch char {
		case '{':
			depth++
		case '}':
			depth--

			if depth == 0 {
				return rest[open+1 : open+offset]
			}
		}
	}

	t.Fatalf("function %s() has an unterminated body", name)

	return ""
}
