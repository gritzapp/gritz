package githubserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"github.com/google/go-github/v88/github"

	"github.com/gritzapp/gritz/internal/model"
)

// VerifyInstallationAccess returns nil if the user is allowed to link the given
// installation to their org: an active member of the installation's GitHub
// organization, or the owner of a personal-account installation. It returns a
// connect PermissionDenied error when the user is not authorized and NotFound
// when the installation does not exist.
//
// The check is performed entirely server-side with the App's own credentials,
// so it requires no user OAuth token. The caller's GitHub identity was already
// verified at login (LinkGitHubAccount stores a GitHub-confirmed GitHubUserID),
// so "is this verified user a member of the installation's account?" is the only
// remaining question.
func (s *Server) VerifyInstallationAccess(ctx context.Context, installationID int64, user *model.User) error {
	// The App JWT identifies the installation's account; the installation token
	// (which carries the Members:read permission) resolves the caller's current
	// login and their org membership.
	appClient, err := github.NewClient(github.WithTransport(s.app))
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("create app client: %w", err))
	}
	instClient, err := github.NewClient(github.WithHTTPClient(s.tokens.Client(installationID)))
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("create installation client: %w", err))
	}
	return verifyInstallationAccess(ctx, appClient, instClient, installationID, user)
}

// verifyInstallationAccess holds the authorization logic, decoupled from how the
// clients are built so it can be exercised against a test server.
func verifyInstallationAccess(ctx context.Context, appClient, instClient *github.Client, installationID int64, user *model.User) error {
	inst, resp, err := appClient.Apps.GetInstallation(ctx, installationID)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("no GitHub installation with id %d", installationID))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("get installation: %w", err))
	}
	account := inst.GetAccount()
	switch account.GetType() {
	case "Organization":
		// Resolve the caller's CURRENT login from their immutable GitHub user id.
		// user.GitHubUsername is only refreshed via webhooks and GitHub recycles
		// usernames, so a stale handle could match a different person who later
		// claimed it. The membership response's user id is asserted below to close
		// the same gap from the other side.
		ghUser, _, err := instClient.Users.GetByID(ctx, user.GitHubUserID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("resolve github user %d: %w", user.GitHubUserID, err))
		}
		m, resp, err := instClient.Organizations.GetOrgMembership(ctx, ghUser.GetLogin(), account.GetLogin())
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return connect.NewError(connect.CodePermissionDenied, errors.New("you are not a member of this GitHub organization"))
			}
			return connect.NewError(connect.CodeInternal, fmt.Errorf("get org membership: %w", err))
		}
		if m.GetState() != "active" || m.GetUser().GetID() != user.GitHubUserID {
			return connect.NewError(connect.CodePermissionDenied, errors.New("you are not an active member of this GitHub organization"))
		}
		return nil
	case "User":
		// A personal account has no members: only the owner may link it.
		if user.GitHubUserID != account.GetID() {
			return connect.NewError(connect.CodePermissionDenied, errors.New("this installation belongs to a different GitHub user"))
		}
		return nil
	default:
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("unsupported installation account type %q", account.GetType()))
	}
}
