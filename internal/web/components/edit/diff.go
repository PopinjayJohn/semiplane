package edit

// The package has no doc comment above the package clause, and that is not an
// oversight: templ writes `// templ: version: …` directly above `package` in every
// file it generates, so a hand-written package comment here is a *second* godoc
// and `godoclint` fails the build with "package has more than one godoc". The
// parent package hit exactly this and records the same answer at the foot of
// `components/viewmodels.go`, so the explanations here live inside the package —
// which is also where `components/ui/table.templ` and `components/states.templ`
// keep theirs.

// The editor's markup: the split surface a GM types into, and the two-column
// conflict diff that replaces its preview pane on a 412.
//
// The diff algorithm is here rather than in the route for one reason: the data a
// conflict view renders *is* a diff, and a route that computed it would have to
// hand the markup a shape this package invented. Putting `Diff` beside the
// template that walks its result means the shape is declared once, next to its
// only consumer, and it is testable without an `http.Request` — which matters
// because a diff that is wrong is the one bug in this phase that still *looks*
// right: two columns of plausible text side by side, silently misaligned.
//
// # Not a three-way merge, and why that is additive rather than a rewrite
//
// The architecture record's §6.2 says this in one line and it is worth repeating
// as code: v1 is side-by-side, not merged. The tempting shortcut is to reach for
// the common ancestor now and call it a merge — but `page_revisions` holds it
// only for pages semiplane itself has published (S-6.4), so a page that has only
// ever been edited in Obsidian has no H1 to merge against, and a merge that
// silently fell back to two-way for those pages would be a merge whose answer
// depends on history this project does not have.
//
// So the two-way diff is the whole of v1, and a real three-way merge arrives
// later as an *addition*: `Diff` keeps producing the two columns the conflict UI
// renders, and a merge is a second function that also takes the ancestor `Diff`
// does not need. Nothing here has to be rewritten, and the fallback disappears
// with the fallback's cause.

import "strings"

// diffContext is how many unchanged lines may sit between two changed regions
// before they are reported as two hunks rather than one.
//
// Two, and not zero and not "unlimited". Zero reports a paragraph break as two
// hunks, so a GM accepts or rejects the same paragraph twice and the two
// decisions are not independent — accepting the second silently undoes a third.
// Unlimited merges a whole page into one hunk, and then per-hunk accept/reject
// stops being per-hunk. Two is the conventional value for the same reason it is
// conventional elsewhere: a reader needs one unchanged line on each side to know
// *where* the change is, and needs the next one to know it is not the same change
// again.
const diffContext = 2

// maxDiffCells caps the LCS table, and the fallback is what happens past it.
//
// A quadratic table over two page-length texts costs one cell per line-pair, so a
// hostile or merely enormous pair of pages could ask for gigabytes of int32. The
// cap is a refusal to *compute*, not a refusal to serve: past it the two sides
// are reported as one hunk covering everything, which is a correct answer
// (everything does differ) and merely less useful. A GM looking at a 5,000-line
// conflict gets one hunk; a GM looking at a 40-line conflict gets hunks.
const maxDiffCells = 1 << 18

// Hunk is one contiguous region in which the two sides differ, with the lines
// each side holds there.
//
// A hunk is *not* a set of matched pairs. `Left` and `Right` are independent
// slices with their own line numbers, and their lengths differ whenever one side
// added or removed lines — which is the whole reason for a two-column layout
// rather than a table of pairs: a paired row forces a blank into the shorter
// side, and a blank in a diff is a claim that a line was deleted that was not.
type Hunk struct {
	// Index is the hunk's 1-based position, so the heading can name it and a
	// reader can say "hunk two" in a report. Assigned by `Diff`, never by a
	// caller: an index the caller supplies is an index that can disagree with
	// the slice's order.
	Index int
	// Left is what the buffer holds in this region.
	Left []DiffLine
	// Right is what the file on disk holds in the same region.
	Right []DiffLine
}

// DiffLine is one line on one side, with the number it has on that side.
//
// The number, not an offset into a rendered string: a line number is what a GM
// reads off in Obsidian to go and look, and it is the only locator that survives
// the two sides having different lengths above this hunk.
type DiffLine struct {
	// Number is the 1-based line number on its own side.
	Number int
	// Text is the line's content, without its terminator.
	Text string
}

// Diff returns the regions in which left and right differ, in order.
//
// Two arguments rather than a Hunk list from the caller, because the whole
// function is the computation: a caller that assembled the hunks would have had
// to reimplement this, and a caller that passed them through would be a caller
// with no reason to exist.
//
// An empty result means the two sides are equal line for line. That is reachable
// on a 412 — a client can present a validator for a page whose content has not
// changed, having derived it from something else — and the conflict view has a
// designed state for it rather than a blank pane.
func Diff(left, right string) []Hunk {
	return groupHunks(editScript(splitLines(left), splitLines(right)))
}

// splitLines is a text's lines, without a trailing empty one.
//
// The trailing element `strings.Split` produces for a text ending in a newline
// is the *absence* of a line, not a line, and counting it would make every
// correctly-terminated file differ from every other by one blank line at the end
// — so a GM would be offered a hunk containing an empty row on both sides and
// asked to accept it. The newline itself is not compared: the write path stores
// the request body byte for byte, and a trailing-newline difference is real but
// invisible here, so it is a difference this view does not claim to have found.
// The bytes are compared by the validator, which is the thing that decides
// whether a write happens at all.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}

	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return lines
}

// opKind is what one step of the walk did to reach the next pair of lines.
type opKind uint8

const (
	// opEqual is a line both sides hold, at the same place in the walk.
	opEqual opKind = iota
	// opRemove is a line only the left side holds: the GM deleted it, or their
	// buffer never had it.
	opRemove
	// opAdd is a line only the right side holds. Something else wrote it, and
	// that something else is the reason this whole view exists.
	opAdd
)

// editOp is one step of the walk, carrying the line's text and its 1-based
// number on whichever side holds it.
//
// The text is carried rather than looked up again by number, because a hunk is
// built after the walk and re-deriving the line from its number would be a second
// place for an off-by-one to live.
type editOp struct {
	kind opKind
	// text is the line, on whichever side the step names.
	text string
	// left is the 1-based number on the left side, or 0 when the step is an
	// addition.
	left int
	// right is the 1-based number on the right side, or 0 when the step is a
	// removal.
	right int
}

// editScript walks from the top of both sides to the bottom, using the length of
// the longest common subsequence to decide each step.
//
// The LCS, and not a greedy "skip ahead to the next matching line", because the
// greedy version reports a moved line as one deletion plus one insertion instead
// of one move — and a GM reconciling a moved heading should see that heading in
// both columns at its new position, rather than be asked to accept its
// disappearance and then separately its reappearance.
//
// The table is filled backwards and walked forwards. Filling forwards and
// walking backwards is the same table and produces the hunks in the opposite
// order, and hunks in the wrong order is a diff whose second half is its first.
func editScript(left, right []string) []editOp {
	rows, columns := len(left), len(right)

	if rows*columns > maxDiffCells {
		return wholeReplace(left, right)
	}

	width := columns + 1
	table := make([]int32, (rows+1)*width)

	for row := rows - 1; row >= 0; row-- {
		for column := columns - 1; column >= 0; column-- {
			switch {
			case left[row] == right[column]:
				table[row*width+column] = table[(row+1)*width+column+1] + 1
			case table[(row+1)*width+column] >= table[row*width+column+1]:
				// A removal when the two candidates tie, so an addition never
				// becomes a removal by accident of the comparison order. The
				// choice is arbitrary in the LCS; making it *repeatable* is the
				// property a diff a reader must reconcile needs.
				table[row*width+column] = table[(row+1)*width+column]
			default:
				table[row*width+column] = table[row*width+column+1]
			}
		}
	}

	ops := make([]editOp, 0, rows+columns)

	// The two cursors are named for the sides they walk rather than for the
	// arithmetic they do, because the arithmetic is the same on both and the
	// asymmetry is the whole subject: `here` advances over the left side, `there`
	// over the right, and every step decides which one moves.
	here, there := 0, 0

	for here < rows && there < columns {
		switch {
		case left[here] == right[there]:
			ops = append(ops, editOp{
				kind:  opEqual,
				text:  left[here],
				left:  here + 1,
				right: there + 1,
			})
			here++
			there++
		case table[(here+1)*width+there] >= table[here*width+there+1]:
			ops = append(ops, editOp{kind: opRemove, text: left[here], left: here + 1})
			here++
		default:
			ops = append(ops, editOp{
				kind:  opAdd,
				text:  right[there],
				right: there + 1,
			})
			there++
		}
	}

	for ; here < rows; here++ {
		ops = append(ops, editOp{kind: opRemove, text: left[here], left: here + 1})
	}

	for ; there < columns; there++ {
		ops = append(ops, editOp{
			kind:  opAdd,
			text:  right[there],
			right: there + 1,
		})
	}

	return ops
}

// wholeReplace is the past-the-cap walk: every left line removed, then every
// right line added, with nothing in common between them.
//
// Not an error and not an empty result. It is the honest "these two have nothing
// in common" answer, and it renders as one hunk whose two columns are the two
// documents — which is what a 5,000-line conflict should look like beside a
// hunk-level merge the reader cannot use anyway.
func wholeReplace(left, right []string) []editOp {
	ops := make([]editOp, 0, len(left)+len(right))

	for line, text := range left {
		ops = append(ops, editOp{kind: opRemove, text: text, left: line + 1})
	}

	for line, text := range right {
		ops = append(ops, editOp{kind: opAdd, text: text, right: line + 1})
	}

	return ops
}

// groupHunks turns a walk into hunks, merging regions that sit closer together
// than the context window.
func groupHunks(ops []editOp) []Hunk {
	var (
		hunks  []Hunk
		first  int
		last   int
		opened bool
	)

	for position, step := range ops {
		if step.kind == opEqual {
			// Only a *run* of equals long enough to be a boundary ends a hunk.
			// Ending on the first equal would make every changed line its own
			// hunk, which is the granularity at which per-hunk accept/reject
			// stops being a decision a person can make.
			if opened && position-last > diffContext {
				hunks = append(hunks, hunkFrom(ops, first, last, len(hunks)+1))
				opened = false
			}

			continue
		}

		if !opened {
			first, opened = position, true
		}

		last = position
	}

	if opened {
		hunks = append(hunks, hunkFrom(ops, first, last, len(hunks)+1))
	}

	return hunks
}

// hunkFrom is the hunk covering the walk's steps in [first, last], inclusive.
func hunkFrom(ops []editOp, first, last, index int) Hunk {
	hunk := Hunk{Index: index}

	for _, step := range ops[first : last+1] {
		if step.left > 0 {
			hunk.Left = append(hunk.Left, DiffLine{Number: step.left, Text: step.text})
		}

		if step.right > 0 {
			hunk.Right = append(hunk.Right, DiffLine{Number: step.right, Text: step.text})
		}
	}

	return hunk
}
