package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

var AgentCommand = &cli.Command{
	Name:  "agent",
	Usage: "Agent management commands",
	Commands: []*cli.Command{
		agentListCommand,
		agentGetCommand,
		agentCreateCommand,
		agentUpdateCommand,
		agentDeleteCommand,
	},
}

var agentFileFlag = &cli.StringFlag{
	Name:     "file",
	Aliases:  []string{"f"},
	Usage:    "YAML or JSON file with the agent, in the shape 'gritz agent get' prints (- for stdin). auto_archive is in seconds, such as 86400s.",
	Required: true,
}

var agentListCommand = &cli.Command{
	Name:  "list",
	Usage: "List the org's agents",
	Flags: configfile.Flags(),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		client, err := newAgentClient(ctx, cmd)
		if err != nil {
			return err
		}
		resp, err := client.ListAgents(ctx, &gritzv1.ListAgentsRequest{})
		if err != nil {
			return fmt.Errorf("failed to list agents: %w", err)
		}
		return printProto(resp)
	},
}

var agentGetCommand = &cli.Command{
	Name:      "get",
	Usage:     "Show an agent, with its triggers and link kinds",
	ArgsUsage: "<agent-id>",
	Flags:     configfile.Flags(),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		id, err := agentIDArg(cmd)
		if err != nil {
			return err
		}
		client, err := newAgentClient(ctx, cmd)
		if err != nil {
			return err
		}
		resp, err := client.GetAgent(ctx, &gritzv1.GetAgentRequest{Id: id})
		if err != nil {
			return fmt.Errorf("failed to get agent: %w", err)
		}
		return printProto(resp.Agent)
	},
}

var agentCreateCommand = &cli.Command{
	Name:  "create",
	Usage: "Create an agent from a file",
	Flags: slices.Concat(configfile.Flags(), []cli.Flag{agentFileFlag}),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		agent, err := readAgent(cmd.String("file"))
		if err != nil {
			return err
		}
		agent.Id = 0
		client, err := newAgentClient(ctx, cmd)
		if err != nil {
			return err
		}
		resp, err := client.CreateAgent(ctx, &gritzv1.CreateAgentRequest{Agent: agent})
		if err != nil {
			return fmt.Errorf("failed to create agent: %w", err)
		}
		return printProto(resp.Agent)
	},
}

var agentUpdateCommand = &cli.Command{
	Name:      "update",
	Usage:     "Replace an agent with the one in a file. Tasks it already created are unchanged.",
	ArgsUsage: "<agent-id>",
	Flags:     slices.Concat(configfile.Flags(), []cli.Flag{agentFileFlag}),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		id, err := agentIDArg(cmd)
		if err != nil {
			return err
		}
		agent, err := readAgent(cmd.String("file"))
		if err != nil {
			return err
		}
		agent.Id = id
		client, err := newAgentClient(ctx, cmd)
		if err != nil {
			return err
		}
		resp, err := client.UpdateAgent(ctx, &gritzv1.UpdateAgentRequest{Agent: agent})
		if err != nil {
			return fmt.Errorf("failed to update agent: %w", err)
		}
		return printProto(resp.Agent)
	},
}

var agentDeleteCommand = &cli.Command{
	Name:      "delete",
	Usage:     "Delete an agent. It fails while an unarchived task or a schedule belongs to it.",
	ArgsUsage: "<agent-id>",
	Flags:     configfile.Flags(),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		id, err := agentIDArg(cmd)
		if err != nil {
			return err
		}
		client, err := newAgentClient(ctx, cmd)
		if err != nil {
			return err
		}
		if _, err := client.DeleteAgent(ctx, &gritzv1.DeleteAgentRequest{Id: id}); err != nil {
			return fmt.Errorf("failed to delete agent: %w", err)
		}
		fmt.Println("Agent deleted.")
		return nil
	},
}

func newAgentClient(ctx context.Context, cmd *cli.Command) (gritzclient.Client, error) {
	_, cfg, err := configfile.Resolve(cmd)
	if err != nil {
		return nil, err
	}
	return gritzclient.New(gritzclient.Options{
		BaseURL:     cfg.Server,
		TokenSource: cfg.TokenSource(ctx),
	}), nil
}

func agentIDArg(cmd *cli.Command) (int64, error) {
	arg := cmd.Args().First()
	if arg == "" {
		return 0, fmt.Errorf("agent ID is required")
	}
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid agent ID: %w", err)
	}
	return id, nil
}

// readAgent reads an agent from the YAML or JSON file at path, or stdin
// when path is "-". Field names are the proto's, in snake_case or camelCase.
func readAgent(path string) (*gritzv1.Agent, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read agent: %w", err)
	}
	// YAML is a superset of JSON: decode either, then hand protojson the
	// JSON form so field names and well-known types follow the proto.
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("failed to parse agent: %w", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to parse agent: %w", err)
	}
	agent := &gritzv1.Agent{}
	if err := protojson.Unmarshal(b, agent); err != nil {
		return nil, fmt.Errorf("invalid agent: %w", err)
	}
	return agent, nil
}

func printProto(m proto.Message) error {
	b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(b))
	return err
}
