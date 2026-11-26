// Package teststore provides helpers for setting up a store in tests.
package teststore

import (
	"cmp"
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"gotest.tools/v3/poll"
)

// openDB opens the test database once per test binary. The pool is never
// closed: it goes away with the process.
var openDB = sync.OnceValues(func() (*sql.DB, error) {
	db, err := store.Open(os.Getenv("TEST_DATABASE_URL"), false)
	if err != nil {
		return nil, err
	}
	// go test runs several packages at once against one Postgres, whose
	// default max_connections is 100.
	db.SetMaxOpenConns(10)
	return db, nil
})

// New returns a *store.Store on the test database, sharing the package's
// pool. It skips the test if TEST_DATABASE_URL is not set.
func New(t *testing.T) *store.Store {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	db, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	return store.New(db)
}

// Org holds the user and org created by CreateOrg.
type Org struct {
	UserID string
	OrgID  int64
}

// LinkOptions configures a link created with a task.
type LinkOptions struct {
	URL   string
	Title string
	Kind  string
	Claim string
}

// TaskOptions configures CreateTask behavior. Zero values are replaced
// with sensible defaults; a zero AgentID creates an agent for the task.
type TaskOptions struct {
	Status    model.TaskStatus
	Runner    string
	Workspace string
	AgentID   int64
	Version   int64
	Links     []LinkOptions
}

// CreateTask creates a task in the given org, along with any links.
func CreateTask(t *testing.T, s *store.Store, org *Org, opts *TaskOptions) *model.Task {
	t.Helper()
	if opts == nil {
		opts = &TaskOptions{}
	}
	if opts.AgentID == 0 {
		opts.AgentID = CreateAgent(t, s, org, nil).ID
	}
	task := &model.Task{
		OrgID:     org.OrgID,
		Status:    cmp.Or(opts.Status, model.TaskStatusPending),
		Runner:    opts.Runner,
		Workspace: cmp.Or(opts.Workspace, "default"),
		AgentID:   opts.AgentID,
		Version:   opts.Version,
	}
	if err := s.CreateTask(t.Context(), nil, task); err != nil {
		t.Fatal(err)
	}
	for _, lo := range opts.Links {
		url := cmp.Or(lo.URL, "https://example.com")
		link := &model.Link{
			TaskID:    task.ID,
			URL:       url,
			Title:     cmp.Or(lo.Title, "test link"),
			Kind:      lo.Kind,
			Claim:     lo.Claim,
			CreatedAt: time.Now(),
		}
		if err := s.CreateLink(t.Context(), nil, link); err != nil {
			t.Fatal(err)
		}
		// Mirror the real link-creation paths: the link event is the timeline
		// source of truth, task_links the projection. Append it alongside the row.
		if err := s.CreateEvent(t.Context(), nil, &model.Event{
			TaskID:  task.ID,
			OrgID:   org.OrgID,
			Payload: link.EventPayload(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return task
}

// CreateAgent creates agent in the given org, filling its OrgID, and a name,
// workspace and runner where agent leaves them unset. agent may be nil.
func CreateAgent(t *testing.T, s *store.Store, org *Org, agent *model.Agent) *model.Agent {
	t.Helper()
	if agent == nil {
		agent = &model.Agent{}
	}
	agent.OrgID = org.OrgID
	agent.Name = cmp.Or(agent.Name, "test-agent")
	agent.Workspace = cmp.Or(agent.Workspace, "default")
	agent.Runner = cmp.Or(agent.Runner, "test-runner")
	if err := s.CreateAgent(t.Context(), nil, agent); err != nil {
		t.Fatal(err)
	}
	return agent
}

// WorkspaceOptions configures a workspace created with an org.
type WorkspaceOptions struct {
	Name     string
	RunnerID string
}

// OrgOptions configures CreateOrg behavior. Zero values are replaced
// with sensible defaults.
type OrgOptions struct {
	Workspaces []WorkspaceOptions
}

// CreateOrg creates a user, org, org membership, and sets the default org.
func CreateOrg(t *testing.T, s *store.Store, opts *OrgOptions) *Org {
	t.Helper()
	ctx := t.Context()
	userID := uuid.NewString()
	err := s.CreateUser(ctx, nil, &model.User{
		ID:    userID,
		Email: userID + "@test.com",
		Name:  "Test User",
	})
	if err != nil {
		t.Fatal(err)
	}
	org := &model.Org{
		Name:  "test-org-" + userID,
		Owner: userID,
	}
	if err := s.CreateOrg(ctx, nil, org); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOrgMember(ctx, nil, &model.OrgMember{
		OrgID:  org.ID,
		UserID: userID,
		Role:   "owner",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDefaultOrgID(ctx, nil, userID, org.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.DestroyOrg(context.Background(), nil, org.ID); err != nil {
			t.Logf("DestroyOrg cleanup: %v", err)
		}
	})
	result := &Org{
		UserID: userID,
		OrgID:  org.ID,
	}
	if opts != nil {
		for _, ws := range opts.Workspaces {
			if err := s.CreateWorkspace(ctx, nil, &model.Workspace{
				RunnerID: cmp.Or(ws.RunnerID, "test-runner"),
				Name:     cmp.Or(ws.Name, "default"),
				OrgID:    org.ID,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return result
}

// WaitRunnerEventsOptions configures WaitRunnerEvents.
type WaitRunnerEventsOptions struct {
	Runner    string
	PageToken string // where to start; empty for the start of the log
	Count     int    // events to wait for
}

// WaitRunnerEvents pages the runner's event log from opts.PageToken until
// opts.Count events have passed the xid watermark (parallel tests' open
// transactions hold it back briefly), and returns them with the token after
// the last one.
func WaitRunnerEvents(t *testing.T, s *store.Store, org *Org, opts WaitRunnerEventsOptions) ([]*model.Event, string) {
	t.Helper()
	token := opts.PageToken
	var got []*model.Event
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		page, err := s.ListRunnerEvents(t.Context(), nil, store.ListRunnerEventsParams{
			OrgID:     org.OrgID,
			Runner:    opts.Runner,
			PageToken: token,
		})
		if err != nil {
			return poll.Error(err)
		}
		got = append(got, page.Items...)
		token = page.NextToken
		if len(got) < opts.Count {
			return poll.Continue("got %d of %d events", len(got), opts.Count)
		}
		return poll.Success()
	}, poll.WithTimeout(10*time.Second), poll.WithDelay(20*time.Millisecond))
	return got, token
}
