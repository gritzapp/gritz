package experimentaldocker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestNewRunSpec(t *testing.T) {
	spec := &backend.Spec{
		TaskID:  7,
		Version: 3,
		Env: []string{
			"GRITZ_TASK_ID=7",
			"GRITZ_TOKEN=tok",
			"GRITZ_SERVER=http://server",
			"GH_TOKEN=ghp_secret",
			"GRITZ_SECRETS=GH_TOKEN",
		},
	}

	rs, err := newRunSpec(spec)

	// The server URL, token and secrets get their own fields; GRITZ_SECRETS
	// is dropped, since the secrets map is the mask.
	assert.NilError(t, err)
	assert.DeepEqual(t, rs, &driverv1.RunSpec{
		TaskId:    7,
		ServerUrl: "http://server",
		Token:     "tok",
		Env: map[string]string{
			"GRITZ_TASK_ID": "7",
			"GRITZ_TOKEN":   "tok",
			"GRITZ_SERVER":  "http://server",
		},
		Secrets: map[string]string{"GH_TOKEN": "ghp_secret"},
	}, protocmp.Transform())
}

func TestDriverAddr(t *testing.T) {
	tests := []struct {
		name string
		info container.InspectResponse
		want string
	}{
		{
			name: "label",
			info: container.InspectResponse{
				Config: &container.Config{Labels: map[string]string{driverAddrLabel: "127.0.0.1:4321"}},
			},
			want: "127.0.0.1:4321",
		},
		{
			name: "first network by name",
			info: container.InspectResponse{
				Config: &container.Config{},
				NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
					"zeta":  {IPAddress: "10.0.0.2"},
					"alpha": {IPAddress: "10.0.0.1"},
				}},
			},
			want: "10.0.0.1:8080",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := driverAddr(tt.info)
			assert.NilError(t, err)
			assert.Equal(t, addr, tt.want)
		})
	}
}

func TestDriverAddr_NoIP(t *testing.T) {
	_, err := driverAddr(container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: "abc"},
		Config:            &container.Config{},
		NetworkSettings:   &container.NetworkSettings{},
	})
	assert.ErrorContains(t, err, "no IP address")
}

func TestLaunch_ReuseGoneReturnsErrGone(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })

	_, err = be.Launch(t.Context(), &backend.Spec{TaskID: 1}, &backend.Handle{Type: HandleType, ID: "nonexistent-container-id"})
	assert.ErrorIs(t, err, backend.ErrGone)
}

func TestProbe_Gone(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })

	st, err := be.Probe(t.Context(), backend.Handle{Type: HandleType, ID: "nonexistent-container-id", Data: []byte(`{"version":1}`)})
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateGone)
}
