package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/store"
)

// The `semiplane admin` subcommands, and the one constraint that shapes them.
//
// There is no bootstrap environment variable. A password in the environment is a
// password in `/proc/<pid>/environ`, in the container's config, in every crash
// dump and in the shell history of whoever ran the command — and a self-hoster
// who reads that will reasonably conclude their admin password is not a secret.
// So the first account is created by a command an operator runs deliberately,
// inside the container, exactly once.
//
// That is also why the command is a subcommand and not a flag on the server: the
// two never run at the same time, so the server's single-instance slot is free
// while this one holds it.

const adminUsage = `Usage:
  semiplane admin create --username NAME [--password SECRET] [--admin]
  semiplane admin campaign add --slug SLUG --name NAME [--owner NAME] [--system ID]
                             [--public] [--ruleset VERSION]

create
  --username   the account's name. Required.
  --password   the account's password. Read from the terminal when omitted, so
               it never reaches the shell history or the process list.
  --admin      grant instance administration. Omit for a campaign-only account.

campaign add
  --slug       the campaign's URL segment: 1-64 characters of a-z, 0-9 and
               single hyphens, no leading or trailing hyphen. Required.
  --name       the campaign's display name. Defaults to the slug.
  --owner      the username seeded as this campaign's GM. Required.
  --system     the gameplay system id, e.g. 5e-2024. Stated rather than
               defaulted: a campaign naming a system that is not registered will
               refuse to start its game and still serve its wiki, and that is
               correct behaviour, not a broken install.
  --public     register the campaign with a wiki anonymous visitors may read.
               Public never grants play; games always require membership.
  --ruleset    the ruleset version the campaign's state is written under. Left
               empty, meaning state under no particular ruleset.

Campaigns are created with mode 0700 beneath SEMIPLANE_CONTENT_ROOT_BASE, and
the directory is left in place if a later step fails — see the note in
internal/httpapi/campaigns.
`

// runAdmin dispatches an admin subcommand.
func runAdmin(args []string) error {
	if len(args) == 0 {
		return usagef("admin has no subcommands")
	}

	switch args[0] {
	case adminSubcommandCreate:
		return runAdminCreate(args[1:])
	case adminSubcommandCampaign:
		return runAdminCampaign(args[1:])
	case subcommandHelp, flagHelpShort, flagHelpLong:
		fmt.Fprint(os.Stdout, adminUsage)

		return nil
	default:
		return usagef("unknown admin subcommand %q", args[0])
	}
}

// flagSet is a minimal `--name value` and `--name` parser.
//
// Not flag.FlagSet because that package cannot express "this flag is required",
// cannot tell `--username` from `--username=` usefully without a custom Value
// per flag, and prints its own usage text — and the usage text here is the
// operator-facing copy that lives in one place, not a generated one that drifts
// from it. The whole grammar is nine flags, so this is a loop.
type flags struct {
	values   map[string]string
	booleans map[string]bool
	args     []string
}

// parseFlags splits args into `--name value` pairs, `--name` booleans, and
// positional arguments. A `--` ends flag parsing, so a value beginning with a
// hyphen can still be passed.
func parseFlags(args []string, booleanNames ...string) (*flags, error) {
	booleans := make(map[string]bool, len(booleanNames))
	for _, name := range booleanNames {
		booleans[name] = true
	}

	parsed := &flags{values: map[string]string{}, booleans: map[string]bool{}}

	for index := 0; index < len(args); index++ {
		arg := args[index]

		if arg == "--" {
			parsed.args = append(parsed.args, args[index+1:]...)

			break
		}

		if !strings.HasPrefix(arg, "-") {
			parsed.args = append(parsed.args, arg)

			continue
		}

		name, inline, hasInline := strings.Cut(strings.TrimLeft(arg, "-"), "=")

		switch {
		case hasInline:
			parsed.values[name] = inline
		case booleans[name]:
			parsed.booleans[name] = true
		case !booleans[name] && index+1 < len(args):
			index++
			parsed.values[name] = args[index]
		default:
			// A boolean flag with no value and a value-taking flag with nothing
			// after it land here together, and they are told apart by the
			// message rather than by guessing which one was meant.
			if booleans[name] {
				parsed.booleans[name] = true

				continue
			}

			return nil, usagef("flag --%s needs a value", name)
		}
	}

	return parsed, nil
}

// String returns a flag's value, or fallback when it was not given.
func (f *flags) String(name, fallback string) string {
	if value, ok := f.values[name]; ok {
		return value
	}

	return fallback
}

// Bool reports whether a boolean flag was given.
func (f *flags) Bool(name string) bool {
	return f.booleans[name]
}

// withStore opens the store for a subcommand, runs fn, and closes it.
//
// A subcommand holds the process's single-instance slot for its whole run, so it
// cannot be issued against a live server. That is a feature: the migration runner
// would otherwise be applying migrations underneath a running process.
func withStore(ctx context.Context, run func(context.Context, *store.Store) error) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "semiplane: close store: %v\n", closeErr)
		}
	}()

	return run(ctx, db)
}

// runAdminCreate creates one account.
func runAdminCreate(args []string) error {
	parsed, err := parseFlags(args, "admin")
	if err != nil {
		return err
	}

	username := strings.TrimSpace(parsed.String("username", ""))
	if username == "" {
		return usagef("admin create needs --username")
	}

	password := parsed.String("password", "")
	if password == "" {
		password, err = readPassword("Password for " + username + ": ")
		if err != nil {
			return err
		}
	}

	// Hashed before the store is opened, so a store that cannot be opened does
	// not cost half a second of PBKDF2 for nothing.
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return withStore(commandContext(), func(ctx context.Context, db *store.Store) error {
		created, err := db.CreateUser(ctx, domain.User{
			Username:     username,
			PasswordHash: hash,
			IsAdmin:      parsed.Bool("admin"),
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("the username %q is already taken", username)
			}

			return fmt.Errorf("create user: %w", err)
		}

		fmt.Fprintf(os.Stdout, "Created account %q (id %d, admin=%t).\n",
			created.Username, created.ID, created.IsAdmin)

		if !created.IsAdmin {
			fmt.Fprintln(os.Stdout,
				"Not an instance administrator. They can play and, once a campaign "+
					"names them GM, edit that campaign. Pass --admin for the ability to "+
					"register campaigns and manage users.")
		}

		return nil
	})
}

// runAdminCampaign dispatches `admin campaign` subcommands.
func runAdminCampaign(args []string) error {
	if len(args) == 0 {
		return usagef("admin campaign has no subcommands")
	}

	switch args[0] {
	case "add":
		return runAdminCampaignAdd(args[1:])
	case subcommandHelp, flagHelpShort, flagHelpLong:
		fmt.Fprint(os.Stdout, adminUsage)

		return nil
	default:
		return usagef("unknown admin campaign subcommand %q", args[0])
	}
}

// runAdminCampaignAdd registers a campaign and seeds its GM.
//
// The registration itself — validating the slug, creating the content root with
// mode 0700, opening the `os.Root`, writing the row — is the campaigns package's
// `Registrar`, and it is the same function the HTTP route will call. Reusing it
// rather than writing a second one for the CLI is the point: a CLI path that
// registered campaigns its own way would be a second implementation of the
// tenancy rules, and the two would drift in exactly the properties that are
// expensive to get wrong (an absolute root, a confined root, an owner).
func runAdminCampaignAdd(args []string) error {
	parsed, err := parseFlags(args, "public")
	if err != nil {
		return err
	}

	slug := strings.TrimSpace(parsed.String("slug", ""))
	if slug == "" {
		return usagef("admin campaign add needs --slug")
	}

	ownerName := strings.TrimSpace(parsed.String("owner", ""))
	if ownerName == "" {
		return usagef("admin campaign add needs --owner")
	}

	name := parsed.String("name", slug)

	visibility := domain.VisibilityPrivate
	if parsed.Bool("public") {
		visibility = domain.VisibilityPublic
	}

	return withStore(commandContext(), func(ctx context.Context, db *store.Store) error {
		owner, err := db.UserByUsername(ctx, ownerName)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf(
					"no account named %q; create one with `semiplane admin create` first",
					ownerName,
				)
			}

			return fmt.Errorf("read owner: %w", err)
		}

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load configuration: %w", err)
		}

		// A Registrar with no retain callback: the CLI creates the root to prove
		// it is creatable and to write an absolute path into the row, then closes
		// it. The process that will read through it is a server started later,
		// which opens its own handle.
		registrar := campaigns.NewRegistrar(db, cfg.ContentRootBase, nil)

		campaign, err := registrar.Register(ctx, campaigns.RegisterRequest{
			Slug:           slug,
			Name:           name,
			SystemID:       parsed.String("system", ""),
			RulesetVersion: parsed.String("ruleset", ""),
			Visibility:     visibility,
			OwnerID:        owner.ID,
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("the slug %q is already taken", slug)
			}

			// Not "register campaign: %w": the registrar already wraps its own
			// errors with those words, and two identical prefixes read as a
			// doubled message rather than as two layers of context.
			return fmt.Errorf("admin campaign add %q: %w", slug, err)
		}

		// After the write, not before: a warning printed above a command that
		// then failed reads as advice about a campaign that does not exist.
		if campaign.SystemID == "" {
			// Not defaulted, and the operator is told so — after the fact, so the
			// message can only be about a campaign that was registered. Naming a
			// system that is not registered produces a campaign whose game
			// refuses to start and whose wiki still answers 200, which is correct
			// behaviour (S-14.8) and a first-run experience that would otherwise
			// look broken.
			fmt.Fprintln(os.Stderr,
				"semiplane: no --system was given. This campaign's pages will be served, and "+
					"its game will refuse to start until a system id names a registered "+
					"gameplay plugin.")
		}

		fmt.Fprintf(os.Stdout, "Registered campaign %q (id %d) with %q as Game Master.\n",
			campaign.Slug, campaign.ID, owner.Username)
		fmt.Fprintf(os.Stdout, "Content root: %s\n", campaign.ContentRoot)

		if campaign.Visibility == domain.VisibilityPublic {
			fmt.Fprintln(os.Stdout,
				"Public: its pages are readable without signing in. Playing it still "+
					"requires membership.")
		}

		return nil
	})
}

// readPassword prompts for a password on the terminal.
//
// Read from a terminal rather than a flag so the secret never reaches the shell
// history, the process list, or a `docker exec` invocation preserved in a
// terminal scrollback.
//
// The read is line-based, not the character-at-a-time no-echo read a password
// prompt ideally uses. That is a deliberate trade: a no-echo read needs either
// golang.org/x/term or a raw-mode syscall, and the first is a new dependency for
// one prompt while the second is a termios call that is not portable to Windows.
// Echoed input is a real cost, and it is paid by an operator who typed the
// password into a terminal they control, on a machine they administer, for an
// account they are creating. A password that never reaches a flag is worth more
// than one that is not echoed, and `docker exec -it` is how an operator runs
// this anyway — so the operator is told which form to use.
//
// When stdin is not a terminal — a CI job, a piped heredoc — the caller is told
// to pass --password rather than being handed an empty password, which would
// create an account nobody can sign in to.
func readPassword(prompt string) (string, error) {
	if !stdinIsTerminal() {
		return "", usagef("no --password given and stdin is not a terminal; pass --password")
	}

	fmt.Fprint(os.Stderr, prompt)

	return readLine(bufio.NewReader(os.Stdin))
}

// stdinIsTerminal reports whether stdin is an interactive terminal.
//
// Not `ModeCharDevice`, which was the first attempt and is wrong: /dev/null and
// /dev/zero are character devices, so `< /dev/null` passed the check and the
// command then read to EOF and reported "read password: EOF" — an error that
// describes the mechanism rather than telling the operator to pass --password.
//
// What is actually being asked is whether a *human* is at the other end, and the
// portable answer without a dependency is the device name. A real tty reports
// "tty", "pts" (a pseudoterminal, Linux), or "ttys" (macOS). Redirects report
// the redirect target: "null", "zero", "pipe", "fd". So this accepts the terminal
// names and rejects everything else.
//
// The cost is that the list is per-platform, and a platform with a third
// spelling reports "stdin is not a terminal; pass --password" — a false negative
// that produces a correct instruction and a flag that works. The failure mode is
// an operator typing a password after being told to pass a flag, which is the
// direction to err in; the alternative failure is a command that hangs on a pipe
// nobody is reading.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}

	if info.Mode()&os.ModeCharDevice == 0 {
		// A named pipe, a regular file, or a socket: never a human.
		return false
	}

	switch name := info.Name(); {
	// Linux pseudoterminal, and the name a `docker exec -it` session reports.
	case name == "tty", strings.HasPrefix(name, "pts"):
		return true
	// BSD and macOS: /dev/tty, /dev/ttys001.
	case strings.HasPrefix(name, "tty"):
		return true
	default:
		// A character device that is not a terminal: /dev/null, /dev/zero, a
		// redirected file handle. Not interactive, so --password is the answer.
		return false
	}
}

// readLine reads one line, stopping at either newline convention.
//
// bufio.Scanner's token limit would silently refuse a long password with a
// confusing error, and ReadString is right for a line a human is typing. The
// trailing \r is stripped because a terminal in CRLF mode sends both, and a
// password ending in a stray carriage return is one nobody can type again.
func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password: %w", err)
	}

	line = strings.TrimRight(line, "\r\n")

	if line == "" {
		return "", usagef("the password is empty")
	}

	return line, nil
}

// commandContext returns the context an admin subcommand runs under.
//
// context.Background rather than the server's signal context, and that is
// deliberate: a subcommand is short and does not need a graceful drain, and
// inheriting a cancelled context would mean a Ctrl-C partway through a write left
// the outcome ambiguous. An operator who interrupts `admin create` re-runs it,
// and the slug or username uniqueness constraint tells them whether the first
// attempt committed — which is why both commands are safe to retry and neither
// is silently idempotent.
func commandContext() context.Context {
	return context.Background()
}
