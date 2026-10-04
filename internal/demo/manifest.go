package demo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/semiplane/semiplane/internal/domain"
)

// ManifestFileName is where the manifest lives inside the extracted artefact: one
// file, at the root, beside the three campaign directories.
//
// **Not inside a campaign's content root**, and that is a confinement decision
// rather than a layout one. A campaign's content root is read through `os.Root`
// and indexed as pages; a file sitting in it would be an unindexed file in a
// directory the wiki is entitled to walk, and the one file in the artefact that
// is not campaign content is the one file that must not be reachable as such.
const ManifestFileName = "demo.manifest.yml"

// MaxManifestBytes bounds the manifest at 64 KiB.
//
// Sized against `theme.ManifestFileName`'s 16 KiB and `content.MaxDocumentBytes`'s
// 4 MiB, and for the same reason both of those are where they are: the cap is not
// there to catch a large manifest, it is there so "read the manifest" is a bounded
// operation even when the file is not a manifest at all. 64 KiB is two orders of
// magnitude past what this schema can express — three campaigns, three accounts
// and a scene — so the cap bounds the parser rather than pretending to bound the
// content.
const MaxManifestBytes = 64 << 10

// The refusals. All of them are refusals to *seed*, not to read: a manifest this
// package will not act on is a refusal with an exit status, never a degraded seed.
var (
	// ErrManifestTooLarge is a manifest over MaxManifestBytes.
	ErrManifestTooLarge = fmt.Errorf("%w over the %d-byte limit", errDemo, MaxManifestBytes)

	// ErrMalformedManifest is a manifest the YAML parser would not read, or one
	// carrying a key this build has no name for.
	//
	// **One sentinel for both**, deliberately. `theme.ErrMalformedManifest` merges
	// them for the same reason and because a caller of either has exactly one thing
	// to do: fix the file. The parser's own message is discarded rather than
	// wrapped — a YAML error quotes the line it choked on, and this file's lines
	// are an artefact a user is about to paste into an issue.
	ErrMalformedManifest = fmt.Errorf("%w: the demo manifest is not valid YAML", errDemo)

	// ErrIncompleteManifest is a manifest that parses and is not usable: no
	// version, no accounts, a campaign with no members, a role or visibility this
	// build does not know.
	ErrIncompleteManifest = fmt.Errorf("%w: the demo manifest is not usable", errDemo)

	// ErrNoManifest is a root with no manifest in it.
	//
	// Its own sentinel because it has its own answer: `--root` was pointed at
	// something that is not an extracted artefact, and the fix is to extract it.
	ErrNoManifest = fmt.Errorf("%w: there is no %s in the demo root", errDemo, ManifestFileName)
)

// errDemo is the umbrella every refusal in this package satisfies, so a command
// can ask one question — "did the demo refuse?" — and never name a sentinel.
//
// The same shape as `plugin.ErrMalformedPlugin`: the individual sentinels say
// *what* was refused and the umbrella says *that refusing is possible*, which are
// two questions and merging them makes the caller ask both with one `errors.Is`
// and get a wrong answer for whichever it did not mean.
var errDemo = errors.New("demo")

// Manifest is one demo artefact: which semiplane release it was built for, the
// accounts it needs, and the campaigns it seeds.
//
// **Five top-level keys and no others**, and `KnownFields(true)` on the decoder
// is what enforces it. That is the whole argument for a struct target rather than
// a map: a manifest carrying `products:` or a second `campaigns:` block decodes
// as a map and validates as almost-empty, and the demo that follows is a demo with
// one campaign and no accounts — a broken product that reports success. `theme`'s
// manifest header states the same reason for the same mechanism.
type Manifest struct {
	// Schema is this manifest's own schema version, matched against `SchemaVersion`.
	//
	// **Separate from `Product`**, and the distinction is what makes both checks
	// meaningful. `Product` compares releases and says "these were built together";
	// `Schema` compares the *document* and says "this build knows what these keys
	// mean". A build that renames or drops a key must move `SchemaVersion`, because
	// a manifest the parser accepts while reading the wrong field is the silent
	// failure `KnownFields` cannot reach.
	Schema int `yaml:"schema"`

	// Product is the semiplane release this artefact was built against — the value
	// the release publish step stamps from the tag being published. Required,
	// because a manifest that names no version is a manifest nothing can be
	// compared to and the skew check would pass vacuously.
	Product string `yaml:"product"`

	// Instance is the instance name a seeded install presents in its header and
	// rail. Optional; empty keeps whatever the instance is configured with, which
	// is right for an operator who set one deliberately.
	Instance string `yaml:"instance"`

	// Accounts are the accounts the seed creates, in manifest order.
	Accounts []Account `yaml:"accounts"`

	// Campaigns are the campaigns the seed registers, in manifest order.
	Campaigns []Campaign `yaml:"campaigns"`
}

// Account is one account the seed creates.
//
// **Two fields, and the absence of a third is the point.** There is no `is_admin`
// key, and `KnownFields(true)` refuses a manifest carrying one: plan D14 makes the
// demo GM "campaign-GM only, never an instance admin", and the strongest form of
// that is a schema in which the claim cannot be written down. Instance
// administration is `semiplane admin create --admin`, a command a human runs
// deliberately, which is exactly why a demo credential does not get it.
type Account struct {
	// Username is the account's name. Required; the store refuses an empty one and
	// a duplicate is `store.ErrConflict`, which the seed reports as a refusal
	// rather than retried.
	Username string `yaml:"username"`
}

// Campaign is one campaign the seed registers, plus everything that makes it a
// demo rather than an empty tenant.
type Campaign struct {
	// Slug is the campaign's URL segment and the name of its vault directory.
	// Required, and validated through `domain.ValidateSlug` — the one rule about
	// what a slug may contain, called rather than restated.
	Slug string `yaml:"slug"`

	// Name is the campaign's display name. Defaults to the slug when empty, which
	// is what `admin campaign add` does and for the same reason: a campaign whose
	// header reads `greyhaven` rather than "Greyhaven" is a demo that looks
	// unfinished.
	Name string `yaml:"name"`

	// Vault is the campaign's content root, **relative to the demo root**.
	//
	// Required, and required to be exactly `Slug` — see `check`. It is stated
	// rather than inferred because it is the one place a manifest and a filesystem
	// meet, and an operator reading the artefact should be able to see which
	// directory is which campaign's pages without counting directories.
	Vault string `yaml:"vault"`

	// System is the gameplay system id this campaign plays under, e.g. `dnd5e`.
	//
	// **Not defaulted and not validated against the registry here**, for the reason
	// `campaigns.RegisterRequest.SystemID` gives: a campaign naming a system this
	// build does not have refuses to start its game and still serves its wiki, which
	// is `forgotten-realm` in the shipped demo and is correct behaviour rather than a
	// broken install (S-14.8). What the seed does check is the consequence: a system
	// this build cannot resolve gets an empty `ruleset_version` and says so.
	System string `yaml:"system"`

	// Visibility is `private` or `public`. Defaults to `private`, the safe half:
	// `public` permits anonymous wiki read and nothing else, and a demo that
	// publishes itself because nobody said otherwise is a demo that leaked.
	Visibility string `yaml:"visibility"`

	// Demonstrates is the demo intent this campaign carries, read by
	// `make demo-check` and **ignored by the seed**.
	//
	// It exists because D10 gives two of the three campaigns a specific reason to
	// exist — "proves anonymous read, visibility-gated assets and search", "proves
	// §10.8" — and a completeness gate that asserted uniform coverage across all
	// three would be asserting that the boundary campaigns demonstrate features they
	// exist to *not* demonstrate. The intent is therefore declared here and the gate
	// reads it. Free-form strings: a vocabulary this package invented would be a
	// second list beside the gate's, and the gate is not this package's.
	Demonstrates []string `yaml:"demonstrates"`

	// Members are this campaign's memberships, in order.
	//
	// **Required and non-empty.** A campaign with no members cannot be played and
	// cannot be edited, and a demo nobody can sign in to demonstrates nothing.
	Members []Member `yaml:"members"`

	// RuleModules are the campaign's house-rule modules, in declaration order.
	//
	// Optional and usually absent, because this build registers no house-rule
	// modules at all (`cmd/server/systems.go` step 4, and the emptiness is the
	// answer rather than a gap). The key exists so that the day one ships the demo
	// can enable it, and so that naming an unregistered module is refused *at the
	// seed* rather than at a GM's first campaign load.
	//nolint:tagliatelle // `rule_modules` is snake_case because this file is a
	// document a person authors by hand, and the only other multi-word YAML in the
	// repository — the 5e data packs — spells its keys the same way. A camelCase
	// spelling here would be a third vocabulary rather than the second.
	RuleModules []Module `yaml:"rule_modules"`

	// State is the seeded tabletop. Nil when the campaign declares none, which is a
	// real answer: a campaign with no state opens as an empty grid, and
	// `realtime.Registry.Open` writes that immediately.
	State *Table `yaml:"state"`

	// visibility is the parsed `Visibility`, and it is what the row is written
	// with. See the note on parsed values at the foot of this file.
	visibility domain.Visibility
}

// Member is one membership: which account, and which role.
//
// **Two fields, with the role as a string parsed by `domain.ParseRole`.** The
// vocabulary is `gm` and `player` and it belongs to `internal/domain`; a manifest
// spelling a third role is refused at parse rather than stored and failed closed
// at every access check, and the role the seed writes is the role the manifest
// declared or the seed does not run.
type Member struct {
	Username string `yaml:"username"`
	Role     string `yaml:"role"`

	// role is the parsed `Role`. See the note on parsed values at the foot of this
	// file.
	role domain.Role
}

// Module is one house-rule module row the manifest declares.
//
// **The persisted shape, not the compiled-in definition**, for the reason
// `determinism.Module` gives: which modules exist is a property of the running
// build and not of a document on disk. `config` is passed through as a JSON object
// and validated only as one, because which keys a module understands is the
// module's own vocabulary (P1d).
type Module struct {
	ID       string         `yaml:"id"`
	Enabled  *bool          `yaml:"enabled"`
	Config   map[string]any `yaml:"config"`
	Position int            `yaml:"position"`
}

// Table is the tabletop a campaign is seeded with.
//
// **Placements and nothing else, because that is the whole of what
// `campaign_state` holds.** The delivery plan asks for "a scene with placements,
// partially-depleted HP, conditions, fog and an initiative order"; fog and
// initiative are not in `realtime.Document`, and inventing fields for them would
// produce bytes `DecodeDocument` refuses or silently ignores. A placement's
// position, hit points, condition set and visibility are all the document has, and
// this schema does not pretend otherwise.
type Table struct {
	// Paused is the tabletop's paused flag.
	Paused bool `yaml:"paused"`

	// Placements is the scene. Required to be present (possibly empty) when a
	// `state:` block is declared, so "an empty tabletop" and "no tabletop" are two
	// different things a reader can see.
	Placements []Placement `yaml:"placements"`
}

// Placement is one game object instance on the seeded tabletop.
//
// **No `version`, and that is deliberate.** `realtime.Document.check` refuses a
// placement at version 0 and refuses one whose version exceeds the document's
// revision, because every placement this project creates is stamped by the
// mutation path and 0 is unreachable. A manifest that could state a version would
// be a second place that invariant is decided; the seed stamps it instead.
type Placement struct {
	ID string `yaml:"id"`
	X  int    `yaml:"x"`
	Y  int    `yaml:"y"`
	HP int    `yaml:"hp"`
	//nolint:tagliatelle // `max_hp` for the reason `RuleModules` gives: it is 5e's
	// own spelling, and it is what a character sheet says.
	MaxHP      int      `yaml:"max_hp"`
	Conditions []string `yaml:"conditions"`

	// Visible defaults to true. A manifest that omits it means a token on the map,
	// and a token that has to be spelled out in every campaign is a schema nobody
	// remembers.
	Visible *bool `yaml:"visible"`
}

// Parse reads a manifest and returns it, refusing anything it would seed wrongly.
//
// Total in the sense that matters: a manifest it will not act on returns an error
// and **no** manifest, so a caller cannot seed half of one. Every refusal names
// the field and the value that broke it, because the file is a release artefact an
// operator is about to edit and the fix is always in the file.
func Parse(src []byte) (Manifest, error) {
	if len(src) > MaxManifestBytes {
		return Manifest{}, ErrManifestTooLarge
	}

	var parsed Manifest

	decoder := yaml.NewDecoder(bytes.NewReader(src))
	decoder.KnownFields(true)

	if err := decoder.Decode(&parsed); err != nil {
		// An empty document is not an error, and this is the one place the theme
		// manifest's handling of `io.EOF` does not carry over: an empty file is not
		// a manifest with nothing in it yet, it is a manifest the artefact is
		// missing, and `check` refuses it below with a message that says so. The
		// distinction is the difference between "you have the wrong file" and "you
		// have a file with nothing in it", and the operator needs the first.
		if errors.Is(err, io.EOF) {
			return Manifest{}, fmt.Errorf("%w: it is empty", ErrIncompleteManifest)
		}

		return Manifest{}, ErrMalformedManifest
	}

	if err := parsed.check(); err != nil {
		return Manifest{}, err
	}

	return parsed, nil
}

// Load reads the manifest from an extracted artefact root.
//
// The one place the manifest is found on disk, so `--root` means the same thing
// to the seed and to `make demo-check`. `root` must be a directory: a manifest
// read through a *file* root would be the artefact pointing at itself.
func Load(root string) (Manifest, error) {
	path := filepath.Join(root, ManifestFileName)

	source, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, fmt.Errorf("%w (%s)", ErrNoManifest, path)
		}

		return Manifest{}, fmt.Errorf("%w: %w", errDemo, err)
	}

	return Parse(source)
}

// check refuses a manifest this build would seed wrongly.
//
// **Every decision in one function, and the order is the order a reader checks a
// manifest in**: the document's own identity first (schema, product), then the
// accounts, then the campaigns in manifest order. A manifest can fail several
// ways and the operator should be told the first one the way they would find it.
func (m Manifest) check() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf(
			"%w: the manifest declares schema %s and this build reads schema %d; "+
				"extract the demo artefact published for this release",
			ErrIncompleteManifest, strconv.Itoa(m.Schema), SchemaVersion,
		)
	}

	if strings.TrimSpace(m.Product) == "" {
		return fmt.Errorf(
			"%w: `product` is required: it is the semiplane release this artefact was "+
				"built for, and it is what `demo seed` compares this binary against",
			ErrIncompleteManifest,
		)
	}

	if len(m.Accounts) == 0 {
		return fmt.Errorf(
			"%w: `accounts` is empty, so the demo has nobody to sign in as",
			ErrIncompleteManifest,
		)
	}

	seenAccounts := make(map[string]struct{}, len(m.Accounts))

	for index := range m.Accounts {
		account := &m.Accounts[index]

		account.Username = strings.TrimSpace(account.Username)
		if account.Username == "" {
			return fmt.Errorf("%w: `accounts[%d].username` is empty", ErrIncompleteManifest, index)
		}

		if _, duplicate := seenAccounts[account.Username]; duplicate {
			return fmt.Errorf(
				"%w: `accounts` names %q twice",
				ErrIncompleteManifest,
				account.Username,
			)
		}

		seenAccounts[account.Username] = struct{}{}
	}

	if len(m.Campaigns) == 0 {
		return fmt.Errorf(
			"%w: `campaigns` is empty, so there is nothing to demonstrate",
			ErrIncompleteManifest,
		)
	}

	seenCampaigns := make(map[string]struct{}, len(m.Campaigns))

	for index := range m.Campaigns {
		if err := m.Campaigns[index].check(seenAccounts); err != nil {
			return fmt.Errorf("campaigns[%d]: %w", index, err)
		}

		slug := m.Campaigns[index].Slug
		if _, duplicate := seenCampaigns[slug]; duplicate {
			return fmt.Errorf("%w: `campaigns` names %q twice", ErrIncompleteManifest, slug)
		}

		seenCampaigns[slug] = struct{}{}
	}

	return nil
}

// check refuses one house-rule module row, and normalises its defaults.
func (m *Module) check() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("%w: `id` is empty", ErrIncompleteManifest)
	}

	if m.Enabled == nil {
		// On by default, because a row a manifest declares is a row it wants.
		// `campaign_rule_modules` defaults `enabled` to 1 for the same reason, and
		// saying `enabled: false` here is how a demo shows what a *disabled* module
		// looks like in the status page.
		enabled := true
		m.Enabled = &enabled
	}

	return nil
}

// ParsedVisibility returns the campaign's visibility as the domain type.
func (c *Campaign) ParsedVisibility() domain.Visibility { return c.visibility }

// check refuses one campaign, given the account names the manifest declares.
func (c *Campaign) check(accounts map[string]struct{}) error {
	if err := domain.ValidateSlug(c.Slug); err != nil {
		// The domain's own error, not a restatement: `ValidateSlug` owns what a slug
		// may contain and a second answer here would be a second rule.
		return fmt.Errorf("%w: `slug`: %w", ErrIncompleteManifest, err)
	}

	// Required to equal the slug, and the reason is a seam rather than a
	// preference: `campaigns.Registrar` — the one door into tenancy, reused here
	// for the reason `admin campaign add` reuses it — derives each content root as
	// `<demo root>/<slug>` and offers no way to say otherwise. A manifest claiming
	// anything else would be a claim the seed cannot honour, so it is refused with
	// the sentence that explains the constraint rather than ignored.
	if c.Vault != c.Slug {
		return fmt.Errorf(
			"%w: `vault` is %q and `slug` is %q; a campaign's content root is created as "+
				"<demo root>/<slug> by the registrar this seed reuses, so the vault directory "+
				"must be named for the slug",
			ErrIncompleteManifest, c.Vault, c.Slug,
		)
	}

	if c.Name == "" {
		c.Name = c.Slug
	}

	if c.Visibility == "" {
		c.Visibility = domain.VisibilityPrivate.String()
	}

	parsed, err := domain.ParseVisibility(c.Visibility)
	if err != nil {
		return fmt.Errorf("%w: `visibility`: %w", ErrIncompleteManifest, err)
	}

	c.visibility = parsed

	if len(c.Members) == 0 {
		return fmt.Errorf(
			"%w: `members` is empty, so nobody can play or edit it",
			ErrIncompleteManifest,
		)
	}

	seenMembers := make(map[string]struct{}, len(c.Members))
	gms := 0

	for index := range c.Members {
		member := &c.Members[index]

		member.Username = strings.TrimSpace(member.Username)
		if member.Username == "" {
			return fmt.Errorf("%w: `members[%d].username` is empty", ErrIncompleteManifest, index)
		}

		if _, declared := accounts[member.Username]; !declared {
			return fmt.Errorf(
				"%w: `members[%d]` names the account %q, which `accounts` does not declare",
				ErrIncompleteManifest, index, member.Username,
			)
		}

		if _, duplicate := seenMembers[member.Username]; duplicate {
			return fmt.Errorf(
				"%w: `members` names %q twice; the pair (campaign, account) is the primary key",
				ErrIncompleteManifest, member.Username,
			)
		}

		seenMembers[member.Username] = struct{}{}

		role, err := domain.ParseRole(member.Role)
		if err != nil {
			return fmt.Errorf("%w: `members[%d].role`: %w", ErrIncompleteManifest, index, err)
		}

		member.role = role

		if role == domain.RoleGM {
			gms++
		}
	}

	// **Exactly one GM, not at least one.** `campaigns.Registrar` is seeded with one
	// owner and creates exactly one GM membership, so a manifest naming two would
	// have the registrar pick one and the other silently never happen — a campaign
	// whose manifest says two GMs and whose roster has one. Naming none is refused
	// for the mirror reason: a campaign nobody can edit or run demonstrates nothing.
	if gms != 1 {
		return fmt.Errorf(
			"%w: `members` names %d accounts with the gm role; a campaign needs exactly one, "+
				"because the registrar seeds one owner as its GM",
			ErrIncompleteManifest, gms,
		)
	}

	seenModules := make(map[string]struct{}, len(c.RuleModules))

	for index := range c.RuleModules {
		if err := c.RuleModules[index].check(); err != nil {
			return fmt.Errorf("rule_modules[%d]: %w", index, err)
		}

		id := c.RuleModules[index].ID
		if _, duplicate := seenModules[id]; duplicate {
			return fmt.Errorf(
				"%w: `rule_modules` names %q twice, and (campaign, module) is the primary key",
				ErrIncompleteManifest, id,
			)
		}

		seenModules[id] = struct{}{}
	}

	return nil
}

// ParsedRole returns one membership's role as the domain type.
//
// A function rather than a field because the manifest holds the *string* the
// author wrote and the seed writes the *parsed* value; keeping both means the
// refusal happens at parse, where the file is open, rather than at the write.
func (m *Member) ParsedRole() domain.Role { return m.role }

// unexported parsed values, populated by `check`.
//
// Named apart from the fields they are parsed from on purpose: `Visibility` and
// `Role` are the manifest's vocabulary — the strings a reader edits — and these
// are the domain's, which are the values a row is written with. Collapsing them
// would mean the parser's answer was the field the author wrote, and the two are
// allowed to differ only in that the author's is refused when it does not parse.
