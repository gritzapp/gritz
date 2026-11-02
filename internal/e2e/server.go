package e2e

import (
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"google.golang.org/protobuf/encoding/prototext"
	"gotest.tools/v3/poll"
)

// The handler and client interfaces share their methods, so a ClientMock can
// stand in for the server's RPC implementation.
var _ gritzv1connect.GritzServiceHandler = (*gritzclient.ClientMock)(nil)

// Server is the harness's API server.
type Server struct {
	// Client makes real API calls to the server as the harness user.
	Client gritzclient.Client
	// URL is the server's base URL.
	URL string

	t       *testing.T
	timeout time.Duration
	mock    atomic.Pointer[gritzclient.ClientMock]
}

// Task is a task in the harness's org.
type Task struct {
	ID int64
}

// CreateTask creates a draft task in the harness's workspace, as the new-task
// page does.
func (s *Server) CreateTask() *Task {
	s.t.Helper()
	resp, err := s.Client.CreateTask(s.t.Context(), &gritzv1.CreateTaskRequest{
		Draft:     true,
		Runner:    runnerID,
		Workspace: workspace,
	})
	if err != nil {
		s.t.Fatalf("CreateTask: %v", err)
	}
	return &Task{ID: resp.Task.Id}
}

// WaitTask waits until cond accepts task id, and returns it.
func (s *Server) WaitTask(id int64, cond func(*gritzv1.Task) bool) *gritzv1.Task {
	s.t.Helper()
	var task *gritzv1.Task
	poll.WaitOn(s.t, func(poll.LogT) poll.Result {
		resp, err := s.Client.GetTask(s.t.Context(), &gritzv1.GetTaskRequest{Id: id})
		if err != nil {
			return poll.Error(fmt.Errorf("GetTask %d: %w", id, err))
		}
		task = resp.Task
		if cond(task) {
			return poll.Success()
		}
		events, err := s.events(id)
		if err != nil {
			return poll.Error(err)
		}
		return poll.Continue("task %d: status %s, version %d, command %s; last events:%s",
			id, task.Status, task.Version, task.Command, formatEvents(events[max(len(events)-5, 0):]))
	}, s.pollOps()...)
	return task
}

// WaitEvent waits until one of task id's events matches, and returns it.
func (s *Server) WaitEvent(id int64, match func(*gritzv1.Event) bool) *gritzv1.Event {
	s.t.Helper()
	var found *gritzv1.Event
	poll.WaitOn(s.t, func(poll.LogT) poll.Result {
		events, err := s.events(id)
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
	}, s.pollOps()...)
	return found
}

// Mock installs m, replacing any previous mock: calls go to m's non-nil
// <Method>Func fields, and every method left nil goes to the real server.
// Mock(nil) removes it.
func (s *Server) Mock(m *gritzclient.ClientMock) {
	s.mock.Store(m)
}

func (s *Server) pollOps() []poll.SettingOp {
	return []poll.SettingOp{poll.WithTimeout(s.timeout), poll.WithDelay(20 * time.Millisecond)}
}

// events returns every event of task id, oldest first.
func (s *Server) events(id int64) ([]*gritzv1.Event, error) {
	var events []*gritzv1.Event
	token := ""
	for {
		resp, err := s.Client.ListEventsByTask(s.t.Context(), &gritzv1.ListEventsByTaskRequest{
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

// route wraps the server's RPC implementation: each call goes to the installed
// mock's method when its <Method>Func is set, and to real otherwise. It sits
// behind the auth and observability interceptors. It isn't an interceptor
// itself because an interceptor would have to wrap the mock's message in a
// *connect.Response[Res], and Res is only known by reflection.
func (s *Server) route(real gritzv1connect.GritzServiceHandler) gritzv1connect.GritzServiceHandler {
	router := &gritzclient.ClientMock{}
	fields := reflect.ValueOf(router).Elem()
	methods := reflect.TypeFor[gritzv1connect.GritzServiceHandler]()
	for i := range methods.NumMethod() {
		name := methods.Method(i).Name
		field := fields.FieldByName(name + "Func")
		if !field.IsValid() {
			panic("e2e: ClientMock has no " + name + "Func; regenerate client_moq.go")
		}
		realMethod := reflect.ValueOf(real).MethodByName(name)
		field.Set(reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
			if m := s.mock.Load(); m != nil && !reflect.ValueOf(m).Elem().FieldByName(name+"Func").IsNil() {
				return reflect.ValueOf(m).MethodByName(name).Call(args)
			}
			return realMethod.Call(args)
		}))
	}
	return router
}
