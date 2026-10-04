package apiserver

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

func (s *Server) LinkGitHubInstallation(ctx context.Context, req *gritzv1.LinkGitHubInstallationRequest) (*gritzv1.LinkGitHubInstallationResponse, error) {
	caller := apiauth.Caller(ctx)
	if caller == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	if req.InstallationId == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("installation_id is required"))
	}
	if s.github == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("GitHub integration is not configured"))
	}

	user, err := s.store.GetUser(ctx, nil, caller.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !user.HasGitHub() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("link your GitHub account first at /github/login"))
	}
	// Authorize against live GitHub membership rather than a single-use pending
	// row, so any active member of the installation's org (not just the original
	// installer) can link it to their own org. This is a network round-trip, so
	// it stays outside any DB transaction.
	if err := s.github.VerifyInstallationAccess(ctx, req.InstallationId, user); err != nil {
		return nil, err
	}
	if err := s.store.SetOrgGitHubInstallation(ctx, nil, caller.OrgID, req.InstallationId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "github installation linked",
		"installation_id", req.InstallationId,
		"user_id", caller.ID)
	return &gritzv1.LinkGitHubInstallationResponse{}, nil
}

// CreateGitHubToken is intentionally not implemented on the server. Minting
// installation tokens from the gritz GitHub App granted runners too much
// access to someone else's app, so the real implementation now lives in the
// runner proxy using user-owned credentials. See issue #806.
func (s *Server) CreateGitHubToken(ctx context.Context, req *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("CreateGitHubToken is not implemented on the server; use the runner proxy"))
}
