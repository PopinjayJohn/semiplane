package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// repositoryRoot is the checkout, relative to this package's directory.
//
// Two levels, not one: `vendor` → `tools` → the root. The manifest and the
// vendor tree are read from the real repository rather than from a fixture,
// because the property under test in
// `TestTheRepositoryPinIsReadableAndItsFilesAreCommitted` is that the pin in the
// tree describes files that are actually there.
const repositoryRoot = "../.."

// member is one file inside a test tarball.
type member struct {
	name string
	body string
}

// tarball builds a gzipped tarball from members, in the order given.
//
// A slice rather than a map on purpose: a test that asserts on archive order
// should not depend on map iteration order.
func tarball(t *testing.T, members ...member) []byte {
	t.Helper()

	var buffer bytes.Buffer

	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)

	for _, entry := range members {
		header := &tar.Header{
			Name: entry.name,
			Mode: 0o644,
			Size: int64(len(entry.body)),
		}

		if err := archive.WriteHeader(header); err != nil {
			t.Fatalf("write the header for %s: %v", entry.name, err)
		}

		if _, err := archive.Write([]byte(entry.body)); err != nil {
			t.Fatalf("write %s: %v", entry.name, err)
		}
	}

	if err := archive.Close(); err != nil {
		t.Fatalf("close the tar: %v", err)
	}

	if err := compressed.Close(); err != nil {
		t.Fatalf("close the gzip: %v", err)
	}

	return buffer.Bytes()
}

// integrity is the Subresource Integrity string npm would publish for body.
func integrity(body []byte) string {
	digest := sha512.Sum512(body)

	return integrityAlgorithm + "-" + base64.StdEncoding.EncodeToString(digest[:])
}

// pin builds a manifest entry with both digests computed from the body, so a
// test cannot accidentally pin the wrong bytes and then pass for the wrong
// reason.
func pin(path, inPackage, origin, body string) fileSpec {
	digest := sha256.Sum256([]byte(body))

	return fileSpec{
		InPackage: inPackage,
		Path:      path,
		Origin:    origin,
		Bytes:     int64(len(body)),
		SHA256:    hex.EncodeToString(digest[:]),
	}
}

// registry is an `httptest` npm. It is TLS because `checkRegistryURL` refuses
// plain HTTP, and a test that used an `http://` URL would be asserting against
// a fetch this reader refuses before it gets that far.
//
// It counts requests, so a test can assert that a package was never fetched at
// all rather than only that a fetch that should not have happened succeeded.
type registry struct {
	hits atomic.Int64
	url  string
}

// serve answers one tarball per path and returns the client that trusts it.
func (reg *registry) serve(t *testing.T, bodies map[string][]byte) *http.Client {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reg.hits.Add(1)

		body, ok := bodies[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		if _, err := w.Write(body); err != nil {
			t.Errorf("serve %s: %v", r.URL.Path, err)
		}
	}))

	t.Cleanup(server.Close)
	reg.url = server.URL

	return server.Client()
}

// sourceFor builds an `npm` source pointing at a path on the test registry.
func (reg *registry) sourceFor(path string, tarballBody []byte) sourceSpec {
	return sourceSpec{
		Kind:      originNPM,
		URL:       reg.url + path,
		Integrity: integrity(tarballBody),
	}
}

// writeManifest writes a manifest into a fresh temporary root and returns the
// root, which is what `fetchAll` resolves every pinned path against.
func writeManifest(t *testing.T, doc manifestDoc) string {
	t.Helper()

	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "vendor.json"), doc)

	return root
}

// writeJSON marshals doc to path.
func writeJSON(t *testing.T, path string, doc manifestDoc) {
	t.Helper()

	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal the manifest: %v", err)
	}

	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// readVendored returns the bytes at a manifest path under root.
func readVendored(t *testing.T, root, path string) []byte {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return body
}

// TestTheFetcherWritesThePinnedBytesOfEveryPackage is the positive claim: the
// pinned member of every package lands at its pinned path, byte for byte.
//
// Two packages with two invented names, because the failure this guards is a
// program that fetched the package it was written for and skipped the one added
// after it — and a skipped package is a silent skip, not a failure.
func TestTheFetcherWritesThePinnedBytesOfEveryPackage(t *testing.T) {
	t.Parallel()

	const (
		firstBody   = "export const alpha = 1;"
		firstNotice = "An Invented Package is MIT licensed."
		secondBody  = "export const beta = 2;"
	)

	firstTarball := tarball(t,
		member{name: "package/dist/alpha.mjs", body: firstBody},
		member{name: "package/LICENSE", body: firstNotice},
	)
	secondTarball := tarball(t, member{name: "package/dist/beta.mjs", body: secondBody})

	reg := &registry{}
	client := reg.serve(t, map[string][]byte{
		"/alpha.tgz": firstTarball,
		"/beta.tgz":  secondTarball,
	})

	doc := manifestDoc{
		Schema: schemaVersion,
		Packages: []pkgSpec{
			{
				Name:    "an-invented-package",
				Version: "1.0.0",
				License: "MIT",
				Source:  reg.sourceFor("/alpha.tgz", firstTarball),
				Files: []fileSpec{
					pin("static/vendor/alpha.mjs", "package/dist/alpha.mjs", originNPM, firstBody),
					pin("static/vendor/alpha.LICENSE", "package/LICENSE", originNPM, firstNotice),
				},
			},
			{
				Name:    "another-invented-package",
				Version: "2.0.0",
				License: "MIT",
				Source:  reg.sourceFor("/beta.tgz", secondTarball),
				Files: []fileSpec{
					pin("static/vendor/beta.mjs", "package/dist/beta.mjs", originNPM, secondBody),
				},
			},
		},
	}

	root := writeManifest(t, doc)

	if err := fetchAll(t.Context(), root, doc, client, io.Discard, false); err != nil {
		t.Fatalf("fetchAll: %v", err)
	}

	for path, want := range map[string]string{
		"static/vendor/alpha.mjs":     firstBody,
		"static/vendor/alpha.LICENSE": firstNotice,
		"static/vendor/beta.mjs":      secondBody,
	} {
		if got := string(readVendored(t, root, path)); got != want {
			t.Errorf("%s is %q, want %q", path, got, want)
		}
	}

	if got := reg.hits.Load(); got != 2 {
		t.Errorf("the registry was asked %d times, want 2: one tarball per package, "+
			"and one package per entry in the manifest", got)
	}
}

// TestAFetchedFileThatIsNotThePinnedOneIsRefusedAndNothingIsWritten is the
// negative control, and it is the assertion the whole command rests on.
//
// A registry that answers with a tarball whose member is one character longer
// than the pin says: the artefact the browser would load, if this wrote it, is
// not the artefact the repository claims to ship.
func TestAFetchedFileThatIsNotThePinnedOneIsRefusedAndNothingIsWritten(t *testing.T) {
	t.Parallel()

	const (
		pinned = "export const alpha = 1;"
		served = "export const alpha = 1 ;"
	)

	tarballBody := tarball(t, member{name: "package/dist/alpha.mjs", body: served})

	reg := &registry{}
	client := reg.serve(t, map[string][]byte{"/alpha.tgz": tarballBody})

	doc := manifestDoc{
		Schema: schemaVersion,
		Packages: []pkgSpec{
			{
				Name:    "an-invented-package",
				Version: "1.0.0",
				License: "MIT",
				Source:  reg.sourceFor("/alpha.tgz", tarballBody),
				Files: []fileSpec{
					pin("static/vendor/alpha.mjs", "package/dist/alpha.mjs", originNPM, pinned),
				},
			},
		},
	}

	root := writeManifest(t, doc)

	err := fetchAll(t.Context(), root, doc, client, io.Discard, false)
	if err == nil {
		t.Fatal("fetchAll accepted a member whose bytes are not the pinned ones. Every " +
			"other assertion here is satisfiable by a command that writes nothing and " +
			"fails on nothing, and this is the one that is not.")
	}

	if !strings.Contains(err.Error(), "static/vendor/alpha.mjs") {
		t.Errorf("the failure does not name the file: %v", err)
	}

	target := filepath.Join(root, filepath.FromSlash("static/vendor/alpha.mjs"))

	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("the file exists after a refused fetch (stat said %v). A refusal that "+
			"leaves a file behind is a refusal with a partial write in it", statErr)
	}
}

// TestATarballThatIsNotThePinnedOneIsRefusedBeforeItIsOpened is the other half
// of the integrity gate: the digest is held before the archive is walked, so a
// tarball the registry altered is refused as bytes rather than as a member.
func TestATarballThatIsNotThePinnedOneIsRefusedBeforeItIsOpened(t *testing.T) {
	t.Parallel()

	served := tarball(t, member{name: "package/dist/alpha.mjs", body: "export const alpha = 1;"})

	reg := &registry{}
	client := reg.serve(t, map[string][]byte{"/alpha.tgz": served})

	// The pin describes the bytes the registry did not serve, which is what a
	// tampered download and a re-published version both look like.
	source := reg.sourceFor("/alpha.tgz", served)
	source.Integrity = integrity(append([]byte("a different tarball"), 0x00))

	if _, err := download(t.Context(), client, source); err == nil {
		t.Fatal("download accepted a tarball whose sha512 is not the pinned one")
	}

	// And the same fetch with the right pin succeeds, so the refusal above is the
	// digest and not the registry.
	source.Integrity = integrity(served)

	if _, err := download(t.Context(), client, source); err != nil {
		t.Errorf("download refused the tarball the pin describes: %v", err)
	}
}

// TestTheIntegrityCheckAcceptsTheBytesItHashes is the positive half of the
// digest gate.
//
// Without it a `verifyIntegrity` that refused everything would satisfy every
// negative test around it — the "an audit nobody can fail is not an audit" rule
// applied to a command rather than to a test.
func TestTheIntegrityCheckAcceptsTheBytesItHashes(t *testing.T) {
	t.Parallel()

	body := []byte("a tarball")

	if err := verifyIntegrity(body, integrity(body)); err != nil {
		t.Errorf("verifyIntegrity refused the bytes it was computed from: %v", err)
	}

	for name, bad := range map[string]string{
		"a flipped byte":   integrity(append([]byte("a tarba"), 'l', 'z')),
		"a different SRI":  integrityAlgorithm + "-" + base64.StdEncoding.EncodeToString(make([]byte, sha512.Size)),
		"a missing dash":   "sha512",
		"sha1, not sha512": "sha1-" + base64.StdEncoding.EncodeToString(make([]byte, 20)),
		"not base64":       integrityAlgorithm + "-not base64 at all",
		"the wrong width":  integrityAlgorithm + "-" + base64.StdEncoding.EncodeToString(make([]byte, 8)),
	} {
		if err := verifyIntegrity(body, bad); err == nil {
			t.Errorf("verifyIntegrity accepted %s (%q)", name, bad)
		}
	}
}

// TestAMemberThePackageDoesNotShipIsNamed proves the lookup reports the member
// it could not find rather than an empty file — the difference between "the pin
// is stale" and "a vendored module is empty and 200s".
func TestAMemberThePackageDoesNotShipIsNamed(t *testing.T) {
	t.Parallel()

	body := tarball(t, member{name: "package/dist/other.mjs", body: "export const other = 0;"})

	if _, err := extract(body, "package/dist/alpha.mjs"); err == nil {
		t.Fatal("extract returned bytes for a member the archive does not carry")
	} else if !strings.Contains(err.Error(), "package/dist/alpha.mjs") {
		t.Errorf("the failure does not name the missing member: %v", err)
	}

	// And the `./` spelling real tarballs use resolves to the same member.
	prefixed := tarball(t, member{
		name: "./package/dist/alpha.mjs",
		body: "export const alpha = 1;",
	})

	got, err := extract(prefixed, "package/dist/alpha.mjs")
	if err != nil {
		t.Fatalf("extract with a `./` prefix: %v", err)
	}

	if string(got) != "export const alpha = 1;" {
		t.Errorf("extract returned %q", got)
	}
}

// TestALocalOriginIsVerifiedOnDiskAndReachesNoNetwork is the licence case, and
// the one that proves a `local` entry is not quietly fetched from the package it
// happens to sit beside.
func TestALocalOriginIsVerifiedOnDiskAndReachesNoNetwork(t *testing.T) {
	t.Parallel()

	const notice = "An Invented Package is MIT licensed. See the licence file."

	doc := manifestDoc{
		Schema: schemaVersion,
		Packages: []pkgSpec{
			{
				Name:    "a-local-only-package",
				Version: "1.0.0",
				License: "MIT",
				// A URL that would fail if it were ever fetched: the claim under
				// test is that it is not.
				Source: sourceSpec{Kind: originNPM, URL: "https://example.invalid/x.tgz"},
				Files: []fileSpec{
					pin("static/vendor/NOTICE", "", originLocal, notice),
				},
			},
		},
	}

	root := writeManifest(t, doc)

	target := filepath.Join(root, "static", "vendor")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	noticePath := filepath.Join(target, "NOTICE")
	if err := os.WriteFile(noticePath, []byte(notice), 0o600); err != nil {
		t.Fatalf("write the notice: %v", err)
	}

	reg := &registry{}
	client := reg.serve(t, nil)

	if err := fetchAll(t.Context(), root, doc, client, io.Discard, false); err != nil {
		t.Fatalf("a matching local file was refused: %v", err)
	}

	if got := reg.hits.Load(); got != 0 {
		t.Errorf("a package with only local files made %d request(s). A local file is "+
			"this repository's own; there is nothing upstream to fetch it from", got)
	}

	// And the check is real: the same manifest against edited bytes must fail.
	if err := os.WriteFile(noticePath, []byte(notice+" edited"), 0o600); err != nil {
		t.Fatalf("edit the notice: %v", err)
	}

	if err := fetchAll(t.Context(), root, doc, client, io.Discard, false); err == nil {
		t.Fatal("a local file whose bytes are not the pinned ones was accepted")
	}

	// And a local file that is not there is an unfinished edit, not a fetch that
	// failed, so the message says which.
	if err := os.Remove(noticePath); err != nil {
		t.Fatalf("remove the notice: %v", err)
	}

	err := fetchAll(t.Context(), root, doc, client, io.Discard, false)
	if err == nil {
		t.Fatal("a missing local file was accepted")
	}

	if !strings.Contains(err.Error(), originLocal) {
		t.Errorf("the failure does not say the file is local: %v", err)
	}
}

// TestAManifestThisReaderDoesNotUnderstandIsRefused covers the shape and the
// emptiness, which are the two ways a manifest is wrong in a way that would
// otherwise be silent — a reader that read schema 2 loosely would write files
// from a manifest whose fields it had not understood.
func TestAManifestThisReaderDoesNotUnderstandIsRefused(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "vendor.json")

	for name, body := range map[string]string{
		"a schema it does not read": `{"schema": 2, "packages": [{"name": "x"}]}`,
		"no packages at all":        `{"schema": 1, "packages": []}`,
		"not JSON at all":           `{"schema": 1,`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("%s: write: %v", name, err)
		}

		if _, err := loadManifest(path); err == nil {
			t.Errorf("loadManifest accepted %s", name)
		}
	}

	if _, err := loadManifest(filepath.Join(root, "absent.json")); err == nil {
		t.Error("loadManifest accepted a manifest that is not there")
	}
}

// TestTheRegistryURLMustBeHTTPSAndCarryNoCredentials is the fetch's one
// boundary. The host is deliberately not pinned — a check naming a registry would
// be a second place to change when a package moves — so the scheme is the whole
// control, and it has to hold.
func TestTheRegistryURLMustBeHTTPSAndCarryNoCredentials(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"plain http":       "http://registry.example/p.tgz",
		"a file scheme":    "file:///etc/passwd",
		"credentials":      "https://user:token@registry.example/p.tgz",
		"no host":          "https:///p.tgz",
		"nothing at all":   "",
		"not a URL at all": "://",
	} {
		if err := checkRegistryURL(raw); err == nil {
			t.Errorf("checkRegistryURL accepted %s (%q)", name, raw)
		}
	}

	for _, raw := range []string{
		"https://registry.npmjs.org/pixi.js/-/pixi.js-8.22.0.tgz",
		"https://example.invalid/deep/path/p.tgz?token=1",
	} {
		if err := checkRegistryURL(raw); err != nil {
			t.Errorf("checkRegistryURL refused %q: %v", raw, err)
		}
	}
}

// TestAnOriginThisReaderDoesNotKnowIsRefused closes the switch: the two origins
// the manifest documents are fetched and verified, and an origin it does not know
// is refused rather than written.
func TestAnOriginThisReaderDoesNotKnowIsRefused(t *testing.T) {
	t.Parallel()

	const body = "export const alpha = 1;"

	tarballBody := tarball(t, member{name: "package/dist/alpha.mjs", body: body})

	reg := &registry{}
	client := reg.serve(t, map[string][]byte{"/alpha.tgz": tarballBody})

	doc := manifestDoc{
		Schema: schemaVersion,
		Packages: []pkgSpec{
			{
				Name:    "an-invented-package",
				Version: "1.0.0",
				License: "MIT",
				Source:  reg.sourceFor("/alpha.tgz", tarballBody),
				Files: []fileSpec{
					pin("static/vendor/alpha.mjs", "package/dist/alpha.mjs", originNPM, body),
					{
						Path:   "static/vendor/whatever.js",
						Origin: "cdn",
						Bytes:  1,
						SHA256: strings.Repeat("a", 64),
					},
				},
			},
		},
	}

	root := writeManifest(t, doc)

	err := fetchAll(t.Context(), root, doc, client, io.Discard, false)
	if err == nil {
		t.Fatal("an unknown origin was accepted. A file whose origin this reader does " +
			"not know is a file nothing verifies.")
	}

	if !strings.Contains(err.Error(), "cdn") {
		t.Errorf("the failure does not name the origin: %v", err)
	}

	// The pinned npm file beside it was written, because the gate is per file and
	// one bad entry must not hide the ones that were checked.
	if _, statErr := os.Stat(filepath.Join(root, "static", "vendor", "alpha.mjs")); statErr != nil {
		t.Errorf("the pinned npm file is missing after an unknown origin in the same "+
			"package: %v", statErr)
	}
}

// TestTheRepositoryPinIsReadableAndItsFilesAreCommitted runs against the real
// `tools/vendor.json` rather than a fixture, so the claim is about this
// repository: the pin parses, names at least one package, and every file it
// declares is committed beside it.
//
// A vendored blob that the pin names and the tree does not carry is a `404` at
// `/assets/`, which is the silent absence `make vendor-check` exists to catch —
// and this is the assertion that the pin and the tree are the same claim.
func TestTheRepositoryPinIsReadableAndItsFilesAreCommitted(t *testing.T) {
	t.Parallel()

	doc, err := loadManifest(filepath.Join(repositoryRoot, defaultManifest))
	if err != nil {
		t.Fatalf("loadManifest on the repository's own pin: %v", err)
	}

	root := os.DirFS(repositoryRoot)

	for index := range doc.Packages {
		pkg := &doc.Packages[index]

		if pkg.Name == "" || pkg.Version == "" || pkg.License == "" {
			t.Errorf("%s pins a package with no name, version or licence", defaultManifest)
		}

		if len(pkg.Files) == 0 {
			t.Errorf("%s pins %s with no files, so `make vendor` would fetch a tarball "+
				"and write nothing", defaultManifest, pkg.Name)
		}

		for file := range pkg.Files {
			if _, statErr := fs.Stat(root, pkg.Files[file].Path); statErr != nil {
				t.Errorf("%s pins %s, which is not committed: %v", defaultManifest,
					pkg.Files[file].Path, statErr)
			}
		}
	}
}

// TestADryRunVerifiesEverythingAndWritesNothing is the check a manifest edit
// needs before it is committed, and the reason it is a flag rather than a
// second code path: the digest is the same digest, and a dry run that skipped it
// would be a check that reports success without having checked anything.
func TestADryRunVerifiesEverythingAndWritesNothing(t *testing.T) {
	t.Parallel()

	const body = "export const alpha = 1;"

	tarballBody := tarball(t, member{name: "package/dist/alpha.mjs", body: body})

	reg := &registry{}
	client := reg.serve(t, map[string][]byte{"/alpha.tgz": tarballBody})

	doc := manifestDoc{
		Schema: schemaVersion,
		Packages: []pkgSpec{
			{
				Name:    "an-invented-package",
				Version: "1.0.0",
				License: "MIT",
				Source:  reg.sourceFor("/alpha.tgz", tarballBody),
				Files: []fileSpec{
					pin("static/vendor/alpha.mjs", "package/dist/alpha.mjs", originNPM, body),
				},
			},
		},
	}

	root := writeManifest(t, doc)

	if err := fetchAll(t.Context(), root, doc, client, io.Discard, true); err != nil {
		t.Fatalf("the dry run refused a package the pin describes: %v", err)
	}

	if got := reg.hits.Load(); got != 1 {
		t.Errorf("the dry run asked the registry %d times, want 1 — a dry run that does "+
			"not fetch has verified nothing", got)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read the root: %v", err)
	}

	for _, entry := range entries {
		if entry.Name() != "vendor.json" {
			t.Errorf("the dry run created %s. A dry run that writes is a `make vendor` "+
				"with a misleading name", entry.Name())
		}
	}

	// And it still holds the bytes to the pin: a mismatching package fails the
	// dry run rather than passing it because nothing was written.
	doc.Packages[0].Files[0] = pin(
		"static/vendor/alpha.mjs", "package/dist/alpha.mjs", originNPM, "export const alpha = 2;")

	if err := fetchAll(t.Context(), root, doc, client, io.Discard, true); err == nil {
		t.Fatal("the dry run accepted a member whose bytes are not the pinned ones")
	}
}

// TestRunRefusesAManifestItCannotRead proves the command's entry point refuses
// rather than exiting zero: a target that cannot find the pin must fail, because
// the alternative is a `make vendor` that appears to have verified everything.
func TestRunRefusesAManifestItCannotRead(t *testing.T) {
	t.Parallel()

	absent := filepath.Join(t.TempDir(), "absent.json")

	if err := run([]string{"-manifest", absent}, io.Discard); err == nil {
		t.Fatal("run reported success for a manifest that is not there")
	}

	if err := run([]string{"-nonsense"}, io.Discard); err == nil {
		t.Fatal("run reported success for a flag it does not know")
	}
}
