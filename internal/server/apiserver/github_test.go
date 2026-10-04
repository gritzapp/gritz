package apiserver

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
)

// newGitHubUserID returns a positive random GitHub user id. Random rather
// than monotonic so values don't collide with rows left over from previous
// test runs (teststore.CreateOrg destroys the org but not its owner user).
func newGitHubUserID() int64 {
	return rand.Int64N(1_000_000_000) + 1
}

// newInstallationID returns a positive random installation id.
func newInstallationID() int64 {
	return rand.Int64N(1_000_000_000) + 1
}

func TestLinkGitHubInstallation(t *testing.T) {
	t.Parallel()
	gh := &GithubServerMock{
		VerifyInstallationAccessFunc: func(context.Context, int64, *model.User) error { return nil },
	}
	srv := New(Options{Store: teststore.New(t), GitHub: gh})
	installationID := newInstallationID()
	org := teststore.CreateOrg(t, srv.store, nil)
	assert.NilError(t, srv.store.LinkGitHubAccount(t.Context(), nil, org.UserID, newGitHubUserID(), "owner"))
	ctx := createCtx(t, org)

	_, err := srv.LinkGitHubInstallation(ctx, &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: installationID,
	})
	assert.NilError(t, err)
	calls := gh.VerifyInstallationAccessCalls()
	assert.DeepEqual(t, testx.ExtractField(calls, "InstallationID"), []int64{installationID})

	got, err := srv.store.GetOrg(ctx, nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.GitHubInstallationID, installationID)
}

// A second org may link an installation already linked to another org: the
// membership check, not a unique index, is what gates linking now.
func TestLinkGitHubInstallation_Shared(t *testing.T) {
	t.Parallel()
	srv := New(Options{
		Store: teststore.New(t),
		GitHub: &GithubServerMock{
			VerifyInstallationAccessFunc: func(context.Context, int64, *model.User) error { return nil },
		},
	})
	installationID := newInstallationID()

	org1 := teststore.CreateOrg(t, srv.store, nil)
	assert.NilError(t, srv.store.LinkGitHubAccount(t.Context(), nil, org1.UserID, newGitHubUserID(), "owner"))
	_, err := srv.LinkGitHubInstallation(createCtx(t, org1), &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: installationID,
	})
	assert.NilError(t, err)

	org2 := teststore.CreateOrg(t, srv.store, nil)
	assert.NilError(t, srv.store.LinkGitHubAccount(t.Context(), nil, org2.UserID, newGitHubUserID(), "owner"))
	_, err = srv.LinkGitHubInstallation(createCtx(t, org2), &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: installationID,
	})
	assert.NilError(t, err)

	for _, o := range []*teststore.Org{org1, org2} {
		got, err := srv.store.GetOrg(t.Context(), nil, o.OrgID)
		assert.NilError(t, err)
		assert.Equal(t, got.GitHubInstallationID, installationID)
	}
}

func TestLinkGitHubInstallation_MissingID(t *testing.T) {
	t.Parallel()
	srv := New(Options{
		Store: teststore.New(t),
		GitHub: &GithubServerMock{
			VerifyInstallationAccessFunc: func(context.Context, int64, *model.User) error { return nil },
		},
	})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	_, err := srv.LinkGitHubInstallation(ctx, &gritzv1.LinkGitHubInstallationRequest{})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestLinkGitHubInstallation_Unauthenticated(t *testing.T) {
	t.Parallel()
	srv := New(Options{
		Store: teststore.New(t),
		GitHub: &GithubServerMock{
			VerifyInstallationAccessFunc: func(context.Context, int64, *model.User) error { return nil },
		},
	})

	_, err := srv.LinkGitHubInstallation(context.Background(), &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: newInstallationID(),
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnauthenticated)
}

func TestLinkGitHubInstallation_NotConfigured(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	assert.NilError(t, srv.store.LinkGitHubAccount(t.Context(), nil, org.UserID, newGitHubUserID(), "owner"))
	ctx := createCtx(t, org)

	_, err := srv.LinkGitHubInstallation(ctx, &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: newInstallationID(),
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
}

func TestLinkGitHubInstallation_NoLinkedGitHubAccount(t *testing.T) {
	t.Parallel()
	srv := New(Options{
		Store: teststore.New(t),
		GitHub: &GithubServerMock{
			VerifyInstallationAccessFunc: func(context.Context, int64, *model.User) error { return nil },
		},
	})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	_, err := srv.LinkGitHubInstallation(ctx, &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: newInstallationID(),
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
}

// A failed membership check propagates as-is (the mock returns PermissionDenied).
func TestLinkGitHubInstallation_AccessDenied(t *testing.T) {
	t.Parallel()
	srv := New(Options{
		Store: teststore.New(t),
		GitHub: &GithubServerMock{
			VerifyInstallationAccessFunc: func(context.Context, int64, *model.User) error {
				return connect.NewError(connect.CodePermissionDenied, errors.New("not a member"))
			},
		},
	})
	org := teststore.CreateOrg(t, srv.store, nil)
	assert.NilError(t, srv.store.LinkGitHubAccount(t.Context(), nil, org.UserID, newGitHubUserID(), "owner"))
	ctx := createCtx(t, org)

	_, err := srv.LinkGitHubInstallation(ctx, &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: newInstallationID(),
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// The installation must not be linked when the check fails.
	got, err := srv.store.GetOrg(ctx, nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.GitHubInstallationID, int64(0))
}

// CreateGitHubToken no longer mints tokens on the server; it always reports
// Unimplemented. The real implementation now lives in the runner proxy (#806).
func TestCreateGitHubToken_Unimplemented(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	_, err := srv.CreateGitHubToken(ctx, &gritzv1.CreateGitHubTokenRequest{})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnimplemented)
}

// Sanity check: the store's clear method removes a previously set installation.
func TestStoreClearGitHubInstallation(t *testing.T) {
	t.Parallel()
	srv := New(Options{
		Store: teststore.New(t),
		GitHub: &GithubServerMock{
			VerifyInstallationAccessFunc: func(context.Context, int64, *model.User) error { return nil },
		},
	})
	installationID := newInstallationID()
	org := teststore.CreateOrg(t, srv.store, nil)
	assert.NilError(t, srv.store.LinkGitHubAccount(t.Context(), nil, org.UserID, newGitHubUserID(), "owner"))
	ctx := createCtx(t, org)
	_, err := srv.LinkGitHubInstallation(ctx, &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: installationID,
	})
	assert.NilError(t, err)

	assert.NilError(t, srv.store.ClearGitHubInstallation(ctx, nil, installationID))

	got, err := srv.store.GetOrg(ctx, nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.GitHubInstallationID, int64(0))
}
