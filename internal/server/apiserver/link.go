package apiserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
)

func (s *Server) CreateLink(ctx context.Context, req *gritzv1.CreateLinkRequest) (*gritzv1.CreateLinkResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Coarse, fail-fast capability gate before the DB read (AllowOp ignores
	// predicates); the instance check happens after the row is loaded.
	if !caller.Scopes.AllowOp(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	// Load the row (org-scoped for tenancy) so the instance check can see the
	// task's id/parent/archived attributes.
	task, err := s.store.GetTask(ctx, nil, req.TaskId, caller.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", req.TaskId))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !caller.Scopes.Allow(authscope.OpTaskWrite, task.ScopeAttr()...) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	if task.Archived {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("task %d is archived", task.ID))
	}
	link := &model.Link{
		TaskID:    req.TaskId,
		Relevance: req.Relevance,
		URL:       req.Url,
		Title:     req.Title,
		CreatedAt: time.Now(),
	}
	if req.Kind != "" {
		if err := s.subscribeLink(ctx, task, link, req.Kind); err != nil {
			return nil, err
		}
	}
	// task_links is the subscription/list projection; the link event is the
	// timeline source of truth. Upsert the projection and append the event in one
	// transaction so they can't drift. link_id points back at the task_links row.
	err = s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.store.CreateLink(ctx, tx, link); err != nil {
			return err
		}
		if err := s.store.CreateEvent(ctx, tx, &model.Event{
			TaskID:  task.ID,
			OrgID:   task.OrgID,
			Payload: link.EventPayload(),
		}); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "link created", "relevance", link.Relevance, "url", link.URL, "kind", link.Kind)
	s.publish(model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task_links", ID: req.TaskId},
			{Action: "created", Type: "link", ID: link.ID},
		},
		OrgID:    caller.OrgID,
		UserID:   caller.ID,
		ClientID: caller.ClientID,
		Time:     time.Now(),
	})
	return &gritzv1.CreateLinkResponse{
		Link: link.Proto(),
	}, nil
}

// subscribeLink makes link a subscribed link of kind, a link kind of the
// task's agent: it stamps the kind and its claim, and replaces the URL with
// the canonical URL of the kind's link type, rejecting one the type doesn't
// accept.
func (s *Server) subscribeLink(ctx context.Context, task *model.Task, link *model.Link, kind string) error {
	agent, err := s.store.GetAgent(ctx, nil, task.OrgID, task.AgentID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	entry, ok := agent.Link(kind)
	if !ok {
		var kinds []string
		for _, l := range agent.Links {
			kinds = append(kinds, strconv.Quote(l.Name()))
		}
		if len(kinds) == 0 {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown link kind %q: agent %q has no link kinds", kind, agent.Name))
		}
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown link kind %q: agent %q has link kinds %s", kind, agent.Name, strings.Join(kinds, ", ")))
	}
	lt, ok := eventrouter.DefaultSchemaRegistry.LinkType(entry.Type)
	if !ok {
		panic(fmt.Sprintf("apiserver: agent %d link kind %q names unregistered link type %q", agent.ID, kind, entry.Type))
	}
	url, ok := lt.Canonical(link.URL)
	if !ok {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("url %q is not a %s, the link type of kind %q", link.URL, lt.Label, kind))
	}
	link.URL = url
	link.Kind = entry.Name()
	link.Claim = entry.Claim
	return nil
}

func (s *Server) ListLinks(ctx context.Context, req *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.AllowOp(authscope.OpTaskRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read task"))
	}
	// The row is loaded for every caller: a predicated caller needs it to check
	// task.id/parent/archived, and the response carries its agent's link kinds.
	task, err := s.store.GetTask(ctx, nil, req.TaskId, caller.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", req.TaskId))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !caller.Scopes.Allow(authscope.OpTaskRead, task.ScopeAttr()...) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read task"))
	}
	links, err := s.store.ListLinksByTask(ctx, nil, req.TaskId, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	agent, err := s.store.GetAgent(ctx, nil, task.OrgID, task.AgentID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListLinksResponse{
		Links: model.ProtoMap(links),
		Kinds: agent.Proto().Links,
	}, nil
}
