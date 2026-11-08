package api_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestGetCaller_Key(t *testing.T) {
	t.Parallel()
	// Arrange - a key in an org other than the owner's default
	h := e2e.New(t, nil)
	other := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "other"})
	key := other.CreateKey(&gritzv1.CreateKeyRequest{Name: "test-key"})

	// Act
	resp, err := key.Client.GetCaller(t.Context(), &gritzv1.GetCallerRequest{})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp.Org,
		&gritzv1.Org{Id: other.ID, Name: "other", Owner: h.Org.Owner.UserID},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Org{}, "created_at", "updated_at"),
	)
}
