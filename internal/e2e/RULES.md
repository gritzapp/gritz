# e2e harness rules

Rules for `internal/e2e` and the tests built on it. The design is in
`proposals/draft/e2e-harness.md`.

## What's real

- Use real code everywhere: the API server and its auth, the store (`teststore`
  Postgres), the runner, the driver. Prefer a few real tests over many small
  ones.
- Fakes only at boundaries we don't control: the **sandbox** (the local backend)
  and the **agent** (the puppet agent). Nothing else gets a second
  implementation.
- The **driver is a black box**: no handle, no hooks, no access to its
  internals. Observe it only through its effects: what it reports to the server
  and what its runs leave in the sandbox.

## Shape

- One harness per test (`e2e.New(t, opts)`), created inside that test, so tests
  can run in parallel.
- Handles: `h.Server`, `h.Org` (the default org), and later `h.Backend` and
  `h.Runner`. Handles hold `t`, and their methods fail the test with `t.Fatal`
  instead of returning errors.
- More orgs come from `h.Server.CreateOrg()`. Users are org owners
  (`org.Owner`); to put a user in another org, add an owner by email with
  `AddOrgMember`. No separate user provisioning.
- Name methods that persist something `Create*`, matching `teststore`.

## Act through the client

- The call under test (`// Act`) always goes through a `gritzclient.Client`:
  `org.Client`, `member.Client`, `key.Client` or `h.Server.Client(token)`. The
  test asserts on that call's own response or error.
- Harness methods are for arranging and asserting only.

## Harness methods

- A shorthand method for an RPC takes that RPC's **proto request** and returns
  the **useful value**: a wrapper when there's behaviour to attach (`*Task`,
  `*Key`, `*Member`), otherwise the plain value (a token string, a
  `*gritzv1.Task`, a slice).
- Wrappers made by a single RPC carry its proto response in `Resp`.
- Methods that are more than one RPC keep their own shapes (`CreateOrg`,
  `WaitTask`, `WaitEvent`).
- Go through the real RPCs, the ones the web UI and MCP tools use. Write to the
  store directly only when no RPC exists for it, and say so in the method's doc
  comment.
- Don't add a method until a test uses it.

## Mocks

- `h.Server.Mock(m *gritzclient.ClientMock)` replaces every method whose
  `<Method>Func` is set, for every caller (runner, driver, test), until
  `h.Server.Mock(nil)`. There is no call-through to the real method.
- Mocks are for injecting failures, not for standing in for behaviour.
- A mock skips the handler's own authorization checks: callers are still
  authenticated, but scopes aren't checked.

## Assertions

- Assert on observable outcomes: task state and events through the API, sandbox
  state, what the agent did. Never on `runner.db` or runner or driver internals.
- Wait for outcomes with the `Wait*` helpers. They poll with a timeout and
  report the last state seen. Don't sleep.
