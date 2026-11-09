package mcpbridge

import (
	"context"
	"sync"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// OrgsInstructions is appended to the server instructions when the org tools
// are registered.
const OrgsInstructions = "\nThis session acts in one gritz organisation at a time. " +
	"list_orgs lists the organisations and marks the current one; switch_org changes it. " +
	"Every other tool acts in the current organisation. " +
	"Check it with list_orgs before changing anything if you're not sure which organisation you're in."

// Orgs is the session's current org and its client.
type Orgs struct {
	ctx      context.Context
	merged   *configfile.Merged
	clientID string
	follow   func(entry *configfile.Entry)

	mu      sync.Mutex
	current string
	client  gritzclient.Client
}

// NewOrgs starts the session in entry, calling follow for it and for every
// org switched to later. switch_org looks entries up in merged. Clients are
// built from entry.TokenSource(ctx).
func NewOrgs(ctx context.Context, merged *configfile.Merged, entry *configfile.Entry, clientID string, follow func(entry *configfile.Entry)) *Orgs {
	o := &Orgs{
		ctx:      ctx,
		merged:   merged,
		clientID: clientID,
		follow:   follow,
		current:  entry.Name,
		client:   newClient(ctx, entry, clientID),
	}
	follow(entry)
	return o
}

func newClient(ctx context.Context, entry *configfile.Entry, clientID string) gritzclient.Client {
	return gritzclient.New(gritzclient.Options{
		BaseURL:     entry.Server,
		TokenSource: entry.TokenSource(ctx),
		ClientID:    clientID,
	})
}

// Service returns the current org's client; it is a mcpserver.ServiceFunc.
func (o *Orgs) Service() gritzv1connect.GritzServiceHandler {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.client
}

// AddTools registers list_orgs and switch_org.
func (o *Orgs) AddTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_orgs",
		Description: "List the gritz organisations this session can switch to, from the " +
			".gritz.yml files. The current one, which every other tool acts in, has current=true.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, o.listOrgsTool)

	mcp.AddTool(server, &mcp.Tool{
		Name: "switch_org",
		Description: "Make the named organisation, an entry name from list_orgs, the current " +
			"one. Every other tool acts in the current organisation. Fails, leaving the " +
			"current organisation unchanged, if the entry's credentials don't work or " +
			"belong to another organisation.",
	}, o.switchOrgTool)
}

type orgSummary struct {
	Name    string `json:"name"`
	ID      int64  `json:"id"`
	Server  string `json:"server"`
	Current bool   `json:"current"`
}

type listOrgsOutput struct {
	Orgs []orgSummary `json:"orgs"`
}

func (o *Orgs) listOrgsTool(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	o.mu.Lock()
	current := o.current
	o.mu.Unlock()
	var out listOrgsOutput
	for _, e := range o.merged.Entries() {
		out.Orgs = append(out.Orgs, orgSummary{Name: e.Name, ID: e.ID, Server: e.Server, Current: e.Name == current})
	}
	return mcpx.JSONResult(out), nil, nil
}

type switchOrgInput struct {
	Name string `json:"name" jsonschema:"An entry name from list_orgs"`
}

type switchOrgOutput struct {
	Name    string `json:"name"`
	ID      int64  `json:"id"`
	Server  string `json:"server"`
	OrgName string `json:"org_name"`
}

func (o *Orgs) switchOrgTool(ctx context.Context, _ *mcp.CallToolRequest, in switchOrgInput) (*mcp.CallToolResult, any, error) {
	entry, err := o.merged.Lookup(in.Name)
	if err != nil {
		return mcpx.ErrorResult("%s", err), nil, nil
	}
	// The token source outlives this call, so it takes the process context.
	client := newClient(o.ctx, entry, o.clientID)
	resp, err := client.GetCaller(ctx, &gritzv1.GetCallerRequest{})
	if err != nil {
		return mcpx.ErrorResult("org %q: %s", entry.Name, err), nil, nil
	}
	if id := resp.GetOrg().GetId(); id != entry.ID {
		return mcpx.ErrorResult("entry %q says org %d, but its key belongs to org %d", entry.Name, entry.ID, id), nil, nil
	}
	o.mu.Lock()
	o.current, o.client = entry.Name, client
	o.mu.Unlock()
	o.follow(entry)
	return mcpx.JSONResult(switchOrgOutput{
		Name:    entry.Name,
		ID:      entry.ID,
		Server:  entry.Server,
		OrgName: resp.GetOrg().GetName(),
	}), nil, nil
}
