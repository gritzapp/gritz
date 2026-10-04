package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/auth/oauthflow"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server"
	"github.com/gritzapp/gritz/internal/server/archiver"
	"github.com/gritzapp/gritz/internal/server/atlassianserver"
	"github.com/gritzapp/gritz/internal/server/githubserver"
	"github.com/gritzapp/gritz/internal/server/notifyserver"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/server/scheduler"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/x/logctx"
	"github.com/gritzapp/gritz/internal/x/otelx"
	"github.com/urfave/cli/v3"
)

var ServerCommand = &cli.Command{
	Name:  "server",
	Usage: "Start the gritz server",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "addr",
			Aliases: []string{"a"},
			Usage:   "Address to listen on",
			Value:   ":6464",
		},
		&cli.StringFlag{
			Name:    "db",
			Aliases: []string{"d"},
			Usage:   "PostgreSQL connection string",
			Sources: cli.EnvVars("GRITZ_DATABASE_URL"),
		},
		&cli.StringFlag{
			Name:    "auth-domain",
			Usage:   "ZITADEL domain (e.g. instance.zitadel.cloud)",
			Sources: cli.EnvVars("GRITZ_AUTH_DOMAIN"),
		},
		&cli.StringFlag{
			Name:    "auth-client-id",
			Usage:   "ZITADEL client ID",
			Sources: cli.EnvVars("GRITZ_AUTH_CLIENT_ID"),
		},
		&cli.StringFlag{
			Name:    "auth-client-secret",
			Usage:   "ZITADEL client secret",
			Sources: cli.EnvVars("GRITZ_AUTH_CLIENT_SECRET"),
		},
		&cli.StringFlag{
			Name:    "base-url",
			Usage:   "Base URL for the server (e.g. https://gritz.example.com)",
			Sources: cli.EnvVars("GRITZ_BASE_URL"),
		},
		&cli.StringFlag{
			Name:    "auth-encryption-key",
			Usage:   "Hex-encoded 32-byte key for session encryption (generated if not set)",
			Sources: cli.EnvVars("GRITZ_AUTH_ENCRYPTION_KEY"),
		},
		&cli.StringFlag{
			Name:    "auth-app-key",
			Usage:   "Hex-encoded 32-byte Ed25519 seed for signing app JWTs (generated if not set)",
			Sources: cli.EnvVars("GRITZ_AUTH_APP_KEY"),
		},
		&cli.BoolFlag{
			Name:  "no-auth",
			Usage: "Disable authentication (for development only)",
		},
		&cli.BoolFlag{
			Name:    "cors",
			Usage:   "Enable permissive CORS headers (for development only)",
			Sources: cli.EnvVars("GRITZ_CORS"),
		},
		&cli.StringFlag{
			Name:    "github-app-id",
			Usage:   "GitHub App ID",
			Sources: cli.EnvVars("GRITZ_GITHUB_APP_ID"),
		},
		&cli.StringFlag{
			Name:    "github-app-slug",
			Usage:   "GitHub App slug (for install URL)",
			Sources: cli.EnvVars("GRITZ_GITHUB_APP_SLUG"),
		},
		&cli.StringFlag{
			Name:    "github-client-id",
			Usage:   "GitHub App OAuth client ID",
			Sources: cli.EnvVars("GRITZ_GITHUB_CLIENT_ID"),
		},
		&cli.StringFlag{
			Name:    "github-client-secret",
			Usage:   "GitHub App OAuth client secret",
			Sources: cli.EnvVars("GRITZ_GITHUB_CLIENT_SECRET"),
		},
		&cli.StringFlag{
			Name:    "github-webhook-secret",
			Usage:   "GitHub App webhook secret",
			Sources: cli.EnvVars("GRITZ_GITHUB_WEBHOOK_SECRET"),
		},
		&cli.StringFlag{
			Name:    "github-private-key",
			Usage:   "GitHub App private key (PEM content)",
			Sources: cli.EnvVars("GRITZ_GITHUB_APP_PRIVATE_KEY"),
		},
		&cli.StringFlag{
			Name:    "atlassian-client-id",
			Usage:   "Atlassian OAuth client ID (for account linking)",
			Sources: cli.EnvVars("GRITZ_ATLASSIAN_CLIENT_ID"),
		},
		&cli.StringFlag{
			Name:    "atlassian-client-secret",
			Usage:   "Atlassian OAuth client secret",
			Sources: cli.EnvVars("GRITZ_ATLASSIAN_CLIENT_SECRET"),
		},
		&cli.DurationFlag{
			Name:    "archive-poll",
			Usage:   "How often to scan for tasks past their auto-archive deadline. 0 (default) disables the archiver.",
			Sources: cli.EnvVars("GRITZ_ARCHIVE_POLL"),
		},
		&cli.IntFlag{
			Name:    "archive-batch",
			Usage:   "Maximum number of tasks the archiver will archive per tick",
			Value:   archiver.DefaultBatchSize,
			Sources: cli.EnvVars("GRITZ_ARCHIVE_BATCH"),
		},
		&cli.DurationFlag{
			Name:    "schedule-poll",
			Usage:   "How often to scan for due schedules and fire them into tasks. 0 disables the scheduler.",
			Value:   scheduler.DefaultInterval,
			Sources: cli.EnvVars("GRITZ_SCHEDULE_POLL"),
		},
		&cli.IntFlag{
			Name:    "schedule-batch",
			Usage:   "Maximum number of schedules the scheduler will fire per tick",
			Value:   scheduler.DefaultBatchSize,
			Sources: cli.EnvVars("GRITZ_SCHEDULE_BATCH"),
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		addr := cmd.String("addr")
		dbPath := cmd.String("db")
		noAuth := cmd.Bool("no-auth")

		// Enrich logs with org_id, task_id and trace_id from the context. The
		// handler must be a concrete one (not slog.Default().Handler()): the
		// default handler emits through the log package, and slog.SetDefault
		// reroutes the log package back through this handler, so wrapping the
		// default handler would deadlock on the first record.
		slog.SetDefault(slog.New(logctx.NewHandler(slog.NewTextHandler(os.Stderr, nil))))

		// Initialize OpenTelemetry (configured via OTEL_EXPORTER_OTLP_ENDPOINT env var)
		otel, err := otelx.Setup(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize OpenTelemetry: %w", err)
		}
		defer func() {
			if err := otel.Shutdown(ctx); err != nil {
				slog.Warn("failed to shut down OpenTelemetry", "err", err)
			}
		}()

		db, err := store.Open(dbPath, true)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer db.Close()

		st := store.New(db)

		domain := cmd.String("auth-domain")
		baseURL := cmd.String("base-url")
		key, err := apiauth.DecodeEncryptionKey(cmd.String("auth-encryption-key"))
		if err != nil && !noAuth {
			return fmt.Errorf("invalid encryption key: %w", err)
		}
		appKey, err := apiauth.DecodeAppKey(cmd.String("auth-app-key"))
		if err != nil {
			return fmt.Errorf("invalid app key: %w", err)
		}
		if appKey == nil {
			appKey, err = apiauth.CreateAppPrivateKey()
			if err != nil {
				return fmt.Errorf("failed to generate app key: %w", err)
			}
		}
		resolver := server.NewStoreUserResolver(st)
		var devUser *apiauth.UserInfo
		if noAuth {
			slog.Warn("SSO authentication disabled, using dev user")
			devUser = &apiauth.UserInfo{
				ID:    "dev",
				Email: "dev@localhost",
				Name:  "Developer",
			}
			user, err := resolver.Provision(ctx, devUser)
			if err != nil {
				return fmt.Errorf("failed to provision dev user: %w", err)
			}
			// Provision a fixed dev API key so a local runner can authenticate
			// against the --no-auth server (the dev-user bypass only applies to
			// requests with no auth header, but the runner always sends a key).
			// keys.token_hash is UNIQUE, so on every restart after the first this
			// is a duplicate-key no-op — that is the idempotency mechanism.
			if err := st.CreateKey(ctx, nil, &model.Key{
				ID:        uuid.NewString(),
				Name:      "dev-runner",
				TokenHash: apiauth.HashKey("xat_dev"),
				OrgID:     user.DefaultOrgID,
				Scopes:    authscope.Admin(),
			}); err != nil {
				slog.Warn("failed to provision dev API key", "err", err)
			}
		}
		auth, err := apiauth.New(ctx, apiauth.Config{
			Domain:        domain,
			ClientID:      cmd.String("auth-client-id"),
			ClientSecret:  cmd.String("auth-client-secret"),
			RedirectURI:   baseURL + "/auth/callback",
			PostLogoutURI: baseURL,
			EncryptionKey: key,
			KeyValidator:  server.NewStoreKeyValidator(st),
			UserResolver:  resolver,
			AppKey:        appKey,
			DevUser:       devUser,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize auth: %w", err)
		}

		oauth, err := oauthflow.New(oauthflow.Options{
			AppKey:  appKey,
			BaseURL: baseURL,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize oauth: %w", err)
		}
		ps := pubsub.NewLocalPubSub()
		notify := notifyserver.New(notifyserver.Options{
			Subscriber:  ps,
			OrgResolver: resolver,
		})
		opts := server.Options{
			Store:         st,
			Auth:          auth,
			BaseURL:       baseURL,
			EncryptionKey: key,
			OAuth:         oauth,
			CORS:          cmd.Bool("cors"),
			Publisher:     ps,
			Notify:        notify,
			AppKey:        appKey,
			OrgResolver:   resolver,
		}
		if cmd.IsSet("github-client-id") {
			gh, err := githubserver.New(githubserver.Options{
				Store:     st,
				BaseURL:   baseURL,
				Publisher: ps,
				Config: &githubserver.Config{
					AppID:         cmd.String("github-app-id"),
					AppSlug:       cmd.String("github-app-slug"),
					ClientID:      cmd.String("github-client-id"),
					ClientSecret:  cmd.String("github-client-secret"),
					WebhookSecret: cmd.String("github-webhook-secret"),
					PrivateKey:    []byte(cmd.String("github-private-key")),
				},
			})
			if err != nil {
				return fmt.Errorf("failed to initialize github server: %w", err)
			}
			opts.GitHub = gh
		}
		if cmd.IsSet("atlassian-client-id") {
			opts.Atlassian = atlassianserver.New(atlassianserver.Options{
				Store:        st,
				BaseURL:      baseURL,
				Publisher:    ps,
				ClientID:     cmd.String("atlassian-client-id"),
				ClientSecret: cmd.String("atlassian-client-secret"),
			})
		}
		srv := server.New(opts)

		httpServer := &http.Server{
			Addr:    addr,
			Handler: srv.Handler(),
		}

		// signal.NotifyContext cancels ctx on SIGINT/SIGTERM. Both the archiver
		// goroutine and the HTTP server shutdown watcher key off the same ctx.
		ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		if interval := cmd.Duration("archive-poll"); interval > 0 {
			arch := archiver.New(archiver.Options{
				Store:     st,
				Publisher: ps,
				Interval:  interval,
				BatchSize: cmd.Int("archive-batch"),
				Log:       slog.With("component", "archiver"),
			})
			go func() {
				if err := arch.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("archiver exited with error", "err", err)
				}
			}()
		} else {
			slog.Info("auto-archive disabled (--archive-poll=0)")
		}

		if interval := cmd.Duration("schedule-poll"); interval > 0 {
			sched := scheduler.New(scheduler.Options{
				Store:     st,
				Publisher: ps,
				Interval:  interval,
				BatchSize: cmd.Int("schedule-batch"),
				Log:       slog.With("component", "scheduler"),
			})
			go func() {
				if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("scheduler exited with error", "err", err)
				}
			}()
		} else {
			slog.Info("scheduler disabled (--schedule-poll=0)")
		}

		go func() {
			<-ctx.Done()
			slog.Info("shutdown signal received")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				slog.Error("shutdown error", "err", err)
			}
		}()

		slog.Info("starting server", "addr", addr, "db", dbPath)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}

		slog.Info("server stopped")
		return nil
	},
}
