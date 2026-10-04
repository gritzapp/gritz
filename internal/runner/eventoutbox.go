package runner

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/outbox"
)

// RunnerEventOutboxOptions configures the runner's durable event outbox.
type RunnerEventOutboxOptions struct {
	// Store is the durable backing store. When nil, a FileStore is opened at
	// StoreDir. Tests pass an already-opened store directly; production supplies
	// StoreDir and lets the factory open it.
	Store outbox.Store
	// StoreDir is the directory for the FileStore opened when Store is nil. It
	// must be durable (under the runner's persistent state dir), not a temp dir,
	// so events survive a restart.
	StoreDir string
	// Client delivers events to the server via SubmitRunnerEvents.
	Client gritzclient.Client
	// Backoff is the retry policy for transient delivery failures.
	Backoff backoff.BackOff
	// Log receives delivery diagnostics.
	Log *slog.Logger
}

// NewRunnerEventOutbox builds the durable outbox that delivers runner lifecycle
// events (started/stopped/failed) to the server. It is the single source of
// truth for the Deliver closure — proto conversion + SubmitRunnerEvents, with
// permanence classified by isPermanentError so unrecoverable events are
// dead-lettered rather than retried forever — shared by the production wiring
// and the tests. When opts.Store is nil, it opens a durable FileStore at
// opts.StoreDir.
func NewRunnerEventOutbox(opts RunnerEventOutboxOptions) (*outbox.Outbox[model.RunnerEvent], error) {
	store := opts.Store
	if store == nil {
		fileStore, err := outbox.Open(opts.StoreDir)
		if err != nil {
			return nil, fmt.Errorf("failed to open outbox store: %w", err)
		}
		store = fileStore
	}
	return outbox.New(outbox.Options[model.RunnerEvent]{
		Store: store,
		Deliver: func(ctx context.Context, ev model.RunnerEvent) (permanent bool, err error) {
			_, err = opts.Client.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
				Events: []*gritzv1.RunnerEvent{ev.Proto()},
			})
			return isPermanentError(err), err
		},
		Backoff: opts.Backoff,
		Log:     opts.Log,
	}), nil
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
