package mcpbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/configfile"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/x/mcptest"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

type orgKey struct{}

// callerService answers GetCaller with the org its middleware found for the
// request's key.
type callerService struct {
	gritzv1connect.UnimplementedGritzServiceHandler
}

func (callerService) GetCaller(ctx context.Context, _ *gritzv1.GetCallerRequest) (*gritzv1.GetCallerResponse, error) {
	org, ok := ctx.Value(orgKey{}).(*gritzv1.Org)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	return &gritzv1.GetCallerResponse{Org: org}, nil
}

// newOrgsTest returns Orgs started in entry "a" against a server that knows
// keys "ka" (org 1) and "kb" (org 2), and the names follow was called with.
func newOrgsTest(t *testing.T, orgs map[string]configfile.Entry) (*Orgs, *[]string) {
	t.Helper()
	path, handler := gritzv1connect.NewGritzServiceHandler(callerService{})
	keys := map[string]*gritzv1.Org{"ka": {Id: 1, Name: "Org A"}, "kb": {Id: 2, Name: "Org B"}}
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if org, ok := keys[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]; ok {
			ctx = context.WithValue(ctx, orgKey{}, org)
		}
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for name, e := range orgs {
		e.Server = srv.URL
		orgs[name] = e
	}
	merged := &configfile.Merged{Orgs: orgs}
	entry, err := merged.Lookup("a")
	assert.NilError(t, err)
	var followed []string
	o := NewOrgs(context.Background(), merged, entry, "", func(e *configfile.Entry) { followed = append(followed, e.Name) })
	return o, &followed
}

func TestSwitchOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	o, followed := newOrgsTest(t, map[string]configfile.Entry{
		"a": {ID: 1, Key: "ka"},
		"b": {ID: 2, Key: "kb"},
	})

	// Act
	res, _, err := o.switchOrgTool(context.Background(), nil, switchOrgInput{Name: "b"})

	// Assert
	assert.NilError(t, err)
	var got switchOrgOutput
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.DeepEqual(t, got, switchOrgOutput{Name: "b", ID: 2, Server: got.Server, OrgName: "Org B"})
	assert.DeepEqual(t, *followed, []string{"a", "b"})
	caller, err := o.Service().GetCaller(context.Background(), &gritzv1.GetCallerRequest{})
	assert.NilError(t, err)
	assert.Equal(t, caller.GetOrg().GetId(), int64(2))
	res, _, err = o.listOrgsTool(context.Background(), nil, struct{}{})
	assert.NilError(t, err)
	var list listOrgsOutput
	mcptest.UnmarshalCallToolResult(t, res, &list)
	assert.DeepEqual(t, list, listOrgsOutput{Orgs: []orgSummary{
		{Name: "a", ID: 1, Server: got.Server},
		{Name: "b", ID: 2, Server: got.Server, Current: true},
	}})
}

// TestSwitchOrg_Fails checks that a switch that fails leaves the current org
// in place.
func TestSwitchOrg_Fails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		entry configfile.Entry
		want  string
	}{
		{name: "bad key", entry: configfile.Entry{ID: 2, Key: "bad"}, want: "unauthenticated"},
		{name: "id mismatch", entry: configfile.Entry{ID: 3, Key: "kb"}, want: `entry "b" says org 3, but its key belongs to org 2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			o, followed := newOrgsTest(t, map[string]configfile.Entry{
				"a": {ID: 1, Key: "ka"},
				"b": tt.entry,
			})

			// Act
			res, _, err := o.switchOrgTool(context.Background(), nil, switchOrgInput{Name: "b"})

			// Assert
			assert.NilError(t, err)
			assert.Assert(t, res.IsError)
			assert.Assert(t, cmp.Contains(mcptest.CallToolResultText(t, res), tt.want))
			assert.DeepEqual(t, *followed, []string{"a"})
			caller, err := o.Service().GetCaller(context.Background(), &gritzv1.GetCallerRequest{})
			assert.NilError(t, err)
			assert.Equal(t, caller.GetOrg().GetId(), int64(1))
		})
	}
}
