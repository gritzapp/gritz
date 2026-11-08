package command

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/docker"
	"github.com/gritzapp/gritz/internal/runner/backend/lambdamicrovm"
	"github.com/gritzapp/gritz/internal/runner/backend/local"
	"github.com/gritzapp/gritz/internal/runner/workspace"
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
	Flags: slices.Concat(configfile.Flags(), []cli.Flag{
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
			Usage:   "Sandbox backend (docker, lambda-microvm, local)",
			Value:   docker.HandleType,
			Sources: cli.EnvVars("GRITZ_BACKEND"),
		},
		&cli.StringFlag{
			Name:    "state-db",
			Usage:   "SQLite database holding the runner's state",
			Value:   "/var/lib/gritz/runner.db",
			Sources: cli.EnvVars("GRITZ_STATE_DB"),
		},
		&cli.StringFlag{
			Name:    "lambda-microvm-region",
			Usage:   "AWS region for the lambda-microvm backend (defaults to the SDK-resolved region)",
			Sources: cli.EnvVars("GRITZ_LAMBDA_MICROVM_REGION"),
		},
		&cli.StringFlag{
			Name:    "local-root",
			Usage:   "Directory holding the local backend's sandboxes (the local backend runs agents on this host, unisolated)",
			Sources: cli.EnvVars("GRITZ_LOCAL_ROOT"),
		},
		&cli.BoolFlag{
			Name:  "debug",
			Usage: "Enable debug logging",
			Value: false,
		},
	}),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		configPath := cmd.String("workspaces")
		pollInterval := cmd.Duration("poll")
		concurrency := cmd.Int("concurrency")
		runnerID := cmd.String("id")
		debug := cmd.Bool("debug")

		// Create logger if debug is enabled
		log := slog.Default()
		if debug {
			opts := &slog.HandlerOptions{
				Level: slog.LevelDebug,
			}
			handler := slog.NewTextHandler(os.Stderr, opts)
			log = slog.New(handler)
		}

		cfg, err := configfile.Resolve(cmd)
		if err != nil {
			return err
		}
		// The sandboxes are handed the URL too, so it is resolved here
		// rather than left to the client's default.
		serverAddr := cmp.Or(cfg.Server, gritzclient.DefaultURL)

		workspaces, err := workspace.LoadConfig(configPath, nil)
		if err != nil {
			return fmt.Errorf("failed to load workspace config: %w", err)
		}

		client := gritzclient.New(gritzclient.Options{BaseURL: serverAddr, Token: cfg.Key})

		backendName := cmd.String("backend")
		var be backend.Backend
		switch backendName {
		case docker.HandleType:
			be, err = docker.New(docker.Options{
				RunnerID: runnerID,
				Log:      log,
			})
			if err != nil {
				return err
			}
		case lambdamicrovm.HandleType:
			loadOpts := []func(*config.LoadOptions) error{}
			if region := cmd.String("lambda-microvm-region"); region != "" {
				loadOpts = append(loadOpts, config.WithRegion(region))
			}
			awsCfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
			if err != nil {
				return fmt.Errorf("lambda-microvm backend: %w", err)
			}
			be, err = lambdamicrovm.New(lambdamicrovm.Options{
				Cloud:  lambdamicrovms.NewFromConfig(awsCfg),
				Region: awsCfg.Region,
				Log:    log,
			})
			if err != nil {
				return err
			}
		case local.HandleType:
			root := cmd.String("local-root")
			if root == "" {
				return errors.New("local backend: --local-root is required")
			}
			binary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("local backend: %w", err)
			}
			be, err = local.New(local.Options{
				Root:    root,
				Binary:  binary,
				Environ: os.Environ(),
				Log:     log,
			})
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown backend: %q", backendName)
		}

		log.Info("runner started", "server", serverAddr, "config", configPath, "poll", pollInterval, "concurrency", concurrency, "backend", backendName)

		return runner.Serve(ctx, runner.ServeOptions{
			Client:       client,
			ServerURL:    serverAddr,
			Token:        cfg.Key,
			Backend:      be,
			StateDB:      cmd.String("state-db"),
			Workspaces:   workspaces,
			RunnerID:     runnerID,
			Concurrency:  int(concurrency),
			PollInterval: pollInterval,
			Log:          log,
		})
	},
}
