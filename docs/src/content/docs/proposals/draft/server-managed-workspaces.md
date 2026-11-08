---
title: "Server-Managed Workspace Configurations"
description: "Deploying a runner requires a workspaces.yaml file on the host, so a runner cannot be deployed with just a Docker Compose file and environment variables."
---

Issue: https://github.com/icholy/gritz/issues/397

## Problem

Deploying a runner requires a `workspaces.yaml` file on the host. The runner loads this file at startup to get the full workspace configuration (container settings, agent config, volumes, env vars, MCP servers). This means you can't deploy a runner with just a Docker Compose file and environment variables -- the config file must be created, mounted, and maintained separately.

## Design

### Store individual workspace YAML configs in the existing workspaces table

Add a `config` column to the existing `workspaces` table that stores the raw YAML configuration for each workspace. When a runner registers workspaces, it includes the individual workspace YAML. Other runners (or the same runner after restart) can pull these configs from the server instead of needing a local `workspaces.yaml` file.

Raw YAML (not structured proto) because:
- The workspace config schema changes frequently (new agent types, container options, MCP configs)
- Variable expansion (`${env:VAR}`, `${sh:command}`) happens runner-side, so the server doesn't need to understand the structure
- Avoids maintaining parallel proto definitions that mirror the YAML schema

### Database Schema

New migration `015_workspace_config.sql`:

```sql
-- Remove runner_id since workspaces are now managed via CRUD, not runner registration
ALTER TABLE workspaces DROP COLUMN runner_id;
DROP INDEX IF EXISTS idx_workspaces_runner_id;

-- Add unique constraint: one workspace per name per org
ALTER TABLE workspaces ADD CONSTRAINT uq_workspaces_org_name UNIQUE (org_id, name);

-- Add config column for the raw YAML
ALTER TABLE workspaces ADD COLUMN config TEXT NOT NULL DEFAULT '';
```

The `config` column holds the raw YAML for an individual workspace entry (the value portion of the workspace's key in `workspaces.yaml`, before variable expansion). Each workspace is now uniquely identified by `(org_id, name)` — the `runner_id` column is removed since `RegisterWorkspaces` is replaced by the CRUD RPCs.

### API Changes

CRUD RPCs for individual workspace configs in `gritz.proto`. These support both CLI usage and a frontend editor (using [monaco-yaml](https://github.com/remcohaszing/monaco-yaml) for in-browser YAML editing).

```proto
// Create or update a workspace config
rpc SetWorkspaceConfig(SetWorkspaceConfigRequest) returns (SetWorkspaceConfigResponse);

// Get a single workspace config by name
rpc GetWorkspaceConfig(GetWorkspaceConfigRequest) returns (GetWorkspaceConfigResponse);

// List all workspace configs for the org
rpc ListWorkspaceConfigs(ListWorkspaceConfigsRequest) returns (ListWorkspaceConfigsResponse);

// Delete a workspace config
rpc DeleteWorkspaceConfig(DeleteWorkspaceConfigRequest) returns (DeleteWorkspaceConfigResponse);

message SetWorkspaceConfigRequest {
  string name = 1;
  string config = 2;  // raw YAML for this workspace
}

message SetWorkspaceConfigResponse {}

message GetWorkspaceConfigRequest {
  string name = 1;
}

message GetWorkspaceConfigResponse {
  string name = 1;
  string config = 2;
}

message ListWorkspaceConfigsRequest {}

message ListWorkspaceConfigsResponse {
  repeated WorkspaceConfig configs = 1;
}

message WorkspaceConfig {
  string name = 1;
  string config = 2;
}

message DeleteWorkspaceConfigRequest {
  string name = 1;
}

message DeleteWorkspaceConfigResponse {}
```

Org context comes from auth, same as all other endpoints.

### Store Methods

Update existing methods and add new ones in `internal/store/`:

```go
func (s *Store) SetWorkspaceConfig(ctx context.Context, tx *sql.Tx, orgID int64, name string, config string) error
func (s *Store) GetWorkspaceConfig(ctx context.Context, tx *sql.Tx, orgID int64, name string) (string, error)
func (s *Store) ListWorkspaceConfigs(ctx context.Context, tx *sql.Tx, orgID int64) ([]WorkspaceConfig, error)
func (s *Store) DeleteWorkspaceConfig(ctx context.Context, tx *sql.Tx, orgID int64, name string) error
```

SQL queries:

```sql
-- name: SetWorkspaceConfig :exec
INSERT INTO workspaces (org_id, name, config, updated_at)
VALUES ($1, $2, $3, CURRENT_TIMESTAMP)
ON CONFLICT (org_id, name) DO UPDATE SET config = $3, updated_at = CURRENT_TIMESTAMP;

-- name: GetWorkspaceConfig :one
SELECT name, config FROM workspaces
WHERE org_id = $1 AND name = $2;

-- name: ListWorkspaceConfigs :many
SELECT name, config FROM workspaces
WHERE org_id = $1
ORDER BY name;

-- name: DeleteWorkspaceConfig :exec
DELETE FROM workspaces WHERE org_id = $1 AND name = $2;
```

### Server Handlers

`SetWorkspaceConfig` validates the YAML parses correctly using `workspace.ParseConfig()` (new function, like `LoadConfig` but takes `[]byte` and skips variable expansion), then stores the config. `GetWorkspaceConfig` and `ListWorkspaceConfigs` return raw YAML. `DeleteWorkspaceConfig` clears the config for a workspace.

### Frontend

The Web UI gets a workspace config editor page using [monaco-yaml](https://github.com/remcohaszing/monaco-yaml):

- List view showing all workspace configs for the org
- Click a workspace to open the YAML editor (monaco with YAML language support)
- Create new workspace configs
- Delete existing workspace configs
- Validation feedback from the server on save

### CLI Commands

Add subcommands to the existing `gritz workspaces` command group (or create it if it doesn't exist):

```
gritz workspaces push [--config path]   # Upload local YAML to server
gritz workspaces pull [--output path]   # Download server YAML to local file or stdout
gritz workspaces list                   # List workspace configs on the server
gritz workspaces get <name>             # Get a single workspace config
gritz workspaces delete <name>          # Delete a workspace config
```

- `push` reads a local file (default `~/.config/gritz/workspaces.yaml`), validates it parses, splits it into individual workspace entries, then uploads each via `SetWorkspaceConfig`
- `pull` downloads via `ListWorkspaceConfigs`, reassembles into a `workspaces.yaml` format, and writes to file or stdout
- `list`, `get`, `delete` map directly to the corresponding RPCs

### Runner Behavior Changes

The key change is per-workspace resolution: local config takes priority, with the server as a fallback for workspaces not defined locally.

When creating a container for a task, the runner resolves the workspace config as follows:

1. If the workspace name exists in the local `workspaces.yaml`, use that definition
2. Otherwise, fetch the workspace config from the server via `GetWorkspaceConfig`
3. If neither source has the workspace, fail the task with an error

This is a per-workspace decision, not all-or-nothing. A runner can have some workspaces defined locally and pull others from the server. This lets runners:
- Override specific workspaces locally (e.g. for development or testing)
- Pull shared workspace definitions from the server without maintaining a full local config
- Run with no local config at all, relying entirely on the server

A runner deployed via Docker Compose only needs `GRITZ_SERVER` and `GRITZ_API_KEY` environment variables. Workspace configs are pulled from the server on demand.

### Variable Expansion

Variable expansion continues to happen runner-side after pulling the config. The server stores the template with `${env:VAR}` references intact. Different runners can resolve different values from their local environment. No secrets are stored on the server.

### Replacing RegisterWorkspaces

The existing `RegisterWorkspaces` RPC is removed. Workspace lifecycle is now fully managed through the CRUD RPCs (`SetWorkspaceConfig`, `DeleteWorkspaceConfig`, etc.) via the CLI or Web UI. The `workspaces` table no longer tracks which runner has which workspace — it's a simple config store keyed by `(org_id, name)`.

The flow becomes:

1. Configs are managed on the server via CLI (`gritz workspaces push`) or the Web UI editor
2. When a task arrives, the runner checks the local `workspaces.yaml` for a matching workspace definition
3. If not found locally, the runner fetches the workspace config from the server via `GetWorkspaceConfig`
4. Runner parses YAML and expands variables locally
5. Runner creates the container using the resolved config

## Trade-offs

| Approach | Pros | Cons |
|----------|------|------|
| **Per-workspace YAML in workspaces table** (proposed) | No new tables, one row per workspace, simple CRUD, supports frontend editor | Removes runner_id tracking (runners no longer register workspaces) |
| **Structured proto fields** | Server can validate fields, UI can render forms | Proto must mirror YAML schema, constant drift as config evolves |
| **Separate workspace_configs table** | Clean separation of config from registration | Extra table to manage, separate lifecycle from workspace registration |

Storing configs directly in the workspaces table is chosen because configs are a property of workspaces, not a separate concept. This keeps the data model simple and avoids a separate table with its own lifecycle.

## Open Questions

1. **Hot reload**: Should the runner periodically re-pull config from the server, or only load at startup? Periodic refresh would let you update workspaces without restarting runners, but adds complexity around when to apply changes (only to new tasks, not running ones).

2. **Server-side validation**: Should `SetWorkspaceConfig` just check that the YAML parses, or also validate the config (e.g. image is set)? Full validation couples the server to the config schema. Parse-only validation catches syntax errors without that coupling.

3. **YAML schema for monaco-yaml**: Should we ship a JSON Schema for the workspace config format to enable autocompletion and validation in the frontend editor? This would improve the editing experience but requires maintaining the schema alongside the Go structs.

4. **Runner availability tracking**: With `RegisterWorkspaces` removed, the server no longer knows which runners are online or which workspaces they can serve. If this is needed (e.g. for task routing), it could be handled by a separate heartbeat/status RPC.
