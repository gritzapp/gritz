package agentauth

import (
	"testing"

	"github.com/gritzapp/gritz/internal/auth/authscope"
	"gotest.tools/v3/assert"
)

func TestScopes_OwnTaskOnly(t *testing.T) {
	scopes := Scopes(ScopeOptions{
		TaskID: 42,
	})

	assert.Assert(t, scopes.Allow(authscope.OpTaskRead, authscope.WithTaskID(42), authscope.WithTaskArchived(false)))
	assert.Assert(t, scopes.Allow(authscope.OpTaskWrite, authscope.WithTaskID(42), authscope.WithTaskArchived(false)))

	// An archived own task is denied: the scope constrains task.archived:"false".
	assert.Assert(t, !scopes.Allow(authscope.OpTaskRead, authscope.WithTaskID(42), authscope.WithTaskArchived(true)))
	assert.Assert(t, !scopes.Allow(authscope.OpTaskWrite, authscope.WithTaskID(42), authscope.WithTaskArchived(true)))

	// An unrelated task stays denied.
	assert.Assert(t, !scopes.Allow(authscope.OpTaskRead, authscope.WithTaskID(99), authscope.WithTaskArchived(false)))
	assert.Assert(t, !scopes.Allow(authscope.OpTaskWrite, authscope.WithTaskID(99), authscope.WithTaskArchived(false)))

	// No create or github-token capability without the flags.
	assert.Assert(t, !scopes.Allow(authscope.OpTaskCreate, authscope.WithTaskWorkspace("ws"), authscope.WithTaskRunner("rn"), authscope.WithTaskArchived(false)))
	assert.Assert(t, !scopes.Allow(authscope.OpGitHubTokenCreate))
}

func TestScopes_GitHubToken(t *testing.T) {
	scopes := Scopes(ScopeOptions{
		TaskID:       42,
		Capabilities: []string{CapabilityGitHubToken},
	})

	assert.Assert(t, scopes.Allow(authscope.OpGitHubTokenCreate))
	// The github-token capability does not widen task access.
	assert.Assert(t, !scopes.Allow(authscope.OpTaskCreate, authscope.WithTaskWorkspace("ws"), authscope.WithTaskRunner("rn"), authscope.WithTaskArchived(false)))
	assert.Assert(t, !scopes.Allow(authscope.OpTaskRead, authscope.WithTaskID(99), authscope.WithTaskArchived(false)))
}
