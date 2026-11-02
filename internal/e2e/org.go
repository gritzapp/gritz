package e2e

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"gotest.tools/v3/poll"
)

// Org is a user in an org, with an admin client.
type Org struct {
	ID     int64
	UserID string
	// Client makes real API calls as the user, acting in the org with admin
	// scopes, as the web UI does.
	Client gritzclient.Client

	server *Server
}

// Task is a task in the harness.
type Task struct {
	ID int64

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
	req.Runner = cmp.Or(req.Runner, runnerID)
	req.Workspace = cmp.Or(req.Workspace, workspace)
	resp, err := o.Client.CreateTask(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return &Task{ID: resp.Task.Id, org: o}
}

// Key is an xat_ key in an org.
type Key struct {
	ID string
	// Client calls the server with the key.
	Client gritzclient.Client
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
	return &Key{ID: resp.Key.Id, Client: o.server.Client(resp.RawToken)}
}

// CreateScopedKey creates a key in the org holding scopes. No RPC can narrow a
// key, so it writes the key to the store.
func (o *Org) CreateScopedKey(scopes authscope.Scopes) *Key {
	o.server.t.Helper()
	id, token := o.server.createKey(o.ID, "e2e", scopes)
	return &Key{ID: id, Client: o.server.Client(token)}
}

// CreateTaskToken mints a task token, as the runner does, and returns it.
func (o *Org) CreateTaskToken(req *gritzv1.CreateTaskTokenRequest) string {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.CreateTaskToken(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateTaskToken: %v", err)
	}
	return resp.Token
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

// AddMember adds other's user to the org, and returns a client acting as that
// user in the org.
func (o *Org) AddMember(other *Org) gritzclient.Client {
	t := o.server.t
	t.Helper()
	if err := o.server.store.AddOrgMember(t.Context(), nil, &model.OrgMember{
		OrgID:  o.ID,
		UserID: other.UserID,
		Role:   "member",
	}); err != nil {
		t.Fatal(err)
	}
	return o.server.Client(o.server.userToken(other.UserID, o.ID))
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
