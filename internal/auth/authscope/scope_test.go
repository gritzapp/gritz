package authscope

import (
	"testing"

	"gotest.tools/v3/assert"
)

func mustParse(t *testing.T, s string) Scope {
	t.Helper()
	sc, err := Parse(s)
	assert.NilError(t, err)
	return sc
}

func TestAllow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		scope Scope
		op    []string
		attrs []Attr
		want  bool
	}{
		{
			name:  "exact path no preds",
			scope: Scope{Op: []string{"task", "read"}},
			op:    []string{"task", "read"},
			want:  true,
		},
		{
			name:  "segment count mismatch scope longer",
			scope: Scope{Op: []string{"task", "read"}},
			op:    []string{"task"},
			want:  false,
		},
		{
			name:  "segment count mismatch scope shorter",
			scope: Scope{Op: []string{"task"}},
			op:    []string{"task", "read"},
			want:  false,
		},
		{
			name:  "wildcard matches exactly one segment",
			scope: Scope{Op: []string{"task", "*"}},
			op:    []string{"task", "read"},
			want:  true,
		},
		{
			name:  "wildcard does not span multiple segments",
			scope: Scope{Op: []string{"task", "*"}},
			op:    []string{"task", "a", "b"},
			want:  false,
		},
		{
			name:  "single wildcard does not match two segments",
			scope: Scope{Op: []string{"*"}},
			op:    []string{"task", "read"},
			want:  false,
		},
		{
			name:  "empty preds matches any instance",
			scope: Scope{Op: []string{"task", "read"}},
			op:    []string{"task", "read"},
			attrs: []Attr{StringAttr("id", "99"), StringAttr("parent", "42")},
			want:  true,
		},
		{
			name:  "absent key in scope is unconstrained",
			scope: Scope{Op: []string{"task", "read"}, Preds: map[string]string{"id": "42"}},
			op:    []string{"task", "read"},
			attrs: []Attr{StringAttr("id", "42"), StringAttr("parent", "7")},
			want:  true,
		},
		{
			name:  "star value is matched literally not as a wildcard",
			scope: Scope{Op: []string{"task", "read"}, Preds: map[string]string{"id": "*"}},
			op:    []string{"task", "read"},
			attrs: []Attr{StringAttr("id", "99")},
			want:  false,
		},
		{
			name:  "star value matches a literal star attribute",
			scope: Scope{Op: []string{"task", "read"}, Preds: map[string]string{"id": "*"}},
			op:    []string{"task", "read"},
			attrs: []Attr{StringAttr("id", "*")},
			want:  true,
		},
		{
			name:  "missing attribute denies",
			scope: Scope{Op: []string{"task", "read"}, Preds: map[string]string{"id": "42"}},
			op:    []string{"task", "read"},
			want:  false,
		},
		{
			name: "value match",
			scope: Scope{
				Op:    []string{"task", "create"},
				Preds: map[string]string{"workspace": "X"},
			},
			op:    []string{"task", "create"},
			attrs: []Attr{StringAttr("workspace", "X")},
			want:  true,
		},
		{
			name: "value mismatch denied",
			scope: Scope{
				Op:    []string{"task", "create"},
				Preds: map[string]string{"workspace": "X"},
			},
			op:    []string{"task", "create"},
			attrs: []Attr{StringAttr("workspace", "Z")},
			want:  false,
		},
		{
			name: "AND across keys all match",
			scope: Scope{
				Op:    []string{"task", "create"},
				Preds: map[string]string{"parent": "42", "workspace": "ws"},
			},
			op:    []string{"task", "create"},
			attrs: []Attr{StringAttr("parent", "42"), StringAttr("workspace", "ws")},
			want:  true,
		},
		{
			name: "AND across keys one fails",
			scope: Scope{
				Op:    []string{"task", "create"},
				Preds: map[string]string{"parent": "42", "workspace": "ws"},
			},
			op:    []string{"task", "create"},
			attrs: []Attr{StringAttr("parent", "42"), StringAttr("workspace", "other")},
			want:  false,
		},
		{
			name: "AND across keys one key missing",
			scope: Scope{
				Op:    []string{"task", "create"},
				Preds: map[string]string{"parent": "42", "workspace": "ws"},
			},
			op:    []string{"task", "create"},
			attrs: []Attr{StringAttr("parent", "42")},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Scopes{tt.scope}.Allow(tt.op, tt.attrs...)
			assert.Equal(t, got, tt.want)
		})
	}
}

// TestAllow_OwnTask reproduces the §6b own-task scenario: a caller holding an
// own-task scope can read its own task, but not an unrelated task.
func TestAllow_OwnTask(t *testing.T) {
	t.Parallel()
	set := Scopes{
		mustParse(t, `task.read:{"task.id":"42"}`),
	}
	tests := []struct {
		name  string
		attrs []Attr
		want  bool
	}{
		{"own task", []Attr{WithTaskID(42), WithTaskArchived(false)}, true},
		{"unrelated task", []Attr{WithTaskID(5), WithTaskArchived(false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := set.Allow(OpTaskRead, tt.attrs...)
			assert.Equal(t, got, tt.want)
		})
	}
}

// TestAllow_CreateConjunction reproduces the §6a create scenario: a single
// fully-constrained scope ANDs all its attributes.
func TestAllow_CreateConjunction(t *testing.T) {
	t.Parallel()
	set := Scopes{mustParse(t, `task.create:{"task.workspace":"ws","task.runner":"rn"}`)}

	ok := set.Allow(OpTaskCreate, WithTaskWorkspace("ws"), WithTaskRunner("rn"))
	assert.Equal(t, ok, true)

	// Wrong workspace is denied even though runner matches.
	denied := set.Allow(OpTaskCreate, WithTaskWorkspace("evil"), WithTaskRunner("rn"))
	assert.Equal(t, denied, false)
}

// TestAllow_SplitConjunctionIsHole documents the §6a failure mode: splitting
// the create conjunction across separate scopes and ORing them leaves the
// unconstrained attributes as holes. This is a regression guard against ever
// minting create scopes that way.
func TestAllow_SplitConjunctionIsHole(t *testing.T) {
	t.Parallel()
	set := Scopes{
		mustParse(t, `task.create:{"task.runner":"rn"}`),
		mustParse(t, `task.create:{"task.workspace":"ws"}`),
	}
	// runner matches the first scope, which leaves workspace unconstrained.
	escalated := set.Allow(OpTaskCreate, WithTaskRunner("rn"), WithTaskWorkspace("evil"))
	assert.Equal(t, escalated, true)
}

// TestAllow_WildcardAdmin reproduces the §6c wildcard scenarios.
func TestAllow_WildcardAdmin(t *testing.T) {
	t.Parallel()

	// task.* covers any action on a task instance.
	taskAdmin := Scopes{mustParse(t, `task.*`)}
	assert.Equal(t, taskAdmin.Allow(OpTaskWrite, WithTaskID(99), WithTaskArchived(false)), true)
	// ...but not a different resource.
	assert.Equal(t, taskAdmin.Allow(OpGitHubTokenCreate), false)

	// *.* covers any 2-segment operation with any instance.
	admin := Scopes{mustParse(t, `*.*`)}
	assert.Equal(t, admin.Allow(OpGitHubTokenCreate), true)
	assert.Equal(t, admin.Allow(OpTaskRead, WithTaskID(1)), true)
	// ...but not a 3-segment operation: * matches exactly one segment.
	assert.Equal(t, admin.Allow([]string{"task", "read", "extra"}), false)
}

// TestAllowOp covers the capability-presence primitive: AllowOp matches on the
// operation path alone and ignores predicates, so a predicated scope still answers
// "do I hold this capability at all?" without a concrete instance to test.
func TestAllowOp(t *testing.T) {
	t.Parallel()

	// A predicated scope satisfies AllowOp for its op even though Allow would need
	// the matching instance attribute.
	narrow := Scopes{mustParse(t, `task.write:{"task.id":"5"}`)}
	assert.Equal(t, narrow.AllowOp(OpTaskWrite), true)
	assert.Equal(t, narrow.AllowOp(OpTaskRead), false)
	// Sanity: the same scope denies Allow for a different instance, but AllowOp
	// (which ignores Preds) still reports the capability is held.
	assert.Equal(t, narrow.Allow(OpTaskWrite, WithTaskID(6)), false)

	// task_token.create is a no-instance capability op.
	minter := Scopes{mustParse(t, `task_token.create`)}
	assert.Equal(t, minter.AllowOp(OpTaskTokenCreate), true)
	assert.Equal(t, minter.AllowOp(OpTaskCreate), false)

	// Wildcards cover any op of matching arity; empty scopes hold nothing.
	assert.Equal(t, Admin().AllowOp(OpTaskTokenCreate), true)
	assert.Equal(t, Scopes{}.AllowOp(OpTaskTokenCreate), false)
}

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want Scope
	}{
		{
			name: "no predicates",
			in:   "github_token.create",
			want: Scope{Op: []string{"github_token", "create"}},
		},
		{
			name: "empty predicate object",
			in:   "task.read:{}",
			want: Scope{Op: []string{"task", "read"}, Preds: map[string]string{}},
		},
		{
			name: "string value",
			in:   `task.create:{"workspace":"X"}`,
			want: Scope{Op: []string{"task", "create"}, Preds: map[string]string{"workspace": "X"}},
		},
		{
			name: "numeric id as string",
			in:   `task.read:{"id":"42"}`,
			want: Scope{Op: []string{"task", "read"}, Preds: map[string]string{"id": "42"}},
		},
		{
			name: "star value parsed as a literal string",
			in:   `task.write:{"id":"*"}`,
			want: Scope{Op: []string{"task", "write"}, Preds: map[string]string{"id": "*"}},
		},
		{
			name: "multiple predicate keys",
			in:   `task.create:{"parent":"42","workspace":"ws","runner":"rn"}`,
			want: Scope{
				Op:    []string{"task", "create"},
				Preds: map[string]string{"parent": "42", "workspace": "ws", "runner": "rn"},
			},
		},
		{
			name: "wildcard segment",
			in:   "task.*",
			want: Scope{Op: []string{"task", "*"}},
		},
		{
			name: "first colon split with colons inside json",
			in:   `task.read:{"url":"http://example.com:8080/p"}`,
			want: Scope{
				Op:    []string{"task", "read"},
				Preds: map[string]string{"url": "http://example.com:8080/p"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.in)
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
	}{
		{"empty string", ""},
		{"empty path with predicates", `:{"id":"1"}`},
		{"empty segment middle", "task..read"},
		{"trailing dot", "task."},
		{"leading dot", ".task"},
		{"bad json", `task.read:{nope}`},
		{"trailing colon without json", "task.read:"},
		{"null predicates", "task.read:null"},
		{"array predicates", "task.read:[1,2]"},
		{"scalar predicates", "task.read:42"},
		{"number value", `task.read:{"id":42}`},
		{"bool value", `task.read:{"flag":true}`},
		{"array value", `task.create:{"workspace":["X","Y"]}`},
		{"nested object value", `task.read:{"a":{"b":"c"}}`},
		{"trailing data after object", `task.read:{}garbage`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tt.in)
			assert.Assert(t, err != nil, "expected error for %q", tt.in)
		})
	}
}

func TestString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		scope Scope
		want  string
	}{
		{
			name:  "no predicates omits colon",
			scope: Scope{Op: []string{"github_token", "create"}},
			want:  "github_token.create",
		},
		{
			name: "predicates emitted with sorted keys",
			scope: Scope{
				Op:    []string{"task", "create"},
				Preds: map[string]string{"workspace": "ws", "parent": "42"},
			},
			want: `task.create:{"parent":"42","workspace":"ws"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.scope.String(), tt.want)
		})
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"task.read",
		"task.*",
		"*.*",
		"github_token.create",
		`task.write:{"id":"*"}`,
		`task.create:{"parent":"42","workspace":"ws","runner":"rn"}`,
		`task.read:{"url":"http://example.com:8080/p"}`,
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			first := mustParse(t, in)
			second := mustParse(t, first.String())
			assert.DeepEqual(t, first, second)
		})
	}
}

func TestValidScope(t *testing.T) {
	t.Parallel()
	valid := []string{
		"task.read",
		"task.*",
		"*.*",
		`task.create:{"parent":"42","workspace":"ws","runner":"rn"}`,
		"github_token.create",
	}
	for _, s := range valid {
		assert.Assert(t, ValidScope(s), "expected %q to be valid", s)
	}
	invalid := []string{
		"",
		"task.",
		"task..read",
		`task.read:{nope}`,
		`task.read:{"id":42}`,
		`task.read:{"workspace":["X","Y"]}`,
		"task.read:null",
	}
	for _, s := range invalid {
		assert.Assert(t, !ValidScope(s), "expected %q to be invalid", s)
	}
}

func TestAdmin(t *testing.T) {
	t.Parallel()
	// Admin allows any 2-segment operation, on any instance.
	set := Admin()
	assert.Assert(t, set.Allow(OpTaskRead, WithTaskID(1)))
	assert.Assert(t, set.Allow(OpGitHubTokenCreate))
	// But not operations of a different arity.
	assert.Assert(t, !set.Allow([]string{"task"}))
	assert.Assert(t, !set.Allow([]string{"task", "read", "x"}))
}

func TestParseScopes(t *testing.T) {
	t.Parallel()
	// Empty input yields an empty set.
	set, err := ParseScopes(nil)
	assert.NilError(t, err)
	assert.Equal(t, len(set), 0)

	// Each string parses into the set.
	set, err = ParseScopes([]string{"task.read", "github_token.create"})
	assert.NilError(t, err)
	assert.Equal(t, len(set), 2)
	assert.Assert(t, set.Allow(OpTaskRead))

	// A malformed scope fails the whole parse.
	_, err = ParseScopes([]string{"task.read", "task."})
	assert.ErrorContains(t, err, "empty segment")
}
