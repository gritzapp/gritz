package taskvar

import (
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestValidName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"single letter", "A", true},
		{"letters digits underscores", "JIRA_ISSUE_2", true},
		{"trailing underscore", "X_", true},
		{"64 bytes", strings.Repeat("A", 64), true},
		{"empty", "", false},
		{"65 bytes", strings.Repeat("A", 65), false},
		{"lowercase", "jira", false},
		{"mixed case", "Jira", false},
		{"leading digit", "1A", false},
		{"leading underscore", "_A", false},
		{"dash", "JIRA-ISSUE", false},
		{"space", "JIRA ISSUE", false},
		{"equals", "A=B", false},
		{"non-ascii", "É", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, ValidName(tt.in), tt.want)
		})
	}
}

func TestEnv(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Env("JIRA_ISSUE"), "GRITZ_VAR_JIRA_ISSUE")
}

func TestError(t *testing.T) {
	t.Parallel()
	err := &Error{Name: "JIRA", Reason: "required"}
	assert.Equal(t, err.Error(), `variable "JIRA": required`)
}

func TestDeclValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		decl Decl
		err  string
	}{
		{"name only", Decl{Name: "JIRA_ISSUE"}, ""},
		{"required", Decl{Name: "JIRA_ISSUE", Required: true}, ""},
		{"default", Decl{Name: "BRANCH", Default: "main"}, ""},
		{"default at limit", Decl{Name: "BRANCH", Default: strings.Repeat("x", MaxValueBytes)}, ""},
		{"invalid name", Decl{Name: "branch"}, `variable "branch": name must be`},
		{"empty name", Decl{}, `variable "": name must be`},
		{"required with default", Decl{Name: "BRANCH", Required: true, Default: "main"}, `variable "BRANCH": a required variable can't have a default`},
		{"default too long", Decl{Name: "BRANCH", Default: strings.Repeat("x", MaxValueBytes+1)}, `variable "BRANCH": default value is 4097 bytes`},
		{"default with NUL", Decl{Name: "BRANCH", Default: "ma\x00in"}, `variable "BRANCH": default value contains a NUL byte`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.decl.Validate()
			if tt.err == "" {
				assert.NilError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.err)
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	decls := []Decl{
		{Name: "JIRA_ISSUE", Description: "The Jira story", Required: true},
		{Name: "BRANCH", Default: "main"},
		{Name: "LABEL"},
	}
	tests := []struct {
		name  string
		given map[string]string
		want  map[string]string
	}{
		{
			name:  "defaults filled, unset optional omitted",
			given: map[string]string{"JIRA_ISSUE": "APP-142"},
			want:  map[string]string{"JIRA_ISSUE": "APP-142", "BRANCH": "main"},
		},
		{
			name:  "given overrides default",
			given: map[string]string{"JIRA_ISSUE": "APP-142", "BRANCH": "dev", "LABEL": "x"},
			want:  map[string]string{"JIRA_ISSUE": "APP-142", "BRANCH": "dev", "LABEL": "x"},
		},
		{
			name:  "empty string is a value",
			given: map[string]string{"JIRA_ISSUE": "", "BRANCH": "", "LABEL": ""},
			want:  map[string]string{"JIRA_ISSUE": "", "BRANCH": "", "LABEL": ""},
		},
		{
			name:  "value at limit, newlines and shell syntax kept",
			given: map[string]string{"JIRA_ISSUE": strings.Repeat("x", MaxValueBytes), "LABEL": "a\nb; $(rm -rf /)"},
			want:  map[string]string{"JIRA_ISSUE": strings.Repeat("x", MaxValueBytes), "BRANCH": "main", "LABEL": "a\nb; $(rm -rf /)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(decls, tt.given)
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestResolve_NoDecls(t *testing.T) {
	t.Parallel()
	got, err := Resolve(nil, nil)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, map[string]string{})
}

func TestResolve_Error(t *testing.T) {
	t.Parallel()
	decls := []Decl{
		{Name: "JIRA_ISSUE", Required: true},
		{Name: "BRANCH", Default: "main"},
	}
	tests := []struct {
		name  string
		given map[string]string
		want  *Error
	}{
		{
			name:  "undeclared",
			given: map[string]string{"JIRA_ISSUE": "APP-142", "JIRA": "APP-1"},
			want:  &Error{Name: "JIRA", Reason: "not declared by the workspace"},
		},
		{
			name:  "first undeclared by name",
			given: map[string]string{"ZZ": "1", "JIRA_ISUE": "APP-142", "MM": "2"},
			want:  &Error{Name: "JIRA_ISUE", Reason: "not declared by the workspace"},
		},
		{
			name:  "invalid name is undeclared",
			given: map[string]string{"JIRA_ISSUE": "APP-142", "PATH": "/tmp", "ld_preload": "x"},
			want:  &Error{Name: "PATH", Reason: "not declared by the workspace"},
		},
		{
			name:  "missing required",
			given: map[string]string{"BRANCH": "dev"},
			want:  &Error{Name: "JIRA_ISSUE", Reason: "required"},
		},
		{
			name:  "value too long",
			given: map[string]string{"JIRA_ISSUE": strings.Repeat("x", MaxValueBytes+1)},
			want:  &Error{Name: "JIRA_ISSUE", Reason: "value is 4097 bytes, more than the limit of 4096"},
		},
		{
			name:  "value with NUL",
			given: map[string]string{"JIRA_ISSUE": "APP\x00142"},
			want:  &Error{Name: "JIRA_ISSUE", Reason: "value contains a NUL byte"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(decls, tt.given)
			assert.Assert(t, got == nil)
			assert.DeepEqual(t, err, error(tt.want))
		})
	}
}

func TestResolve_TooManyGiven(t *testing.T) {
	t.Parallel()
	// Arrange -- every given name is declared, so only the limit rejects it.
	var decls []Decl
	given := map[string]string{}
	for i := range MaxVariables + 1 {
		name := fmt.Sprintf("V%d", i)
		decls = append(decls, Decl{Name: name})
		given[name] = "x"
	}

	// Act
	got, err := Resolve(decls, given)

	// Assert
	assert.Assert(t, got == nil)
	assert.Error(t, err, "33 variables, more than the limit of 32")
}

func TestResolve_TooManyWithDefaults(t *testing.T) {
	t.Parallel()
	// Arrange -- one given variable plus 32 defaults.
	decls := []Decl{{Name: "GIVEN"}}
	for i := range MaxVariables {
		decls = append(decls, Decl{Name: fmt.Sprintf("D%d", i), Default: "x"})
	}

	// Act
	got, err := Resolve(decls, map[string]string{"GIVEN": "x"})

	// Assert
	assert.Assert(t, got == nil)
	assert.Error(t, err, "33 variables, more than the limit of 32")
}

func TestResolve_AtLimit(t *testing.T) {
	t.Parallel()
	// Arrange
	var decls []Decl
	given := map[string]string{}
	for i := range MaxVariables {
		name := fmt.Sprintf("V%d", i)
		decls = append(decls, Decl{Name: name})
		given[name] = "x"
	}

	// Act
	got, err := Resolve(decls, given)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, given)
}
