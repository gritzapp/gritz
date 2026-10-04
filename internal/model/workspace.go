package model

import (
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Workspace represents a registered workspace from a runner.
type Workspace struct {
	ID          int64     `json:"id"`
	RunnerID    string    `json:"runner_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OrgID       int64     `json:"org_id"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Proto converts a Workspace to its protobuf representation.
func (w *Workspace) Proto() *gritzv1.RegisteredWorkspace {
	return &gritzv1.RegisteredWorkspace{
		Name:        w.Name,
		RunnerId:    w.RunnerID,
		Description: w.Description,
		UpdatedAt:   timestamppb.New(w.UpdatedAt),
	}
}

// WorkspaceFromProto converts a protobuf RegisteredWorkspace to a model Workspace.
func WorkspaceFromProto(pb *gritzv1.RegisteredWorkspace) *Workspace {
	return &Workspace{
		Name:        pb.Name,
		Description: pb.Description,
	}
}
