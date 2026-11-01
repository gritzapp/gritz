package runner

import (
	"cmp"
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/x/common"
	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// RunnerEventOutboxOptions configures the runner's durable event outbox.
type RunnerEventOutboxOptions struct {
	// Store holds the queue of runner events. Required.
	Store *runnerstore.Store
	// Client delivers events to the server via SubmitRunnerEvents.
	Client gritzclient.Client
	// Backoff is the retry policy for transient delivery failures. It
	// defaults to a capped exponential policy when nil.
	Backoff backoff.BackOff
	// Log receives delivery diagnostics. Defaults to slog.Default when nil.
	Log *slog.Logger
}

// RunnerEventOutbox delivers runner lifecycle events (started/stopped/failed)
// to the server in order, at least once. Each event is stored before delivery
// and removed only after the server accepts it, so a crash between the two
// redelivers it on restart. Delivery is strict FIFO with head-of-line
// blocking.
type RunnerEventOutbox struct {
	store   *runnerstore.Store
	client  gritzclient.Client
	notify  wakeup.Chan
	backoff backoff.BackOff
	log     *slog.Logger
}

// NewRunnerEventOutbox returns a RunnerEventOutbox over opts.Store.
func NewRunnerEventOutbox(opts RunnerEventOutboxOptions) *RunnerEventOutbox {
	return &RunnerEventOutbox{
		store:   opts.Store,
		client:  opts.Client,
		notify:  wakeup.New(),
		backoff: cmp.Or[backoff.BackOff](opts.Backoff, backoff.NewExponentialBackOff()),
		log:     cmp.Or(opts.Log, slog.Default()),
	}
}

// Enqueue durably adds ev to the queue and wakes Run. It returns an error
// only if the event could not be stored.
func (o *RunnerEventOutbox) Enqueue(ev model.RunnerEvent) error {
	if err := o.store.AddRunnerEvent(context.Background(), ev); err != nil {
		return err
	}
	o.notify.Wake()
	return nil
}

// Len returns the number of undelivered events.
func (o *RunnerEventOutbox) Len() (int, error) {
	return o.store.CountRunnerEvents(context.Background())
}

// Run delivers queued events until ctx is cancelled. Its first pass delivers
// whatever is already stored, so events that survived a restart are
// redelivered without a separate recovery path.
func (o *RunnerEventOutbox) Run(ctx context.Context) {
	for {
		o.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-o.notify:
		}
	}
}

// drain delivers the oldest pending event until none is left or ctx is
// cancelled. On a transient failure it sleeps for the backoff interval and
// retries the same event; on a permanent one (isPermanentError) it
// dead-letters the event and moves on. A failed store call is logged and
// retried after the backoff. The backoff resets once the queue empties.
func (o *RunnerEventOutbox) drain(ctx context.Context) {
	for ctx.Err() == nil {
		seq, ev, ok, err := o.store.OldestRunnerEvent(ctx)
		if err != nil {
			o.log.Warn("runner event outbox read failed, will retry", "err", err)
			if !common.SleepContext(ctx, o.backoff.NextBackOff()) {
				return
			}
			continue
		}
		if !ok {
			o.backoff.Reset()
			return
		}
		_, err = o.client.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
			Events: []*gritzv1.RunnerEvent{ev.Proto()},
		})
		switch {
		case err == nil:
			o.log.Debug("runner event delivered", "seq", seq)
			if !o.settle(ctx, seq, false) {
				return
			}
		case isPermanentError(err):
			o.log.Warn("runner event dead-lettered due to permanent error", "seq", seq, "err", err)
			if !o.settle(ctx, seq, true) {
				return
			}
		default:
			o.log.Warn("runner event delivery failed, will retry", "seq", seq, "err", err)
			if !common.SleepContext(ctx, o.backoff.NextBackOff()) {
				return
			}
		}
	}
}

// settle deletes the event at seq, or dead-letters it when dead, retrying
// with backoff if the store fails, so a delivered event is not submitted
// again. It returns false if ctx was cancelled while retrying.
func (o *RunnerEventOutbox) settle(ctx context.Context, seq int64, dead bool) bool {
	for {
		var err error
		if dead {
			err = o.store.MarkRunnerEventDead(ctx, seq)
		} else {
			err = o.store.DeleteRunnerEvent(ctx, seq)
		}
		if err == nil {
			return true
		}
		o.log.Error("runner event outbox write failed, will retry", "seq", seq, "dead", dead, "err", err)
		if !common.SleepContext(ctx, o.backoff.NextBackOff()) {
			return false
		}
	}
}

// isPermanentError returns true if the error indicates a condition that will
// never succeed on retry (e.g. task not found, invalid argument). The outbox
// dead-letters permanent failures instead of retrying them.
func isPermanentError(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodePermissionDenied:
		return true
	default:
		return false
	}
}
