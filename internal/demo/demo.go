// Package demo reads the demo vault's manifest and turns it into an instance:
// `semiplane demo seed` writes the users, campaigns, memberships, house-rule
// module sets and live `campaign_state` rows a populated demo needs, and
// `semiplane demo reset` takes them away again.
//
// # What is here and what is in the composition root
//
// Everything this package does is a function of three inputs — the manifest, the
// database, and the registries the process registered — and it builds none of
// them. The command line, the configuration and the plugin registrations live in
// `cmd/server/demo`, because `internal/demo` cannot reach `package main` and a
// package that imported it would be a cycle. The one thing that genuinely must
// come from outside is **which ruleset fingerprint each gameplay system resolves
// under**, because only the composition root holds an engine
// (`plugins.fingerprint` is the only code in the tree that builds one). That is
// handed in as a `Fingerprints` value, and `BuildFingerprints` is the one
// function that can produce it from a registry.
//
// # Four refusals, and each names two versions or two names
//
//  1. **Version skew.** The manifest declares the semiplane release it was built
//     against; the binary knows its own. A disagreement is refused with both
//     versions in the message, because a manifest and a binary from different
//     releases produce a vault whose pages render against a different product
//     and there is no other place either number appears. `Check` is that
//     comparison, and it is a refusal rather than a warning because the failure
//     it prevents is silent: a populated vault that looks fine.
//  2. **A second seed.** Seeding twice would duplicate campaigns, memberships
//     and accounts, so it is refused before a single row is written. The refusal
//     names what already exists and what `demo reset` would remove, which is the
//     whole of what the operator can do about it.
//  3. **A campaign or account this instance already has.** Distinct from the
//     second seed: this is one slug or one username colliding with an instance
//     the demo did not create, and removing it is not something a demo command
//     may do.
//  4. **A house-rule module this build does not register**, and **a vault
//     directory that is not there**. Both are `§10.8`'s shape: the row is not
//     at fault, the build is, and the answer is to say which id is wanted rather
//     than to substitute something else.
//
// # `ruleset_version` is resolved, never written
//
// The delivery plan's phrase is "resolved through the real house-rule path, never
// hardcoded", and the two halves of that are separate mechanisms and it is worth
// being exact about which is which:
//
//   - **`campaigns.ruleset_version`** is the four-component fingerprint built by
//     `realtime.FingerprintOf` from the engine's own `Versions()`. It is computed
//     here and never appears in the manifest, because a manifest cannot know what
//     a build compiled in. A campaign whose `system` this build does not register
//     gets the **empty string**, which migration 0005 defines as "state written
//     under no particular ruleset" and which `Gate.Inspect` reports as
//     `Unfingerprinted` — a real value, not a fault. That is exactly
//     `forgotten-realm` in the shipped demo: it names `pathfinder-2e` on purpose,
//     its wiki serves 200, and its game refuses by name.
//   - **The house-rule layer** is `campaign_rule_modules`, and ADR 0018 keeps it
//     *out* of the fingerprint so that toggling a house rule can never strand a
//     campaign. So the seed writes the module rows through the store's own
//     `ReplaceRuleModules` and then **resolves** them with
//     `houserules.Registry.Apply`, refusing when the application fails. Running it
//     is not needed to compute the fingerprint; it is done so that a module set
//     which cannot be applied is refused at the seed rather than at a GM's first
//     campaign load, mid-session.
//
// # The demo credential is campaign-GM only, and it is printed once
//
// Plan D14. Three consequences, each structural rather than promised:
//
//   - **No manifest key can grant instance administration.** The schema has no
//     such key and the seed writes `IsAdmin: false` on every account it creates,
//     so `users.is_admin` is `0` for a demo account by construction. A demo
//     credential that could register campaigns and manage users would reach every
//     other campaign on the instance, and the demo vault is the thing people paste
//     into a public issue.
//   - **The password is a `Secret`.** Its `String` method renders `[redacted]`, so
//     a `%v`, a `%s`, a `%q`, a `slog.Any` or a `%+v` of it cannot carry the value
//     anywhere; `Reveal` is the only way out and the command prints through it
//     exactly once. The value is drawn from `crypto/rand` — deliberately, and
//     unlike everything else in this repository: AGENTS.md's ban is on ambient
//     randomness in *rule* code, where a result must be a function of
//     `(state, intent, seed)`, and a password is the one value whose whole
//     property is that nothing predicts it. The reader is still injectable so a
//     test can assert the format without drawing real entropy.
//   - **`--password` overrides and is not echoed back.** A password the operator
//     supplied is one they already have; a generated one is lost forever if it is
//     not printed, so only the generated case prints.
//
// # `reset` is about the database, and the vault is not in it
//
// `Reset` deletes the campaigns the manifest names, their `campaign_state` rows
// and the accounts it created, and it touches no file. "Leaves the vault
// untouched" is the load-bearing word: the artefact is the thing an operator
// re-extracts, and a reset that rewrote it would destroy the state it is resetting
// *to*. The three deletions also happen in that order — state row first, because
// `campaign_state` has no foreign key to `campaigns` (migration 0005 records the
// rebuild that will add one, and `DeleteCampaign` documents that its own deletion
// is knowingly incomplete) — and accounts last, because a membership references
// the account and `store.DeleteUser` refuses while one exists.
package demo
