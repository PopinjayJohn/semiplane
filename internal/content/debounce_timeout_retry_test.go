package content_test

// The second half of §13's "mid-write read" row: the drop is picked up again.
//
// `debounce.go` documents the drop this way: "A dropped path is picked up by
// the next event, or by the periodic rescan (S-4.5) if no more events are
// coming." `TestStableReadTimeoutWhenTheSizeNeverSettles` holds the first
// half — the timeout fires and nothing is emitted while the writer keeps
// moving — and this test holds the second: the next event re-arms the path
// from a clean state and the page settles whole. A filter that dropped the
// path and then forgot how to re-arm it would pass that test and leave the
// page unsettled until the next rescan, which is the "stale index the rescan
// closes minutes later" outcome the settle budget exists to avoid.
//
// The exactly-one-timeout assertion is the "retry once" half made observable:
// the retry settles, and a settle reports no timeout
// (`TestASettledWriteReportsNoTimeout`), so the whole episode carries a single
// `content.stable_read_timeout` line.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/observability"
)

func TestATimedOutPathIsRearmedByTheNextEventAndSettlesWhole(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "retry")
	const rel = "stuck.md"

	fx.write("retry", rel, pageVersion(1))

	stop := make(chan struct{})
	finished := make(chan struct{})

	var gaps gapRecorder

	// One byte every few milliseconds, and no event after the first: the quiet
	// period expires, the confirmation starts, and every pair of samples
	// disagrees. Exactly the "writer appears stuck" case, borrowed from the
	// timeout test because the premise is the same and only the aftermath
	// differs.
	go func() {
		defer close(finished)

		full := filepath.Join(fx.dirs["retry"], filepath.FromSlash(rel))

		for {
			select {
			case <-stop:
				return
			case <-time.After(sampleGap / 3):
			}

			handle, err := os.OpenFile(full, os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return
			}

			if _, err := handle.WriteString("x"); err != nil {
				handle.Close()

				return
			}

			handle.Close()

			gaps.mark()
		}
	}()

	var stopOnce sync.Once

	stopWriter := func() {
		stopOnce.Do(func() {
			close(stop)
			<-finished
		})
	}

	t.Cleanup(stopWriter)

	// The drop half: the timeout fires, carrying the campaign and the path,
	// because without either an operator cannot tell whose writer is stuck.
	line := fx.awaitEvent(string(observability.EventContentStableReadTimeout), &gaps)
	if !strings.Contains(line, `"campaign_id":"`+strconv.FormatInt(fx.ids["retry"], 10)+`"`) {
		t.Errorf("the timeout event carries no campaign_id: %s", line)
	}

	if !strings.Contains(line, `"path":"`+rel+`"`) {
		t.Errorf("the timeout event carries no path: %s", line)
	}

	// Nothing was emitted, and nothing will be while the file keeps moving
	// without an event. A plain silence window, deliberately, for the reason
	// the timeout test gives: this writer delivers no events, so every arrival
	// here would be trivially entitled and any arrival at all is the defect.
	fx.silent(wantSilence)

	// The writer finishes. Stop the appender first, so the whole page below is
	// the only version on disk and the settle has exactly one answer.
	stopWriter()

	// The retry half: a fresh event re-arms the dropped path, and the page
	// settles as one whole version rather than as the fragments the appender
	// left behind.
	fx.write("retry", rel, pageVersion(2))

	seen := fx.next()
	fx.mustBeWhole(seen)

	if version := seenVersion(t, seen.raw); version != 2 {
		t.Fatalf("settled version = %d, want the rewritten page (2)", version)
	}

	// Exactly one timeout for the whole episode. The retry settled, and a
	// settle reports no timeout, so a second line would mean the drop armed
	// something the retry did not clear.
	if got := strings.Count(
		fx.logs.String(),
		`"event":"`+string(observability.EventContentStableReadTimeout)+`"`,
	); got != 1 {
		t.Errorf("want exactly 1 stable-read timeout for the episode, got %d:\n%s",
			got, fx.logs.String())
	}

	fx.silent(wantSilence)
}
