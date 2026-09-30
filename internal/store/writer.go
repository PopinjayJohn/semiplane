package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

// ErrWriteFailed wraps any failure reported by the writer goroutine, so a
// caller can tell a rejected write from a lost one with errors.Is rather than
// by inspecting a message.
var ErrWriteFailed = errors.New("store: write failed")

// writeFunc performs one write. It receives the context that was in force when
// the write was submitted.
//
// The context matters: a write that is abandoned because its request was
// cancelled should not run later against a connection whose state nobody is
// waiting for.
type writeFunc func(ctx context.Context, tx *sql.Tx) error

// writer serialises writes through one goroutine.
//
// Not a mutex and not a pool. A mutex would hold the lock across a caller's
// transaction, which serialises correctly but blocks the requesting goroutine
// for the duration of somebody else's fsync. A queue lets the writer own the
// connection outright: exactly one statement is ever in flight, SQLite's
// single-writer limit is never contended, and `busy_timeout` becomes a
// belt-and-braces setting rather than the mechanism.
//
// The queue is unbounded. That is a deliberate choice and its failure mode is
// stated here because it is the kind of thing that gets rediscovered as an
// incident: if producers outrun the disk, memory grows until the process is
// killed. Bounding it would mean choosing which writes to drop, and dropping a
// campaign_state flush or a revision row silently is worse than an OOM kill,
// which at least leaves the vault — the source of truth — untouched.
type writer struct {
	queue chan writeRequest
	done  chan struct{}

	// closing is closed by stop and never the queue itself. Closing the queue
	// would make a concurrent send panic, and a send is always possible: a
	// caller can pass the stopped check and be descheduled before its select
	// runs. A separate signal channel is the standard answer, because the
	// sender selects on it.
	closing chan struct{}

	// closeOnce guards stop. Shutdown racing a test's cleanup is the normal
	// case, not the exceptional one.
	closeOnce sync.Once
	stopped   bool
	stopMu    sync.Mutex
}

// The struct carries a context because the alternative is worse. The queue is
// the only place a caller's context can live between submission and execution:
// storing it on a wrapper the caller holds would be a context that outlives the
// request, and dropping it would let a write that was cancelled still commit.
//
//nolint:containedctx // A queued write is the request's work; its deadline is part of the work.
type writeRequest struct {
	ctx  context.Context
	fn   writeFunc
	done chan error
}

func newWriter(db *sql.DB) *writer {
	w := &writer{
		queue:   make(chan writeRequest),
		done:    make(chan struct{}),
		closing: make(chan struct{}),
	}

	go w.run(db)

	return w
}

// run is the single writer loop. It owns the connection for the process's
// lifetime.
func (w *writer) run(db *sql.DB) {
	defer close(w.done)

	for {
		select {
		case req := <-w.queue:
			w.execute(db, req)

		case <-w.closing:
			// Drain what is already queued and refuse anything new. Draining
			// rather than abandoning is what makes shutdown correct: a write
			// submitted a millisecond before the signal was already accepted,
			// and dropping it would lose a campaign_state flush the process
			// had promised to persist.
			w.drain(db)

			return
		}
	}
}

// drain executes every queued write and then refuses the channel, so a sender
// blocked in write observes `closing` instead of blocking forever.
func (w *writer) drain(db *sql.DB) {
	for {
		select {
		case req := <-w.queue:
			w.execute(db, req)
		default:
			return
		}
	}
}

func (w *writer) execute(db *sql.DB, req writeRequest) {
	defer close(req.done)

	if err := req.ctx.Err(); err != nil {
		// The caller gave up before the write started. Running it anyway would
		// commit a change nobody is waiting to hear about — for a
		// campaign_state flush that means a resurrected row after a cancel.
		req.done <- fmt.Errorf("%w: %w", ErrWriteFailed, err)

		return
	}

	tx, err := db.BeginTx(req.ctx, nil)
	if err != nil {
		req.done <- fmt.Errorf("%w: begin: %w", ErrWriteFailed, err)

		return
	}

	if err := req.fn(req.ctx, tx); err != nil {
		// Rollback on a throwing-away transaction. A commit failure leaves the
		// transaction open, and an open transaction holds the write lock until
		// the connection is closed — which would wedge the writer for the rest
		// of the process's life.
		rollback(tx)

		req.done <- fmt.Errorf("%w: %w", ErrWriteFailed, err)

		return
	}

	if err := tx.Commit(); err != nil {
		rollback(tx)

		req.done <- fmt.Errorf("%w: commit: %w", ErrWriteFailed, err)

		return
	}

	req.done <- nil
}

// rollback abandons a transaction whose outcome is already decided.
//
// The error is dropped, and that is the whole argument for the helper existing:
// every caller has a real error to report instead, and a rollback failure cannot
// change the outcome. It is not invisible either — a failed rollback leaves the
// connection unusable, and the next statement on it surfaces that.
func rollback(tx *sql.Tx) {
	//nolint:errcheck // The transaction is being discarded; its error is already reported.
	_ = tx.Rollback()
}

// write submits fn to the writer and waits for it.
//
// A context cancelled while the write is queued is honoured here rather than
// inside the writer: the writer may still run the statement, and this returns
// as soon as the caller stops caring. The write is only abandoned outright if it
// has not started when the writer picks it up.
func (w *writer) write(ctx context.Context, fn writeFunc) error {
	req := writeRequest{ctx: ctx, fn: fn, done: make(chan error, 1)}

	w.stopMu.Lock()
	if w.stopped {
		w.stopMu.Unlock()

		return fmt.Errorf("%w: store is closed", ErrWriteFailed)
	}
	w.stopMu.Unlock()

	// Select rather than a bare send: without this, a caller that abandons its
	// context leaves this goroutine blocked forever on a queue nobody is
	// draining, and a store that has stopped draining is exactly the situation
	// a timeout must not turn into a leaked goroutine.
	select {
	case w.queue <- req:
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrWriteFailed, ctx.Err())
	case <-w.closing:
		return fmt.Errorf("%w: store is closed", ErrWriteFailed)
	}

	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrWriteFailed, ctx.Err())
	}
}

// stop drains the queue, waits for the writer goroutine to exit, and refuses
// further writes. Safe to call more than once.
func (w *writer) stop() {
	w.closeOnce.Do(func() {
		w.stopMu.Lock()
		w.stopped = true
		w.stopMu.Unlock()

		close(w.closing)
		<-w.done
	})
}

// Write runs fn inside a transaction on the writer goroutine, and returns
// after the transaction commits.
//
// This is the only way to write. Reaching for s.DB().ExecContext bypasses the
// queue, which is precisely the thing the queue exists to prevent: a write that
// contends for SQLite's lock and returns SQLITE_BUSY instead of waiting its
// turn.
func (s *Store) Write(ctx context.Context, fn writeFunc) error {
	return s.writer.write(ctx, fn)
}
