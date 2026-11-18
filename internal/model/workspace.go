package model

import (
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/taskvar"
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
	// Variables are the task variables the workspace declares, as the runner
	// registered them.
	Variables []taskvar.Decl `json:"variables,omitempty"`
}

// Proto converts a Workspace to its protobuf representation.
func (w *Workspace) Proto() *gritzv1.RegisteredWorkspace {
	return &gritzv1.RegisteredWorkspace{
		Name:        w.Name,
		RunnerId:    w.RunnerID,
		Description: w.Description,
		UpdatedAt:   timestamppb.New(w.UpdatedAt),
		Variables:   VariableDeclsProto(w.Variables),
	}
}

// WorkspaceFromProto converts a protobuf RegisteredWorkspace to a model Workspace.
func WorkspaceFromProto(pb *gritzv1.RegisteredWorkspace) *Workspace {
	return &Workspace{
		Name:        pb.Name,
		Description: pb.Description,
		Variables:   VariableDeclsFromProto(pb.Variables),
	}
}

// VariableDeclsProto converts variable declarations to their protobuf representation.
func VariableDeclsProto(decls []taskvar.Decl) []*gritzv1.VariableDecl {
	var pb []*gritzv1.VariableDecl
	for _, d := range decls {
		pb = append(pb, &gritzv1.VariableDecl{
			Name:        d.Name,
			Description: d.Description,
			Required:    d.Required,
			Default:     d.Default,
		})
	}
	return pb
}

// VariableDeclsFromProto converts protobuf variable declarations to taskvar.Decls.
func VariableDeclsFromProto(pb []*gritzv1.VariableDecl) []taskvar.Decl {
	var decls []taskvar.Decl
	for _, d := range pb {
		decls = append(decls, taskvar.Decl{
			Name:        d.Name,
			Description: d.Description,
			Required:    d.Required,
			Default:     d.Default,
		})
	}
	return decls
}
