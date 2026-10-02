package assets

import (
	"path"
	"slices"
	"strings"
)

// How a file is served, decided from its extension and from nothing else.
//
// Three rules hold the whole file together, and each is a security decision
// rather than a formatting one:
//
//   - **The extension is the authority.** Not the bytes. `http.ServeContent`
//     sniffs the first bytes of anything whose `Content-Type` it does not
//     already know, which is `http.DetectContentType` reading attacker-supplied
//     content — and a content root is writable by an Obsidian sync client, by a
//     community plugin and by any device that syncs that vault (S-4.7). So every
//     response this route writes sets its `Content-Type` from the table below,
//     which means the sniffing branch in `serveContent` is unreachable: the code
//     only reaches `ServeContent` after a table hit.
//   - **The table is closed.** An extension nobody decided about is a 404, not a
//     fallback. A fallback is how a closed table stops being closed — and the
//     obvious fallback is the platform's own: `mime.TypeByExtension` consults
//     `/etc/mime.types`, so the same binary serves different types on different
//     hosts and a `.xml` comes back as `text/xml; charset=utf-8` on one and
//     `application/xml` on another. A deployment whose asset types depend on its
//     distribution's package selection is not a deployment anybody can reason
//     about.
//   - **Nothing in the table is a type a browser will execute or treat as a
//     document.** That is why there is no `text/html` entry, no `text/javascript`
//     entry, and no `application/xhtml+xml` entry. The active types a campaign
//     might genuinely want — SVG, XML — are served under their real media type
//     with `Content-Disposition: attachment` and a `sandbox` policy, so a
//     navigation downloads them rather than rendering them in semiplane's origin.
//     A `.svg` still draws as an `<img>`, because `Content-Disposition` governs
//     navigations and not subresource loads.

// assetMedia is how one extension is served.
type assetMedia struct {
	// contentType is the `Content-Type` header, media type and charset together.
	// Never empty: an empty value is what sends `ServeContent` to its sniffing
	// branch, which is the one branch of this route that must not run.
	contentType string

	// attachment forces `Content-Disposition: attachment`, so a navigation to the
	// URL downloads the file instead of rendering it in semiplane's origin.
	//
	// It costs nothing for the one case that matters most: a battle map dropped
	// in a `<img>` renders identically, because the Fetch standard applies
	// `Content-Disposition` to navigations and downloads only, never to a
	// subresource load.
	attachment bool

	// sandbox adds `Content-Security-Policy: sandbox`, which puts any document
	// the browser does create for this response into an opaque origin with no
	// script, no plugins and no same-origin reach back into semiplane.
	//
	// It is belt to the `attachment` braces and is set for one reason: a browser
	// is free to ignore `Content-Disposition` for a type it believes it must
	// render, and `sandbox` is the one directive that holds even then.
	//
	// `default-src 'none'` is deliberately not alongside it, and nothing is set on
	// the image types at all: `img-src` falls back to `default-src`, so that
	// directive on an image response blocks the very resource the reader asked
	// for. A battle map that does not draw is the bug this route exists to avoid.
	sandbox bool
}

// The `Content-Disposition` value a non-inline asset carries.
const dispositionAttachment = "attachment"

// The `Content-Security-Policy` an asset a browser may treat as a document
// carries.
//
// `sandbox` with no token list, which is the strictest form the directive has:
// an opaque origin, no scripts, no forms, no plugins. The policy is the
// response's and not the route's, because a policy here would also govern the
// shell and the stylesheet, and `default-src 'none'` on a campaign asset would
// stop the asset loading at all.
const sandboxPolicy = "sandbox"

// assetTypes is the closed table of servable extensions.
//
// A package-level map rather than a field, and the reason is that it holds no
// state: it is written once by the literal below and never written again, which
// is what makes it a table rather than the registration `AGENTS.md` rules out.
// A per-handler copy would be a second table that could differ from this one.
//
// The set is deliberately the formats a VTT and a wiki actually use — battle
// maps, handouts, fonts, a data pack's JSON — and it is grouped by what the
// browser will do with the answer, which is the distinction that matters:
//
//   - images, audio, video, PDF, fonts and the plain-text types render;
//   - SVG and XML are documents a browser *can* run, so they are forced to a
//     download;
//   - ZIP is never rendered, so it is too.
var assetTypes = map[string]assetMedia{
	// Battle maps and handouts, raster.
	".png":  {contentType: "image/png"},
	".jpg":  {contentType: "image/jpeg"},
	".jpeg": {contentType: "image/jpeg"},
	".gif":  {contentType: "image/gif"},
	".webp": {contentType: "image/webp"},
	".avif": {contentType: "image/avif"},
	".bmp":  {contentType: "image/bmp"},
	".tif":  {contentType: "image/tiff"},
	".tiff": {contentType: "image/tiff"},
	".ico":  {contentType: "image/vnd.microsoft.icon"},

	// SVG: a real, legitimate asset format and the one this table has to be most
	// careful about. An SVG is a document — it can carry a `<script>`, an
	// `onload`, and a `<foreignObject>` — so served inline from semiplane's own
	// origin it is stored XSS against every reader of the campaign. It is served
	// as `image/svg+xml` rather than as octet-stream so that `<img>` still draws
	// it, `attachment` so a navigation downloads it, and `sandbox` so that a
	// browser which renders it anyway cannot reach this origin.
	".svg": {contentType: "image/svg+xml", attachment: true, sandbox: true},

	// Documents a browser can execute. `application/xml` renders through
	// whatever stylesheet the document names, and XSLT is a scripting language,
	// so XML is treated exactly as SVG is.
	".xml": {contentType: "application/xml", attachment: true, sandbox: true},

	// Handouts.
	".pdf": {contentType: "application/pdf"},

	// Text, with an explicit charset so the bytes are decoded the way the author
	// wrote them rather than by whatever the browser guesses.
	".txt":  {contentType: "text/plain; charset=utf-8"},
	".csv":  {contentType: "text/csv; charset=utf-8"},
	".json": {contentType: "application/json"},
	".css":  {contentType: "text/css; charset=utf-8"},

	// Audio and video, for a recording of a session.
	".mp3":  {contentType: "audio/mpeg"},
	".ogg":  {contentType: "audio/ogg"},
	".opus": {contentType: "audio/opus"},
	".wav":  {contentType: "audio/wav"},
	".flac": {contentType: "audio/flac"},
	".m4a":  {contentType: "audio/mp4"},
	".mp4":  {contentType: "video/mp4"},
	".webm": {contentType: "video/webm"},
	".ogv":  {contentType: "video/ogg"},
	".mov":  {contentType: "video/quicktime"},

	// Fonts, for a data pack's own stylesheet.
	".woff":  {contentType: "font/woff"},
	".woff2": {contentType: "font/woff2"},
	".ttf":   {contentType: "font/ttf"},
	".otf":   {contentType: "font/otf"},

	// Archives. A download, because there is no such thing as rendering one and
	// a browser asked to will produce something semiplane did not intend.
	".zip": {contentType: "application/zip", attachment: true},
}

// refusedExtensions are extensions that exist inside a content root and are
// **never** served, whatever their bytes are.
//
// A named set rather than an absence from `assetTypes`, because the difference
// matters and the two answers are different. An extension absent from the table
// is something nobody decided about and gets a 404; an extension in this set is
// something this route has decided about and gets a 403, because a GM who
// dropped a `.md` into the vault deserves to be told the route will not serve it
// rather than to be told it does not exist.
//
// Each entry is here for a reason, and the reason is never "it looked odd":
//
//   - `.md`: a page belongs to `/wiki/`. Serving the source here would bypass
//     the render pipeline and, with it, S-5.7's ordering — the bytes would reach
//     a reader having passed no redactor at all, which is exactly the failure
//     ADR 0029 exists to prevent. Phase 10's `[!secret]` callouts would be
//     published in full to anybody who asked for the file.
//   - `.html`, `.htm`, `.xhtml`: there is no `text/html` in this product outside
//     the shell's own template, and an asset served as one is stored XSS against
//     the origin the session cookie lives in.
//   - `.js`, `.mjs`, `.cjs`: a script served from this origin runs in this origin.
//   - `.wasm`: the same, in a language with no route back out.
var refusedExtensions = []string{
	".md",
	".markdown",
	".html",
	".htm",
	".xhtml",
	".js",
	".mjs",
	".cjs",
	".wasm",
}

// mediaFor returns how a root-relative asset path is served, and whether it is
// served at all.
//
// Two answers come out of one lookup because a caller has to be able to tell
// them apart to say why: `refused` is a decision about the kind of file and
// `found` is the decision about the bytes' type.
func mediaFor(rel string) (media assetMedia, found, refused bool) {
	ext := strings.ToLower(path.Ext(rel))

	if slices.Contains(refusedExtensions, ext) {
		return assetMedia{}, false, true
	}

	media, found = assetTypes[ext]

	return media, found, false
}

// hiddenName reports whether a root-relative path is one this route will not
// address even though it is inside the campaign's root.
//
// The rule is a leading dot on **any** component, or a `node_modules` component,
// and the reason it is the index's rule is the point: `content`'s `indexablePath`
// refuses a page through `content`'s `skipDirectory` on exactly these two, so a
// file this route serves and the index has no row for would be a file with two
// answers to "is this campaign's content" — and the asset route's answer is the one
// a sync client can change without a page changing. `.git/`, `.obsidian/`,
// `.trash/`, `.stfolder/` and this process's own `.semiplane-….tmp` staging file
// are all five spellings of "an editor, a sync client or this process writes its
// own bookkeeping here", which is what Obsidian's own hidden rule means.
//
// **The `.tmp` / `.swp` / `~` names do not need a rule here, and that is a
// conclusion rather than an omission.** Architecture §5.2's list is a list of
// *suffixes*, and each of them is the extension of the name it applies to, so the
// closed table already refuses every one of them: `path.Ext("map.png.tmp")` is
// `.tmp`, `path.Ext("map.png.swp")` is `.swp`, and `path.Ext("map.png~")` is
// `.png~` — none of which is in `assetTypes`, so each is a 404 through the same
// lookup that refuses `.bin`. Copying the list here would be a second source of
// truth that cannot change any answer today and would be a second source of truth
// to keep in step; `TestHiddenNamesAreNotServed` asserts the outcome either way,
// and names the mechanism, so the reasoning is checked rather than assumed.
func hiddenName(rel string) bool {
	for segment := range strings.SplitSeq(rel, "/") {
		if strings.HasPrefix(segment, ".") || segment == hiddenDirectory {
			return true
		}
	}

	return false
}

// hiddenDirectory is the one component `content`'s `skipDirectory` refuses that is
// not a dotfile: a dependency tree, which is not campaign content and which a wiki
// index must not pull in.
const hiddenDirectory = "node_modules"
