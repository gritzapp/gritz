package testx_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	acmp "gotest.tools/v3/assert/cmp"
)

func TestExtractField(t *testing.T) {
	// Anonymous-struct slice, mirroring a moq "...Calls()" log.
	calls := []struct {
		InstallationID int64
		Actor          string
	}{
		{InstallationID: 7, Actor: "alice"},
		{InstallationID: 42, Actor: "bob"},
	}

	got := testx.ExtractField(calls, "InstallationID")
	assert.DeepEqual(t, got, []int64{7, 42})

	// A different field projects into its own typed slice.
	assert.DeepEqual(t, testx.ExtractField(calls, "Actor"), []string{"alice", "bob"})
}

func TestExtractField_Mismatch(t *testing.T) {
	calls := []struct{ ID int64 }{{ID: 1}, {ID: 2}}
	got := testx.ExtractField(calls, "ID")

	// The concrete []int64 return type lets go-cmp compare structurally, so a
	// value mismatch produces a real diff rather than a type-mismatch complaint.
	assert.Assert(t, cmp.Diff(got, []int64{1, 2}) == "", "expected equality, got diff")
	assert.Assert(t, cmp.Diff(got, []int64{99, 2}) != "", "expected a non-empty diff")
}

func TestExtractField_Empty(t *testing.T) {
	calls := []struct{ ID int64 }{}
	got := testx.ExtractField(calls, "ID")
	assert.DeepEqual(t, got, []int64{})
	// The dynamic type is []int64, not []any or nil.
	_, ok := got.([]int64)
	assert.Assert(t, ok, "expected concrete []int64")
}

func TestExtractField_PointerElements(t *testing.T) {
	type call struct{ ID int64 }
	calls := []*call{{ID: 3}, {ID: 4}}
	got := testx.ExtractField(calls, "ID")
	assert.DeepEqual(t, got, []int64{3, 4})
}

func TestExtractField_PanicNotSlice(t *testing.T) {
	defer func() {
		r := recover()
		assert.Assert(t, r != nil, "expected panic")
		assert.Assert(t, acmp.Contains(fmt.Sprint(r), "must be a slice or array"))
	}()
	testx.ExtractField(42, "ID")
}

func TestExtractField_PanicUnknownField(t *testing.T) {
	defer func() {
		r := recover()
		assert.Assert(t, r != nil, "expected panic")
		msg := fmt.Sprint(r)
		assert.Assert(t, acmp.Contains(msg, `no field "Missing"`))
		// Available field names are listed to make the failure actionable.
		assert.Assert(t, acmp.Contains(msg, "ID"))
	}()
	testx.ExtractField([]struct{ ID int64 }{{ID: 1}}, "Missing")
}
