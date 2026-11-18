// Package taskvar validates and resolves task variables: per-task values a
// workspace declares, set when a task is created and injected into its sandbox
// as the environment variable GRITZ_VAR_<NAME>.
//
// The server resolves a task's variables once, at creation, against the
// declarations its workspace registered; the stored result is complete, so the
// runner injects exactly what the task carries.
package taskvar

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
)

// EnvPrefix is the prefix of every task variable's environment variable.
const EnvPrefix = "GRITZ_VAR_"

// Limits on a task's variables.
const (
	MaxVariables  = 32
	MaxValueBytes = 4096
)

// maxNameBytes is the longest valid name.
const maxNameBytes = 64

// Decl is a variable a workspace declares.
type Decl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
}

// Error is a variable that fails resolution; callers map it to InvalidArgument.
type Error struct {
	Name   string
	Reason string
}

// Error returns the failure as `variable "NAME": reason`.
func (e *Error) Error() string {
	return fmt.Sprintf("variable %q: %s", e.Name, e.Reason)
}

// ValidName reports whether name is a valid variable name: an uppercase letter
// followed by at most 63 uppercase letters, digits and underscores.
func ValidName(name string) bool {
	if len(name) == 0 || len(name) > maxNameBytes {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_'):
		default:
			return false
		}
	}
	return true
}

// Env returns the name of the environment variable a task variable is injected as.
func Env(name string) string {
	if !ValidName(name) {
		panic(fmt.Sprintf("taskvar: Env called with invalid name %q", name))
	}
	return EnvPrefix + name
}

// Validate checks a declaration: its name, and that a default isn't set on a
// required variable. A default must also be a valid value.
func (d Decl) Validate() error {
	if !ValidName(d.Name) {
		return &Error{Name: d.Name, Reason: "name must be an uppercase letter followed by up to 63 uppercase letters, digits or underscores"}
	}
	if d.Required && d.Default != "" {
		return &Error{Name: d.Name, Reason: "a required variable can't have a default"}
	}
	if len(d.Default) > MaxValueBytes {
		return &Error{Name: d.Name, Reason: fmt.Sprintf("default value is %d bytes, more than the limit of %d", len(d.Default), MaxValueBytes)}
	}
	if strings.IndexByte(d.Default, 0) >= 0 {
		return &Error{Name: d.Name, Reason: "default value contains a NUL byte"}
	}
	return nil
}

// Resolve checks given against decls and returns the task's variables, with
// defaults filled in and unset optional variables omitted. decls must each
// pass [Decl.Validate]. Every error it returns is an [*Error] or a limit
// violation, both the caller's fault.
func Resolve(decls []Decl, given map[string]string) (map[string]string, error) {
	// Checked up front too, so an oversized request is rejected without
	// sorting it.
	if len(given) > MaxVariables {
		return nil, fmt.Errorf("%d variables, more than the limit of %d", len(given), MaxVariables)
	}
	declared := make(map[string]Decl, len(decls))
	for _, d := range decls {
		declared[d.Name] = d
	}
	for _, name := range slices.Sorted(maps.Keys(given)) {
		if _, ok := declared[name]; !ok {
			return nil, &Error{Name: name, Reason: "not declared by the workspace"}
		}
		v := given[name]
		if len(v) > MaxValueBytes {
			return nil, &Error{Name: name, Reason: fmt.Sprintf("value is %d bytes, more than the limit of %d", len(v), MaxValueBytes)}
		}
		if strings.IndexByte(v, 0) >= 0 {
			return nil, &Error{Name: name, Reason: "value contains a NUL byte"}
		}
	}
	out := make(map[string]string, len(decls))
	for _, d := range decls {
		switch v, ok := given[d.Name]; {
		case ok:
			out[d.Name] = v
		case d.Required:
			return nil, &Error{Name: d.Name, Reason: "required"}
		case d.Default != "":
			out[d.Name] = d.Default
		}
	}
	if len(out) > MaxVariables {
		return nil, fmt.Errorf("%d variables, more than the limit of %d", len(out), MaxVariables)
	}
	return out, nil
}

// ParseAssignments parses NAME=VALUE assignments, as a command line takes
// them. A value of @file is read from the file, and @- from stdin, which only
// one assignment can use. It doesn't validate names or values: Resolve does.
func ParseAssignments(args []string, stdin io.Reader) (map[string]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	vars := map[string]string{}
	usedStdin := false
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("%q: want NAME=VALUE", arg)
		}
		if _, dup := vars[name]; dup {
			return nil, fmt.Errorf("%s: set more than once", name)
		}
		if path, ok := strings.CutPrefix(value, "@"); ok {
			var data []byte
			var err error
			if path == "-" {
				if usedStdin {
					return nil, fmt.Errorf("%s: only one variable can read stdin", name)
				}
				usedStdin = true
				data, err = io.ReadAll(stdin)
			} else {
				data, err = os.ReadFile(path)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			value = string(data)
		}
		vars[name] = value
	}
	return vars, nil
}
