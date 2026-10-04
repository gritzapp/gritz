package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/cenkalti/backoff/v5"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/backend"
	dockerbackend "github.com/gritzapp/gritz/internal/runner/backend/docker"
	"github.com/gritzapp/gritz/internal/runner/backend/lambdamicrovm"
	"github.com/gritzapp/gritz/internal/runner/backend/lambdamicrovm/awsmvm"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
	"github.com/gritzapp/gritz/internal/x/common"
	"github.com/urfave/cli/v3"
)

func defaultWorkspacesPath() string {
	path, err := workspace.DefaultPath()
	if err != nil {
		return "workspaces.yaml"
	}
	return path
}

func defaultRunnerID() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "default"
	}
	return hostname
}

var RunnerCommand = &cli.Command{
	Name:  "runner",
	Usage: "Start the runner (monitors tasks, manages containers)",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
		&cli.StringFlag{
			Name:    "workspaces",
			Aliases: []string{"w"},
			Usage:   "Workspace config file",
			Value:   defaultWorkspacesPath(),
		},
		&cli.DurationFlag{
			Name:  "poll",
			Usage: "Fallback poll interval when SSE wake-ups are unavailable",
			Value: 30 * time.Second,
		},
		&cli.IntFlag{
			Name:  "concurrency",
			Usage: "Maximum number of concurrent tasks (0 for unlimited)",
			Value: 5,
		},
		&cli.StringFlag{
			Name:    "id",
			Usage:   "Unique identifier for this runner (no spaces or special characters)",
			Value:   defaultRunnerID(),
			Sources: cli.EnvVars("GRITZ_RUNNER_ID"),
		},
		&cli.StringFlag{
			Name:    "backend",
			Usage:   "Sandbox backend (docker, lambda-microvm)",
			Value:   "docker",
			Sources: cli.EnvVars("GRITZ_BACKEND"),
		},
		&cli.StringFlag{
			Name:    "state-dir",
			Usage:   "Directory for the runner-local task→sandbox-handle store",
			Value:   "/var/lib/gritz/tasks",
			Sources: cli.EnvVars("GRITZ_STATE_DIR"),
		},
		&cli.StringFlag{
			Name:    "lambda-microvm-region",
			Usage:   "AWS region for the lambda-microvm backend (defaults to the SDK-resolved region)",
			Sources: cli.EnvVars("GRITZ_LAMBDA_MICROVM_REGION"),
		},
		&cli.BoolFlag{
			Name:  "debug",
			Usage: "Enable debug logging",
			Value: false,
		},
		&cli.StringFlag{
			Name:    "key",
			Aliases: []string{"k"},
			Usage:   "API key (takes priority over config file)",
			Sources: cli.EnvVars("GRITZ_API_KEY"),
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		serverAddr := cmd.String("server")
		configPath := cmd.String("workspaces")
		pollInterval := cmd.Duration("poll")
		concurrency := cmd.Int("concurrency")
		runnerID := cmd.String("id")
		debug := cmd.Bool("debug")

		// Wrap the incoming ctx so a durable local-store write failure can
		// terminate the runner by cancelling it with a FatalError cause. This
		// must happen before anything that takes ctx is spawned (Load's supervise
		// goroutines, queue.Run, the supervise/autoprune/notify goroutines, and the
		// Poll loop) so they all observe the cancellation.
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)

		// Create logger if debug is enabled
		log := slog.Default()
		if debug {
			opts := &slog.HandlerOptions{
				Level: slog.LevelDebug,
			}
			handler := slog.NewTextHandler(os.Stderr, opts)
			log = slog.New(handler)
		}

		cfg, err := configfile.Load(&configfile.Overrides{
			Token: cmd.String("key"),
		})
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if cfg.Token == "" {
			return fmt.Errorf("not authenticated, run setup first or provide -key flag")
		}

		workspaces, err := workspace.LoadConfig(configPath, nil)
		if err != nil {
			return fmt.Errorf("failed to load workspace config: %w", err)
		}

		client := gritzclient.New(gritzclient.Options{
			BaseURL: serverAddr,
			Token:   cfg.Token,
		})

		// The outbox is durable: it lives under the runner's persistent state
		// directory (a sibling of the taskstate dir, on the same volume), so
		// runner lifecycle events survive a restart and are redelivered on the
		// next Run pass rather than being lost with an in-memory buffer.
		queue, err := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
			StoreDir: filepath.Join(filepath.Dir(cmd.String("state-dir")), "outbox"),
			Client:   client,
			// Reproduce the old EventQueue's fixed retry interval (the poll
			// interval) with a constant backoff, for a drop-in match.
			Backoff: backoff.NewConstantBackOff(pollInterval),
			Log:     log,
		})
		if err != nil {
			return fmt.Errorf("failed to create runner event outbox: %w", err)
		}

		backendName := cmd.String("backend")
		var be backend.Backend
		switch backendName {
		case "docker":
			be, err = dockerbackend.New(dockerbackend.Options{
				RunnerID: runnerID,
				Log:      log,
			})
			if err != nil {
				return err
			}
		case "lambda-microvm":
			loadOpts := []func(*config.LoadOptions) error{}
			if region := cmd.String("lambda-microvm-region"); region != "" {
				loadOpts = append(loadOpts, config.WithRegion(region))
			}
			awsCfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
			if err != nil {
				return fmt.Errorf("lambda-microvm backend: %w", err)
			}
			be, err = lambdamicrovm.New(lambdamicrovm.Options{
				Cloud:    awsmicrovm.NewClient(awsCfg),
				Stager:   awsmvm.NewS3Stager(awsCfg),
				RunnerID: runnerID,
				Region:   awsCfg.Region,
				Log:      log,
			})
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown backend: %q", backendName)
		}

		// Open the runner-local task→sandbox-handle store, the single source of
		// truth for which sandbox belongs to which task.
		store, err := taskstate.Open(cmd.String("state-dir"))
		if err != nil {
			return fmt.Errorf("failed to open task store: %w", err)
		}

		r, err := runner.New(runner.Options{
			Client:      client,
			Backend:     be,
			Store:       store,
			ServerURL:   serverAddr,
			Workspaces:  workspaces,
			Concurrency: int(concurrency),
			RunnerID:    runnerID,
			Log:         log,
			Queue:       queue,
			Fatal:       cancel,
		})
		if err != nil {
			return err
		}
		defer r.Close()

		log.Info("runner started", "server", serverAddr, "config", configPath, "poll", pollInterval, "concurrency", concurrency, "backend", cmd.String("backend"))

		// Register workspaces with the server (non-fatal if it fails)
		if err := r.RegisterWorkspaces(ctx); err != nil {
			log.Warn("failed to register workspaces", "err", err)
		}

		// Rehydrate sandboxes that were running when the runner was stopped: this
		// re-attaches a supervise goroutine per running sandbox and seeds the
		// concurrency semaphore. It runs once, before Poll admits new work.
		if err := r.Load(ctx); err != nil {
			return fmt.Errorf("failed to load sandboxes: %w", err)
		}

		// Start the outbox delivery goroutine. Its first pass redelivers any
		// events that were persisted but not yet acknowledged before a restart.
		go queue.Run(ctx)

		// Start autoprune goroutine
		go func() {
			for common.SleepContext(ctx, pollInterval) {
				if err := r.Prune(ctx); err != nil {
					log.Error("failed to prune containers", "err", err)
				}
			}
		}()

		// Subscribe to server-sent task change notifications so the runner
		// reacts to new commands immediately instead of waiting for the
		// fallback poll.
		go func() {
			nc := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
				BaseURL: serverAddr,
				Runner:  runnerID,
				Token:   cfg.Token,
				Log:     log,
				Handler: func(model.Notification) { r.Wake() },
			})
			if err := nc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("notification client stopped", "err", err)
			}
		}()

		for {
			if err := r.Poll(ctx); err != nil {
				log.Error("failed to poll tasks", "err", err)
			}
			select {
			case <-r.WakeC():
			case <-time.After(pollInterval):
			case <-ctx.Done():
				// Distinguish a durable-store-write crash (die set a
				// FatalError cause) from a graceful signal shutdown. The
				// sentinel is what lets us return non-zero on a broken disk
				// without depending on whatever cause signal.NotifyContext sets.
				var fatal runner.FatalError
				if cause := context.Cause(ctx); errors.As(cause, &fatal) {
					log.Error("runner terminating: durable store write failed", "err", cause)
					return cause // non-zero exit → supervisor restarts + alerts
				}
				return nil // graceful signal shutdown → exit 0
			}
		}
	},
}
