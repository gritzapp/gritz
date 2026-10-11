package command

import "github.com/urfave/cli/v3"

var ToolCommand = &cli.Command{
	Name:  "tool",
	Usage: "In-container agent tools",
	Commands: []*cli.Command{
		AgentMcpCommand,
		GitCredentialCommand,
		GitHubMCPCommand,
		ProxyCommand,
	},
}
