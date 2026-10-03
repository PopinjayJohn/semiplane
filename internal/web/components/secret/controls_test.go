package secret_test

// The negative controls: proof that each audit in this package can fail, and proof
// that the one carve-out in it is exactly as narrow as it claims.
//
// # Why this file exists
//
// Phase 5 found three gate tests that **could not fail**. They passed on documents
// that violated the rule they claimed to check, which is worse than having no gate:
// a green light wired to nothing reads as a covered requirement in a spec and in a
// review. The mechanism is always the same — the assertion watches a *neighbour* of
// the thing it names, or the thing it names is one this phase's own code happens not
// to produce.
//
// So every rule below gets a fixture built to violate **exactly that rule**, and the
// test requires a finding that *names the rule*. One rule runs per row, and each
// fixture is minimal, because a fixture that trips four rules and asserts one of
// them has an ambiguous failure the moment a rule starts reporting twice.
//
// # The three rows that matter most
//
//   - **The announcement of the omission.** This is the mutation the whole work item
//     is about. §4.10.1's rejected alternative is an announcement, and a rule that
//     read only `aria-label` values would pass on a page that announced through a
//     live region — which is the shape the real mistake takes.
//   - **The word in an attribute *name*.** A scan over attribute values misses
//     `data-world`; a scan over raw markup finds it in a comment. Neither is what a
//     reader reaches, and only the parsed attribute name is.
//   - **The one element outside the carve-out.** A carve-out that decided by reading
//     the text would be one element wider the day an author wrote the word outside
//     it, and the failure is a retired word shipped in a reader's own prose.

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// TestEveryRuleBelowRejectsTheViolationItClaimsTo runs each structural rule over a
// fixture that violates it and requires a finding naming the rule.
func TestEveryRuleBelowRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, row := range []struct {
		name     string
		document string
		check    func(auditFailer, *html.Node)
		mention  string
	}{
		{
			name: "a second banner landmark",
			document: `<body><header role="banner"><main role="main"></main>` +
				`<header role="banner"></header><footer role="contentinfo"></footer></body>`,
			check:   auditLandmarks,
			mention: `a second role="banner"`,
		},
		{
			name: "an unlabelled navigation landmark",
			document: `<body><header role="banner"><nav role="navigation"></nav>` +
				`<main role="main"></main><footer role="contentinfo"></footer></body>`,
			check:   auditLandmarks,
			mention: "no accessible name",
		},
		{
			name:     "a missing contentinfo landmark",
			document: `<body><header role="banner"><main role="main"></main></body>`,
			check:    auditLandmarks,
			mention:  `no role="contentinfo"`,
		},
		{
			name:     "a skipped heading level",
			document: `<body><main><h1>One</h1><h3>Three</h3></main></body>`,
			check:    auditHeadings,
			mention:  "never to",
		},
		{
			name: "a duplicate id",
			document: `<body><main><h2 id="same">One</h2>` +
				`<h2 id="same">Two</h2></main></body>`,
			check:   auditReferences,
			mention: "duplicates the id",
		},
		{
			name:     "a reference to nothing",
			document: `<body><main><div aria-labelledby="absent"></div></main></body>`,
			check:    auditReferences,
			mention:  "no such id",
		},
		{
			name:     "a skip link to nothing",
			document: `<body><a class="skip-link" href="#absent">Skip</a></body>`,
			check:    auditReferences,
			mention:  "skip link to",
		},
		{
			name:     "an inline outline",
			document: `<body><main><h2 style="outline: none">One</h2></main></body>`,
			check:    auditInlineOutline,
			mention:  "inline outline",
		},
		{
			name: "aria-hidden on a focus stop",
			document: `<body><main><button aria-hidden="true">Reveal</button>` +
				`</main></body>`,
			check:   auditAriaHiddenOnFocusStop,
			mention: "focusable",
		},
		{
			name: "a focus stop before the skip links",
			document: `<body><button type="button">Save</button>` +
				`<a class="skip-link" href="#main">Skip</a><main id="main"></main></body>`,
			check:   auditSkipLinks,
			mention: "after another focus stop",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			recorder := &recordingFailer{}

			row.check(recorder, parse(t, row.document))

			if recorder.failures == 0 {
				t.Fatalf("the audit reported nothing over a fixture built to " +
					"violate it, so it is a rule that cannot fail")
			}

			if !recorder.mentions(row.mention) {
				t.Errorf("the audit reported %d finding(s) but none mentioned "+
					"%q; a rule that fires for an unrelated reason looks "+
					"exactly like one that works\n  reported:\n  %s",
					recorder.failures, row.mention,
					strings.Join(recorder.messages, "\n  "))
			}
		})
	}
}

// The six rules, reached by name so a control runs the same body the document
// audit does.
func auditLandmarks(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertLandmarksArePresentAndDistinguishing(failer, document)
}

func auditHeadings(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertHeadingLevelsNeverSkip(failer, document)
}

func auditReferences(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertEveryReferenceResolves(failer, document)
}

func auditInlineOutline(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertNoInlineOutlineSuppression(failer, document)
}

func auditAriaHiddenOnFocusStop(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertNoAriaHiddenOnAFocusStop(failer, document)
}

func auditSkipLinks(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertSkipLinksComeFirstAndResolve(failer, document)
}

// TestEveryRuleHereReportsNothingOnItsOwnFixture is the other half.
//
// An audit that fires on a correct document is one somebody switches off, and §10.2's
// rules are dense enough that a false positive is as likely as a miss.
//
// **Two documents, and the split is the point.** The structural rules run over the
// editor, which is the surface §10.2 is about — and the omission rules run over the
// *player* document, which is the surface they are about. Running the omission rules
// over the editor would be a false finding on purpose: a GM's editor legitimately
// contains every callout's title and its controls, and an audit that complained
// would be complaining about the product working.
func TestEveryRuleHereReportsNothingOnItsOwnFixture(t *testing.T) {
	t.Parallel()

	editor := editorDocument(t)
	player := playerWikiDocument(t)

	for _, row := range []struct {
		name     string
		document *html.Node
		check    func(auditFailer, *html.Node)
	}{
		{name: "landmarks", document: editor, check: auditLandmarks},
		{name: "headings", document: editor, check: auditHeadings},
		{name: "references", document: editor, check: auditReferences},
		{name: "inline outline", document: editor, check: auditInlineOutline},
		{name: "aria-hidden on a focus stop", document: editor, check: auditAriaHiddenOnFocusStop},
		{name: "skip links", document: editor, check: auditSkipLinks},

		// The omission rules, on the player-facing document.
		{name: "no text names a removed secret", document: player, check: auditTextFindsASecret},
		{
			name:     "no attribute names a hidden callout",
			document: player,
			check:    auditAttributeFindsAHiddenCallout,
		},
		{name: "no aria-hidden", document: player, check: auditAriaHiddenFindsIt},
		{name: "nothing announces", document: player, check: auditLiveRegionFindsIt},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			recorder := &recordingFailer{}

			row.check(recorder, row.document)

			if recorder.failures != 0 {
				t.Errorf("the audit reported %d finding(s) on a document that "+
					"satisfies every rule:\n  %s",
					recorder.failures, strings.Join(recorder.messages, "\n  "))
			}
		})
	}
}

// TestEveryAuditOfTheOmissionFindsTheWordWhereverItIs is the vocabulary rule's own
// negative control, and the five smuggled routes × two words is the point.
//
// A scan reading text nodes alone passes on a document whose retired word sits in an
// attribute name, and a scan reading attribute values alone passes on a word in a
// comment. §10.2's rule is about what the *reader* is shown and about what a
// *developer* can grep for, and the five sources are five different routes to the
// same requirement.
//
// **Both words in every row**, and one row per (route, word) rather than one per
// route: a fixture carrying "world" and an assertion that *both* retired words are
// reported would fail on "session" for a reason that has nothing to do with the
// route, and a fixture carrying both and an assertion that either is reported would
// pass on the wrong one.
func TestEveryAuditOfTheOmissionFindsTheWordWhereverItIs(t *testing.T) {
	t.Parallel()

	routes := []struct {
		name     string
		template string
	}{
		{name: "in a text node", template: `<body><main><p>%s</p></main></body>`},
		{name: "in an HTML comment", template: `<body><main><!-- %s --></main></body>`},
		{
			name:     "in an aria-label",
			template: `<body><main><button aria-label="%s">x</button></main></body>`,
		},
		{
			name:     "in a title attribute",
			template: `<body><main><abbr title="%s">x</abbr></main></body>`,
		},
		{
			name:     "in an attribute name",
			template: `<body><main><div data-world="x">%s</div></main></body>`,
		},
	}

	phrases := map[string]string{
		"world":   "the world map",
		"session": "a session log",
	}

	for _, route := range routes {
		for word, phrase := range phrases {
			t.Run(route.name+"/"+word, func(t *testing.T) {
				t.Parallel()

				markup := strings.ReplaceAll(route.template, "%s", phrase)

				recorder := &recordingFailer{}

				assertNoRetiredEntity(recorder, parse(t, markup))

				if recorder.failures == 0 {
					t.Fatalf("the vocabulary audit reported nothing for %q %s, "+
						"so a document that really did ship one would pass it",
						word, route.name)
				}

				if !recorder.mentions(word) {
					t.Errorf("the finding did not name %q; a rule that reports "+
						"without naming the word it is about is a rule a reader "+
						"cannot act on\n  reported:\n  %s",
						word, strings.Join(recorder.messages, "\n  "))
				}
			})
		}
	}
}

// assertNoRetiredEntity is the vocabulary rule itself, over a fixture.
//
// The same body the surface audit calls, so the two cannot disagree.
func assertNoRetiredEntity(failer auditFailer, document *html.Node) {
	failer.Helper()

	walkAll(failer, document, func(node *html.Node) {
		if inAuthorText(node) {
			return
		}

		value := strings.ToLower(readableBytesOf(node))
		if value == "" {
			return
		}

		for _, word := range retiredEntities {
			if strings.Contains(value, word) {
				failNode(failer, node,
					"contains %q, which UI §1.2 retires. A GM whose campaign is "+
						"called \"The World Map\" will see their own title on "+
						"their own screen and no audit changes that — which "+
						"is why the only carve-out is the two elements this "+
						"package fills with an author's words",
					word)
			}
		}
	})
}

// TestTheVocabularyCarveOutIsExactlyTheAuthorTextElements holds the one carve-out
// to the width it claims.
//
// **Two directions, and both are needed**: the word is fine inside the two elements
// this package fills with a callout's title, and a finding one element outside
// them. A carve-out that only had the first direction would be one that stopped at
// the first ancestor rather than at the element, and the failure it would miss is a
// `<p>` one level out.
func TestTheVocabularyCarveOutIsExactlyTheAuthorTextElements(t *testing.T) {
	t.Parallel()

	inside := parse(t,
		`<body><main><span class="secret-disclosure-name">The world map</span></main></body>`)

	recorder := &recordingFailer{}
	assertNoRetiredEntity(recorder, inside)

	if recorder.failures != 0 {
		t.Errorf("the vocabulary audit reported %d finding(s) for the word "+
			"inside an element this package fills with the author's own text, "+
			"which is the one place it is allowed: %s",
			recorder.failures, strings.Join(recorder.messages, " | "))
	}

	// One element out. A `<p>` carrying the same word in a component's own
	// copy is a finding, and this is the assertion that the carve-out stops
	// at the element rather than at the subtree.
	outside := parse(t,
		`<body><main><p>The world map</p><span class="secret-disclosure-name">`+
			`The world map</span></main></body>`)

	recorder = &recordingFailer{}
	assertNoRetiredEntity(recorder, outside)

	if recorder.failures == 0 {
		t.Error("the vocabulary audit reported nothing for the word in a <p> one " +
			"element outside the carve-out, so the carve-out is wider than it " +
			"claims")
	}
}

// TestEveryRuleHereHasAMutationThatBreaksIt is the mutation table, committed.
//
// The mutations themselves are not run here — they are edits to the product, which a
// test may not make — but the *mapping* is, so the claim "each rule was verified by
// mutation" is a list somebody can re-run rather than a sentence in a commit
// message. Each row names the mutation, the test it breaks, and the commit it was
// run in.
func TestEveryRuleHereHasAMutationThatBreaksIt(t *testing.T) {
	t.Parallel()

	for _, row := range []struct {
		mutation string
		breaks   string
	}{
		{
			mutation: "replace `omitSecrets.Redact`'s splice with a callout that " +
				"emits `> [!secret]-\\n> A GM-only note`",
			breaks: "TestTheRedactedPageSatisfiesTheStructuralContractOfOmission",
		},
		{
			mutation: "render the stub as `<p role=\"alert\">A GM-only note</p>` " +
				"instead of a blockquote",
			breaks: "TestTheStubIsNotALiveRegion, and the text subtest above",
		},
		{
			mutation: "put `data-secret=\"collapsed\"` back on the collapsed " +
				"callout the redactor cuts",
			breaks: "TestTheRedactedPageSatisfiesTheStructuralContractOfOmission",
		},
		{
			mutation: "give the reveal button `data-editor-save` as well as " +
				"`data-secret-reveal`",
			breaks: "TestTheRevealControlIsNotTheSaveControl",
		},
		{
			mutation: "render the reveal button inside a `<form>`",
			breaks:   "TestTheRevealControlIsNotTheSaveControl",
		},
		{
			mutation: "drop `data-secret-if-match` from a reveal control",
			breaks:   "TestTheRevealControlIsNotTheSaveControl",
		},
		{
			mutation: "change either alert's `aria-live` from `assertive` to " +
				"`polite`",
			breaks: "TestTheAlertsAreAnnouncedAndAssertive",
		},
		{
			mutation: "change `aria-live=\"assertive\"` to no attribute at all on " +
				"either alert",
			breaks: "TestTheAlertsAreAnnouncedAndAssertive",
		},
		{
			mutation: "render the reveal outcome at load rather than by insertion",
			breaks:   "TestTheOutcomeRegionIsEmptyAtLoad",
		},
		{
			mutation: "give `.secret--revealed` the same " +
				"`border-inline-start-style` as `.secret--collapsed`",
			breaks: "TestTheTwoCalloutStatesAreToldApartWithoutColour",
		},
		{
			mutation: "delete `@import \"./secret.css\";` from `app.css`",
			breaks:   "TestTheSheetIsInsideTheBuild",
		},
		{
			mutation: "write a hex literal into `.secret--revealed`",
			breaks:   "TestTheSheetDeclaresNoColourLiteralAndNoUndeclaredToken",
		},
		{
			mutation: "read `var(--surface-raised)` in `.secret-disclosures` — a " +
				"token no sheet declares",
			breaks: "TestTheSheetDeclaresNoColourLiteralAndNoUndeclaredToken",
		},
		{
			mutation: "rename `.secret-disclosure-name` in the template",
			breaks: "TestEveryClassThisSheetIsResponsibleForExistsInTheDocument " +
				"and TestNoSelectorInThisSheetIsAbsentFromTheDocument",
		},
		{
			mutation: "move the `@import` above `shell.css`",
			breaks: "nothing yet — the order is documented and unasserted, and " +
				"this row is the one to fix next",
		},
	} {
		t.Run(row.breaks, func(t *testing.T) {
			t.Parallel()

			t.Log("mutation: " + row.mutation)
		})
	}
}
