package mcpbridge

import (
	"context"
	"testing"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/x/mcptest"
	"gotest.tools/v3/assert"
)

// newOrgSwitcherTest returns an OrgSwitcher started in entry "a" of orgs
// "a" and "b", and the names follow was called with.
func newOrgSwitcherTest(t *testing.T) (*OrgSwitcher, *[]string) {
	t.Helper()
	merged := &configfile.Merged{Orgs: map[string]configfile.Entry{
		"a": {ID: 1, Key: "ka"},
		"b": {ID: 2, Key: "kb", Server: "http://localhost:8080"},
	}}
	entry, err := merged.Lookup("a")
	assert.NilError(t, err)
	var followed []string
	o := NewOrgSwitcher(context.Background(), OrgSwitcherOptions{
		Merged: merged,
		Entry:  entry,
		Follow: func(e *configfile.Entry) { followed = append(followed, e.Name) },
	})
	return o, &followed
}

func listOrgs(t *testing.T, o *OrgSwitcher) listOrgsOutput {
	t.Helper()
	res, _, err := o.listOrgsTool(context.Background(), nil, struct{}{})
	assert.NilError(t, err)
	var out listOrgsOutput
	mcptest.UnmarshalCallToolResult(t, res, &out)
	return out
}

func TestSwitchOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	o, followed := newOrgSwitcherTest(t)
	before := o.Service()

	// Act
	res, _, err := o.switchOrgTool(context.Background(), nil, switchOrgInput{Name: "b"})

	// Assert
	assert.NilError(t, err)
	var got switchOrgOutput
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.DeepEqual(t, got, switchOrgOutput{Name: "b", ID: 2, Server: "http://localhost:8080"})
	assert.DeepEqual(t, *followed, []string{"a", "b"})
	assert.Assert(t, o.Service() != before)
	assert.DeepEqual(t, listOrgs(t, o), listOrgsOutput{Orgs: []orgSummary{
		{Name: "a", ID: 1, Server: configfile.DefaultServer},
		{Name: "b", ID: 2, Server: "http://localhost:8080", Current: true},
	}})
}

// TestSwitchOrg_Unknown checks that a failed switch leaves the current org in
// place.
func TestSwitchOrg_Unknown(t *testing.T) {
	t.Parallel()
	// Arrange
	o, followed := newOrgSwitcherTest(t)
	before := o.Service()

	// Act
	res, _, err := o.switchOrgTool(context.Background(), nil, switchOrgInput{Name: "x"})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, res.IsError)
	assert.DeepEqual(t, *followed, []string{"a"})
	assert.Assert(t, o.Service() == before)
	assert.Assert(t, listOrgs(t, o).Orgs[0].Current)
}
