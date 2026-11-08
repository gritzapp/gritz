# API reference docs from the proto

Issues: https://github.com/gritzapp/gritz/issues/2098,
https://github.com/gritzapp/gritz/issues/2106

## Problem

Everything gritz does goes through one Connect service,
[`gritz.v1.GritzService`](https://github.com/gritzapp/gritz/blob/master/proto/gritz/v1/gritz.proto):
54 RPCs, 138 messages and 3 enums. API keys exist so that programs can
call it, but docs.gritz.dev documents only the CLI, the MCP tools and the
webhook envelopes. Someone writing a client has to find the 950-line proto file
in the repository, and learn the endpoint and authentication from the code.

The proto file was also not written to be read by them. Of its ~160 comment
lines, many were contributor notes: links to proposal files by repo path
(`docs/src/content/docs/proposals/implemented/unified-task-event-stream.md`),
history (`the stream replacement for the old logs rows with type='llm'`,
`Previously: parent (child tasks removed)`) and storage details (`the db type
column is materialized from the arm`). Most messages and RPCs had no comment
at all.

## Design

### Overview

- One hand-written page, `reference/api` (`.mdx`), covers what the proto can't:
  the endpoint, the protocols, authentication and a `curl` example.
- The same page embeds the whole of `proto/gritz/v1/gritz.proto`, imported as a
  string at docs build time and rendered with Starlight's `<Code>` component
  with protobuf highlighting. The reference is the proto itself, as a client
  author would read it to generate a client.
- Nothing is generated or checked in, so there is nothing to keep in sync: the
  published page is always built from the proto on the branch being built.
- The proto comments are the reference text. They follow the docs rules;
  contributor notes sit in detached comments above the element.

`driver.v1` (`proto/driver/v1/driver.proto`) is the runner↔driver protocol
inside a sandbox, not something a user or operator calls. It is not embedded.

### `reference/api`

A page in the Reference section, and the entry point from the rest of the docs:

- **Endpoint**: the server's base URL; each RPC is `POST
  <server>/gritz.v1.GritzService/<Method>`.
- **Protocols**: Connect (JSON or binary protobuf), gRPC and gRPC-Web, as
  served by `connect-go`; JSON field names are the camelCase form of the proto
  names.
- **Authentication**: `Authorization: Bearer <API key>`, linking
  [API keys](/concepts/orgs-members-keys/#api-keys). `concepts/orgs-members-keys`
  links back to the page.
- **Example**: `curl` calling `Ping` and `GetCaller` with JSON. Both work with
  an API key (`GetProfile` needs a signed-in user), and together they check the
  connection, the server version and which organisation the key acts for.
- **Service definition**: the embedded proto.

The embed is two lines of MDX:

```mdx
import { Code } from '@astrojs/starlight/components';
import proto from '../../../../../proto/gritz/v1/gritz.proto?raw';

<Code code={proto} lang="protobuf" title="proto/gritz/v1/gritz.proto" />
```

Vite's `?raw` import reads the file at build time; no Astro or Vite config is
needed, in `pnpm build` or the dev server. The docs workflows check out the
whole repository, so the proto is there in both `docs-pages.yml` and
`docs-preview.yml`. `docs-preview.yml` only runs for PRs touching `docs/` or
`diagrams/`, so a PR that only edits the proto gets no preview; the page still
updates when it merges.

### Comments

The proto comments are published as written, so CLAUDE.md's "What belongs in
the docs" rules apply to them:

- RPC, message, field and enum names are the reader's interface: a client calls
  `CreateTask` with a `CreateTaskRequest`. Naming them is not an implementation
  detail.
- The comments' contents are held to the rules: no history, no storage or
  package names, no proposal paths. Contributor notes go in a detached comment
  (separated from the element by a blank line), which reads as a note about the
  code rather than about the next element. On a page showing the whole file,
  detached comments are still visible; the split keeps the documentation of
  each element clean, not hidden.
- Runner- and driver-facing RPCs (`RegisterWorkspaces`, `SubmitRunnerEvents`,
  `UploadLogs`, `AppendLogChunk`, `CreateTaskToken`, …) are on the same service
  and callable with an API key, so they carry a comment saying who calls them
  ("Called by the runner …").

CLAUDE.md's docs mapping table gets one row: `proto/gritz/v1/gritz.proto` and
the server's Connect endpoint and auth → `reference/api`, noting that the proto
is embedded at build time so its comments are published.

## Implementation Plan

1. **Proto comment cleanup** (done, #2108) — Delivers: every comment in
   `gritz.proto` that broke the docs rules moved into a detached comment or
   rewritten (proposal paths, history, storage details, the `auto_archive`
   block layout turned into a sentence); one-line comments on the service and
   on runner/driver-facing RPCs saying who calls them. Depends on: nothing.
   Verifiable by: `go tool buf lint`, and generated Go code unchanged apart from
   the descriptor's source info.
2. **`reference/api` with the embedded proto** — Delivers: removal of the page
   generator that an earlier version of this plan added (`internal/protodocs`,
   `cmd/protoc-gen-gritz-docs`, its `buf.gen.yaml` entry, the generated
   `api/` pages, their sidebar group and llms.txt set, and its CLAUDE.md text);
   the hand-written `reference/api` page (endpoint, protocols, auth, `curl`
   example, the embedded proto), linked from `concepts/orgs-members-keys`; its
   CLAUDE.md mapping-table row. Depends on: (1). Verifiable by: `mise run
   generate` producing the Go and TS code with no diff; `cd docs && pnpm build`
   passing the links validator; the `curl` example against a local server.

## Trade-offs

- **A generator writing a page per service, message and enum** (an in-repo
  `protoc-gen-gritz-docs` plugin, modelled on the
  [Buf Schema Registry](https://buf.build/docs/bsr/documentation/) layout, with
  field tables, type links and "Used by" back-links). This was the first plan,
  and its message and enum pages were built. Dropped for the embed:
  - **Simpler.** The generator was ~400 lines of Go plus tests, a third buf
    plugin, 141 checked-in pages, a sidebar group and an llms.txt set, with
    service pages, back-links and comment references still to come. The embed
    is one MDX page.
  - **Can't drift.** Generated pages were checked in, so they could fall behind
    the proto, and needed a CI job that regenerates them and fails on a diff.
    The embed is built from the proto at docs build time, so there is no copy
    to fall behind and no sync check to run.
  - **Fewer rules on comments.** Field comments rendered in table cells had to
    be inline Markdown, and generation failed on block syntax. In the embedded
    file a comment is just a comment.
  - **Cost:** no page or search hit per type, no field tables, no links between
    types or back-links. A reader finds a type with the browser's search on the
    page. For a client author who will generate a client from the proto anyway,
    the proto is the reference they need.
- **protoc-gen-doc with a custom template**: renders one file per template, so
  splitting into pages means post-processing; no back-link index; Go templates
  over its own data model rather than the descriptors. Rejected in #2098.
- **OpenAPI + `starlight-openapi`**: Connect's HTTP shape is one `POST` per RPC,
  so the result reads as 54 identical endpoints with schemas, not services,
  messages and enums. It also needs a proto → OpenAPI step. Rejected in #2098.
- **Linking to the proto on GitHub** instead of embedding it: no build-time
  import at all, but the docs would point off-site for the reference, and it
  would be missing from the site's search and from `llms-full.txt`.

## Open Questions

- Should runner- and driver-facing RPCs be left out of the published reference?
  They are documented, since they're callable with an API key. Hiding them would
  mean splitting them into a second service, which is a bigger change.
- Should the page split the proto into sections (services, then messages, then
  enums) or link RPC names to their messages? That would mean parsing the proto
  at build time, which brings back some of the generator's complexity.