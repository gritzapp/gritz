package mcpbridge

import (
	"context"
	"sync"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// OrgsInstructions is appended to gritz mcp's server instructions.
const OrgsInstructions = "\nThis session acts in one gritz organisation at a time. " +
	"list_orgs lists the organisations and marks the current one; switch_org changes it. " +
	"Every other tool acts in the current organisation. " +
	"Check it with list_orgs before changing anything if you're not sure which organisation you're in."

// OrgSwitcherOptions configures an OrgSwitcher.
type OrgSwitcherOptions struct {
	// Merged is the config switch_org looks entries up in.
	Merged *configfile.Merged
	// Entry is the org the session starts in.
	Entry *configfile.Entry
	// ClientID is sent by every client the switcher builds.
	ClientID string
	// OnSwitch is called for Entry and for every org switched to later.
	OnSwitch func(entry *configfile.Entry)
}

// OrgSwitcher holds the session's current org and its client.
type OrgSwitcher struct {
	ctx  context.Context
	opts OrgSwitcherOptions

	mu      sync.Mutex
	current string
	client  gritzclient.Client
}

// NewOrgSwitcher starts the session in opts.Entry and calls OnSwitch for it. Clients are
// built from the entry's TokenSource(ctx).
func NewOrgSwitcher(ctx context.Context, opts OrgSwitcherOptions) *OrgSwitcher {
	o := &OrgSwitcher{
		ctx:     ctx,
		opts:    opts,
		current: opts.Entry.Name,
		client:  newClient(ctx, opts.Entry, opts.ClientID),
	}
	opts.OnSwitch(opts.Entry)
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
func (o *OrgSwitcher) Service() gritzv1connect.GritzServiceHandler {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.client
}

// AddTools registers list_orgs and switch_org.
func (o *OrgSwitcher) AddTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_orgs",
		Description: "List the gritz organisations this session can switch to. The current one, which every other tool acts in, has current=true.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, o.listOrgsTool)

	mcp.AddTool(server, &mcp.Tool{
		Name: "switch_org",
		Description: "Make the named organisation, an entry name from list_orgs, the current " +
			"one. Every other tool acts in the current organisation.",
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

func (o *OrgSwitcher) listOrgsTool(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	o.mu.Lock()
	current := o.current
	o.mu.Unlock()
	var out listOrgsOutput
	for _, e := range o.opts.Merged.Entries() {
		out.Orgs = append(out.Orgs, orgSummary{Name: e.Name, ID: e.ID, Server: e.Server, Current: e.Name == current})
	}
	return mcpx.JSONResult(out), nil, nil
}

type switchOrgInput struct {
	Name string `json:"name" jsonschema:"An entry name from list_orgs"`
}

type switchOrgOutput struct {
	Name   string `json:"name"`
	ID     int64  `json:"id"`
	Server string `json:"server"`
}

func (o *OrgSwitcher) switchOrgTool(_ context.Context, _ *mcp.CallToolRequest, in switchOrgInput) (*mcp.CallToolResult, any, error) {
	entry, err := o.opts.Merged.Lookup(in.Name)
	if err != nil {
		return mcpx.ErrorResult("%s", err), nil, nil
	}
	o.mu.Lock()
	// The token source outlives this call, so it takes the process context.
	o.current, o.client = entry.Name, newClient(o.ctx, entry, o.opts.ClientID)
	o.mu.Unlock()
	o.opts.OnSwitch(entry)
	return mcpx.JSONResult(switchOrgOutput{Name: entry.Name, ID: entry.ID, Server: entry.Server}), nil, nil
}
