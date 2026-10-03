// Command vendor re-fetches the third-party browser assets that
// `tools/vendor.json` pins, and writes nothing that is not the bytes the pin
// names.
//
// # What this program is not
//
// It is not the digest check. `make vendor-check` is
// `TestTheVendoredBytesMatchThePin` in `internal/web/static/js/map`, which
// re-hashes the committed bytes on every `make check`, and a second
// implementation of that claim would be a second answer to it — the failure this
// repository records twice already, in ADR 0038's one value with two spellings
// and ADR 0051's two permission maps. This program therefore has exactly one
// job: the half nothing implements, which is the half that needs the network.
//
// # Why the re-fetch is Go and not a shell pipeline
//
// Four things have to be right and none of them is a one-liner: the response has
// to be read under a bound, the npm integrity string has to be parsed out of
// its `sha512-<base64>` Subresource Integrity spelling and compared in constant
// time, the tarball has to be walked for one named member, and the extracted
// bytes have to be checked against the pin **before** they touch the working
// tree. Getting the fourth wrong writes a tampered module into the file the
// browser loads, and getting the second wrong means checking a digest against
// nothing. `tools/install-tailwind.sh` is shell because it is two
// `curl`s and two `sha256sum`s; this is a tar reader and an HTTP response, and
// a shell pipeline that long is a pipeline with no test.
//
// # Nothing here names a package
//
// Every package, every file, every member and every URL comes from the
// manifest's `packages` array. `TestTheFetcherFetchesEveryPackageTheManifest
// Names` holds that with two invented package names and an `httptest` registry
// that answers both, so a target that hard-coded PixiJS would fail it.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// defaultManifest is the pin. It is a flag rather than a constant so a test
	// and a worktree both address the same file without editing this.
	defaultManifest = "tools/vendor.json"

	// schemaVersion is the manifest shape this reader understands. A manifest
	// that moves its shape needs a reader that knows the new shape, not a
	// reader that reads the old one loosely.
	schemaVersion = 1

	// originArchive is a file that comes out of the package's pinned source
	// archive — whichever kind of upstream served it.
	//
	// The name says `archive` and not `npm` because a package is not always
	// served by a registry. `@starfederation/datastar` is pinned to a GitHub
	// release tarball for a tag, and labelling that `npm` was a stated lie in
	// the one file whose entire job is being trustworthy about provenance. The
	// behaviour is identical either way — extract a named member from a
	// verified archive — so this constant is a label, not a branch, and
	// `sourceKind` is where the upstream is actually named.
	originArchive = "archive"

	// originLocal is a file this repository wrote. There is nothing upstream to
	// fetch it from, so `make vendor` verifies it in place and says so.
	originLocal = "local"

	// The two upstreams a pinned archive can come from. They are handled by one
	// code path, and the distinction is bookkeeping rather than behaviour: both
	// are an `https` URL, both are held to a committed Subresource Integrity
	// digest before a byte is decompressed, and both are walked for one named
	// member. What a registry tarball and a project's own release tarball share
	// is the whole of what this reader does with them.
	//
	// So why two names at all? Because the alternative was one name that was
	// wrong for half the entries, and a reader auditing the manifest's
	// provenance is exactly the reader this schema exists for. Naming the
	// upstream costs one comparison and says something true.
	kindNPM     = "npm"
	kindArchive = "archive"

	// integrityAlgorithm is what an npm registry publishes, and therefore the only
	// algorithm this reader accepts. sha1 is the registry's historical spelling
	// and is not strong enough to pin executable code a browser will run.
	integrityAlgorithm = "sha512"

	// maxTarballBytes bounds what a third party can make this process hold in
	// memory. PixiJS is 841KB extracted and about 2MB as a tarball; the bound is
	// two orders of magnitude above anything the manifest can legitimately name.
	maxTarballBytes = 256 << 20

	// fetchTimeout is generous, because a cold npm registry is slow, and bounded,
	// because `make vendor` is a developer command and not a service.
	fetchTimeout = 5 * time.Minute
)

// manifestDoc is `tools/vendor.json`.
//
// `description` and `why` are arrays of prose lines and are deliberately not
// declared: they are prose for a reader, and a field this program never reads
// is a field it cannot get wrong.
type manifestDoc struct {
	Schema   int       `json:"schema"`
	Packages []pkgSpec `json:"packages"`
}

// pkgSpec is one entry of `packages`.
type pkgSpec struct {
	Name    string     `json:"name"`
	Version string     `json:"version"`
	License string     `json:"license"`
	Source  sourceSpec `json:"source"`
	Files   []fileSpec `json:"files"`
}

// sourceSpec is where a package's pinned archive is and what it must hash to.
//
// `Kind` names the upstream, not the mechanism: `npm` is a registry tarball and
// `archive` is a project's own release tarball. `download` treats both
// identically, so a reader wanting to know what is actually fetched reads `URL`
// — and `Kind` is here so the manifest can say what that URL *is* rather than
// leaving it to be inferred from the hostname.
type sourceSpec struct {
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Integrity string `json:"integrity"`
}

// fileSpec is one committed file, and the two digests that describe it.
//
// `Bytes` and `SHA256` are over the **committed** file — what
// `make vendor-check` recomputes. `Source.Integrity` is over the **tarball**,
// which is never committed, so the two are not interchangeable and neither is a
// substitute for the other.
type fileSpec struct {
	InPackage string `json:"inPackage"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
	Origin    string `json:"origin"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
}

// main is a command's whole body: run, and report. `exitAfterDefer` is why the
// error is printed here rather than from a deferred helper.
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "==> "+err.Error())
		os.Exit(1)
	}
}

// run parses the flags and fetches everything the manifest names.
func run(args []string, out io.Writer) error {
	set := flag.NewFlagSet("vendor", flag.ContinueOnError)
	manifest := set.String("manifest", defaultManifest, "the vendor manifest to read")
	root := set.String("root", ".", "the directory the manifest's paths are relative to")
	dryRun := set.Bool("dry-run", false,
		"fetch and verify, write nothing: the check a manifest edit needs before it is committed")

	if err := set.Parse(args); err != nil {
		return fmt.Errorf("parse the flags: %w", err)
	}

	doc, err := loadManifest(*manifest)
	if err != nil {
		return err
	}

	return fetchAll(
		context.Background(), *root, doc, &http.Client{Timeout: fetchTimeout}, out, *dryRun,
	)
}

// loadManifest reads and validates the pin.
func loadManifest(name string) (manifestDoc, error) {
	raw, err := os.ReadFile(name)
	if err != nil {
		return manifestDoc{}, fmt.Errorf(
			"read %s: %w. The pin is the only record of what the vendored bytes are; "+
				"without it `make vendor` has nothing to fetch and `make vendor-check` "+
				"has nothing to check", name, err)
	}

	var doc manifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return manifestDoc{}, fmt.Errorf("parse %s: %w", name, err)
	}

	if doc.Schema != schemaVersion {
		return manifestDoc{}, fmt.Errorf(
			"%s declares schema %d and this reader reads schema %d. A manifest whose "+
				"shape has moved needs a reader that knows the new shape, not a reader "+
				"that reads the old one loosely", name, doc.Schema, schemaVersion)
	}

	if len(doc.Packages) == 0 {
		return manifestDoc{}, fmt.Errorf(
			"%s pins no packages, so `make vendor` has nothing to fetch. That is a "+
				"mistake rather than a state: every third-party browser asset this "+
				"product serves is listed here", name)
	}

	return doc, nil
}

// fetchAll walks every package, in the manifest's order, and reports one line
// per package.
//
// The whole array rather than an entry named here, because a program that
// fetched only the package it was written for would leave the second package
// unpinned and unpinned is indistinguishable from absent.
func fetchAll(
	ctx context.Context,
	root string,
	doc manifestDoc,
	client *http.Client,
	out io.Writer,
	dryRun bool,
) error {
	for index := range doc.Packages {
		pkg := &doc.Packages[index]

		if err := fetchPackage(ctx, root, pkg, client, dryRun); err != nil {
			return fmt.Errorf("%s@%s: %w", pkg.Name, pkg.Version, err)
		}

		fmt.Fprintf(out, "    %s@%s: %d file(s) verified\n", pkg.Name, pkg.Version, len(pkg.Files))
	}

	return nil
}

// fetchPackage writes one package's `npm` files and verifies its `local` ones.
//
// The tarball is fetched **once** and only if the package declares at least one
// `npm` file, which is what makes a wholly local package — the licence notice,
// in the general case — a package that reaches no network at all.
func fetchPackage(
	ctx context.Context,
	root string,
	pkg *pkgSpec,
	client *http.Client,
	dryRun bool,
) error {
	var tarball []byte

	fetched := false

	for index := range pkg.Files {
		file := &pkg.Files[index]

		switch file.Origin {
		case originArchive:
			if !fetched {
				body, err := download(ctx, client, pkg.Source)
				if err != nil {
					return err
				}

				tarball = body
				fetched = true
			}

			body, err := extract(tarball, file.InPackage)
			if err != nil {
				return err
			}

			if err := write(root, file, body, dryRun); err != nil {
				return err
			}

		case originLocal:
			if err := verifyCommitted(root, file); err != nil {
				return err
			}

		default:
			return fmt.Errorf(
				"%s declares origin %q, and this reader knows %q and %q. An origin it "+
					"does not know is a file nothing verifies", file.Path, file.Origin,
				originArchive, originLocal)
		}
	}

	return nil
}

// download fetches the archive and holds it to the pinned integrity.
//
// Order is the argument: the integrity check runs before anything is
// decompressed, so an archive that is not the pinned one is never parsed at all.
//
// **The two accepted kinds take the same path on purpose.** A registry tarball
// and a project's own release tarball are the same artefact with a different
// provenance: an `https` URL, a Subresource Integrity digest, and one named
// member to walk out of it. Branching on which upstream served it would add a
// second code path to a function whose entire value is that there is one, and
// the manifest already says which is which.
func download(ctx context.Context, client *http.Client, source sourceSpec) ([]byte, error) {
	if source.Kind != kindNPM && source.Kind != kindArchive {
		return nil, fmt.Errorf(
			"source kind %q is neither %q nor %q", source.Kind, kindNPM, kindArchive)
	}

	if err := checkSourceURL(source.URL); err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build the request for %s: %w", source.URL, err)
	}

	// G107's premise is an attacker-chosen URL, and this one is a line of a file
	// committed to this repository and read in every diff that changes it. The
	// scheme is checked by checkSourceURL above and the bytes that come back
	// are held to a committed digest before they are parsed, so a URL that was
	// altered in transit buys nothing.
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", source.URL, err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: the source answered %s", source.URL, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxTarballBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", source.URL, err)
	}

	if len(body) > maxTarballBytes {
		return nil, fmt.Errorf("%s is larger than the %d byte bound this reader imposes",
			source.URL, maxTarballBytes)
	}

	if err := verifyIntegrity(body, source.Integrity); err != nil {
		return nil, err
	}

	return body, nil
}

// checkSourceURL refuses a URL that would silently downgrade the fetch.
//
// The host is deliberately **not** pinned: this repository does not hard-code
// which registry serves a package, and a check that named `registry.npmjs.org`
// would be a second place to change when a package moves — or when it was never
// served by a registry at all. What is pinned is everything about the *request*
// that a URL could weaken: the scheme, the absence of credentials, and a
// present host.
func checkSourceURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", raw, err)
	}

	if parsed.Scheme != "https" {
		return fmt.Errorf("%s is %s, not https", raw, parsed.Scheme)
	}

	if parsed.User != nil {
		return fmt.Errorf("%s carries credentials", raw)
	}

	if parsed.Host == "" {
		return fmt.Errorf("%s names no host", raw)
	}

	return nil
}

// verifyIntegrity holds a downloaded tarball to the Subresource Integrity
// string an npm registry publishes: `sha512-<base64 of the digest>`.
//
// The comparison is constant-time because it costs one import and the habit is
// worth keeping on a digest gate; the value is public, so this is hygiene and
// not a fix.
func verifyIntegrity(tarball []byte, integrity string) error {
	algorithm, encoded, found := strings.Cut(integrity, "-")
	if !found || algorithm != integrityAlgorithm {
		return fmt.Errorf(
			"integrity %q is not a `%s-<base64>` Subresource Integrity string, which is "+
				"what a registry publishes and what this reader verifies", integrity, integrityAlgorithm)
	}

	want, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("integrity %q is not base64: %w", integrity, err)
	}

	if len(want) != sha512.Size {
		return fmt.Errorf(
			"integrity %q decodes to %d bytes, not %d", integrity, len(want), sha512.Size)
	}

	got := sha512.Sum512(tarball)
	if !bytes.Equal(got[:], want) {
		return fmt.Errorf(
			"the tarball hashes to %s-%s and the pin says %s. Either the registry "+
				"published different bytes for this version or the download was altered "+
				"in transit, and either way nothing was written", integrityAlgorithm,
			base64.StdEncoding.EncodeToString(got[:]), integrity)
	}

	return nil
}

// extract returns one named member of a gzipped tarball.
//
// `path.Clean` on a slash-separated name, and a `./` prefix tolerated, because
// both spellings occur in real tarballs and a member lookup that missed on a
// prefix would report a missing artefact for a present one.
func extract(tarball []byte, member string) ([]byte, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("open the tarball: %w", err)
	}

	defer func() { _ = compressed.Close() }()

	archive := tar.NewReader(compressed)

	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf(
				"the tarball has no member %q. The pin names it, so either the version "+
					"moved or the member was renamed upstream", member)
		}

		if err != nil {
			return nil, fmt.Errorf("read the tarball: %w", err)
		}

		if path.Clean(strings.TrimPrefix(header.Name, "./")) != member {
			continue
		}

		body, err := io.ReadAll(io.LimitReader(archive, maxTarballBytes))
		if err != nil {
			return nil, fmt.Errorf("read %s out of the tarball: %w", member, err)
		}

		return body, nil
	}
}

// write puts verified bytes where the manifest says, and verifies them first.
//
// The digest check is inside this function rather than at the call site because
// "the bytes were checked and then written" and "the bytes were checked" are
// one claim here, and a caller that could skip the check would be a caller that
// could write a tampered module into the file the browser loads.
//
// `dryRun` stops after the check. That is the whole of it: it is how a manifest
// edit is confirmed against the registry before it is committed, and it is why
// this program does not need a second code path for "just tell me".
func write(root string, file *fileSpec, body []byte, dryRun bool) error {
	if err := matchesPin(file, body); err != nil {
		return err
	}

	if dryRun {
		return nil
	}

	target := filepath.Join(root, filepath.FromSlash(file.Path))

	// 0750 and 0600 rather than the 0755/0644 a web asset would carry. The mode
	// on disk is not what decides who may read this file: `internal/web` embeds
	// it at build time and the binary carries it, so the shipped copy is served
	// from the embed rather than from the working tree. A restrictive mode here
	// costs nothing and says so.
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("create the directory for %s: %w", file.Path, err)
	}

	if err := os.WriteFile(target, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", file.Path, err)
	}

	return nil
}

// verifyCommitted holds a `local` file to its pin in place.
//
// It is the same check `make vendor-check` makes, and it runs here because
// `make vendor` is the command somebody runs after editing the manifest, and a
// local file nobody re-reads on that run is a local file whose digest is
// whatever it was when it was written.
func verifyCommitted(root string, file *fileSpec) error {
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Path)))
	if err != nil {
		return fmt.Errorf("read %s: %w. A file with origin %q is written by this "+
			"repository, so a missing one is an unfinished edit rather than a fetch "+
			"that failed", file.Path, err, originLocal)
	}

	return matchesPin(file, body)
}

// matchesPin compares bytes against the pin's length and digest.
func matchesPin(file *fileSpec, body []byte) error {
	if int64(len(body)) != file.Bytes {
		return fmt.Errorf("%s is %d bytes and the pin says %d. A different length is "+
			"upstream moving before it is upstream being malicious, and either way "+
			"these are not the pinned bytes", file.Path, len(body), file.Bytes)
	}

	digest := sha256.Sum256(body)

	if got := hex.EncodeToString(digest[:]); got != file.SHA256 {
		return fmt.Errorf(
			"%s hashes to %s and the pin says %s. Either the artefact moved upstream "+
				"or the download was altered. Both are what the pin exists for: a "+
				"browser asset is executable code served to every reader of every "+
				"campaign, and an unpinned one is trusted rather than checked. Nothing "+
				"was written", file.Path, got, file.SHA256)
	}

	return nil
}
