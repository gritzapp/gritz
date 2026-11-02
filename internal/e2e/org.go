package e2e

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"gotest.tools/v3/poll"
)

// Org is an org with its owner.
type Org struct {
	ID int64
	// Owner is the user CreateOrg made, owning the org.
	Owner *Member
	// Client is shorthand for Owner.Client.
	Client gritzclient.Client

	server *Server
	// runner calls the server with the org's runner key.
	runner gritzclient.Client
}

// Member is a user acting in an org.
type Member struct {
	UserID string
	Email  string
	// Client makes real API calls as the user, acting in the org with admin
	// scopes, as the web UI does.
	Client gritzclient.Client
	// Resp is the AddOrgMember response that added the user; nil for an org's
	// owner.
	Resp *gritzv1.AddOrgMemberResponse
}

// Task is a task in the harness.
type Task struct {
	ID int64
	// Resp is the CreateTask response the task was made from.
	Resp *gritzv1.CreateTaskResponse

	org *Org
}

// CreateTask creates a task, as the new-task page does. It fills the harness
// defaults where req leaves them unset: the org's e2e runner and workspace. The
// task is always a draft, since Draft has no unset value. req may be nil.
func (o *Org) CreateTask(req *gritzv1.CreateTaskRequest) *Task {
	t := o.server.t
	t.Helper()
	if req == nil {
		req = &gritzv1.CreateTaskRequest{}
	} else {
		req = proto.CloneOf(req)
	}
	req.Draft = true
	req.Runner = cmp.Or(req.Runner, DefaultRunner)
	req.Workspace = cmp.Or(req.Workspace, DefaultWorkspace)
	resp, err := o.Client.CreateTask(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return &Task{ID: resp.Task.Id, Resp: resp, org: o}
}

// UpdateTask updates a task, as the task page does.
func (o *Org) UpdateTask(req *gritzv1.UpdateTaskRequest) {
	t := o.server.t
	t.Helper()
	if _, err := o.Client.UpdateTask(t.Context(), req); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
}

// ArchiveTask archives a task, as the task page does.
func (o *Org) ArchiveTask(req *gritzv1.ArchiveTaskRequest) {
	t := o.server.t
	t.Helper()
	if _, err := o.Client.ArchiveTask(t.Context(), req); err != nil {
		t.Fatalf("ArchiveTask: %v", err)
	}
}

// CancelTask cancels a task, as the task page does.
func (o *Org) CancelTask(req *gritzv1.CancelTaskRequest) {
	t := o.server.t
	t.Helper()
	if _, err := o.Client.CancelTask(t.Context(), req); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
}

// SubmitRunnerEvents reports runner events as the org's runner does, with its
// runner key.
func (o *Org) SubmitRunnerEvents(req *gritzv1.SubmitRunnerEventsRequest) {
	t := o.server.t
	t.Helper()
	if _, err := o.runner.SubmitRunnerEvents(t.Context(), req); err != nil {
		t.Fatalf("SubmitRunnerEvents: %v", err)
	}
}

// RegisterWorkspaces registers a runner's workspaces, as the org's runner does
// on startup, with its runner key.
func (o *Org) RegisterWorkspaces(req *gritzv1.RegisterWorkspacesRequest) {
	t := o.server.t
	t.Helper()
	if _, err := o.runner.RegisterWorkspaces(t.Context(), req); err != nil {
		t.Fatalf("RegisterWorkspaces: %v", err)
	}
}

// ListWorkspaces returns the org's workspaces.
func (o *Org) ListWorkspaces(req *gritzv1.ListWorkspacesRequest) []*gritzv1.RegisteredWorkspace {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListWorkspaces(t.Context(), req)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	return resp.Workspaces
}

// CreateOrg creates another org owned by o's owner, as the org switcher does,
// registers the harness's workspace in it, and returns it with the owner acting
// in it.
func (o *Org) CreateOrg(req *gritzv1.CreateOrgRequest) *Org {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.CreateOrg(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	return o.server.newOrg(resp.Org.Id, o.Owner.UserID, o.Owner.Email)
}

// ListOrgs returns the orgs o's owner belongs to.
func (o *Org) ListOrgs(req *gritzv1.ListOrgsRequest) []*gritzv1.Org {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListOrgs(t.Context(), req)
	if err != nil {
		t.Fatalf("ListOrgs: %v", err)
	}
	return resp.Orgs
}

// WaitRunnerEvents waits until runner's event log, read from the start, holds
// at least count events, and returns the page token that resumes after them.
// An event shows in the log only once every earlier transaction has finished.
func (o *Org) WaitRunnerEvents(runner string, count int) string {
	t := o.server.t
	t.Helper()
	token := ""
	n := 0
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		resp, err := o.Client.ListRunnerEvents(t.Context(), &gritzv1.ListRunnerEventsRequest{
			Runner:    runner,
			PageToken: token,
		})
		if err != nil {
			return poll.Error(fmt.Errorf("ListRunnerEvents %s: %w", runner, err))
		}
		n += len(resp.Events)
		token = resp.NextPageToken
		if n < count {
			return poll.Continue("runner %s: got %d of %d events", runner, n, count)
		}
		return poll.Success()
	}, o.pollOps()...)
	return token
}

// Key is an xat_ key in an org.
type Key struct {
	ID string
	// Client calls the server with the key.
	Client gritzclient.Client
	// Resp is the CreateKey response the key was made from; nil for a key
	// from CreateScopedKey, which doesn't go through the RPC.
	Resp *gritzv1.CreateKeyResponse
}

// CreateKey creates a key in the org, as the keys page does. The RPC grants
// admin scopes; CreateScopedKey makes a narrower key.
func (o *Org) CreateKey(req *gritzv1.CreateKeyRequest) *Key {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.CreateKey(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	return &Key{ID: resp.Key.Id, Client: o.server.Client(resp.RawToken), Resp: resp}
}

// CreateScopedKey creates a key in the org holding scopes. No RPC can narrow a
// key, so it writes the key to the store, and the Key's Resp is nil.
func (o *Org) CreateScopedKey(scopes authscope.Scopes) *Key {
	o.server.t.Helper()
	id, token := o.server.createKey(o.ID, "e2e", scopes)
	return &Key{ID: id, Client: o.server.Client(token)}
}

// TaskToken is a task JWT, as the runner mints it.
type TaskToken struct {
	Token string
	// Client calls the server with the token.
	Client gritzclient.Client
	// Resp is the CreateTaskToken response the token was made from.
	Resp *gritzv1.CreateTaskTokenResponse
}

// CreateTaskToken mints a task token, as the runner does.
func (o *Org) CreateTaskToken(req *gritzv1.CreateTaskTokenRequest) *TaskToken {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.CreateTaskToken(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateTaskToken: %v", err)
	}
	return &TaskToken{Token: resp.Token, Client: o.server.Client(resp.Token), Resp: resp}
}

// GetTask returns the task.
func (o *Org) GetTask(req *gritzv1.GetTaskRequest) *gritzv1.Task {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.GetTask(t.Context(), req)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return resp.Task
}

// ListEventsByTask returns one page of a task's events, oldest first, as the
// task timeline loads them.
func (o *Org) ListEventsByTask(req *gritzv1.ListEventsByTaskRequest) []*gritzv1.Event {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListEventsByTask(t.Context(), req)
	if err != nil {
		t.Fatalf("ListEventsByTask: %v", err)
	}
	return resp.Events
}

// CreateLink links a task to an external resource, as the agent's create_link
// tool does.
func (o *Org) CreateLink(req *gritzv1.CreateLinkRequest) *gritzv1.TaskLink {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.CreateLink(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	return resp.Link
}

// ListKeys returns the org's keys.
func (o *Org) ListKeys(req *gritzv1.ListKeysRequest) []*gritzv1.Key {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListKeys(t.Context(), req)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	return resp.Keys
}

// AddOrgMember adds a user to the org as its owner does, and returns the user
// acting in the org.
func (o *Org) AddOrgMember(req *gritzv1.AddOrgMemberRequest) *Member {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.AddOrgMember(t.Context(), req)
	if err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	return &Member{
		UserID: resp.Member.UserId,
		Email:  resp.Member.Email,
		Client: o.server.Client(o.server.userToken(resp.Member.UserId, o.ID)),
		Resp:   resp,
	}
}

// ListTasks returns the org's tasks.
func (o *Org) ListTasks(req *gritzv1.ListTasksRequest) []*gritzv1.Task {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListTasks(t.Context(), req)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	return resp.Tasks
}

// SetRoutingRules replaces the org's routing rules, as the routing rules page
// does, and returns the rules as stored.
func (o *Org) SetRoutingRules(req *gritzv1.SetRoutingRulesRequest) []*gritzv1.RoutingRule {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.SetRoutingRules(t.Context(), req)
	if err != nil {
		t.Fatalf("SetRoutingRules: %v", err)
	}
	return resp.Rules
}

// GetRoutingRules returns the org's routing rules.
func (o *Org) GetRoutingRules(req *gritzv1.GetRoutingRulesRequest) []*gritzv1.RoutingRule {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.GetRoutingRules(t.Context(), req)
	if err != nil {
		t.Fatalf("GetRoutingRules: %v", err)
	}
	return resp.Rules
}

// TestEvent routes a synthetic event through the org's routing rules, as the
// test event dialog does. With Fire set it writes external events to the tasks
// it routes to, the same way a webhook-born event does; it is the only RPC that
// does.
func (o *Org) TestEvent(req *gritzv1.TestEventRequest) *gritzv1.TestEventResponse {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.TestEvent(t.Context(), req)
	if err != nil {
		t.Fatalf("TestEvent: %v", err)
	}
	return resp
}

// GetEvent returns the event.
func (o *Org) GetEvent(req *gritzv1.GetEventRequest) *gritzv1.Event {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.GetEvent(t.Context(), req)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	return resp.Event
}

// ListExternalEvents returns the org's external event feed, newest first.
func (o *Org) ListExternalEvents(req *gritzv1.ListExternalEventsRequest) []*gritzv1.Event {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListExternalEvents(t.Context(), req)
	if err != nil {
		t.Fatalf("ListExternalEvents: %v", err)
	}
	return resp.Events
}

// ListOrgMembers returns the org's members.
func (o *Org) ListOrgMembers(req *gritzv1.ListOrgMembersRequest) []*gritzv1.OrgMember {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListOrgMembers(t.Context(), req)
	if err != nil {
		t.Fatalf("ListOrgMembers: %v", err)
	}
	return resp.Members
}

// AppendLogChunk appends a chunk to a task's log, as the driver's log shipper
// does.
func (o *Org) AppendLogChunk(req *gritzv1.AppendLogChunkRequest) {
	t := o.server.t
	t.Helper()
	if _, err := o.Client.AppendLogChunk(t.Context(), req); err != nil {
		t.Fatalf("AppendLogChunk: %v", err)
	}
}

// ListLogChunksByTask returns one page of a task's log chunks, oldest first.
func (o *Org) ListLogChunksByTask(req *gritzv1.ListLogChunksByTaskRequest) []*gritzv1.LogChunk {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListLogChunksByTask(t.Context(), req)
	if err != nil {
		t.Fatalf("ListLogChunksByTask: %v", err)
	}
	return resp.Chunks
}

// CreateSchedule creates a schedule, as the schedules page does. It fills the
// harness defaults where req leaves them unset: the org's e2e runner and
// workspace.
func (o *Org) CreateSchedule(req *gritzv1.CreateScheduleRequest) *gritzv1.Schedule {
	t := o.server.t
	t.Helper()
	req = proto.CloneOf(req)
	req.Runner = cmp.Or(req.Runner, DefaultRunner)
	req.Workspace = cmp.Or(req.Workspace, DefaultWorkspace)
	resp, err := o.Client.CreateSchedule(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	return resp.Schedule
}

// GetSchedule returns the schedule.
func (o *Org) GetSchedule(req *gritzv1.GetScheduleRequest) *gritzv1.Schedule {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.GetSchedule(t.Context(), req)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	return resp.Schedule
}

// ListSchedules returns the org's schedules.
func (o *Org) ListSchedules(req *gritzv1.ListSchedulesRequest) []*gritzv1.Schedule {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.ListSchedules(t.Context(), req)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	return resp.Schedules
}

// WaitTask waits until cond accepts task id, and returns it.
func (o *Org) WaitTask(id int64, cond func(*gritzv1.Task) bool) *gritzv1.Task {
	t := o.server.t
	t.Helper()
	var task *gritzv1.Task
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		resp, err := o.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: id})
		if err != nil {
			return poll.Error(fmt.Errorf("GetTask %d: %w", id, err))
		}
		task = resp.Task
		if cond(task) {
			return poll.Success()
		}
		events, err := o.events(id)
		if err != nil {
			return poll.Error(err)
		}
		return poll.Continue("task %d: status %s, version %d, command %s; last events:%s",
			id, task.Status, task.Version, task.Command, formatEvents(events[max(len(events)-5, 0):]))
	}, o.pollOps()...)
	return task
}

// WaitEvent waits until one of task id's events matches, and returns it.
func (o *Org) WaitEvent(id int64, match func(*gritzv1.Event) bool) *gritzv1.Event {
	t := o.server.t
	t.Helper()
	var found *gritzv1.Event
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		events, err := o.events(id)
		if err != nil {
			return poll.Error(err)
		}
		for _, e := range events {
			if match(e) {
				found = e
				return poll.Success()
			}
		}
		return poll.Continue("task %d: no matching event in:%s", id, formatEvents(events))
	}, o.pollOps()...)
	return found
}

func (o *Org) pollOps() []poll.SettingOp {
	return []poll.SettingOp{poll.WithTimeout(o.server.timeout), poll.WithDelay(20 * time.Millisecond)}
}

// events returns every event of task id, oldest first.
func (o *Org) events(id int64) ([]*gritzv1.Event, error) {
	var events []*gritzv1.Event
	token := ""
	for {
		resp, err := o.Client.ListEventsByTask(o.server.t.Context(), &gritzv1.ListEventsByTaskRequest{
			TaskId:    id,
			PageSize:  200,
			PageToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("ListEventsByTask %d: %w", id, err)
		}
		events = append(resp.Events, events...)
		if resp.PrevPageToken == "" {
			return events, nil
		}
		token = resp.PrevPageToken
	}
}

func formatEvents(events []*gritzv1.Event) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("\n\t")
		b.WriteString(prototext.MarshalOptions{}.Format(e))
	}
	return b.String()
}
