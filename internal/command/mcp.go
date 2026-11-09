package command

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/mcpbridge"
	"github.com/gritzapp/gritz/internal/server/mcpserver"
	"github.com/gritzapp/gritz/internal/x/mcpchannel"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/urfave/cli/v3"
)

// McpCommand runs a local stdio MCP bridge that re-exposes the
// user-facing gritz tools by proxying calls to the server's
// Connect RPC API, and pushes task change notifications to the host
// Claude Code session as `notifications/claude/channel` events.
//
// The bridge declares the experimental `claude/channel` capability so
// Claude Code routes the notification stream into the session as
// `<channel>` tags. The push half is a translator on top of the
// existing per-org SSE notification stream — no new server endpoints
// or schemas are involved.
var McpCommand = &cli.Command{
	Name:  "mcp",
	Usage: "Run a stdio MCP server for managing tasks",
	Flags: slices.Concat(configfile.Flags(), []cli.Flag{
		&cli.BoolFlag{
			Name:  "channel",
			Usage: "Enable experimental claude/channel support",
		},
		&cli.DurationFlag{
			Name:  "auto-archive",
			Usage: "Default auto-archive delay for tasks created via create_task when the call omits auto_archive. 0 = never, negative = archive immediately on terminal status, positive = delay (e.g. 1h, 24h). The per-call auto_archive param overrides this.",
		},
	}),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		// One stable id per bridge process. It is sent on every mutation
		// RPC so the server stamps the resulting notifications with this
		// id, and on the SSE subscription so the notification client
		// filters those same events back out. Without it the bridge
		// would echo its own create_task/update_task changes back to the
		// host Claude Code session as channel events (#718).
		clientID := uuid.NewString()
		var capabilities mcp.ServerCapabilities
		if cmd.Bool("channel") {
			capabilities.Experimental = mcpchannel.Experimental()
		}
		transport := mcpchannel.NewTransport(&mcp.StdioTransport{})

		// The mute set and its channel_mute/channel_unmute/channel_muted
		// tools only exist when there's a channel stream to filter. Without
		// --channel the bridge behaves exactly as before. Orgs are followed
		// before the session starts, so the mute tools always find the
		// startup org.
		var ch *mcpbridge.Channel
		follow := func(*configfile.Entry) {}
		if cmd.Bool("channel") {
			ch = mcpbridge.NewChannel(transport, clientID)
			follow = func(entry *configfile.Entry) { ch.Follow(ctx, entry) }
		}

		// --key (and --server, which requires it) selects one raw key with
		// no entries to switch between; everything else is a .gritz.yml
		// session with list_orgs and switch_org.
		instructions := mcpserver.Instructions
		var service mcpserver.ServiceFunc
		var switcher *mcpbridge.OrgSwitcher
		if cmd.String("key") != "" || cmd.String("server") != "" {
			entry, err := configfile.Resolve(cmd)
			if err != nil {
				return err
			}
			service = mcpserver.Static(gritzclient.New(gritzclient.Options{
				BaseURL:     entry.Server,
				TokenSource: entry.TokenSource(ctx),
				ClientID:    clientID,
			}))
			follow(entry)
		} else {
			merged, err := configfile.Load(configfile.LoadOptions{})
			if err != nil {
				return err
			}
			if err := merged.Validate(); err != nil {
				return err
			}
			entry, err := merged.Lookup(cmd.String("org"))
			if err != nil {
				return err
			}
			switcher = mcpbridge.NewOrgSwitcher(ctx, merged, entry, clientID, follow)
			service = switcher.Service
			instructions += mcpbridge.OrgsInstructions
		}

		server := mcp.NewServer(&mcp.Implementation{
			Name:    "gritz",
			Version: "1.0.0",
		}, &mcp.ServerOptions{
			Instructions: instructions,
			Capabilities: &capabilities,
		})
		var toolOpts []mcpserver.Option
		if cmd.IsSet("auto-archive") {
			toolOpts = append(toolOpts, mcpserver.WithDefaultAutoArchive(cmd.Duration("auto-archive")))
		}
		mcpserver.AddTools(server, service, toolOpts...)
		if switcher != nil {
			switcher.AddTools(server)
		}
		if ch != nil {
			ch.AddTools(server)
		}

		session, err := server.Connect(ctx, transport, nil)
		if err != nil {
			return err
		}
		return session.Wait()
	},
}
