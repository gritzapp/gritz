package api_test

import (
	"math/rand/v2"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

func TestLinkGitHubInstallation_MissingID(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.LinkGitHubInstallation(t.Context(), &gritzv1.LinkGitHubInstallationRequest{})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestLinkGitHubInstallation_Unauthenticated(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	client := h.Server.Client("xat_bad")

	// Act
	_, err := client.LinkGitHubInstallation(t.Context(), &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: rand.Int64N(1_000_000_000) + 1,
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnauthenticated)
}

// The harness server has no GitHub integration, so linking is refused even
// for a user with a linked GitHub account.
func TestLinkGitHubInstallation_NotConfigured(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	// Linking a GitHub account is an OAuth flow with no RPC, so write it to the
	// store directly. The id is random so it can't collide with rows left by
	// previous runs.
	err := h.Server.Store.LinkGitHubAccount(t.Context(), nil, h.Org.Owner.UserID, rand.Int64N(1_000_000_000)+1, "owner")
	assert.NilError(t, err)

	// Act
	_, err = h.Org.Client.LinkGitHubInstallation(t.Context(), &gritzv1.LinkGitHubInstallationRequest{
		InstallationId: rand.Int64N(1_000_000_000) + 1,
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
}

// CreateGitHubToken no longer mints tokens on the server; it always reports
// Unimplemented. The real implementation now lives in the runner proxy (#806).
func TestCreateGitHubToken_Unimplemented(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.CreateGitHubToken(t.Context(), &gritzv1.CreateGitHubTokenRequest{})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnimplemented)
}
