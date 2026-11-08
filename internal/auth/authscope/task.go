package authscope

import "strconv"

// This file defines the concrete scope taxonomy that rides on the generic
// matching engine in scope.go: the operation paths, the namespaced attribute
// keys, and the typed attribute constructors that call sites pass to
// Scopes.Allow. It covers both the task-caller surface (the task-token minter
// agentauth.Scopes) and the API-caller surface (the apiserver handlers, which
// enforce per-task scopes directly); see the per-RPC mapping in
// docs/src/content/docs/proposals/implemented/scope-based-permissions.md §7.

// Operation paths, as segment slices. The single source of truth for each
// operation: scope creation (the runner's token minter, agentauth.Scopes)
// and Allow lookups share these, so the two can't drift. Treat them as
// immutable — never mutate a returned slice. A wildcard operation would be
// expressed the same way, e.g. var OpTaskAny = []string{"task", "*"}.
var (
	OpTaskRead          = []string{"task", "read"}
	OpTaskWrite         = []string{"task", "write"}
	OpTaskCreate        = []string{"task", "create"}
	OpGitHubTokenCreate = []string{"github_token", "create"}

	// OpTaskTokenCreate is a no-instance, capability-only op: the right to mint a
	// narrow task token via the CreateTaskToken RPC. It has no instance attribute,
	// so its handler gates on Scopes.AllowOp (see
	// docs/src/content/docs/proposals/implemented/eliminate-runner-socket-proxy.md
	// §1/§7).
	OpTaskTokenCreate = []string{"task_token", "create"}

	// API-caller operation paths (proposal §7). Events and keys are managed
	// coarsely — there is no instance attribute for them — so their RPCs are
	// op-level checks. Lifecycle and sub-resource verbs fold into write.
	OpEventRead      = []string{"event", "read"}
	OpEventWrite     = []string{"event", "write"} // delete + add/remove task fold into write
	OpEventCreate    = []string{"event", "create"}
	OpWorkspaceRead  = []string{"workspace", "read"}
	OpWorkspaceWrite = []string{"workspace", "write"} // register + clear
	OpKeyRead        = []string{"key", "read"}
	OpKeyCreate      = []string{"key", "create"}
	OpKeyWrite       = []string{"key", "write"} // delete folds into write (coarse, no key.id)
	OpOrgRead        = []string{"org", "read"}  // settings, members, routing-rule reads
	OpOrgWrite       = []string{"org", "write"} // members, settings, routing-rules, GH-installation link
	OpOrgCreate      = []string{"org", "create"}
	OpOrgDelete      = []string{"org", "delete"}
	OpAccountWrite   = []string{"account", "write"} // unlink GitHub/Atlassian — user-identity axis
)

// Attribute keys, namespaced by resource ("task.id", not "id") so attribute
// names stay globally unambiguous as the taxonomy grows. These are used by the
// task-token minter and the apiserver task handlers' per-instance Allow checks;
// the coarse API-caller op checks ignore them (proposal §7).
const (
	AttrTaskID        = "task.id"
	AttrTaskWorkspace = "task.workspace"
	AttrTaskRunner    = "task.runner"
	AttrTaskArchived  = "task.archived"
)

// WithTaskID, WithTaskWorkspace, and WithTaskRunner build the attributes for a
// task-resource request, pairing each namespaced key with its value. Call sites
// pass them straight to Scopes.Allow.
func WithTaskID(id int64) Attr { return Int64Attr(AttrTaskID, id) }

func WithTaskWorkspace(workspace string) Attr { return StringAttr(AttrTaskWorkspace, workspace) }

func WithTaskRunner(runner string) Attr { return StringAttr(AttrTaskRunner, runner) }

// WithTaskArchived builds the task.archived attribute. "false" is a real value
// (not a zero/absent case), so it is always emitted as "true"/"false" — a scope
// constraining task.archived:"false" denies a request carrying "true", which is
// how archive-based revocation falls out of the ordinary predicate rule. See
// docs/src/content/docs/proposals/implemented/eliminate-runner-socket-proxy.md
// §3.
func WithTaskArchived(archived bool) Attr {
	return StringAttr(AttrTaskArchived, strconv.FormatBool(archived))
}
