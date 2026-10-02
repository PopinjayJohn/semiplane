package determinism

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// repoRootMarker is the file whose presence identifies the repository root.
//
// Chosen over `.git`, which a worktree does not have as a directory and which a
// vendored copy may not have at all; `.golangci.yml` is present in every checkout
// of this project and is what the audit's own scope is configured in, so finding
// it is the same search twice.
const repoRootMarker = ".golangci.yml"

// maxRepoRootHops bounds the walk up from this source file.
//
// Seven is not derived from the repository's depth — it is a bound, so that a
// mislaid source file produces "cannot find the repository root" instead of
// walking to the filesystem root and finding somebody else's checkout.
const maxRepoRootHops = 7

// ErrNoRepoRoot is a source file that does not sit inside a checkout carrying
// this project's configuration.
var ErrNoRepoRoot = errors.New("determinism: cannot find the repository root from this source file")

// RepoRoot returns the repository root, located by walking up from this source
// file until `.golangci.yml` appears.
//
// Located rather than taken as an argument because every caller wants the same one
// and a caller that computed it differently would compare paths that look alike
// and are not. `runtime.Caller` rather than the working directory: a test's
// working directory is the package under test, which is three levels below the
// root and is not guaranteed to be where it was run from.
func RepoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("%w: runtime.Caller reported no frame", ErrNoRepoRoot)
	}

	dir := filepath.Dir(thisFile)

	for range maxRepoRootHops {
		if _, err := os.Stat(filepath.Join(dir, repoRootMarker)); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}

		dir = parent
	}

	return "", fmt.Errorf(
		"%w: no %s above %s",
		ErrNoRepoRoot,
		repoRootMarker,
		filepath.Dir(thisFile),
	)
}

// resolver is the process's single type resolver: one file set and one source
// importer, behind one mutex.
//
// **One of each, not one per audit, and the reason is the cost.** The source
// importer type-checks a package's dependencies from source and caches what it
// built — but only within one importer. A fresh importer per `AuditAt` means
// re-resolving `time`'s whole transitive closure for every fixture that mentions a
// clock; measured, that is 41 seconds for this package under `-race` against 6
// shared, and the difference is almost entirely `time` being resolved four times
// over by four different fixtures.
//
// The mutex is not optional. `token.FileSet` is not safe for concurrent use, the
// tests are parallel, and the alternative is the 41 seconds. Serialising the audits
// costs almost nothing once resolution is cached, because the second audit of a
// package that imports `time` is a lookup rather than a rebuild.
var resolver = struct {
	mu       sync.Mutex
	fileSet  *token.FileSet
	importer types.Importer
}{fileSet: token.NewFileSet()}

// acquireResolver takes the resolver and returns the file set to parse with. The
// caller must give it back; see `AuditAt`.
func acquireResolver() *token.FileSet {
	resolver.mu.Lock()

	if resolver.importer == nil {
		resolver.importer = importer.ForCompiler(resolver.fileSet, "source", nil)
	}

	return resolver.fileSet
}

// releaseResolver gives the resolver back.
func releaseResolver() {
	resolver.mu.Unlock()
}

// Audit walks the Go files under dirs — each relative to the repository root — and
// returns every determinism violation it finds, in a deterministic order.
//
// **Every failure is loud.** A directory that does not parse, and a package that
// does not type-check, are errors rather than findings: an audit that could not
// read its input and reported nothing is precisely the "audit that cannot fail"
// this repository has been bitten by twice. Type information is not optional
// either — telling a `range` over a map from a `range` over a slice is a question
// only a type checker answers, and answering it syntactically is how "the audit
// passed" and "the audit saw nothing" become the same observation.
//
// **Non-test files only.** Rule code is what a resolution runs; a `_test.go` file
// resolves nothing, and the fixtures that prove this audit works are themselves
// files containing `time.Now`. The linter's scope is the whole directory, so a
// clock call in a test is still reported by `forbidigo`; the difference is
// deliberate and stated here rather than left to be discovered.
//
// Fails when no file was audited at all, so that renaming every rule directory
// turns this into an error rather than into a pass.
func Audit(dirs ...string) ([]Violation, error) {
	root, err := RepoRoot()
	if err != nil {
		return nil, err
	}

	return AuditAt(root, dirs...)
}

// AuditAt is `Audit` against an explicit root, with dirs relative to it.
//
// Split out because the fixtures that prove this audit *works* are directories of
// deliberately-broken source, and they have no repository above them. Exported for
// that reason alone — a test in this package's own directory could have reached the
// unexported half, and the exported one costs a doc comment.
func AuditAt(root string, dirs ...string) ([]Violation, error) {
	audit := &auditor{root: root, fset: acquireResolver()}

	defer releaseResolver()

	audit.conf = types.Config{
		// "source" rather than the default: the default importer reads compiled
		// export data, which a module build does not produce, so every import of a
		// project package would fail to resolve. The source importer type-checks
		// dependencies from source, which works, and `resolver` keeps what it built
		// so that the next audit does not pay for it again.
		Importer: resolver.importer,
		Error:    func(err error) { audit.typeErrors = append(audit.typeErrors, err) },
	}

	audited := 0

	for _, dir := range dirs {
		count, err := audit.auditDir(dir)
		if err != nil {
			return nil, err
		}

		audited += count
	}

	if len(audit.typeErrors) > 0 {
		return nil, fmt.Errorf(
			"determinism: %d type errors under %s, so the audit cannot vouch for what it "+
				"did and did not see: %w",
			len(audit.typeErrors), strings.Join(dirs, ", "), audit.typeErrors[0],
		)
	}

	if audited == 0 {
		return nil, fmt.Errorf(
			"determinism: no Go files found under %s; the audit would be reporting on nothing",
			strings.Join(dirs, ", "),
		)
	}

	// `slices.SortStableFunc` rather than `sort.Slice`: two violations on one line
	// are two rules breaking one line, and either order is correct, so the tie must
	// not depend on the order they were found in.
	slices.SortStableFunc(audit.violations, func(first, second Violation) int {
		return cmp.Or(
			strings.Compare(first.Path, second.Path),
			cmp.Compare(first.Line, second.Line),
			strings.Compare(first.Rule, second.Rule),
		)
	})

	return audit.violations, nil
}

// auditor holds the state one Audit run accumulates.
type auditor struct {
	root       string
	fset       *token.FileSet
	conf       types.Config
	info       *types.Info
	violations []Violation
	typeErrors []error
}

// auditDir audits one directory and returns how many files it read.
//
// A directory that is not there is not an error: `internal/domain/systems` has no
// files until the phase that writes them lands, and refusing to run until then
// would put a red build on the phase branch for a package that does not exist. The
// refusal that matters — "no file was audited anywhere" — is `Audit`'s.
func (a *auditor) auditDir(dir string) (int, error) {
	absolute := filepath.Join(a.root, dir)

	// A directory that is not there is skipped, and *only* that: any other stat
	// failure is a real problem with the checkout and is reported. `errors.Is`
	// rather than discarding the error, so that a permission problem does not read
	// as "the package has not landed yet" — which is the shape of a gate that
	// quietly stops auditing.
	if _, err := os.Stat(absolute); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}

		return 0, fmt.Errorf("determinism: stat %s: %w", dir, err)
	}

	// `parser.ParseFile` per file rather than `parser.ParseDir`, because ParseDir
	// is deprecated: it associates files with packages without consulting build
	// tags, so a package split by a tag would be audited as two half-packages and
	// neither would type-check. Grouping by the package clause is what it was
	// approximating, and it is exact for the thing this audit cares about — which
	// files declare which package.
	groups := map[string][]*ast.File{}

	err := filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || !isRuleFile(path) {
			return nil
		}

		// Object resolution is skipped: every fact this audit needs comes from
		// `go/types`, and resolving identifiers twice is the slower of the two.
		file, parseErr := parser.ParseFile(a.fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}

		groups[file.Name.Name] = append(groups[file.Name.Name], file)

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("determinism: walk %s: %w", dir, err)
	}

	// Sorted, because `groups` is a map and ranging one is what this package
	// exists to forbid — including here. `slices.Sorted(maps.Keys(...))` is the
	// sanctioned spelling, and it is what the `range/map` rule's own message tells
	// the next contributor to write. The order also decides which type error a
	// broken package reports first.
	names := slices.Sorted(maps.Keys(groups))

	audited := 0

	for _, name := range names {
		a.info = &types.Info{
			Types: map[ast.Expr]types.TypeAndValue{},
			Defs:  map[*ast.Ident]types.Object{},
			Uses:  map[*ast.Ident]types.Object{},
		}

		files := groups[name]

		if _, err := a.conf.Check(dir+"/"+name, a.fset, files, a.info); err != nil {
			a.typeErrors = append(a.typeErrors, err)

			continue
		}

		audited += len(files)

		for _, file := range files {
			a.auditFile(dir, file)
		}
	}

	return audited, nil
}

// isRuleFile reports whether a path names rule code: a Go file, and not a test.
//
// `_test.go` because a test resolves nothing — see `Audit`. Build-tagged files are
// included, deliberately: a file behind a tag is still rule code that will be
// compiled into somebody's build, and skipping it would make the audit's coverage
// depend on the tag it happens to be behind. That also means the audit reports what
// a build with *no* tags would compile, which is the strictest of the answers and
// the one that does not depend on which tag was set.
func isRuleFile(path string) bool {
	name := filepath.Base(path)

	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// auditFile walks one parsed file and records what it breaks.
//
// The path is recovered from the file's own first token rather than from the
// directory argument, so a violation carries the path an operator can open even if
// the directory was reached through a symlink.
func (a *auditor) auditFile(dir string, file *ast.File) {
	path := filepath.Join(dir, filepath.Base(a.fset.Position(file.Pos()).Filename))

	// Imports first, because a file that imports `time` is wrong whether or not it
	// calls anything on it, and reporting the import as well as the call is two
	// lines naming one mistake.
	for _, imported := range file.Imports {
		entry, banned := forbiddenImport(imported.Path.Value)
		if banned {
			a.report(path, a.fset.Position(imported.Pos()), entry, imported.Path.Value)
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch found := node.(type) {
		case *ast.SelectorExpr:
			a.auditSelector(path, found)
		case *ast.RangeStmt:
			a.auditRange(path, found)
		}

		return true
	})
}

// auditSelector refuses a reference to a forbidden selector, called or not.
//
// Not restricted to `*ast.CallExpr`, because `now := time.Now` is the same hole as
// `now := time.Now()` with one more step before it is used, and a rule that fired
// only on the call would be satisfied by the shorter spelling.
func (a *auditor) auditSelector(path string, selector *ast.SelectorExpr) {
	qualifier, isIdentifier := selector.X.(*ast.Ident)
	if !isIdentifier {
		return
	}

	object := a.info.Uses[qualifier]

	pkgName, isPackage := object.(*types.PkgName)
	if !isPackage {
		return
	}

	entry, banned := forbiddenSelector(pkgName.Imported().Path(), selector.Sel.Name, KindCall)
	if banned {
		a.report(path, a.fset.Position(selector.Pos()), entry, selector.Sel.Name)
	}
}

// auditRange refuses a range over a map, and a range over an iterator one of
// `maps.Keys`, `maps.Values` or `maps.All` returned.
//
// The second is the first in three more spellings. It is resolved through the
// type information rather than by matching the name `maps`, so a locally declared
// `Keys` cannot trip it and a vendored `maps` with the same name cannot escape it.
func (a *auditor) auditRange(path string, stmt *ast.RangeStmt) {
	if stmt.X == nil {
		// `for range ch`: a channel receive, and a channel's ordering is as fixed as
		// anything in Go.
		return
	}

	value, known := a.info.Types[stmt.X]
	if !known || value.Type == nil {
		return
	}

	if _, isMap := value.Type.Underlying().(*types.Map); isMap {
		a.report(path, a.fset.Position(stmt.X.Pos()), ruleNamed("range/map"), value.Type.String())

		return
	}

	call, isCall := stmt.X.(*ast.CallExpr)
	if !isCall {
		return
	}

	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	if !isSelector {
		return
	}

	qualifier, isIdentifier := selector.X.(*ast.Ident)
	if !isIdentifier {
		return
	}

	pkgName, isPackage := a.info.Uses[qualifier].(*types.PkgName)
	if !isPackage {
		return
	}

	entry, banned := forbiddenSelector(pkgName.Imported().Path(), selector.Sel.Name, KindRange)
	if banned {
		a.report(path, a.fset.Position(stmt.X.Pos()), entry, selector.Sel.Name)
	}
}

// report records one violation.
func (a *auditor) report(path string, position token.Position, entry Forbidden, detail string) {
	a.violations = append(a.violations, Violation{
		Path:   filepath.ToSlash(path),
		Line:   position.Line,
		Rule:   entry.Rule,
		Detail: entry.Reason + " (" + detail + ")",
	})
}

// forbiddenImport returns the entry refusing an import path, if there is one.
func forbiddenImport(pathLiteral string) (Forbidden, bool) {
	// Unquoting rather than trimming quotes: an import is a Go string literal, and
	// a backquoted path is legal.
	path := strings.Trim(pathLiteral, "`\"")

	for _, entry := range forbiddenRules {
		if entry.Kind == KindImport && entry.Import == path {
			return entry, true
		}
	}

	return Forbidden{}, false
}

// forbiddenSelector returns the entry refusing a selector within a package, if
// there is one.
//
// Two args rather than one qualified string because the table stores an import
// path and a selector separately, and re-assembling them to take them apart again
// is the kind of string surgery that is correct until somebody names a package
// with a dot in its last element.
func forbiddenSelector(importPath, selector string, kind Kind) (Forbidden, bool) {
	for _, entry := range forbiddenRules {
		if entry.Kind != kind || entry.Import != importPath {
			continue
		}

		if indexOf(entry.Calls, selector) >= 0 {
			return entry, true
		}
	}

	return Forbidden{}, false
}

// ruleNamed returns the entry with the given Rule name.
//
// The lookup is by name because `range/map` carries no package and no selector, so
// there is nothing else to match it on.
func ruleNamed(name string) Forbidden {
	for _, entry := range forbiddenRules {
		if entry.Rule == name {
			return entry
		}
	}

	return Forbidden{}
}
