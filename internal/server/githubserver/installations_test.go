package githubserver

import (
	"net/http"
	"path"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-github/v88/github"
	"github.com/migueleliasweb/go-github-mock/src/mock"
	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/model"
)

func TestVerifyInstallationAccess_OrgMemberAllowed(t *testing.T) {
	const installationID, ghUserID = int64(42), int64(1001)
	client, err := github.NewClient(github.WithHTTPClient(mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetAppInstallationsByInstallationId, github.Installation{
			ID:      github.Ptr(installationID),
			Account: &github.User{Login: github.Ptr("acme"), Type: github.Ptr("Organization")},
		}),
		mock.WithRequestMatch(mock.GetUserByAccountId, github.User{
			ID: github.Ptr(ghUserID), Login: github.Ptr("alice"),
		}),
		mock.WithRequestMatch(mock.GetOrgsMembershipsByOrgByUsername, github.Membership{
			State: github.Ptr("active"), User: &github.User{ID: github.Ptr(ghUserID)},
		}),
	)))
	assert.NilError(t, err)
	user := &model.User{GitHubUserID: ghUserID, GitHubUsername: "alice"}
	err = verifyInstallationAccess(t.Context(), client, client, installationID, user)
	assert.NilError(t, err)
}

// A member whose username was renamed/recycled is still resolved by their
// immutable id: the membership endpoint must be queried with the current login,
// not the stale one cached on the user.
func TestVerifyInstallationAccess_StaleUsernameResolvedByID(t *testing.T) {
	const installationID, ghUserID = int64(42), int64(1001)
	var requestedLogin string
	client, err := github.NewClient(github.WithHTTPClient(mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetAppInstallationsByInstallationId, github.Installation{
			ID:      github.Ptr(installationID),
			Account: &github.User{Login: github.Ptr("acme"), Type: github.Ptr("Organization")},
		}),
		mock.WithRequestMatch(mock.GetUserByAccountId, github.User{
			ID: github.Ptr(ghUserID), Login: github.Ptr("newlogin"),
		}),
		mock.WithRequestMatchHandler(mock.GetOrgsMembershipsByOrgByUsername, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestedLogin = path.Base(r.URL.Path)
			_, _ = w.Write(mock.MustMarshal(github.Membership{
				State: github.Ptr("active"), User: &github.User{ID: github.Ptr(ghUserID)},
			}))
		})),
	)))
	assert.NilError(t, err)
	// The cached username is stale; only the id is trustworthy.
	user := &model.User{GitHubUserID: ghUserID, GitHubUsername: "oldlogin"}
	err = verifyInstallationAccess(t.Context(), client, client, installationID, user)
	assert.NilError(t, err)
	assert.Equal(t, requestedLogin, "newlogin")
}

func TestVerifyInstallationAccess_NonMemberDenied(t *testing.T) {
	const installationID, ghUserID = int64(42), int64(1001)
	client, err := github.NewClient(github.WithHTTPClient(mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetAppInstallationsByInstallationId, github.Installation{
			ID:      github.Ptr(installationID),
			Account: &github.User{Login: github.Ptr("acme"), Type: github.Ptr("Organization")},
		}),
		mock.WithRequestMatch(mock.GetUserByAccountId, github.User{
			ID: github.Ptr(ghUserID), Login: github.Ptr("alice"),
		}),
		mock.WithRequestMatchHandler(mock.GetOrgsMembershipsByOrgByUsername, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mock.WriteError(w, http.StatusNotFound, "Not Found")
		})),
	)))
	assert.NilError(t, err)
	user := &model.User{GitHubUserID: ghUserID, GitHubUsername: "alice"}
	err = verifyInstallationAccess(t.Context(), client, client, installationID, user)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// A pending invitation is not active membership.
func TestVerifyInstallationAccess_PendingMembershipDenied(t *testing.T) {
	const installationID, ghUserID = int64(42), int64(1001)
	client, err := github.NewClient(github.WithHTTPClient(mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetAppInstallationsByInstallationId, github.Installation{
			ID:      github.Ptr(installationID),
			Account: &github.User{Login: github.Ptr("acme"), Type: github.Ptr("Organization")},
		}),
		mock.WithRequestMatch(mock.GetUserByAccountId, github.User{
			ID: github.Ptr(ghUserID), Login: github.Ptr("alice"),
		}),
		mock.WithRequestMatch(mock.GetOrgsMembershipsByOrgByUsername, github.Membership{
			State: github.Ptr("pending"), User: &github.User{ID: github.Ptr(ghUserID)},
		}),
	)))
	assert.NilError(t, err)
	user := &model.User{GitHubUserID: ghUserID, GitHubUsername: "alice"}
	err = verifyInstallationAccess(t.Context(), client, client, installationID, user)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// The login resolved to a recycled handle now owned by someone else: the
// membership row is active but its user id does not match the caller.
func TestVerifyInstallationAccess_MembershipUserIDMismatchDenied(t *testing.T) {
	const installationID, ghUserID = int64(42), int64(1001)
	client, err := github.NewClient(github.WithHTTPClient(mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetAppInstallationsByInstallationId, github.Installation{
			ID:      github.Ptr(installationID),
			Account: &github.User{Login: github.Ptr("acme"), Type: github.Ptr("Organization")},
		}),
		mock.WithRequestMatch(mock.GetUserByAccountId, github.User{
			ID: github.Ptr(ghUserID), Login: github.Ptr("alice"),
		}),
		mock.WithRequestMatch(mock.GetOrgsMembershipsByOrgByUsername, github.Membership{
			State: github.Ptr("active"), User: &github.User{ID: github.Ptr(int64(9999))},
		}),
	)))
	assert.NilError(t, err)
	user := &model.User{GitHubUserID: ghUserID, GitHubUsername: "alice"}
	err = verifyInstallationAccess(t.Context(), client, client, installationID, user)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestVerifyInstallationAccess_UserAccountOwnerAllowed(t *testing.T) {
	const installationID, ghUserID = int64(42), int64(1001)
	client, err := github.NewClient(github.WithHTTPClient(mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetAppInstallationsByInstallationId, github.Installation{
			ID:      github.Ptr(installationID),
			Account: &github.User{ID: github.Ptr(ghUserID), Login: github.Ptr("alice"), Type: github.Ptr("User")},
		}),
	)))
	assert.NilError(t, err)
	user := &model.User{GitHubUserID: ghUserID, GitHubUsername: "alice"}
	err = verifyInstallationAccess(t.Context(), client, client, installationID, user)
	assert.NilError(t, err)
}

func TestVerifyInstallationAccess_UserAccountNonOwnerDenied(t *testing.T) {
	const installationID, ghUserID = int64(42), int64(1001)
	client, err := github.NewClient(github.WithHTTPClient(mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetAppInstallationsByInstallationId, github.Installation{
			ID:      github.Ptr(installationID),
			Account: &github.User{ID: github.Ptr(int64(2002)), Login: github.Ptr("bob"), Type: github.Ptr("User")},
		}),
	)))
	assert.NilError(t, err)
	user := &model.User{GitHubUserID: ghUserID, GitHubUsername: "alice"}
	err = verifyInstallationAccess(t.Context(), client, client, installationID, user)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestVerifyInstallationAccess_InstallationNotFound(t *testing.T) {
	const installationID, ghUserID = int64(42), int64(1001)
	client, err := github.NewClient(github.WithHTTPClient(mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(mock.GetAppInstallationsByInstallationId, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mock.WriteError(w, http.StatusNotFound, "Not Found")
		})),
	)))
	assert.NilError(t, err)
	user := &model.User{GitHubUserID: ghUserID, GitHubUsername: "alice"}
	err = verifyInstallationAccess(t.Context(), client, client, installationID, user)
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}
