package wiki

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
)

// The two types that make S-5.7's ordering structural rather than conventional.
//
// The requirement is that redaction happens before sanitisation and before the
// value reaches any template, so that no intermediate buffer holds an unredacted
// copy for a non-GM. Stated as an ordering it is a convention: the pipeline is a
// sequence of calls in one function, and a later change — a cache warmed
// somewhere else, a "helpful" debug log of the renderer's input, a second route
// that renders a page — can put a parse in front of a redact without anything
// noticing.
//
// Stated as two types it is not. `source` holds a page's bytes and its only
// method hands them to a `content.Redactor`. `redacted` holds what came back and
// its only method parses it. Neither type exports its body, neither is
// constructible from outside this package, and there is no conversion between
// them that does not run the redactor. So for a non-GM the bytes never reach
// `content.Parse`, never reach `Renderer.Render`, never reach the sanitiser, and
// never reach the cache: the type that holds them has no other way out.
//
// The guarantee this file does *not* make is that the bytes are never in memory.
// They are — they were just read from the vault, and they had to be in order to
// be hashed. What is guaranteed is that no buffer *downstream of the redactor*
// holds an unredacted copy, and that the only such buffer is the one the read
// fills and the hash consumes.

// source is one page's bytes as they were read, and the only value in this
// package that holds them.
//
// Its body is unexported and it has exactly one method, which is what makes the
// ordering above a property of the type rather than of a comment. A future route
// that wants to render a page has the same two steps available in the same order,
// because the second one has nothing to take but the first one's output.
type source struct {
	// body is the file as it was written, and may hold content the reader may not
	// see. Nothing outside `redact` reads it.
	body string
	// hash is the sha256 of the same bytes, hex encoded, and is the cache key's
	// `content_hash`. Carried on the value rather than returned alongside it
	// because the two are read together on every path and a caller that could get
	// one without the other could key a cache on a hash of different bytes.
	hash string
}

// newSource returns the source for a page's bytes.
//
// The hash is taken here, over the bytes as read, rather than from a stat or
// from the file afterwards: S-5.2 makes validity a fact about content, and a
// hash of a re-read is a hash of whatever a sync client wrote in between.
func newSource(data []byte) source {
	sum := sha256.Sum256(data)

	return source{body: string(data), hash: hex.EncodeToString(sum[:])}
}

// redact removes whatever the redactor removes, and returns the result.
//
// The only way out of a `source`, and the only call in this package that hands a
// page's bytes to anything else. The redactor receives the whole file rather than
// the prose alone, so a `[!secret]` in a front-matter field is removed for a
// non-GM on the same terms as one in a paragraph; a redactor that could only see
// `Document.Body` would have to be trusted to not care about the fields, and
// front matter is attacker-reachable (S-4.7).
//
// The error is wrapped with the campaign's slug by the caller rather than here,
// because this type does not know which campaign it is for — the bytes are
// campaign-confined but not campaign-labelled.
func (s source) redact(
	redactor content.Redactor,
	includeSecrets bool,
) (redacted, error) {
	body, err := redactor.Redact(s.body, includeSecrets)
	if err != nil {
		return redacted{}, fmt.Errorf("redact page: %w", err)
	}

	return redacted{body: body}, nil
}

// redacted is a page's bytes as a `content.Redactor` returned them.
//
// Reached only from `source.redact`, and leaving only through `parse`. That is
// the whole of its type contract: a value of this type is by definition not
// something the redactor declined to remove.
type redacted struct {
	// body is the redacted file. What the redactor left, which is what the
	// renderer is given — so this is the value S-5.7 is about, and the reason it
	// is a separate type from `source` rather than a field on it.
	body string
}

// parse interprets the front matter and returns the document the renderer takes.
//
// The step after redaction and before rendering, and the only call to
// `content.Parse` in this package. It is total, by C1's design: a document whose
// front matter did not interpret still carries a body and still renders, and the
// reason travels in `FrontMatter.Err` for a caller that wants to say so. This
// handler does not read it — S-3.3 makes a malformed block inert rather than
// fatal, and P6's edit view is where a GM is told about it.
func (d redacted) parse(kinds domain.PageKindRegistry) content.Document {
	return content.Parse([]byte(d.body), kinds)
}
