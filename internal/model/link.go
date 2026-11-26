package model

import (
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Link represents a link between a task and an external resource.
type Link struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	Relevance string    `json:"relevance"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	// Kind is the link kind of the task's agent the link was created with: a
	// link with a kind is subscribed, and URL is the canonical URL of the
	// kind's link type. Empty for a plain reference link.
	Kind string `json:"kind,omitempty"`
	// Claim is copied from the kind when the link is created. The task holds
	// it on URL while it is unarchived.
	Claim string `json:"claim,omitempty"`
}

// Proto converts a Link to its protobuf representation.
func (l *Link) Proto() *gritzv1.TaskLink {
	return &gritzv1.TaskLink{
		Id:        l.ID,
		TaskId:    l.TaskID,
		Relevance: l.Relevance,
		Url:       l.URL,
		Title:     l.Title,
		Kind:      l.Kind,
		Claim:     l.Claim,
		CreatedAt: timestamppb.New(l.CreatedAt),
	}
}

// EventPayload builds the link event body that mirrors this task_links row.
// The link event is the timeline source of truth; task_links is the projection.
// LinkID points back at this row, so the link must already be persisted (ID set)
// when this is called.
func (l *Link) EventPayload() *LinkPayload {
	return &LinkPayload{
		LinkID:    l.ID,
		Relevance: l.Relevance,
		URL:       l.URL,
		Title:     l.Title,
		Kind:      l.Kind,
		Claim:     l.Claim,
	}
}

// LinkFromProto converts a protobuf TaskLink to a model Link.
func LinkFromProto(pb *gritzv1.TaskLink) *Link {
	var createdAt time.Time
	if pb.CreatedAt != nil {
		createdAt = pb.CreatedAt.AsTime()
	}
	return &Link{
		ID:        pb.Id,
		TaskID:    pb.TaskId,
		Relevance: pb.Relevance,
		URL:       pb.Url,
		Title:     pb.Title,
		Kind:      pb.Kind,
		Claim:     pb.Claim,
		CreatedAt: createdAt,
	}
}
