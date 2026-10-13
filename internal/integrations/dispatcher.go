package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/store"
)

// Options tunes a Dispatcher. Zero values take the defaults noted.
type Options struct {
	// PollInterval is how often the event log is tailed. Default 1s, the same
	// cadence the SSE endpoint polls the same log at.
	PollInterval time.Duration
	// MaxAttempts bounds retries before a row is dead-lettered. Default 8,
	// which with the default backoff spans roughly ten minutes -- long enough
	// to ride out a Slack incident or a laptop waking from sleep, short
	// enough that a revoked token surfaces in `idea integrations status`
	// instead of retrying for a day.
	MaxAttempts int
	// BaseBackoff doubles per attempt up to MaxBackoff. Defaults 2s and 5m.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// DeliveryTimeout bounds one Deliver call. Default 30s.
	DeliveryTimeout time.Duration
	// Now is the clock (tests).
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 8
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = 2 * time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 5 * time.Minute
	}
	if o.DeliveryTimeout <= 0 {
		o.DeliveryTimeout = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// maxRetryAfter caps a server-supplied Retry-After hint, so a hostile or
// confused server cannot park a subscriber for a week.
const maxRetryAfter = time.Hour

// Dispatcher tails one project's event log and delivers typed events to its
// subscribers through the persistent outbox.
//
// Per subscriber, in order:
//  1. ingest: read events past the subscriber's cursor, Translate, filter by
//     Wants, and insert outbox rows + advance the cursor in ONE transaction.
//  2. deliver: send the oldest pending row; on success mark it delivered, on
//     failure schedule a retry with exponential backoff, or dead-letter it.
//
// Delivery is strictly in event order per subscriber -- a pending head row in
// backoff holds back the rows behind it -- because order is part of the
// meaning (a Slack thread reply must not land before the message it replies
// to; a Linear issue must exist before its state can be synced). Dead rows
// release the queue. Different subscribers never block each other.
type Dispatcher struct {
	db        *store.DB
	projectID string
	subs      []Subscriber
	log       *slog.Logger
	opts      Options
	wake      chan struct{}

	mu        sync.Mutex
	ingestErr map[string]string
}

// NewDispatcher builds a dispatcher. It does nothing until Run.
func NewDispatcher(db *store.DB, projectID string, subs []Subscriber, log *slog.Logger, opts Options) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		db: db, projectID: projectID, subs: subs, log: log,
		opts: opts.withDefaults(), wake: make(chan struct{}, 1), ingestErr: map[string]string{},
	}
}

// Subscribers returns the dispatcher's subscribers.
func (d *Dispatcher) Subscribers() []Subscriber { return d.subs }

// Run tails and delivers until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	if err := d.Init(ctx); err != nil {
		d.log.Warn("integrations: initializing cursors", "project", d.projectID, "err", err)
	}
	t := time.NewTicker(d.opts.PollInterval)
	defer t.Stop()
	for {
		d.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.wake:
		}
	}
}

// Wake asks Run for an immediate tick. Never blocks.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Init gives every subscriber seen for the first time a cursor at the
// current end of the log. Turning Slack on for a project with a year of
// history must not post a year of history; a subscriber starts with the
// first event after it was configured. Subscribers that already have a
// cursor -- every one after a restart -- are left where they were, which is
// what makes a restart resume rather than skip or replay.
func (d *Dispatcher) Init(ctx context.Context) error {
	for _, s := range d.subs {
		if _, ok, err := d.db.IntegrationCursor(ctx, s.Name()); err != nil {
			return err
		} else if ok {
			continue
		}
		max, err := d.db.MaxEventSeq(ctx)
		if err != nil {
			return err
		}
		if err := d.db.IngestOutbox(ctx, s.Name(), max, nil); err != nil {
			return err
		}
	}
	return nil
}

// Tick runs one ingest+deliver pass for every subscriber.
func (d *Dispatcher) Tick(ctx context.Context) {
	for _, s := range d.subs {
		if ctx.Err() != nil {
			return
		}
		d.setIngestErr(s.Name(), d.ingest(ctx, s))
		d.deliver(ctx, s)
	}
}

// IngestError returns the most recent ingest failure for a subscriber ("" if
// the last ingest succeeded).
func (d *Dispatcher) IngestError(subscriber string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ingestErr[subscriber]
}

func (d *Dispatcher) setIngestErr(sub string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		delete(d.ingestErr, sub)
		return
	}
	d.ingestErr[sub] = err.Error()
	d.log.Warn("integrations: ingest failed; retrying next tick", "project", d.projectID, "subscriber", sub, "err", err)
}

const ingestPage = 200

func (d *Dispatcher) ingest(ctx context.Context, s Subscriber) error {
	for {
		cursor, _, err := d.db.IntegrationCursor(ctx, s.Name())
		if err != nil {
			return err
		}
		rows, err := d.db.ListEvents(ctx, cursor, ingestPage)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var inserts []store.OutboxInsert
		last := cursor
		for _, row := range rows {
			evs, err := Translate(ctx, d.db, d.projectID, row)
			if err != nil {
				// Commit what translated so far; this row is retried next
				// tick rather than skipped.
				if last > cursor {
					if ierr := d.db.IngestOutbox(ctx, s.Name(), last, inserts); ierr != nil {
						return ierr
					}
				}
				return fmt.Errorf("translate event %d (%s): %w", row.Seq, row.EventType, err)
			}
			for _, ev := range evs {
				if !s.Wants(ev) {
					continue
				}
				payload, err := json.Marshal(ev)
				if err != nil {
					return err
				}
				inserts = append(inserts, store.OutboxInsert{
					Subscriber: s.Name(), IdempotencyKey: ev.IdempotencyKey(s.Name()),
					EventSeq: ev.Seq, Kind: string(ev.Kind), PayloadJSON: string(payload),
				})
			}
			last = row.Seq
		}
		if err := d.db.IngestOutbox(ctx, s.Name(), last, inserts); err != nil {
			return err
		}
		if len(rows) < ingestPage {
			return nil
		}
	}
}

// maxDeliveriesPerTick bounds one subscriber's share of a tick, so a large
// backlog drains steadily without starving the other subscriber.
const maxDeliveriesPerTick = 50

func (d *Dispatcher) deliver(ctx context.Context, s Subscriber) {
	for i := 0; i < maxDeliveriesPerTick; i++ {
		if ctx.Err() != nil {
			return
		}
		row, err := d.db.NextOutbox(ctx, s.Name())
		if err != nil {
			d.log.Warn("integrations: reading outbox", "project", d.projectID, "subscriber", s.Name(), "err", err)
			return
		}
		if row == nil || row.NextAttemptAt.After(d.opts.Now()) {
			return
		}
		var ev Event
		if err := json.Unmarshal([]byte(row.PayloadJSON), &ev); err != nil {
			d.fail(ctx, s, row, Permanent(fmt.Errorf("corrupt outbox payload: %w", err)))
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, d.opts.DeliveryTimeout)
		err = safeDeliver(dctx, s, ev)
		cancel()
		if err == nil {
			if err := d.db.MarkOutboxDelivered(ctx, row.ID); err != nil {
				d.log.Warn("integrations: recording delivery", "project", d.projectID, "subscriber", s.Name(), "err", err)
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return // shutting down: not the remote's fault, do not burn an attempt
		}
		if d.fail(ctx, s, row, err) {
			continue // dead-lettered: the queue moves on
		}
		return // in backoff: hold the rows behind it
	}
}

// safeDeliver turns a subscriber panic into an error: an integration bug must
// cost one delivery, never the daemon.
func safeDeliver(ctx context.Context, s Subscriber, ev Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = Permanent(fmt.Errorf("subscriber panicked: %v", r))
		}
	}()
	return s.Deliver(ctx, ev)
}

// fail records a failed attempt and reports whether the row is now dead.
func (d *Dispatcher) fail(ctx context.Context, s Subscriber, row *store.OutboxRow, err error) bool {
	attempts := row.Attempts + 1
	dead := IsPermanent(err) || attempts >= d.opts.MaxAttempts
	next := d.opts.Now().Add(d.backoff(attempts, err))
	if dead {
		d.log.Error("integrations: delivery failed permanently; dead-lettered",
			"project", d.projectID, "subscriber", s.Name(), "kind", row.Kind, "attempts", attempts, "err", err)
	} else {
		d.log.Warn("integrations: delivery failed; will retry",
			"project", d.projectID, "subscriber", s.Name(), "kind", row.Kind, "attempts", attempts, "retry_at", next, "err", err)
	}
	if merr := d.db.MarkOutboxFailed(ctx, row.ID, err.Error(), next, dead); merr != nil {
		d.log.Warn("integrations: recording failure", "project", d.projectID, "subscriber", s.Name(), "err", merr)
	}
	return dead
}

func (d *Dispatcher) backoff(attempts int, err error) time.Duration {
	if hint, ok := RetryAfter(err); ok {
		return min(hint, maxRetryAfter)
	}
	b := d.opts.BaseBackoff
	for i := 1; i < attempts && b < d.opts.MaxBackoff; i++ {
		b *= 2
	}
	return min(b, d.opts.MaxBackoff)
}
