package outbox

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cenkalti/backoff/v5"

	"github.com/gritzapp/gritz/internal/x/common"
	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// Options configures an Outbox.
type Options[T any] struct {
	// Store is the durable backing store. Required.
	Store Store
	// Deliver performs the outbound call for one message. It reports whether a
	// failure is permanent as its first return value: a permanent failure
	// (permanent == true, err != nil) dead-letters the message, a transient one
	// (permanent == false, err != nil) is retried after a backoff. Required.
	Deliver func(ctx context.Context, msg T) (permanent bool, err error)
	// Backoff is the retry policy for transient failures. It defaults to a
	// capped exponential policy when nil. NewConstantBackOff reproduces a fixed
	// interval.
	Backoff backoff.BackOff
	// Log receives delivery diagnostics. Defaults to slog.Default when nil.
	Log *slog.Logger
}

// Outbox is a durable, at-least-once outbox generic over a JSON-serializable
// payload type T. Messages are persisted before delivery and removed only after
// delivery succeeds, so a crash between the two simply redelivers the head on
// restart. Delivery is strict FIFO with head-of-line blocking.
type Outbox[T any] struct {
	store   Store
	notify  wakeup.Chan
	deliver func(ctx context.Context, msg T) (permanent bool, err error)
	backoff backoff.BackOff
	log     *slog.Logger
}

// New constructs an Outbox from opts.
func New[T any](opts Options[T]) *Outbox[T] {
	return &Outbox[T]{
		store:   opts.Store,
		notify:  wakeup.New(),
		deliver: opts.Deliver,
		backoff: cmp.Or[backoff.BackOff](opts.Backoff, backoff.NewExponentialBackOff()),
		log:     cmp.Or(opts.Log, slog.Default()),
	}
}

// Enqueue durably persists msg, then wakes Run. It returns an error only if the
// message could not be persisted.
func (o *Outbox[T]) Enqueue(msg T) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := o.store.Append(payload); err != nil {
		return err
	}
	o.notify.Wake()
	return nil
}

// Len reports the number of undelivered messages.
func (o *Outbox[T]) Len() (int, error) {
	return o.store.Len()
}

// Run delivers persisted messages until ctx is cancelled. Its first pass drains
// whatever is already in the store, so records that survived a restart are
// redelivered without a separate recovery path.
func (o *Outbox[T]) Run(ctx context.Context) {
	for {
		o.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-o.notify:
		}
	}
}

// drain delivers from the head until the store is empty or ctx is cancelled. On
// a transient failure it sleeps for the backoff interval and retries the same
// head (head-of-line blocking); on a permanent failure it dead-letters the head
// and advances. The backoff resets once the store fully drains.
func (o *Outbox[T]) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		rec, ok, err := o.store.Peek()
		if err != nil {
			o.log.Warn("outbox peek failed, will retry", "err", err)
			if !common.SleepContext(ctx, o.backoff.NextBackOff()) {
				return
			}
			continue
		}
		if !ok {
			o.backoff.Reset()
			return
		}
		var msg T
		if err := json.Unmarshal(rec.Payload, &msg); err != nil {
			// An undecodable payload can never be delivered; dead-letter it so
			// it does not block the queue forever.
			o.log.Error("outbox payload decode failed, dead-lettering", "seq", rec.Seq, "err", err)
			if !o.drop(ctx, rec.Seq, true) {
				return
			}
			continue
		}
		permanent, err := o.deliver(ctx, msg)
		switch {
		case err == nil:
			o.log.Debug("outbox message delivered", "seq", rec.Seq)
			if !o.drop(ctx, rec.Seq, false) {
				return
			}
		case permanent:
			o.log.Warn("outbox message dead-lettered due to permanent error", "seq", rec.Seq, "err", err)
			if !o.drop(ctx, rec.Seq, true) {
				return
			}
		default:
			o.log.Warn("outbox delivery failed, will retry", "seq", rec.Seq, "err", err)
			if !common.SleepContext(ctx, o.backoff.NextBackOff()) {
				return
			}
		}
	}
}

// drop removes the head, retrying with backoff if the store errors. It returns
// false if ctx was cancelled while retrying.
func (o *Outbox[T]) drop(ctx context.Context, seq uint64, dead bool) bool {
	for {
		if err := o.store.Drop(dead); err != nil {
			o.log.Error("outbox drop failed, will retry", "seq", seq, "err", err)
			if !common.SleepContext(ctx, o.backoff.NextBackOff()) {
				return false
			}
			continue
		}
		return true
	}
}
