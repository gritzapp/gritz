---
title: "Multi-org CLI auth and default org config"
description: "The CLI holds a single API key bound to one org, so users in several orgs cannot switch orgs without rewriting its config, and nothing ties a directory to an org."
---

Issues: https://github.com/gritzapp/gritz/issues/1940, https://github.com/gritzapp/gritz/issues/2065

## Problem

The CLI holds exactly one credential. `internal/configfile` reads and writes
`os.UserConfigDir()/gritz/config.json` (`~/.config/gritz/config.json` on Linux),
whose only field is `File.Token`. API keys are bound to a single org server-side
(`CreateKey` stamps `model.Key.OrgID` from the caller; bearer-key requests
authenticate as that org), so the CLI is logged in to one org at a time:

- A user in several orgs has to rewrite `config.json`, or juggle env vars, to
  switch orgs.
- Nothing ties a repo or directory to an org, so a command run in the wrong
  checkout quietly acts on whichever org the configured key belongs to.
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

- One file format, `.gritz.yml`, used at every level. It names orgs, holds their
  keys and servers, and picks a default.
- Every user-facing command merges the `.gritz.yml` files from the working
  directory up to `/`, on top of `$CONFIG_DIR/.gritz.yml`, then selects one org
  entry. The entry supplies the server, org id and key.
- `--key` / `GRITZ_API_KEY` bypasses all of that, for CI and deployed runners.
- One new command, `gritz login`, writes an entry into `$CONFIG_DIR/.gritz.yml`.
  Everything else is done by editing the files by hand.

### The `.gritz.yml` format

YAML, parsed with the `gopkg.in/yaml.v3` dependency that `workspaces.yaml`
already uses. Every field is optional:

```yaml
default: acme
allowed: [acme, local]
orgs:
  acme:
    id: 12
    key: gritz_k_...
  local:
    id: 1
    server: http://localhost:8080
    key: gritz_k_...
```

- `orgs` maps a local name to an entry:
  - `id`: the org id (`orgs.id`).
  - `key`: an API key for that org, the `CreateKeyResponse.raw_token` string.
  - `server`: the server URL; omitted means `gritzclient.DefaultURL`.
- `default` names the entry used when none is selected explicitly.
- `allowed` lists the entry names that may be selected. Unset allows every entry.

Unknown fields are a parse error, so a typo like `defualt:` doesn't silently do
nothing.

**Names are local aliases.** The server only ever sees the key, which determines
the org. Names exist so files and flags stay readable (`--org acme` instead of
`--org 12`) and so one user can hold two keys for the same org (`acme`,
`acme-ci`) or the same org id on two servers (`acme`, `local`). The id is kept
in the entry so the file says which org a key is for, so `login` can recognise
an existing entry, and so commands can build web UI links (`?org=12`). Because
names never reach the server, there's no need for a unique server-side slug.

Keys are read as is: there is no file-permission check. `login` creates
`$CONFIG_DIR/.gritz.yml` with mode `0600`.

### Discovery and merge

Starting at the working directory, walk up one directory at a time to `/`,
collecting every `<dir>/.gritz.yml` that exists. The walk does not stop at a git
root. `$CONFIG_DIR/.gritz.yml` is always merged as the base layer, beneath
everything the walk found. `$CONFIG_DIR` is `configfile.Dir()`, unchanged:
`GRITZ_CONFIG_DIR`, else `os.UserConfigDir()/gritz`. A `.gritz.yml` path that exists
but isn't a regular file is an error.

Layers merge from the base up, so nearer files win:

- `default` and `allowed` each come from the nearest file that sets them.
- `orgs` merges by name. A nearer entry replaces a farther one with the same
  name whole; fields are not merged across files, so an id from one file can't
  be paired with a key from another.

The merged result remembers which file set `default`, `allowed` and each entry,
for error messages.

The intended layering:

- `$CONFIG_DIR/.gritz.yml` holds the user's entries and keys, written by `login`.
- A `.gritz.yml` checked into a repo sets `default`, and optionally `allowed`, using
  names the team agrees on. It holds no keys.
- A `.gritz.yml` in an uncommitted parent directory (`~/work/acme/.gritz.yml`) can pin
  everything below it, including several repos.

Nothing stops a committed `.gritz.yml` from holding `orgs` with keys; that's the
same risk as committing any other file containing a secret.

### Selection

Every user-facing command selects one entry at startup:

1. If `--key` / `GRITZ_API_KEY` is set, use that key, and skip the files,
   entries and `allowed` entirely (see Explicit key below).
2. Load and merge the files. A `default` that isn't in the merged `allowed` is
   an error, whether or not it's the entry being selected:

   ```
   error: default "acme" (from /home/me/src/labs/.gritz.yml) is not in allowed [labs] (from /home/me/src/labs/.gritz.yml)
   ```

3. The selected name is `--org` / `GRITZ_ORG` if set, else the merged `default`.
   With neither:

   ```
   error: no org selected: pass --org or set default in a .gritz.yml file
   ```

4. If `allowed` is set and doesn't contain the name, fail, naming the file that
   set `allowed`:

   ```
   error: org "local" is not allowed here: allowed is [acme] (from /home/me/src/acme/.gritz.yml)
   ```

5. If no merged entry has the name, fail, pointing at `login`:

   ```
   error: no org entry named "acme": run `gritz login --key <key> --name acme`
   ```

6. The entry supplies the org id, the key and the server. An entry without `id`
   or `key` is an error naming the file it came from.

**Server URL.** `--server` > `GRITZ_SERVER` > the selected entry's `server` >
`gritzclient.DefaultURL`. Every user-facing command gets the `GRITZ_SERVER` env
source; `mcp` and `notify` lack it today.

**Explicit key.** `--key` / `GRITZ_API_KEY` supplies a raw key directly. The key
determines the org server-side, and the server is `--server` > `GRITZ_SERVER` >
`gritzclient.DefaultURL`. `--org` / `GRITZ_ORG` and every `.gritz.yml` file are
ignored. This is the path for CI and for runners deployed with an injected key.

### API change: report the caller's org

`login` has to learn which org a key belongs to. Nothing returns that today:
`GetProfileResponse.default_org_id` is the user's web-UI default, not the org
the request authenticated as, and `GetProfile` reads the caller's user, which
an API key doesn't have. Add a dedicated RPC:

```proto
rpc GetCaller(GetCallerRequest) returns (GetCallerResponse);

message GetCallerRequest {}

message GetCallerResponse {
  Org org = 1; // the org the request is authenticated against
}
```

`apiserver.GetCaller` loads the org from `apiauth.MustCaller(ctx).OrgID` with no
user lookup, so it works for API keys, web UI sessions and MCP OAuth tokens
alike. `GetProfile` is unchanged.

### CLI commands

**`gritz login --key <key> [--name <name>] [--server <url>]`**

1. Call `GetCaller` with the key against `--server` (else `GRITZ_SERVER`, else
   `gritzclient.DefaultURL`). A failure means the key is invalid; report it and
   write nothing.
2. The org id and name are `resp.org.id` and `resp.org.name`.
3. The entry name is `--name`, else a slug of the org name: lowercase, each run
   of non-alphanumeric characters becomes a single `-`, and leading and trailing
   `-` are trimmed (`Acme Labs` → `acme-labs`). An empty slug is an error asking
   for `--name`.
4. Load `$CONFIG_DIR/.gritz.yml` alone, not the merged view. If it has an entry with
   that name:
   - for the same id and server, replace its key;
   - for a different id or server, fail: `entry "acme" is org 31 on https://gritz.dev; pass --name to add org 12 under another name`.
5. Otherwise add `{id, key, server}` under the name, writing `server` only when
   it isn't `gritzclient.DefaultURL`.
6. If the file has no `default`, set it to the entry's name, so the first org
   added becomes the default. An existing `default` is never changed.
7. Write the file to a temp file in `$CONFIG_DIR` and rename it into place,
   creating it `0600` if it didn't exist. The edit goes through a `yaml.Node`
   so the user's comments and key order survive.
8. Print `Logged in to Acme Labs (org 12) as "acme-labs"` and, when step 6 set
   it, `Default org set to "acme-labs"`.

The key is taken from `--key` only; there is no stdin or TTY prompt. `--key`
here reads the same `GRITZ_API_KEY` env source as every other command, so
`GRITZ_API_KEY=... gritz login` also works. Apart from step 6, `login` never
touches `default`, and it never touches `allowed`.

There's no `logout` and no `org` subcommand. Removing an entry, switching the
default or restricting `allowed` is an edit to a `.gritz.yml` file.

**Existing commands**

Each user-facing command gets the same three flags, defined once by
`configfile.Flags()` and appended to each command's `Flags`:

| Flag | Env | Notes |
|---|---|---|
| `--org` | `GRITZ_ORG` | new; an entry name |
| `--key`, `-k` | `GRITZ_API_KEY` | replaces `mcp`/`notify` `--token`/`GRITZ_TOKEN` |
| `--server`, `-s` | `GRITZ_SERVER` | unchanged, plus the env source on `mcp`/`notify` |

Each command's action replaces its `configfile.Load` + `cfg.Token == ""` block
(or `cmd.String("token")`) with one `configfile.Resolve(ctx, cmd)` call, which
runs the selection above and returns the entry to use. The command builds its
client options inline from it,
`gritzclient.Options{BaseURL: e.Server, Token: e.Key, ...}`, and the same way
for `NotificationClientOptions` and `shell.Attach`:

- `task list`, `task create`, `task update`, `logs`, `shell`: drop the
  `configfile.Load(nil)` blocks. `shell` also passes the selected key to
  `shell.Attach` in place of `cfg.Token`.
- `mcp`: `--token` (required) is replaced by selection. A Claude Code MCP config
  of `{"command": "gritz", "args": ["mcp"]}` launched from a repo picks up the
  repo's `default` without a token in the config. The notification client gets
  the same key and server.
- `notify`: same change as `mcp`. Running one `notify` per org with `--org` is
  how to watch several orgs.
- `runner`: `configfile.Load(&Overrides{Token: ...})` is replaced by the same
  selection, so a runner on a laptop uses an entry from `.gritz.yml`. Deployed
  runners keep using `GRITZ_API_KEY`.

**Not changed:** `gritz tool agent-mcp`, the GitHub MCP proxy and the git
credential helper run inside the sandbox with the task JWT on
`--token`/`GRITZ_TOKEN`. They never read `.gritz.yml`, and they keep their flags.
`server`, `driver`, `containers`, `workspaces`, `download` and `version` don't
authenticate as a user.

### Dropped state

Per the no-backwards-compatibility policy there's no import path:

- `$CONFIG_DIR/config.json` (`configfile.File{Token}`) is no longer read, and
  its code is deleted. Users run `gritz login` once per org.
- `--token` / `GRITZ_TOKEN` on `gritz mcp` and `gritz notify` are removed.
  MCP configs that pass `--token` need the flag dropped, or switched to
  `--key` / `GRITZ_API_KEY`.
- The "run setup first" error messages go away with the `configfile.Load` call sites.

`workspaces.yaml` and `prebuilt/` stay in `$CONFIG_DIR`, and `configfile.Dir()`
doesn't change. The old `config.json` isn't deleted automatically; it's just
ignored.

### API

```go
package configfile // github.com/gritzapp/gritz/internal/configfile

// Dir returns the gritz config directory: GRITZ_CONFIG_DIR, else os.UserConfigDir()/gritz.
func Dir() (string, error) // unchanged

// Name is the file name used at every level.
const Name = ".gritz.yml" // new

// Entry is one named org in a .gritz.yml file.
type Entry struct { // new
	// ID is the org id.
	ID int64 `yaml:"id"`
	// Key is an API key for the org.
	Key string `yaml:"key"`
	// Server is the server URL; empty means gritzclient.DefaultURL.
	Server string `yaml:"server,omitempty"`
}

// File is the contents of one .gritz.yml file.
type File struct { // replaced: was {Token}
	// Default is the entry name selected when none is given.
	Default string `yaml:"default,omitempty"`
	// Allowed lists the entry names that may be selected; nil allows all.
	Allowed []string `yaml:"allowed,omitempty"`
	// Orgs maps local names to entries.
	Orgs map[string]Entry `yaml:"orgs,omitempty"`
}

// Read parses the .gritz.yml file at path, rejecting unknown fields.
func Read(path string) (*File, error) // new

// Merged is the result of merging .gritz.yml files, nearest winning.
type Merged struct { // new
	Default     string
	DefaultFrom string // path of the file that set Default
	Allowed     []string
	AllowedFrom string // path of the file that set Allowed
	Orgs        map[string]Entry
	OrgsFrom    map[string]string // entry name -> path of the file that set it
}

// Load merges $CONFIG_DIR/.gritz.yml and every .gritz.yml from dir up to /.
func Load(dir string) (*Merged, error) // replaced: was Load(*Overrides) (*File, error)

// Login writes entry under name into $CONFIG_DIR/.gritz.yml, replacing the key of
// an existing entry with the same id and server, and failing with ErrNameTaken
// if name belongs to a different org or server. It sets default to name when
// the file has none. The caller validates the key.
func Login(name string, entry Entry) error // new

// ErrNameTaken is returned by Login when name is in use by another org or server.
var ErrNameTaken = errors.New("entry name in use") // new

// Selection is the entry a command will use.
type Selection struct { // new
	// Name is the entry name.
	Name string
	// Entry is the selected entry.
	Entry Entry
}

// Select picks the entry named org, or m.Default when org is empty, enforcing
// m.Allowed and returning errors that name the file responsible.
func (m *Merged) Select(org string) (*Selection, error) // new

// Flags returns the --org, --key and --server flags (GRITZ_ORG, GRITZ_API_KEY,
// GRITZ_SERVER) shared by user-facing commands.
func Flags() []cli.Flag // new

// Resolve applies --key, --server and --org from cmd and the merged .gritz.yml
// files from the working directory, and returns the entry to use. Server is
// always set (gritzclient.DefaultURL when no layer sets it). With --key, the
// entry has only Key and Server.
func Resolve(ctx context.Context, cmd *cli.Command) (*Entry, error) // new

// Slug turns an org name into an entry name, returning "" if nothing is left.
func Slug(orgName string) string // new

type Overrides struct{ ... }       // removed
func Path() (string, error)        // removed
func Save(f *File) error           // removed
```

```go
package command // github.com/gritzapp/gritz/internal/command

var LoginCommand *cli.Command // new: gritz login
```

**How it fits:** `configfile` owns everything about how a command finds its
credentials: the file format, discovery, merging, selection, the shared flags,
and the `login` write. `Merged.Select` holds the selection rules and takes no
urfave/cli types, so it can be table-tested against a `Merged` value.
`Resolve` is the only part that reads the command: it takes `--key` as is, or
calls `Load` with the working directory and `Select` with `--org`, then applies
the server precedence and returns the `Entry`. `configfile` doesn't build
client options; it references `gritzclient` only for the `DefaultURL` constant,
and there's no helper in `gritzclient` either. Every user-facing command
appends `configfile.Flags()...`, calls `configfile.Resolve` in its action, and
builds `gritzclient.Options` (and `NotificationClientOptions` or the
`shell.Attach` arguments) inline from the entry's `Server` and `Key`. `LoginCommand` calls `GetCaller`,
`configfile.Slug` and `configfile.Login`.

## Implementation Plan

1. **Report the caller's org** — Delivers: the `GetCaller` RPC (proto,
   regenerated code, `apiserver.GetCaller`). Depends on: nothing.
   Verifiable by: an e2e API test where `GetCaller` with a key in a
   non-default org returns that org.
2. **`.gritz.yml` load, merge and select** — Delivers: `configfile.Entry`, the new
   `File` shape under a temporary name beside the old one, `Read`, `Load`
   under a temporary name, `Merged.Select` and `Selection`, and `Slug`.
   Nothing calls them yet.
   Depends on: nothing. Verifiable by: unit tests over temp dirs: the walk
   merges past a `.git` directory up to the temp root; the base layer is always
   merged; `default`/`allowed` come from the nearest file; entries replace
   whole by name; a `default` outside `allowed`, a disallowed `--org` and a
   missing entry each fail naming the right file (table tests on `Select`
   over `Merged` values); unknown fields fail.
3. **`gritz login`** — Delivers: `configfile.Login`, `LoginCommand` and
   `main.go` registration. Depends on: (1), (2). Verifiable by: unit tests for
   `Slug` and for `Login` (new entry, key replaced for same id and server,
   `ErrNameTaken` otherwise, `default` set only when absent, comments preserved); an e2e test that creates a
   key, runs `gritz login --key`, and checks the written entry.
4. **Wire existing commands and delete the old config** — Delivers:
   `configfile.Flags`/`configfile.Resolve`; `task`, `logs`, `shell`, `mcp`, `notify` and
   `runner` switched over; `mcp`/`notify` `--token` removed; the old
   `File`/`Load`/`Save`/`Overrides`/`Path` and `config.json` handling deleted
   and the step-2 names taken over; docs updated (`reference/cli`, `using/cli`,
   `integrations/mcp`, `operators/runner`). Depends on: (2), (3).
   Verifiable by: an e2e test that logs in to two orgs and runs
   `gritz task list` from a directory whose
   `.gritz.yml` sets `default` to org A while the base default is org B, and sees
   only org A's tasks; and one where `--org` names an entry outside `allowed`
   and fails.

## Trade-offs

- **One format at every level vs. a global config plus a repo file.** Separate
  files let the repo file be locked down to `org:` only, but they need two
  parsers, two sets of rules and a key store on the side. One format with one
  merge rule is simpler to explain and lets any directory, not just a repo,
  carry a full configuration. The cost is that nothing structurally prevents a
  key in a committed file.
- **Walk to `/` vs. stop at the git root.** Stopping at `.git` scopes a file to
  its repo, but it rules out a parent directory pinning several checkouts
  (`~/work/acme/.gritz.yml`). Because files merge rather than the nearest one
  winning outright, a stray parent `.gritz.yml` can only supply defaults that
  nearer files override, and `allowed` lets a repo refuse entries it doesn't
  expect.
- **Whole-entry vs. field-wise merge of `orgs`.** Field-wise merging would let
  a committed file declare `acme: {id: 12}` and a private one add the key, but
  it would also let a key from one file be used under an id or server from
  another. Whole-entry replacement keeps each entry self-consistent.
- **Local names vs. org ids.** Server-side org names aren't unique and can be
  renamed, and adding a unique slug column is a schema change with its own UX.
  Local names sidestep both: the user chooses them, they never reach the
  server, and `login` derives a sensible default.
- **Keys in the config file vs. a separate key store or OS keychain.** A
  keychain is more secure at rest but adds a cgo or D-Bus dependency and fails
  in headless and container environments. Keeping the key in the entry means
  one file holds everything about an org, which is what makes hand-editing the
  whole workflow. No permission check is enforced; `login` creates the file
  `0600`.
- **YAML vs. JSON vs. TOML.** YAML is already a dependency, used for
  `workspaces.yaml`, and allows comments in a committed `.gritz.yml`. JSON doesn't
  allow comments, and TOML would add a dependency.
- **One `login` command vs. a full `logout`/`org` command set.** Commands to
  list, remove and switch entries would each be a thin wrapper over an edit to
  a short YAML file. Leaving them out keeps the CLI surface to one command,
  and `login` exists only because it's the step that needs the server.
- **Login by key vs. browser flow.** A browser OAuth/PKCE flow (reusing
  `internal/auth/oauthflow`) would be smoother, but it needs a server-side
  key-minting endpoint and a loopback redirect listener. A key from
  `/ui/keys/new` works with one proto field, and a browser flow can later be
  layered on top to produce the same entry.
