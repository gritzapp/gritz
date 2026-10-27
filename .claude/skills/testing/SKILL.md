---
name: testing
description: Guidelines for writing Go tests. Apply when creating or modifying test files.
---

# Testing Guidelines

## Test Structure

Use the **Arrange, Act, Assert** pattern. Separate each phase with a blank line or comment.

```go
func TestGetTask_Permissions(t *testing.T) {
    t.Parallel()
    // Arrange
    srv := setupTestServer(t)
    ctxA, _ := createTestOrg(t, srv, testOrgOptions{Workspaces: true})
    ctxB, _ := createTestOrg(t, srv, testOrgOptions{Workspaces: true})
    resp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
        Name: "User A's Task", Runner: "test-runner", Workspace: "test-workspace",
    })
    assert.NilError(t, err)

    // Act - User B tries to get User A's task
    _, err = srv.GetTask(ctxB, &gritzv1.GetTaskRequest{Id: resp.Task.Id})

    // Assert
    assert.ErrorContains(t, err, "not found")
}
```

## Test Naming

- Use `TestFunctionName` for the happy path.
- Use `TestFunctionName_Scenario` for specific cases (e.g. `TestCreateTask_BadRunner`).
- Keep names concise and descriptive.

## Subtests

Only use `t.Run` for table-driven tests where you're iterating over a slice of test cases. Do NOT use `t.Run` to group individual cases -- write separate top-level test functions instead.

```go
// Good: table-driven
func TestValidate(t *testing.T) {
    tests := []struct {
        name  string
        input string
        err   string
    }{
        {"empty", "", "required"},
        {"too long", strings.Repeat("x", 256), "too long"},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := validate(tt.input)
            assert.ErrorContains(t, err, tt.err)
        })
    }
}

// Bad: don't wrap individual cases in t.Run
func TestCreateTask(t *testing.T) {
    t.Run("bad runner", func(t *testing.T) { ... })    // NO
    t.Run("bad workspace", func(t *testing.T) { ... }) // NO
}
```

## Server Test Helpers

- `setupTestServer(t)` creates a server with a clean database connection.
- `createTestOrg(t, srv, testOrgOptions{})` returns `(context.Context, *testOrg)`:
  - `testOrgOptions{Workspaces: true}` registers default workspaces (`test-runner`, `runner-1`, `runner-2` with `test-workspace`, `workspace-1`, `workspace-2`, `default`).
  - `testOrgOptions{}` creates only the user and org, no workspaces.
- `testOrg` has `UserID` and `OrgID` fields.

## Interface Test Doubles

When a test needs a stand-in for an interface, **generate a mock with moq** -- do NOT hand-write a `fakeX` struct that implements the interface. This is a near-absolute default; a hand-written fake is only acceptable in genuinely exceptional cases.

Add a `//go:generate` directive next to the interface and run `go generate ./...`. Generated mocks live beside the interface as `*_moq.go` (importable from production code) or `*_moq_test.go` (test-only). For example, `internal/server/notifyserver/notifyserver.go` declares:

```go
//go:generate go tool moq -out org_resolver_moq_test.go . OrgResolver
```

which generates `org_resolver_moq_test.go` containing `OrgResolverMock`. A test sets the mock's `...Func` fields to control behavior and asserts against the generated `...Calls()` accessor:

```go
// Good: configure behavior via the generated Func field
mockedOrgResolver := &OrgResolverMock{
    ResolveOrgFunc: func(ctx context.Context, userID string, orgID int64) (int64, error) {
        return orgID, nil
    },
}

srv := New(Options{OrgResolver: mockedOrgResolver})
// ... exercise srv ...

// assert the method was called exactly once (see "Asserting on Mock Calls")
assert.Assert(t, cmp.Len(mockedOrgResolver.ResolveOrgCalls(), 1))

// Bad: hand-written fake implementing the interface
type fakeOrgResolver struct{}
func (fakeOrgResolver) ResolveOrg(ctx context.Context, userID string, orgID int64) (int64, error) {
    return orgID, nil
}
```

See `internal/gritzclient/client_moq.go` (`ClientMock`) for a checked-in `*_moq.go` example.

## Extending Generated Mocks

Hand-written helper methods on a generated mock live in a sibling `*_moq_ext.go` file named after the generated file — extensions to `client_moq.go` belong in `client_moq_ext.go`, in the same package. Never add them to the `*_moq.go` file itself: `go generate` regenerates it and clobbers manual edits. For a test-only mock generated as `*_moq_test.go`, extensions go in `*_moq_ext_test.go`.

Reach for an extension when multiple tests (especially across packages) re-implement the same call-log extraction. An extension flattens or projects recorded calls into a shape one assertion can cover. It returns data — it never takes a `*testing.T` (a non-test file importing `testing` links it into production builds) and never asserts itself:

```go
// client_moq_ext.go

// SubmittedRunnerEvents returns every runner event submitted across all
// SubmitRunnerEvents calls, flattened in submission order.
func (m *ClientMock) SubmittedRunnerEvents() []*gritzv1.RunnerEvent {
	var events []*gritzv1.RunnerEvent
	for _, call := range m.SubmitRunnerEventsCalls() {
		events = append(events, call.SubmitRunnerEventsRequest.GetEvents()...)
	}
	return events
}
```

which collapses the per-call count-and-index dance into one comparison:

```go
// Good: one whole-stream comparison via the extension
assert.DeepEqual(t,
	mock.SubmittedRunnerEvents(),
	[]*gritzv1.RunnerEvent{
		{TaskId: 1, Version: 7, Event: "started"},
		{TaskId: 1, Version: 7, Event: "stopped"},
	},
	protocmp.Transform(),
)

// Bad: cmp.Len plus an indexed DeepEqual per call against each request wrapper
calls := mock.SubmitRunnerEventsCalls()
assert.Assert(t, cmp.Len(calls, 2))
assert.DeepEqual(t,
	calls[0].SubmitRunnerEventsRequest,
	&gritzv1.SubmitRunnerEventsRequest{Events: []*gritzv1.RunnerEvent{{TaskId: 1, Version: 7, Event: "started"}}},
	protocmp.Transform(),
)
assert.DeepEqual(t,
	calls[1].SubmitRunnerEventsRequest,
	&gritzv1.SubmitRunnerEventsRequest{Events: []*gritzv1.RunnerEvent{{TaskId: 1, Version: 7, Event: "stopped"}}},
	protocmp.Transform(),
)
```

Name extensions so they cannot collide with what moq generates for the interface (`Foo`, `FooFunc`, `FooCalls` per method): `SubmittedRunnerEvents`, not another `SubmitRunnerEvents*` variant.

Do NOT add an extension overfit to one test case. An extension must be a general projection of the call log — whole recorded values that other tests can assert against too — not a narrow slice of it that only one test wants. If only one test needs it, keep a test-local helper (or assert inline); if the projection drops most of the data, it's too narrow to be an extension:

```go
// Good: general — returns the whole events; any test can compare any fields
func (m *ClientMock) SubmittedRunnerEvents() []*gritzv1.RunnerEvent

// Bad: overfit — a single-field projection only one test will ever want
func (m *ClientMock) SubmittedRunnerEventVersions() []int64
```

## Asserting on Mock Calls

Assert on a mock's `...Calls()` log instead of hand-rolling `len(...)` count checks. Use `cmp.Len` from `gotest.tools/v3/assert/cmp` for the count:

```go
assert.Assert(t, cmp.Len(sender.SendChannelCalls(), 0))  // len == 0
assert.Assert(t, cmp.Len(sender.SendChannelCalls(), 1))  // len == n
```

Store the log in a local, assert its length, then index into it directly. Once `cmp.Len` (or any `len(...)` check) has fixed the count, plain `calls[i]` provably can't be out of bounds -- so chain field access or a `DeepEqual` off it:

```go
// Bad: re-invokes the accessor and hand-rolls the count
assert.Equal(t, len(sender.SendChannelCalls()), 1)
assert.Equal(t, sender.SendChannelCalls()[0].P.Content, "Task 7 completed.")

// Good: store the log once, assert count, then index directly
calls := sender.SendChannelCalls()
assert.Assert(t, cmp.Len(calls, 1))
assert.DeepEqual(t,
    calls[0].P,
    mcpchannel.Params{Content: "Task 7 completed."},
)
```

### When to use `testx.At`

Reach for `testx.At` from `internal/x/testx` **only** when you index into a call log *without* first asserting its length -- it fails the test gracefully (via `t.Fatal`) instead of panicking on an out-of-bounds access:

```go
// No cmp.Len guard, so index safely: testx.At fails the test if the call is absent.
call := testx.At(t, sender.SendChannelCalls(), 0)
```

Do NOT use `testx.At` after you've already asserted the length with `cmp.Len` (or any `len(...)` check) on the same slice. The bound is already guaranteed, so plain `calls[i]` is clearer and pairing the two is redundant noise.

When a recorded argument is a **named** struct and you care about *several* of its fields, compare against a literal with `internal/x/cmpx`. `cmpx.OnlyFields` is the inverse of `cmpopts.IgnoreFields` -- it ignores everything *except* the named fields, so the selection documents what the test actually checks:

```go
assert.DeepEqual(t,
    calls[0].P,
    mcpchannel.Params{Content: "Task 7 completed.", Priority: 2},
    cmpx.OnlyFields("Content", "Priority"),
)
```

When the test cares about only a **single** field, do NOT wrap it in the `DeepEqual` + `OnlyFields` machinery -- index into that field and assert on it directly. This is shorter and reads better:

```go
// Good: one field, asserted directly
assert.DeepEqual(t, got.Muted, []int64{3, 9})
assert.Equal(t, calls[0].P.Content, "Task 7 completed.")

// Bad: whole-struct compare with a one-field selector
assert.DeepEqual(t, got, muteState{Muted: []int64{3, 9}}, cmpx.OnlyFields("Muted"))
```

So: reach for `OnlyFields` when you keep a few of many fields; when you ignore only one or two, `cmpopts.IgnoreFields` is shorter; and when you keep exactly one, assert on the field directly. `OnlyFields` does not apply to moq's anonymous per-call arg structs (you cannot write a clean literal for an unnamed type) -- assert those fields individually, or project a single field across the whole call-log with `testx.ExtractField` (see below).

The same rule covers decoding a tool result into a named struct (e.g. via `mcptest.UnmarshalCallToolResult`): assert on the decoded field you care about (`got.Muted`) rather than rebuilding the whole struct to compare with `OnlyFields`.

### Projecting one call-log field with `testx.ExtractField`

When you assert on a **single field across every call** in an anonymous moq call-log -- e.g. each call's `InstallationID` -- `testx.ExtractField` folds the count check and the per-call field asserts into one declarative comparison. It reflects over the log and projects the named field into a concrete typed slice (`[]int64`, etc.) that you compare whole with `assert.DeepEqual`:

```go
// Bad: a count assert plus a per-call field assert
calls := gh.VerifyInstallationAccessCalls()
assert.Assert(t, cmp.Len(calls, 1))
assert.Equal(t, calls[0].InstallationID, installationID)

// Good: one assertion covers both the call count and the field values
calls := gh.VerifyInstallationAccessCalls()
assert.DeepEqual(t, testx.ExtractField(calls, "InstallationID"), []int64{installationID})
```

`ExtractField(calls any, name string) any` returns the projected slice boxed in an `any` whose dynamic type is the field's own type, so go-cmp compares it against the expected `[]int64{...}` literal structurally and cleanly (a `[]any` or a wrapper struct would reintroduce a type-mismatch diff). This is precisely the case `OnlyFields` can't handle: you never spell the unnamed element type -- you name one field and compare a plain typed slice. The slice's length also encodes the call count, so no separate `cmp.Len` is needed.

Scope it to a single top-level field -- one `ExtractField` call per field you check. Bad input (a non-slice argument, or a field name absent from the element struct) panics with a descriptive message rather than failing the test, since the helper takes no `testing.TB`.

### Comparing proto messages

When the recorded argument (or any value) is a **protobuf message**, compare the whole message against a literal and pass `protocmp.Transform()` (`google.golang.org/protobuf/testing/protocmp`). It recurses into nested messages, so one whole-message compare covers the nested fields -- include every field that's actually set:

```go
calls := mock.SubmitRunnerEventsCalls()
assert.Assert(t, cmp.Len(calls, 2))
assert.DeepEqual(t,
    calls[0].SubmitRunnerEventsRequest,
    &gritzv1.SubmitRunnerEventsRequest{
        Events: []*gritzv1.RunnerEvent{{TaskId: 1, Event: "stopped"}},
    },
    protocmp.Transform(),
)
```

`protocmp.Transform()` is **required**, not optional: `assert.DeepEqual` runs go-cmp, and go-cmp refuses to touch the unexported internal state every proto embeds (`state`, `sizeCache`, `unknownFields`). Without the transform the assertion fails outright with `cannot handle unexported field at {*gritzv1.SubmitRunnerEventsRequest}.state`. The transform exposes the public fields (and applies proto equality semantics). Do **not** reach for `cmpx.OnlyFields` / `cmpopts.IgnoreFields` on a proto -- if a field is genuinely noise, drop it with `protocmp.IgnoreFields(&gritzv1.RunnerEvent{}, "field_name")` (proto snake_case names).

Whole-value `assert.DeepEqual` pays off for proto messages and **named** structs, where the expected literal is clean. It does **not** pay off for moq's anonymous per-call arg structs (per the note above) -- there the literal means re-spelling the unnamed type inline, which reads worse than asserting the fields individually (or projecting one field across the log with `testx.ExtractField`).

Put each `assert.DeepEqual` argument on its own line with a trailing comma, as above -- it stays readable as the comparison options grow and is gofmt-stable.

## Whole-Value Struct Assertions

The mock-call guidance above is one instance of a general rule: when a test makes **two or more** field-by-field asserts against the **same named struct value** -- a local var, a return value, a decoded result, or a recorded mock argument -- collapse them into a single whole-value `assert.DeepEqual` against a struct literal.

```go
// Bad: piecemeal asserts silently ignore every field they don't name
assert.Equal(t, apiErr.Op, "GetMicrovm")
assert.Equal(t, apiErr.StatusCode, http.StatusNotFound)

// Good: one literal covers the whole value and diffs on any mismatch
assert.DeepEqual(t, apiErr, &APIError{
    Op:         "GetMicrovm",
    StatusCode: http.StatusNotFound,
    Code:       "ResourceNotFoundException",
    Message:    "microvm mvm-x not found",
})
```

The literal must include **every field actually set** -- the whole-value compare covers strictly more than the piecemeal asserts, so it catches fields they ignored (here `Code` and `Message`) and prints a full diff on failure. That extra coverage is the point of collapsing, not just brevity.

**Check the target type for unexported fields first -- go-cmp panics on them.** If the struct has any, either `cmpopts.IgnoreUnexported(T{})` or whitelist the fields you assert with `cmpx.OnlyFields(...)` (see above). Named struct types with all-exported fields (most `model.*` and payload types) need neither.

**Drop only nondeterministic or generated fields** (timestamps, DB-assigned ids) with `cmpopts.IgnoreFields`, so the rest of the value is still checked whole. Comparing a stored-then-loaded row against the value you wrote is a clean fit:

```go
assert.DeepEqual(t, links[0], link,
    cmpopts.IgnoreFields(model.Link{}, "CreatedAt"),
)
```

Do **not** collapse when:

- the value is a **proto message** -- use the whole-message + `protocmp.Transform()` form above, never bare go-cmp;
- it's a **moq anonymous call-log struct** (`...Calls()[i]`) -- the unnamed type carrying a live `context.Context` has no clean literal; assert its fields individually, or project a single field across the log with `testx.ExtractField` (see "Asserting on Mock Calls");
- there is only a **single** field to check -- assert it directly (`assert.Equal(t, got.Field, want)`);
- the asserts are a **deliberate projection** of a big struct you don't fully control (a handful of fields of a DB row with many generated ones). Leave them field-by-field, or reach for `cmpx.OnlyFields` only when it documents intent -- it adds no coverage, so don't pretend it does.

## Don't Test Impossible Call Orders

Test what production code can actually do. Don't write tests for call orders or states no production caller can produce (for example, destroying a sandbox while its run is still being followed). Code asserts those invariants with a panic instead of handling them (see "Invariants" in `CLAUDE.md`), and those panics are not tested either.

If an existing test is the only thing exercising a branch, check whether production can reach that branch. If it can't, delete the branch and the test together.

## Prefer Duplication Over Helpers

Do NOT create little factory/helper functions that build a struct for a couple of call sites. Inline and duplicate the values in each test case -- duplication in tests is preferred over this kind of indirection.

Only extract a helper for genuinely large shared setup (e.g. `setupTestServer` / `createTestOrg` above).

```go
// Bad: don't wrap a small struct literal in a helper
func taskNotification(id int64, msg string) model.Notification {
    return model.Notification{
        Type:           "change",
        Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: id}},
        ChannelMessage: msg,
    }
}

// Good: write the literal inline in each test case
want := model.Notification{
    Type:           "change",
    Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 1}},
    ChannelMessage: "first",
}
// ... and again in the next case ...
want := model.Notification{
    Type:           "change",
    Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 2}},
    ChannelMessage: "second",
}
```

## Assertions

Use `gotest.tools/v3/assert` -- not the standard library `testing` package for assertions.

```go
assert.NilError(t, err)
assert.Equal(t, got, want)
assert.ErrorContains(t, err, "not found")
assert.DeepEqual(t, got, want, protocmp.Transform())
```

## Running Tests

```bash
mise run build    # Create bins & webui (required)
mise run up:test  # Setup dependencies (required)
mise run test     # Run all tests
mise run test -- -run=TestFoo -v  # Run specific tests
```
