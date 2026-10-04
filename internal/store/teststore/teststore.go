// Package teststore provides helpers for setting up a store in tests.
package teststore

import (
	"cmp"
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
)

// New creates a *store.Store connected to the test database.
// It skips the test if TEST_DATABASE_URL is not set and registers
// a cleanup function to close the database connection.
func New(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	db, err := store.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
	})
	return store.New(db)
}

// Org holds the user and org created by CreateOrg.
type Org struct {
	UserID string
	OrgID  int64
}

// LinkOptions configures a link created with a task.
type LinkOptions struct {
	URL        string
	RoutingKey string
	Title      string
	Subscribe  bool
}

// TaskOptions configures CreateTask behavior. Zero values are replaced
// with sensible defaults.
type TaskOptions struct {
	Status    model.TaskStatus
	Runner    string
	Workspace string
	Namespace string
	Links     []LinkOptions
}

// CreateTask creates a task in the given org, along with any links.
func CreateTask(t *testing.T, s *store.Store, org *Org, opts *TaskOptions) *model.Task {
	t.Helper()
	if opts == nil {
		opts = &TaskOptions{}
	}
	task := &model.Task{
		OrgID:     org.OrgID,
		Status:    cmp.Or(opts.Status, model.TaskStatusPending),
		Runner:    opts.Runner,
		Workspace: cmp.Or(opts.Workspace, "default"),
		Namespace: opts.Namespace,
	}
	if err := s.CreateTask(t.Context(), nil, task); err != nil {
		t.Fatal(err)
	}
	for _, lo := range opts.Links {
		url := cmp.Or(lo.URL, "https://example.com")
		link := &model.Link{
			TaskID:     task.ID,
			URL:        url,
			RoutingKey: cmp.Or(lo.RoutingKey, model.RoutingKey(url)),
			Title:      cmp.Or(lo.Title, "test link"),
			Subscribe:  lo.Subscribe,
			CreatedAt:  time.Now(),
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
			if err := s.CreateWorkspace(ctx, nil,
				cmp.Or(ws.RunnerID, "test-runner"),
				cmp.Or(ws.Name, "default"),
				"",
				org.ID,
			); err != nil {
				t.Fatal(err)
			}
		}
	}
	return result
}
