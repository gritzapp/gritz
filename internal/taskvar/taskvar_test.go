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
	// For the limit cases: 32 and 33 declared, optional variables, all given;
	// and one given variable alongside 32 defaults.
	var decls32, decls33 []Decl
	given32, given33 := map[string]string{}, map[string]string{}
	for i := range MaxVariables + 1 {
		name := fmt.Sprintf("V%d", i)
		decls33 = append(decls33, Decl{Name: name})
		given33[name] = "x"
		if i < MaxVariables {
			decls32 = append(decls32, Decl{Name: name})
			given32[name] = "x"
		}
	}
	defaults32 := []Decl{{Name: "GIVEN"}}
	for i := range MaxVariables {
		defaults32 = append(defaults32, Decl{Name: fmt.Sprintf("D%d", i), Default: "x"})
	}
	tests := []struct {
		name  string
		decls []Decl
		given map[string]string
		want  map[string]string
		err   string
	}{
		{
			name:  "defaults filled, unset optional omitted",
			decls: decls,
			given: map[string]string{"JIRA_ISSUE": "APP-142"},
			want:  map[string]string{"JIRA_ISSUE": "APP-142", "BRANCH": "main"},
		},
		{
			name:  "given overrides default",
			decls: decls,
			given: map[string]string{"JIRA_ISSUE": "APP-142", "BRANCH": "dev", "LABEL": "x"},
			want:  map[string]string{"JIRA_ISSUE": "APP-142", "BRANCH": "dev", "LABEL": "x"},
		},
		{
			name:  "empty string is a value",
			decls: decls,
			given: map[string]string{"JIRA_ISSUE": "", "BRANCH": "", "LABEL": ""},
			want:  map[string]string{"JIRA_ISSUE": "", "BRANCH": "", "LABEL": ""},
		},
		{
			name:  "value at limit, newlines and shell syntax kept",
			decls: decls,
			given: map[string]string{"JIRA_ISSUE": strings.Repeat("x", MaxValueBytes), "LABEL": "a\nb; $(rm -rf /)"},
			want:  map[string]string{"JIRA_ISSUE": strings.Repeat("x", MaxValueBytes), "BRANCH": "main", "LABEL": "a\nb; $(rm -rf /)"},
		},
		{
			name: "no declarations",
			want: map[string]string{},
		},
		{
			name:  "variables at limit",
			decls: decls32,
			given: given32,
			want:  given32,
		},
		{
			name:  "undeclared",
			decls: decls,
			given: map[string]string{"JIRA_ISSUE": "APP-142", "JIRA": "APP-1"},
			err:   `variable "JIRA": not declared by the workspace`,
		},
		{
			name:  "first undeclared by name",
			decls: decls,
			given: map[string]string{"ZZ": "1", "JIRA_ISUE": "APP-142", "MM": "2"},
			err:   `variable "JIRA_ISUE": not declared by the workspace`,
		},
		{
			name:  "invalid name is undeclared",
			decls: decls,
			given: map[string]string{"JIRA_ISSUE": "APP-142", "PATH": "/tmp", "ld_preload": "x"},
			err:   `variable "PATH": not declared by the workspace`,
		},
		{
			name:  "undeclared without declarations",
			given: map[string]string{"JIRA_ISSUE": "APP-142"},
			err:   `variable "JIRA_ISSUE": not declared by the workspace`,
		},
		{
			name:  "missing required",
			decls: decls,
			given: map[string]string{"BRANCH": "dev"},
			err:   `variable "JIRA_ISSUE": required`,
		},
		{
			name:  "value too long",
			decls: decls,
			given: map[string]string{"JIRA_ISSUE": strings.Repeat("x", MaxValueBytes+1)},
			err:   `variable "JIRA_ISSUE": value is 4097 bytes, more than the limit of 4096`,
		},
		{
			name:  "value with NUL",
			decls: decls,
			given: map[string]string{"JIRA_ISSUE": "APP\x00142"},
			err:   `variable "JIRA_ISSUE": value contains a NUL byte`,
		},
		{
			name:  "too many given",
			decls: decls33,
			given: given33,
			err:   "33 variables, more than the limit of 32",
		},
		{
			name:  "too many with defaults",
			decls: defaults32,
			given: map[string]string{"GIVEN": "x"},
			err:   "33 variables, more than the limit of 32",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(tt.decls, tt.given)
			if tt.err != "" {
				assert.Error(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestParseAssignments(t *testing.T) {
	t.Parallel()
	// Act
	got, err := ParseAssignments([]string{
		"JIRA_ISSUE=APP-142",
		"LABEL=a,b=c",
		"EMPTY=",
		"NOTES=@notes.md",
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, map[string]string{
		"JIRA_ISSUE": "APP-142",
		"LABEL":      "a,b=c",
		"EMPTY":      "",
		"NOTES":      "@notes.md",
	})
}

func TestParseAssignments_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		err  string
	}{
		{"no equals", []string{"JIRA_ISSUE"}, "want NAME=VALUE"},
		{"no name", []string{"=APP-142"}, "want NAME=VALUE"},
		{"duplicate", []string{"A=1", "A=2"}, "set more than once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAssignments(tt.args)
			assert.ErrorContains(t, err, tt.err)
		})
	}
}
