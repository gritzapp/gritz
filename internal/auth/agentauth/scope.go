package agentauth

import (
	"slices"

	"github.com/gritzapp/gritz/internal/auth/authscope"
)

// ScopeOptions describes the task identity and workspace capabilities a token's
// scopes are minted from.
type ScopeOptions struct {
	TaskID       int64
	Capabilities []string
}

// Scopes builds the scopes granted to a task token from the task's identity and
// the workspace's enabled capabilities (CapabilityGitHubToken). Every task
// always gets read and write on its own id; the github-token capability adds
// github_token.create.
//
// Every task scope additionally constrains task.archived:"false", which is what
// makes archiving a task revoke its token: an archived task's row carries
// task.archived:"true", failing the "false" predicate uniformly for reads and
// writes (see proposals/implemented/eliminate-runner-socket-proxy.md §3). The handlers
// pass the task's real archived state from the loaded row (via Task.ScopeAttr).
func Scopes(opts ScopeOptions) authscope.Scopes {
	scopes := authscope.Scopes{
		authscope.New(authscope.OpTaskRead, authscope.WithTaskID(opts.TaskID), authscope.WithTaskArchived(false)),
		authscope.New(authscope.OpTaskWrite, authscope.WithTaskID(opts.TaskID), authscope.WithTaskArchived(false)),
	}
	if slices.Contains(opts.Capabilities, CapabilityGitHubToken) {
		scopes = append(scopes, authscope.New(authscope.OpGitHubTokenCreate))
	}
	return scopes
}
