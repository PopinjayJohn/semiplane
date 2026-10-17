package markdown

import (
	"reflect"
	"strings"
	"testing"
)

func mustPipeline(t *testing.T, role Role) *Pipeline {
	t.Helper()
	pl, err := New(role)
	if err != nil {
		t.Fatalf("New(%q): %v", role, err)
	}
	return pl
}

func convert(t *testing.T, role Role, src string) string {
	t.Helper()
	out, err := mustPipeline(t, role).Convert([]byte(src))
	if err != nil {
		t.Fatalf("Convert(%q): %v", role, err)
	}
	return string(out)
}

func TestNewRejectsUnknownRole(t *testing.T) {
	for _, role := range []Role{"", "dm", "GM", "admin"} {
		if _, err := New(role); err == nil {
			t.Errorf("New(%q): want error, got nil", role)
		}
	}
}

func TestParseFrontmatter(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantMeta map[string]any
		wantBody string
		wantErr  bool
	}{
		{
			name:     "basic",
			src:      "---\ntitle: Vault\ntags: [a, b]\n---\n# Hi\n",
			wantMeta: map[string]any{"title": "Vault", "tags": []any{"a", "b"}},
			wantBody: "# Hi\n",
		},
		{
			name:     "none",
			src:      "# Hi\n\nplain\n",
			wantMeta: map[string]any{},
			wantBody: "# Hi\n\nplain\n",
		},
		{
			name:     "empty",
			src:      "---\n---\nbody\n",
			wantMeta: map[string]any{},
			wantBody: "body\n",
		},
		{
			name:     "unclosed is body",
			src:      "---\ntitle: x\n\n# still body\n",
			wantMeta: map[string]any{},
			wantBody: "---\ntitle: x\n\n# still body\n",
		},
		{
			name:     "yaml end marker",
			src:      "---\ntitle: x\n...\nbody\n",
			wantMeta: map[string]any{"title": "x"},
			wantBody: "body\n",
		},
		{
			name:     "crlf",
			src:      "---\r\ntitle: x\r\n---\r\nbody\r\n",
			wantMeta: map[string]any{"title": "x"},
			wantBody: "body\r\n",
		},
		{
			name:     "bom",
			src:      "\xef\xbb\xbf---\ntitle: x\n---\nbody\n",
			wantMeta: map[string]any{"title": "x"},
			wantBody: "body\n",
		},
		{
			name:    "invalid yaml errors",
			src:     "---\ntitle: [unclosed\n---\nbody\n",
			wantErr: true,
		},
		{
			name:     "leading spaces disqualify",
			src:      "  ---\ntitle: x\n---\nbody\n",
			wantMeta: map[string]any{},
			wantBody: "  ---\ntitle: x\n---\nbody\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := ParseFrontmatter([]byte(tc.src))
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(doc.Body) != tc.wantBody {
				t.Errorf("body = %q, want %q", doc.Body, tc.wantBody)
			}
			if !reflect.DeepEqual(doc.Meta, tc.wantMeta) {
				t.Errorf("meta = %#v, want %#v", doc.Meta, tc.wantMeta)
			}
		})
	}
	// TagsFromMeta: tags extraction from parsed frontmatter meta.
	// yaml.v3 decodes flow AND block lists as []any, never []string —
	// the []string arm exists for hand-built maps.
	t.Run("tags", func(t *testing.T) {
		cases := []struct {
			name string
			meta map[string]any
			want []string
		}{
			{"nil map", nil, []string{}},
			{"no key", map[string]any{}, []string{}},
			{"nil value", map[string]any{"tags": nil}, []string{}},
			{"absent + other keys", map[string]any{"title": "x"}, []string{}},
			{"flow list []any", map[string]any{"tags": []any{"a", "b"}}, []string{"a", "b"}},
			{"block list []any", map[string]any{"tags": []any{"a", "b", "c"}}, []string{"a", "b", "c"}},
			{"typed []string", map[string]any{"tags": []string{"a", "b"}}, []string{"a", "b"}},
			{"scalar", map[string]any{"tags": "solo"}, []string{"solo"}},
			{"non-string items dropped", map[string]any{"tags": []any{"a", 2, true, "b"}}, []string{"a", "b"}},
			{"mixed typed list", map[string]any{"tags": []string{"q"}, "title": "t"}, []string{"q"}},
			{"quoted trimmed", map[string]any{"tags": []any{"\"a\"", "'b'", " c "}}, []string{"a", "b", "c"}},
			{"empty items dropped", map[string]any{"tags": []any{"", "  ", "\"\""}}, []string{}},
			{"non string non list", map[string]any{"tags": 42}, []string{}},
			{"nested no leak", map[string]any{"semiplane": map[string]any{"tags": []any{"x", "y"}}}, []string{}},
			{
				"top level wins over nested",
				map[string]any{
					"tags":      []any{"a"},
					"semiplane": map[string]any{"tags": []any{"x", "y"}},
				},
				[]string{"a"},
			},
			{"scalar no comma split", map[string]any{"tags": "a, b"}, []string{"a, b"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := TagsFromMeta(tc.meta)
				if got == nil {
					t.Fatal("TagsFromMeta returned nil, want non-nil empty slice")
				}
				if len(tc.want) == 0 && len(got) == 0 {
					return
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("TagsFromMeta(%#v) = %#v, want %#v", tc.meta, got, tc.want)
				}
			})
		}
	})

	// TagsFromMeta over real parsed docs: the shapes the index will
	// actually feed it.
	t.Run("tags from parsed docs", func(t *testing.T) {
		cases := []struct {
			name string
			src  string
			want []string
		}{
			{"flow", "---\ntags: [a, b]\n---\nbody", []string{"a", "b"}},
			{"block", "---\ntags:\n  - a\n  - b\n---\n", []string{"a", "b"}},
			{"nested semiplane no leak", "---\nsemiplane:\n  tags: [x]\ntags: [a]\n---\n", []string{"a"}},
			{"nested only no leak", "---\nsemiplane:\n  tags: [x]\n---\n", []string{}},
			{"scalar", "---\ntags: solo\n---\n", []string{"solo"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				doc, err := ParseFrontmatter([]byte(tc.src))
				if err != nil {
					t.Fatalf("ParseFrontmatter: %v", err)
				}
				got := TagsFromMeta(doc.Meta)
				if len(tc.want) == 0 && len(got) == 0 {
					return
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("TagsFromMeta(ParseFrontmatter(%q)) = %#v, want %#v", tc.src, got, tc.want)
				}
			})
		}
	})
}

func TestWikilinks(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string // required substrings
	}{
		{
			name: "plain",
			src:  "go to [[tavern]] now",
			want: []string{`<a class="wikilink" data-testid="wikilink" href="/notes/tavern">tavern</a>`},
		},
		{
			name: "aliased",
			src:  "at the [[tavern|Rusty Flagon]]",
			want: []string{`href="/notes/tavern">Rusty Flagon</a>`},
		},
		{
			name: "anchor",
			src:  "see [[lore#sealed-orders|orders]]",
			want: []string{`href="/notes/lore#sealed-orders">orders</a>`},
		},
		{
			name: "anchor without alias shows full target",
			src:  "see [[lore#sealed-orders]]",
			want: []string{`href="/notes/lore#sealed-orders">lore#sealed-orders</a>`},
		},
		{
			name: "md suffix converges",
			src:  "see [[lore.md]]",
			want: []string{`href="/notes/lore">lore.md</a>`},
		},
		{
			name: "space escaped",
			src:  "see [[my tavern]]",
			want: []string{`href="/notes/my%20tavern">my tavern</a>`},
		},
		{
			name: "empty target stays literal",
			src:  "a [[ ]] b",
			want: []string{`[[ ]]`},
		},
		{
			name: "unclosed stays literal",
			src:  "a [[tavern b",
			want: []string{`[[tavern`},
		},
		{
			name: "stock links unaffected",
			src:  "a [link](https://example.com) here",
			want: []string{`<a href="https://example.com">link</a>`},
		},
		{
			name: "xss escaped",
			src:  `[[a"><script>|x]]`,
			want: []string{`href="/notes/a%22%3E%3Cscript%3E">x</a>`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := convert(t, RoleGM, tc.src)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("output missing %q\nfull output:\n%s", w, got)
				}
			}
		})
	}
}

func TestGMBlocks(t *testing.T) {
	const secret = "The barkeep feeds the rats."
	src := "public\n\n:::gm\n" + secret + "\n:::\n\nafter\n"
	gm := convert(t, RoleGM, src)
	if !strings.Contains(gm, secret) {
		t.Errorf("GM output missing secret:\n%s", gm)
	}
	if !strings.Contains(gm, `data-testid="gm-block"`) {
		t.Errorf("GM output missing gm-block section:\n%s", gm)
	}
	for _, keep := range []string{"public", "after"} {
		if !strings.Contains(gm, keep) {
			t.Errorf("GM output missing %q:\n%s", keep, gm)
		}
	}

	player := convert(t, RolePlayer, src)
	if strings.Contains(player, secret) {
		t.Errorf("player output leaks secret:\n%s", player)
	}
	if strings.Contains(player, "gm-block") {
		t.Errorf("player output contains gm-block markup:\n%s", player)
	}
	for _, keep := range []string{"public", "after"} {
		if !strings.Contains(player, keep) {
			t.Errorf("player output missing %q:\n%s", keep, player)
		}
	}
}

func TestGMBlockVariants(t *testing.T) {
	t.Run("title", func(t *testing.T) {
		gm := convert(t, RoleGM, ":::gm Reveal later\nsecret\n:::\n")
		if !strings.Contains(gm, `<p class="gm-title">Reveal later</p>`) {
			t.Errorf("missing gm title:\n%s", gm)
		}
		if strings.Contains(convert(t, RolePlayer, ":::gm Reveal later\nsecret\n:::\n"), "secret") {
			t.Error("player sees titled secret")
		}
	})
	t.Run("unclosed fails closed", func(t *testing.T) {
		player := convert(t, RolePlayer, "public\n\n:::gm\nsecret to EOF\n")
		if strings.Contains(player, "secret to EOF") {
			t.Errorf("unclosed block leaks to player:\n%s", player)
		}
		if !strings.Contains(player, "public") {
			t.Errorf("public prose lost:\n%s", player)
		}
		gm := convert(t, RoleGM, "public\n\n:::gm\nsecret to EOF\n")
		if !strings.Contains(gm, "secret to EOF") {
			t.Errorf("GM should see unclosed secret:\n%s", gm)
		}
	})
	t.Run("gmlookalike is not an opener", func(t *testing.T) {
		got := convert(t, RolePlayer, ":::gmx\nnot secret\n:::\n")
		if !strings.Contains(got, "not secret") {
			t.Errorf(":::gmx must stay visible prose:\n%s", got)
		}
	})
	t.Run("indented opener is code", func(t *testing.T) {
		got := convert(t, RolePlayer, "    :::gm\n    secret\n")
		if strings.Contains(got, "gm-block") {
			t.Errorf("indented opener must not parse:\n%s", got)
		}
		if !strings.Contains(got, "secret") {
			t.Errorf("indented block should render as code, visible:\n%s", got)
		}
	})
	t.Run("secret markdown parsed for gm", func(t *testing.T) {
		gm := convert(t, RoleGM, ":::gm\n- item\n- [[tavern]]\n:::\n")
		if !strings.Contains(gm, "<li>") || !strings.Contains(gm, "wikilink") {
			t.Errorf("secret children must parse as Markdown:\n%s", gm)
		}
	})
}

func TestCallouts(t *testing.T) {
	t.Run("note with title and body", func(t *testing.T) {
		got := convert(t, RoleGM, "> [!note] Delver's warning\n> Bring rope.\n")
		for _, w := range []string{
			`<div class="callout callout-note" data-testid="callout" data-callout="note">`,
			`<p class="callout-title">Delver's warning</p>`,
			`<p>Bring rope.</p>`,
		} {
			if !strings.Contains(got, w) {
				t.Errorf("missing %q:\n%s", w, got)
			}
		}
		if strings.Count(got, "Delver") != 1 {
			t.Errorf("title must not duplicate into body:\n%s", got)
		}
	})
	t.Run("bare marker defaults title", func(t *testing.T) {
		got := convert(t, RoleGM, "> [!warning]\n> Careful.\n")
		if !strings.Contains(got, `<p class="callout-title">Warning</p>`) {
			t.Errorf("missing default title:\n%s", got)
		}
	})
	t.Run("unknown type degrades to box", func(t *testing.T) {
		got := convert(t, RoleGM, "> [!homebrew] Hi\n> body\n")
		if !strings.Contains(got, `callout-homebrew`) || !strings.Contains(got, "body") {
			t.Errorf("unknown type must degrade, not drop:\n%s", got)
		}
	})
	t.Run("malformed marker stays quote", func(t *testing.T) {
		got := convert(t, RoleGM, "> [!note]x\n> body\n")
		if strings.Contains(got, "callout-title") {
			t.Errorf("malformed marker must not parse:\n%s", got)
		}
		if !strings.Contains(got, "<blockquote>") {
			t.Errorf("malformed marker must stay a quote:\n%s", got)
		}
	})
	t.Run("plain quotes unaffected", func(t *testing.T) {
		got := convert(t, RoleGM, "> just a quote\n")
		if !strings.Contains(got, "<blockquote>") {
			t.Errorf("plain quote broken:\n%s", got)
		}
	})
	t.Run("uppercase type lowercased", func(t *testing.T) {
		got := convert(t, RoleGM, "> [!NOTE] T\n> b\n")
		if !strings.Contains(got, `callout-note`) {
			t.Errorf("type must lowercase:\n%s", got)
		}
	})
}

func TestFences(t *testing.T) {
	t.Run("statblock", func(t *testing.T) {
		got := convert(t, RoleGM, "```ad-statblock\nname: Cellar Rat\nsize: Small\nhp: 4\n```\n")
		for _, w := range []string{
			`<div class="ad-statblock" data-testid="statblock">`,
			`<div class="statblock-row"><dt>name</dt><dd>Cellar Rat</dd></div>`,
			`<div class="statblock-row"><dt>hp</dt><dd>4</dd></div>`,
		} {
			if !strings.Contains(got, w) {
				t.Errorf("missing %q:\n%s", w, got)
			}
		}
	})
	t.Run("statblock free text degrades", func(t *testing.T) {
		got := convert(t, RoleGM, "```ad-statblock\nname: Rat\njust words\n```\n")
		if !strings.Contains(got, `<div class="statblock-text"><p>just words</p></div>`) {
			t.Errorf("free text must degrade, not drop:\n%s", got)
		}
	})
	t.Run("dice fence", func(t *testing.T) {
		got := convert(t, RoleGM, "```dice\n2d20kh1+5\n```\n")
		if !strings.Contains(got, `<div class="dice-block" data-testid="dice-roll" data-formula="2d20kh1+5">`) {
			t.Errorf("missing dice block:\n%s", got)
		}
	})
	t.Run("stock fences untouched", func(t *testing.T) {
		got := convert(t, RoleGM, "```go\nfmt.Println(\"hi\")\n```\n")
		if !strings.Contains(got, `<pre><code class="language-go">`) {
			t.Errorf("stock fence changed:\n%s", got)
		}
	})
	t.Run("indented code untouched", func(t *testing.T) {
		got := convert(t, RoleGM, "    plain code\n")
		if !strings.Contains(got, "<pre><code>plain code") {
			t.Errorf("indented code changed:\n%s", got)
		}
	})
}

func TestUnsafeOff(t *testing.T) {
	for _, src := range []string{
		"<script>alert(1)</script>\n",
		"para\n\n<div onclick=\"evil()\">x</div>\n",
		"a <b>bold</b> move\n",
	} {
		got := convert(t, RoleGM, src)
		if strings.Contains(got, "<script>") || strings.Contains(got, "onclick") || strings.Contains(got, "<b>") {
			t.Errorf("raw HTML passed through for %q:\n%s", src, got)
		}
		if !strings.Contains(got, "<!-- raw HTML omitted -->") {
			t.Errorf("expected omission marker for %q:\n%s", src, got)
		}
	}
}

func TestGFMExtensions(t *testing.T) {
	got := convert(t, RoleGM, "| A | B |\n|---|---|\n| 1 | 2 |\n\n- [ ] todo\n- [x] done\n\n~~strike~~\n")
	for _, w := range []string{"<table>", "checked", "<del>strike</del>"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q:\n%s", w, got)
		}
	}
}
