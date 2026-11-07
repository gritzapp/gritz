// Package cmpx provides field-selection helpers for
// github.com/google/go-cmp, intended for use with the project's existing
// gotest.tools/v3/assert.DeepEqual (which accepts cmp.Option values).
//
// The central helper, OnlyFields, is the inverse of cmpopts.IgnoreFields:
// instead of ignoring the named fields it ignores everything except them.
// go-cmp has no built-in for this; see the long-standing open issue
// google/go-cmp#66 ("Add IgnoreFieldsExcept"). It is implemented the same
// way IgnoreFields is internally — cmp.FilterPath combined with cmp.Ignore()
// — but with the predicate negated.
//
// OnlyFields scopes its names to the top-level fields of whatever type is
// being compared: the comparison root is always the outermost compared type,
// derived at match time from the comparison path. Nested fields remain
// reachable via dot-delimited names (e.g. "Inner.Foo"), but — unlike
// cmpopts.IgnoreFields(Inner{}, ...) — it cannot use a nested type as the
// entry point for name resolution, because it discovers the root from the
// values rather than from a caller-supplied type.
//
// Known limitation: OnlyFields does not replicate IgnoreFields's embedded-field
// promotion semantics. Names promoted from embedded structs may not resolve;
// it targets flat and explicitly-nested field paths.
//
// OnlyFields cannot be used on protobuf messages: after protocmp.Transform()
// they are protocmp.Message maps rather than structs, so its root-struct check
// never matches. OnlyProtoFields is the proto counterpart. protocmp has no
// "compare only these fields" option either, so it inverts
// protocmp.IgnoreFields via the message descriptor, scoped to one message type.
package cmpx

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/testing/protocmp"
)

// OnlyFields is the inverse of cmpopts.IgnoreFields: it compares only the
// named top-level fields of the compared struct, ignoring all others. Names
// may be dot-delimited to target nested fields (e.g. "Inner.Foo").
func OnlyFields(names ...string) cmp.Option {
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	return cmp.FilterPath(func(p cmp.Path) bool {
		// Derive the comparison root type fresh on every call rather than
		// capturing it in the closure. The root never varies within a single
		// comparison, so memoizing buys nothing; keeping it stateless makes the
		// option safe to reuse across comparisons of different types (a cached
		// root would latch onto the first type and misapply it).
		root := p.Index(0).Type()
		for root.Kind() == reflect.Pointer {
			root = root.Elem()
		}
		if root.Kind() != reflect.Struct {
			return false
		}
		path := fieldPath(p)
		if path == "" {
			return false
		}
		return !keptOrAncestor(path, keep)
	}, cmp.Ignore())
}

// fieldPath returns the dot-joined struct-field names along p, walking from the
// comparison root (index 0) down to the current step.
func fieldPath(p cmp.Path) string {
	var parts []string
	for _, s := range p {
		if sf, ok := s.(cmp.StructField); ok {
			parts = append(parts, sf.Name())
		}
	}
	return strings.Join(parts, ".")
}

// keptOrAncestor reports whether path is a kept field, or a prefix of one
// (so we descend into "Inner" when "Inner.Foo" is requested).
func keptOrAncestor(path string, keep map[string]bool) bool {
	if keep[path] {
		return true
	}
	for k := range keep {
		if strings.HasPrefix(k, path+".") {
			return true
		}
	}
	return false
}

// OnlyProtoFields compares only the named fields of message m (proto field
// names), ignoring every other field m declares. Use with protocmp.Transform().
func OnlyProtoFields(m proto.Message, names ...protoreflect.Name) cmp.Option {
	md := m.ProtoReflect().Descriptor()
	fields := md.Fields()
	for _, n := range names {
		if fields.ByName(n) == nil {
			panic(fmt.Sprintf("cmpx.OnlyProtoFields: %q is not a field of %s", n, md.FullName()))
		}
	}
	var ignored []protoreflect.Name
	for i := range fields.Len() {
		if n := fields.Get(i).Name(); !slices.Contains(names, n) {
			ignored = append(ignored, n)
		}
	}
	return protocmp.IgnoreFields(m, ignored...)
}
