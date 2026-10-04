package model

import (
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Link represents a link between a task and an external resource.
type Link struct {
	ID         int64     `json:"id"`
	TaskID     int64     `json:"task_id"`
	Relevance  string    `json:"relevance"`
	URL        string    `json:"url"`
	RoutingKey string    `json:"routing_key"`
	Title      string    `json:"title"`
	Subscribe  bool      `json:"subscribe"`
	CreatedAt  time.Time `json:"created_at"`
	// Namespace is the namespace of the link's task, surfaced read-only via the
	// task join for subscription routing. It is not stored on the link itself.
	Namespace string `json:"namespace,omitempty"`
}

// Proto converts a Link to its protobuf representation.
func (l *Link) Proto() *gritzv1.TaskLink {
	return &gritzv1.TaskLink{
		Id:         l.ID,
		TaskId:     l.TaskID,
		Relevance:  l.Relevance,
		Url:        l.URL,
		RoutingKey: l.RoutingKey,
		Title:      l.Title,
		Subscribe:  l.Subscribe,
		CreatedAt:  timestamppb.New(l.CreatedAt),
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
		Subscribe: l.Subscribe,
	}
}

// LinkFromProto converts a protobuf TaskLink to a model Link.
func LinkFromProto(pb *gritzv1.TaskLink) *Link {
	var createdAt time.Time
	if pb.CreatedAt != nil {
		createdAt = pb.CreatedAt.AsTime()
	}
	return &Link{
		ID:         pb.Id,
		TaskID:     pb.TaskId,
		Relevance:  pb.Relevance,
		URL:        pb.Url,
		RoutingKey: pb.RoutingKey,
		Title:      pb.Title,
		Subscribe:  pb.Subscribe,
		CreatedAt:  createdAt,
	}
}
