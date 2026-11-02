---
title: "Multi-org CLI auth and default org config"
description: "The CLI holds a single API key bound to one org, so users in several orgs cannot switch orgs without rewriting its config, and nothing ties a repo to an org."
---

Issue: https://github.com/gritzapp/gritz/issues/1940

## Problem

The CLI holds exactly one credential. `internal/configfile` reads and writes
`os.UserConfigDir()/gritz/config.json` (`~/.config/gritz/config.json` on Linux),
whose only field is `File.Token`. API keys are bound to a single org server-side
(`CreateKey` stamps `model.Key.OrgID` from the caller; bearer-key requests
authenticate as that org), so the CLI is logged in to one org at a time:

- A user in several orgs has to rewrite `config.json`, or juggle env vars, to
  switch orgs.
- Nothing ties a repo to an org, so a command run in the wrong checkout quietly
  acts on whichever org the configured key belongs to.
- No command writes the file. `gritz task`, `gritz logs` and `gritz shell` fail
  with "not authenticated, run setup first", but there is no `setup` command.
- Each command gets its credential differently:

  | Command | Credential source today |
  |---|---|
  | `task list/create/update`, `logs`, `shell` | `configfile.Load(nil)` → `config.json` |
  | `mcp`, `notify` | `--token` / `GRITZ_TOKEN`, required; no config fallback; `--server` has no env source |
  | `runner` | `--key` / `GRITZ_API_KEY`, falling back to `config.json` |

## Design

### Overview

- `~/.gritz/` holds the CLI's state: a `config` file with the global default
  org, and one API key file per org under `keys/`.
- A `.gritz` file in a repo names the org for commands run inside that repo.
- Every user-facing command resolves one org (flag > env > repo `.gritz` >
  `~/.gritz/config`), then loads that org's key.
- New commands: `gritz login`, `gritz logout`, `gritz org list`,
  `gritz org default`, `gritz org current`.
- Files and flags refer to orgs by **numeric id** only. Names are display-only.

### Org references: ids only

Orgs are referenced by their `orgs.id` (`BIGSERIAL`) everywhere a user writes
one: `--org`, `GRITZ_ORG`, `.gritz`, `~/.gritz/config`, and key file names.

- `orgs.name` has no unique constraint, so a name can't identify an org.
  There is no slug column.
- The id is already what users see: the web UI deep-links with `?org=12`
  (see [org query param deep linking](/proposals/implemented/org-query-param-deep-linking/)), and the
  `/auth/token` endpoint takes `org_id`.
- Ids never change. Names can be renamed, which would silently invalidate a
  name-based `.gritz` checked into many repos.

Commands that print orgs (`org list`, `org current`, `login`) show the name next
to the id, fetched from the server.

### `~/.gritz/` layout

```
~/.gritz/                 0700
├── config                0600   global settings (YAML)
├── keys/                 0700
│   ├── 12                0600   raw API key for org 12
│   └── 31                0600   raw API key for org 31
├── workspaces.yaml              runner workspace config (moved, unchanged)
└── prebuilt/                    runner prebuilt binaries (moved, unchanged)
```

`configfile.Dir()` changes from `os.UserConfigDir()/gritz` to
`os.UserHomeDir()/.gritz`. `GRITZ_CONFIG_DIR` still overrides it, which tests
and anyone running a local dev server against a separate state directory rely on.
`workspace.DefaultPath()` and `prebuilt.Dir()` already build on that directory,
so they move along with it.

**`config`.** YAML, parsed with the `gopkg.in/yaml.v3` dependency that
`workspaces.yaml` already uses. Both fields are optional:

```yaml
# ~/.gritz/config
org: 12                    # global default org
server: https://gritz.dev  # default server URL; omitted = gritzclient.DefaultURL
```

It holds no secrets, but it's written `0600` like everything else in the
directory, so there's one rule to follow.

**`keys/<org-id>`.** One file per org. The content is the raw API key: the
`CreateKeyResponse.raw_token` string, plus a trailing newline that the loader
trims. The file name is the decimal org id. One file per org, rather than one
combined credentials file:

- `login` and `logout` each touch one file. Two concurrent logins to different
  orgs can't lose each other's write, and nothing needs read-modify-write locking.
- `logout` is `os.Remove`. The authenticated orgs are exactly the directory listing.
- The key never shares a file with non-secret config that people might copy,
  paste or commit.

Writes go to a temp file in the same directory, which is then renamed into
place, so a crash can't leave a truncated key. The loader rejects a key file
whose mode grants any group or other bits (`mode&0o077 != 0`), as ssh does:
`key file ~/.gritz/keys/12 has mode 0644; run chmod 600`.

### Repo `.gritz` file

```yaml
# <repo>/.gritz
org: 12
```

Same YAML encoding as `~/.gritz/config`. `org` is the only field; anything else
is a parse error, since the file is meant to be committed and shared and must
not hold a server URL or a key. It's meant to be checked in: everyone working in
the repo targets the same org, and org ids are server-wide.

**Discovery.** Starting at the working directory, walk up one directory at a time:

1. If `<dir>/.gritz` is a **regular file**, use it and stop.
2. Else if `<dir>/.git` exists (directory or file, so worktrees and submodules
   count), stop. No repo file applies.
3. Else if `<dir>` is the filesystem root, stop. No repo file applies.
4. Else continue with the parent.

Consequences:

- The nearest `.gritz` wins, so a monorepo subdirectory can pin a different org
  than the repo root.
- The walk never crosses a git root, so a `.gritz` in a parent directory doesn't
  leak into an unrelated repo checked out below it.
- `~/.gritz` is a directory, so step 1 skips it. Running a command from `$HOME`
  doesn't mistake the state directory for a repo file.
- No `git` subprocess. The check is `os.Stat` on `.git`.

Outside any repo, the walk still finds a `.gritz` file in an ancestor directory
up to `/`. That's intentional: a plain directory of scratch work can be pinned too.

### Org resolution

Every user-facing command resolves its org once, at startup, in this order:

| # | Source | Example |
|---|---|---|
| 1 | `--org` flag | `gritz task list --org 31` |
| 2 | `GRITZ_ORG` env var | `GRITZ_ORG=31 gritz task list` |
| 3 | Nearest repo `.gritz` (discovery above) | `org: 31` |
| 4 | `~/.gritz/config` `org` | `org: 12` |

The first source that's set wins; later sources are never consulted. A malformed
value at any level is an error, not a fall-through: a `.gritz` with `org: acme`
fails the command rather than silently using the global default.

The key for the resolved org is then read from `~/.gritz/keys/<org-id>`.

**Explicit key.** `--key` / `GRITZ_API_KEY` supplies a raw key directly and
skips both org resolution and the key directory. The key determines the org
server-side. This is the path for CI and for runners deployed with an injected
key. If `--org`/`GRITZ_ORG` is also set, the command checks it against the key's
org (see `GetProfileResponse.org_id` below) and fails on a mismatch. A repo
`.gritz` or the global default is not checked against an explicit key: those
are defaults, not assertions.

**Errors.** Both say what to run next:

```
error: no org selected: pass --org, set GRITZ_ORG, add a .gritz file to the repo, or run `gritz org default <org-id>`
```

```
error: not logged in to org 31 (from /home/me/src/acme/.gritz): run `gritz login --org 31`
```

The second error names the source that picked the org, so a surprising `.gritz`
is easy to find.

**Server URL.** Resolved independently of the org: `--server` > `GRITZ_SERVER` >
`~/.gritz/config` `server` > `gritzclient.DefaultURL`. Every user-facing command
gets the `GRITZ_SERVER` env source; `mcp` and `notify` lack it today. The repo
`.gritz` deliberately can't set the server.

### API change: report the caller's org

`login` has to learn which org a pasted key belongs to. Nothing returns that
today: `GetProfileResponse.default_org_id` is the user's web-UI default, not the
org the request authenticated as. Add it:

```proto
message GetProfileResponse {
  Profile profile = 1;
  repeated Org orgs = 2;
  int64 default_org_id = 3;
  GitHubAccount github_account = 4;
  AtlassianAccount atlassian_account = 5;
  int64 org_id = 6; // new: the org this request is authenticated against (apiauth.UserInfo.OrgID)
}
```

`apiserver.GetProfile` sets it from `apiauth.MustCaller(ctx).OrgID`. The org's
name comes from the matching entry in `orgs`, which `GetProfile` already returns.

### CLI commands

**`gritz login [--org <id>]`**

1. Read the key from stdin, never from argv (it would land in shell history and
   `ps`). On a TTY, print the URL to create one,
   `<server>/ui/keys/new?org=<id>` (or without `?org=` when `--org` is absent),
   then prompt with echo off. Off a TTY, read one line, so
   `pbpaste | gritz login` and `gritz login < keyfile` work.
2. Call `GetProfile` with the key. A failure there means the key is invalid;
   report it and write nothing.
3. If `--org` was given and differs from `resp.org_id`, fail:
   `key belongs to org 12 (Acme), not 31`.
4. Write `keys/<resp.org_id>`, replacing any existing key for that org.
5. If `~/.gritz/config` has no `org`, set it to this org, so the first login
   just works.
6. Print `Logged in to org 12 (Acme)` and, when it was set,
   `Default org set to 12`.

`--org` is optional because the key already identifies its org. Passing it
guards against pasting the wrong key.

**`gritz logout [--org <id> | --all]`**

Deletes `keys/<id>`, or every file under `keys/` with `--all`. Exactly one of
`--org` and `--all` is required. Logout doesn't fall back on resolution:
removing credentials for whatever org the current directory happens to resolve
to is too easy to do by accident. If the removed org was the global default,
`org` is cleared from `~/.gritz/config`, so the next command fails with
"no org selected" rather than "not logged in". The key is not revoked
server-side (see Open Questions).

**`gritz org list`**

One row per file in `keys/`, ordered by id. Each key is validated against the
server with `GetProfile`, which also supplies the name:

```
ORG  NAME        DEFAULT  CURRENT
12   Acme        *
31   Acme Labs            *        (from /home/me/src/labs/.gritz)
44   ?                             key rejected: unauthenticated
```

`DEFAULT` marks the `~/.gritz/config` org; `CURRENT` marks what resolution
picks in the working directory. A rejected key is listed, not hidden, so the
user knows to log in again.

**`gritz org default [<id>]`**

With an id, writes `org: <id>` to `~/.gritz/config`. It fails if `keys/<id>`
doesn't exist, so the default can't point at an org with no credentials.
Without an id, prints the current default.

**`gritz org current`**

Prints the resolved org, its name and the source that picked it
(`flag`, `GRITZ_ORG`, the `.gritz` path, or `~/.gritz/config`), then exits
non-zero with the resolution error if nothing resolves. This command exists to
debug the precedence.

There's no command that writes a repo `.gritz`. It's a one-line file that gets
committed, so it's created by hand like any other repo config.

**Existing commands**

Each user-facing command gets the same three flags, defined once in
`internal/command` and appended to each command's `Flags`:

| Flag | Env | Notes |
|---|---|---|
| `--org` | `GRITZ_ORG` | new |
| `--key`, `-k` | `GRITZ_API_KEY` | replaces `mcp`/`notify` `--token`/`GRITZ_TOKEN` |
| `--server`, `-s` | `GRITZ_SERVER` | unchanged, plus the env source on `mcp`/`notify` |

Each command's action replaces its `configfile.Load` + `cfg.Token == ""` block
(or `cmd.String("token")`) with one `clientOptions(cmd)` call, which runs the
resolution above and returns `gritzclient.Options`:

- `task list`, `task create`, `task update`, `logs`, `shell`: drop the
  `configfile.Load(nil)` blocks. `shell` also passes the resolved key to
  `shell.Attach` in place of `cfg.Token`.
- `mcp`: `--token` (required) is replaced by resolution. A Claude Code MCP config
  of `{"command": "gritz", "args": ["mcp"]}` launched from a repo picks up the
  repo's `.gritz` org without a token in the config. The notification client
  gets the same key and server.
- `notify`: same change as `mcp`. Running one `notify` per org with `--org` is
  how to watch several orgs.
- `runner`: `configfile.Load(&Overrides{Token: ...})` is replaced by the same
  resolution, so a runner on a laptop uses the logged-in key for `--org` or the
  default. Deployed runners keep using `GRITZ_API_KEY`.

**Not changed:** `gritz tool agent-mcp`, the GitHub MCP proxy and the git
credential helper run inside the sandbox with the task JWT on
`--token`/`GRITZ_TOKEN`. They never read `~/.gritz`, and they keep their flags.
`server`, `driver`, `containers`, `prune`, `download` and `version` don't
authenticate as a user.

### Dropped state

Per the no-backwards-compatibility policy there's no import path:

- `~/.config/gritz/config.json` (`configfile.File{Token}`) is no longer read.
  Users run `gritz login` once per org.
- `~/.config/gritz/workspaces.yaml` and `~/.config/gritz/prebuilt/` are no
  longer read. The runner creates a fresh example `workspaces.yaml` under
  `~/.gritz/` the way it does today (`workspace.CreateDefault`); users with a
  customized file move it by hand. `mise run build` keeps writing prebuilt
  binaries into the repo's `prebuilt/`.
- `--token` / `GRITZ_TOKEN` on `gritz mcp` and `gritz notify` are removed.
  MCP configs that pass `--token` need the flag dropped, or switched to
  `--key` / `GRITZ_API_KEY`.
- The "run setup first" error messages go away with the `configfile.Load` call sites.

None of the old files is deleted automatically. They're just ignored.

### API

```go
package configfile // github.com/gritzapp/gritz/internal/configfile

// Dir returns the gritz state directory: GRITZ_CONFIG_DIR, else ~/.gritz.
func Dir() (string, error) // changed: ~/.gritz instead of os.UserConfigDir()/gritz

// Config is the contents of ~/.gritz/config.
type Config struct { // new
	// Org is the global default org id; zero means unset.
	Org int64 `yaml:"org,omitempty"`
	// Server is the default server URL; empty means gritzclient.DefaultURL.
	Server string `yaml:"server,omitempty"`
}

// LoadConfig reads ~/.gritz/config, returning a zero Config if it does not exist.
func LoadConfig() (*Config, error) // new

// SaveConfig atomically writes ~/.gritz/config.
func SaveConfig(c *Config) error // new

// ErrNoKey is returned by LoadKey when no key is stored for the org.
var ErrNoKey = errors.New("no key for org") // new

// LoadKey returns the stored API key for orgID.
func LoadKey(orgID int64) (string, error) // new

// SaveKey atomically stores key as orgID's API key with mode 0600.
func SaveKey(orgID int64, key string) error // new

// DeleteKey removes orgID's stored key.
func DeleteKey(orgID int64) error // new

// KeyOrgs returns the ids of all orgs with a stored key, ascending.
func KeyOrgs() ([]int64, error) // new

// RepoFile is the contents of a repo-level .gritz file.
type RepoFile struct { // new
	// Org is the org id commands in the repo default to.
	Org int64 `yaml:"org"`
}

// FindRepoFile walks up from dir to the nearest .gritz file, stopping at a git root; path is empty if none applies.
func FindRepoFile(dir string) (path string, f *RepoFile, err error) // new

type File struct{ ... }                      // removed
type Overrides struct{ ... }                 // removed
func Path() (string, error)                  // removed
func Load(overrides *Overrides) (*File, error) // removed
func Save(f *File) error                     // removed
```

```go
package orgselect // github.com/gritzapp/gritz/internal/orgselect

// Source identifies where the org was resolved from.
type Source string

// Sources, in precedence order.
const (
	SourceFlag   Source = "flag"
	SourceEnv    Source = "GRITZ_ORG"
	SourceRepo   Source = "repo"
	SourceConfig Source = "config"
)

// Input is everything resolution reads besides the filesystem.
type Input struct {
	// Org is the --org flag or GRITZ_ORG value; zero if neither is set.
	Org int64
	// OrgSource is SourceFlag or SourceEnv, describing Org.
	OrgSource Source
	// Dir is the working directory to start .gritz discovery from.
	Dir string
}

// Selection is a resolved org.
type Selection struct {
	// OrgID is the resolved org.
	OrgID int64
	// Source is where OrgID came from.
	Source Source
	// Path is the .gritz path when Source is SourceRepo.
	Path string
}

// Resolve picks the org by precedence, returning an error naming the fixes when none applies.
func Resolve(in Input) (*Selection, error)

// Key loads the stored key for sel, returning an error naming `gritz login` when none exists.
func Key(sel *Selection) (string, error)
```

```go
package command // github.com/gritzapp/gritz/internal/command

// clientFlags returns the --org, --key and --server flags shared by user-facing commands.
func clientFlags() []cli.Flag // new

// clientOptions resolves the server, org and key for cmd into client options.
func clientOptions(ctx context.Context, cmd *cli.Command) (gritzclient.Options, error) // new

var LoginCommand *cli.Command  // new: gritz login
var LogoutCommand *cli.Command // new: gritz logout
var OrgCommand *cli.Command    // new: gritz org list|default|current
```

**How it fits:** `configfile` owns the on-disk formats and nothing else.
`orgselect` holds the precedence rules, with no CLI dependency, so they can be
table-tested against temp directories. `command.clientOptions` reads the flags,
calls `orgselect.Resolve` and `orgselect.Key` (or takes `--key` as is), applies
the server precedence, and returns `gritzclient.Options`. Every user-facing
command's action calls it. `login`, `logout` and `org` call `configfile`
directly to write state, and `orgselect` for `org current` and the `CURRENT`
column.

## Implementation Plan

1. **Report the caller's org** — Delivers: `GetProfileResponse.org_id` (proto,
   regenerated code, `apiserver.GetProfile`). Depends on: nothing.
   Verifiable by: an apiserver test where a key-authenticated `GetProfile`
   returns the key's org.
2. **`~/.gritz` storage and `.gritz` discovery** — Delivers: the new
   `configfile` functions (`Config`, keys, `FindRepoFile`) alongside the old
   ones, plus `orgselect`. Nothing calls them yet. Depends on: nothing.
   Verifiable by: unit tests over temp dirs: discovery stops at `.git`, skips a
   `.gritz` directory, nearest file wins; precedence table; permission check on
   key files.
3. **`login`, `logout`, `org` commands** — Delivers: the three commands and
   `main.go` registration. Depends on: (1), (2). Verifiable by: an e2e test
   that creates keys in two orgs, logs in to both, and checks `org list`,
   `org default` and `logout`.
4. **Wire existing commands and drop the old config** — Delivers:
   `clientFlags`/`clientOptions`; `task`, `logs`, `shell`, `mcp`, `notify` and
   `runner` switched over; `mcp`/`notify` `--token` removed; `Dir()` moved to
   `~/.gritz`; `File`/`Load`/`Save`/`Overrides`/`Path` deleted; README and
   docs updated. Depends on: (2), (3). Verifiable by: an e2e test that runs
   `gritz task list` from a repo dir with a `.gritz` for org A while the
   global default is org B, and sees only org A's tasks.

## Trade-offs

- **`~/.gritz` vs. staying in `~/.config/gritz`.** XDG is the better
  convention, and it's where the config lives today, but the requested layout
  is `~/.gritz`, it matches the `.gritz` repo file name, and it's what users
  will look for. Moving `workspaces.yaml` and `prebuilt/` with it keeps one
  state directory rather than splitting CLI and runner state across two.
- **One key file per org vs. a single credentials file.** A single
  `credentials.yaml` map is one fewer directory, but every login and logout
  becomes read-modify-write of a shared secrets file. Per-org files make each
  operation a single atomic write or remove.
- **OS keychain.** More secure at rest, but adds a cgo or D-Bus dependency,
  fails in headless and container environments, and isn't what was asked for.
  `0600` files match how `gh`, `aws` and `kubectl` store tokens. This can be
  added later behind the same `LoadKey`/`SaveKey` functions.
- **Ids vs. names or slugs.** Names aren't unique and can change. Adding a
  unique slug column is a schema change with its own UX (choosing, renaming)
  and isn't needed to ship this. Ids are already user-visible via `?org=`.
- **YAML vs. JSON vs. TOML.** YAML is already a dependency and is used for
  `workspaces.yaml`, and allows comments in a committed `.gritz`. JSON doesn't
  allow comments, and TOML would add a dependency.
- **Stop at the git root vs. walk to `/`.** Walking to `/` could let a stray
  `.gritz` in a parent directory apply to every repo below it. Stopping at
  `.git` scopes the file to its repo, and checking for `.git` with `os.Stat`
  avoids shelling out to `git rev-parse`.
- **Login by pasted key vs. browser flow.** A browser OAuth/PKCE flow
  (reusing `internal/auth/oauthflow`) would be smoother, but it needs a
  server-side key-minting endpoint and a loopback redirect listener. Pasting a
  key from `/ui/keys/new` works today with one proto field, and a browser flow
  can later be layered on top to produce the same `keys/<org-id>` file.

## Open Questions

1. **Multiple servers.** Org ids are only unique per server, and `keys/<id>`
   assumes one server per `~/.gritz`. Today the answer for local development is
   a separate `GRITZ_CONFIG_DIR`. Is that enough, or should keys be namespaced
   by server host (`keys/gritz.dev/12`)?
2. **Server-side revoke on logout.** Should `logout` also delete the key on the
   server? That needs the key's id, which the raw token doesn't carry, so
   `GetProfile` (or a new RPC) would have to return it, and `logout` would need
   the server to be reachable. Local-only logout is proposed for now.
3. **Should `runner` honor `.gritz`?** For consistency it does here, but a
   runner started from inside a repo checkout picking that repo's org may
   surprise people. The alternative is that the runner skips step 3 and only
   reads `--org`, `GRITZ_ORG` and the global default.
4. **Key-only `--key` and `.gritz` mismatch.** When `--key` is set inside a
   repo whose `.gritz` names a different org, should the command warn, rather
   than staying silent as proposed?
