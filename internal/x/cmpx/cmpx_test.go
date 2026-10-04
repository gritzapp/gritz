package cmpx_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/x/cmpx"
)

type flat struct {
	A int
	B int
	C int
}

type inner struct {
	Foo int
	Bar int
}

type outer struct {
	Name  string
	Inner inner
}

// meta shares the field name "ID" with record to exercise the scoping guard
// (google/go-cmp#75): the nested "ID" must not be treated as the kept top-level
// "ID".
type meta struct {
	ID   int
	Name string
}

type record struct {
	ID   int
	Meta meta
}

func TestOnlyFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		diff      string
		wantEqual bool
	}{
		{
			// Flat struct, single field selected: the unselected fields differ
			// but the assertion still passes.
			name:      "flat unselected fields ignored",
			diff:      cmp.Diff(flat{A: 1, B: 2, C: 3}, flat{A: 1, B: 9, C: 9}, cmpx.OnlyFields("A")),
			wantEqual: true,
		},
		{
			// Flat struct, the selected field differs: the assertion correctly
			// fails.
			name:      "flat selected field differs",
			diff:      cmp.Diff(flat{A: 1, B: 2, C: 3}, flat{A: 9, B: 2, C: 3}, cmpx.OnlyFields("A")),
			wantEqual: false,
		},
		{
			// Multiple selected fields: only the remaining field is ignored.
			name:      "flat multiple selected",
			diff:      cmp.Diff(flat{A: 1, B: 2, C: 3}, flat{A: 1, B: 2, C: 9}, cmpx.OnlyFields("A", "B")),
			wantEqual: true,
		},
		{
			// Nested selection descends into the parent (keptOrAncestor prefix
			// behavior): Name and Inner.Bar are ignored, so differing values do
			// not fail the comparison.
			name:      "nested sibling and parent ignored",
			diff:      cmp.Diff(outer{Name: "x", Inner: inner{Foo: 1, Bar: 2}}, outer{Name: "y", Inner: inner{Foo: 1, Bar: 9}}, cmpx.OnlyFields("Inner.Foo")),
			wantEqual: true,
		},
		{
			// Nested selected field differs: comparison correctly fails, proving
			// cmp actually descended into Inner rather than ignoring it wholesale.
			name:      "nested selected field differs",
			diff:      cmp.Diff(outer{Name: "x", Inner: inner{Foo: 1, Bar: 2}}, outer{Name: "x", Inner: inner{Foo: 2, Bar: 2}}, cmpx.OnlyFields("Inner.Foo")),
			wantEqual: false,
		},
		{
			// Top-level field selected: the whole nested struct is ignored.
			name:      "top-level selected nested ignored",
			diff:      cmp.Diff(outer{Name: "x", Inner: inner{Foo: 1, Bar: 2}}, outer{Name: "x", Inner: inner{Foo: 9, Bar: 9}}, cmpx.OnlyFields("Name")),
			wantEqual: true,
		},
		{
			// Top-level field selected and differing: comparison fails.
			name:      "top-level selected differs",
			diff:      cmp.Diff(outer{Name: "x", Inner: inner{Foo: 1, Bar: 2}}, outer{Name: "y", Inner: inner{Foo: 1, Bar: 2}}, cmpx.OnlyFields("Name")),
			wantEqual: false,
		},
		{
			// Scoping guard (google/go-cmp#75): "ID" is kept only at the top
			// level of record. The identically-named meta.ID is NOT kept, so it
			// is ignored even though it differs.
			name:      "scoping guard nested same-name field ignored",
			diff:      cmp.Diff(record{ID: 1, Meta: meta{ID: 5, Name: "a"}}, record{ID: 1, Meta: meta{ID: 99, Name: "z"}}, cmpx.OnlyFields("ID")),
			wantEqual: true,
		},
		{
			// The genuinely kept top-level "ID" differs: comparison fails.
			name:      "scoping guard top-level ID differs",
			diff:      cmp.Diff(record{ID: 1, Meta: meta{ID: 5, Name: "a"}}, record{ID: 2, Meta: meta{ID: 5, Name: "a"}}, cmpx.OnlyFields("ID")),
			wantEqual: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantEqual {
				assert.Equal(t, tt.diff, "")
			} else {
				assert.Assert(t, tt.diff != "", "expected a non-empty diff")
			}
		})
	}
}

// TestOnlyFields_DeepEqual confirms the option composes with the project's
// assert.DeepEqual, which is the intended usage.
func TestOnlyFields_DeepEqual(t *testing.T) {
	t.Parallel()
	// Only A is compared; B and C differ but the assertion passes.
	assert.DeepEqual(t, flat{A: 1, B: 2, C: 3}, flat{A: 1, B: 8, C: 9}, cmpx.OnlyFields("A"))
}

// TestOnlyFields_ReuseAcrossTypes confirms a single option value is safe to
// reuse across comparisons of different root types: because the root type is
// derived from the path on each call rather than captured, the same "Foo"
// selector applies correctly to both flat and inner.
func TestOnlyFields_ReuseAcrossTypes(t *testing.T) {
	t.Parallel()
	// Arrange: one shared, reused option value.
	only := cmpx.OnlyFields("Foo", "A")

	// inner: only Foo compared, Bar ignored.
	assert.Equal(t, cmp.Diff(inner{Foo: 1, Bar: 2}, inner{Foo: 1, Bar: 9}, only), "")
	assert.Assert(t, cmp.Diff(inner{Foo: 1, Bar: 2}, inner{Foo: 2, Bar: 2}, only) != "")

	// flat: only A compared, B and C ignored — same option value, different root.
	assert.Equal(t, cmp.Diff(flat{A: 1, B: 2, C: 3}, flat{A: 1, B: 9, C: 9}, only), "")
	assert.Assert(t, cmp.Diff(flat{A: 1, B: 2, C: 3}, flat{A: 9, B: 2, C: 3}, only) != "")
}

// TestOnlyFields_NonStructRoot confirms the option is a no-op when the root is
// not a struct: every element of the slice is compared normally.
func TestOnlyFields_NonStructRoot(t *testing.T) {
	t.Parallel()
	got := cmp.Diff([]int{1, 2, 3}, []int{1, 9, 3}, cmpx.OnlyFields("Foo"))
	assert.Assert(t, got != "", "non-struct root should compare normally")
}
