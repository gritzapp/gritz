package dockerx

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func newInspectResponse(networks map[string]*network.EndpointSettings) container.InspectResponse {
	return container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: networks,
		},
	}
}

func TestRepairNetworks_NoNetworks(t *testing.T) {
	// Arrange
	m := &NetworkRepairerMock{}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", nil)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(repaired), 0)
	assert.Assert(t, cmp.Len(m.ContainerInspectCalls(), 0))
}

func TestRepairNetworks_AllInSync(t *testing.T) {
	// Arrange
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponse(map[string]*network.EndpointSettings{
				"net-a": {NetworkID: "id-a"},
				"net-b": {NetworkID: "id-b"},
			}), nil
		},
		NetworkInspectFunc: func(ctx context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{Name: name, ID: "id-" + name[len(name)-1:]}, nil
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"net-a", "net-b"})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(repaired), 0)
	assert.Assert(t, cmp.Len(m.NetworkDisconnectCalls(), 0))
	assert.Assert(t, cmp.Len(m.NetworkConnectCalls(), 0))
}

func TestRepairNetworks_StaleAttachment(t *testing.T) {
	// Arrange
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponse(map[string]*network.EndpointSettings{
				"compose-default": {NetworkID: "old-id"},
			}), nil
		},
		NetworkInspectFunc: func(ctx context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{Name: name, ID: "new-id"}, nil
		},
		NetworkDisconnectFunc: func(ctx context.Context, networkID, containerID string, force bool) error {
			return nil
		},
		NetworkConnectFunc: func(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error {
			return nil
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"compose-default"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, repaired, []string{"compose-default"})

	disc := m.NetworkDisconnectCalls()
	assert.Assert(t, cmp.Len(disc, 1))
	assert.Equal(t, disc[0].NetworkID, "compose-default")
	assert.Equal(t, disc[0].ContainerID, "c1")
	assert.Equal(t, disc[0].Force, true)

	conn := m.NetworkConnectCalls()
	assert.Assert(t, cmp.Len(conn, 1))
	assert.Equal(t, conn[0].NetworkID, "compose-default")
	assert.Equal(t, conn[0].ContainerID, "c1")
}

func TestRepairNetworks_NotAttached(t *testing.T) {
	// Arrange — workspace declares a network the container isn't attached to.
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponse(map[string]*network.EndpointSettings{}), nil
		},
		NetworkInspectFunc: func(ctx context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{Name: name, ID: "live-id"}, nil
		},
		NetworkConnectFunc: func(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error {
			return nil
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"new-net"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, repaired, []string{"new-net"})
	// No disconnect when the container isn't attached.
	assert.Assert(t, cmp.Len(m.NetworkDisconnectCalls(), 0))
	assert.Assert(t, cmp.Len(m.NetworkConnectCalls(), 1))
}

func TestRepairNetworks_MixedDriftAndHealthy(t *testing.T) {
	// Arrange
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponse(map[string]*network.EndpointSettings{
				"healthy": {NetworkID: "id-healthy"},
				"stale":   {NetworkID: "old-stale"},
			}), nil
		},
		NetworkInspectFunc: func(ctx context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
			switch name {
			case "healthy":
				return network.Inspect{Name: name, ID: "id-healthy"}, nil
			case "stale":
				return network.Inspect{Name: name, ID: "new-stale"}, nil
			}
			t.Fatalf("unexpected network %q", name)
			return network.Inspect{}, nil
		},
		NetworkDisconnectFunc: func(ctx context.Context, networkID, containerID string, force bool) error {
			return nil
		},
		NetworkConnectFunc: func(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error {
			return nil
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"healthy", "stale"})

	// Assert — only the stale one was touched.
	assert.NilError(t, err)
	assert.DeepEqual(t, repaired, []string{"stale"})

	disc := m.NetworkDisconnectCalls()
	assert.DeepEqual(t, testx.ExtractField(disc, "NetworkID"), []string{"stale"})

	conn := m.NetworkConnectCalls()
	assert.DeepEqual(t, testx.ExtractField(conn, "NetworkID"), []string{"stale"})
}

func TestRepairNetworks_DisconnectNotConnectedIsSwallowed(t *testing.T) {
	// Arrange — daemon reports "is not connected" on the disconnect, which
	// can happen when the recreate dropped the endpoint mid-flight. The
	// caller still wants the connect to run.
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponse(map[string]*network.EndpointSettings{
				"net": {NetworkID: "old"},
			}), nil
		},
		NetworkInspectFunc: func(ctx context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{Name: name, ID: "new"}, nil
		},
		NetworkDisconnectFunc: func(ctx context.Context, networkID, containerID string, force bool) error {
			return errors.New("container c1 is not connected to network net")
		},
		NetworkConnectFunc: func(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error {
			return nil
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"net"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, repaired, []string{"net"})
	assert.Assert(t, cmp.Len(m.NetworkConnectCalls(), 1))
}

func TestRepairNetworks_NetworkInspectError(t *testing.T) {
	// Arrange — the declared network doesn't exist in the live registry.
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponse(map[string]*network.EndpointSettings{
				"net": {NetworkID: "old"},
			}), nil
		},
		NetworkInspectFunc: func(ctx context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{}, errors.New("Error: No such network: net")
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"net"})

	// Assert — bail out before any disconnect/connect.
	assert.ErrorContains(t, err, "inspect network \"net\"")
	assert.Equal(t, len(repaired), 0)
	assert.Assert(t, cmp.Len(m.NetworkDisconnectCalls(), 0))
	assert.Assert(t, cmp.Len(m.NetworkConnectCalls(), 0))
}

func TestRepairNetworks_PartialRepairReturnedOnError(t *testing.T) {
	// Arrange — first network repaired OK, second one fails on connect.
	// The returned slice should reflect what actually succeeded.
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponse(map[string]*network.EndpointSettings{
				"first":  {NetworkID: "old1"},
				"second": {NetworkID: "old2"},
			}), nil
		},
		NetworkInspectFunc: func(ctx context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{Name: name, ID: "new-" + name}, nil
		},
		NetworkDisconnectFunc: func(ctx context.Context, networkID, containerID string, force bool) error {
			return nil
		},
		NetworkConnectFunc: func(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error {
			if networkID == "second" {
				return errors.New("connect failed")
			}
			return nil
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"first", "second"})

	// Assert
	assert.ErrorContains(t, err, "connect \"second\"")
	assert.DeepEqual(t, repaired, []string{"first"})
}

func TestRepairNetworks_ContainerInspectError(t *testing.T) {
	// Arrange
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{}, errors.New("no such container")
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"net"})

	// Assert
	assert.ErrorContains(t, err, "inspect container")
	assert.Equal(t, len(repaired), 0)
}

func TestRepairNetworks_NilNetworkSettings(t *testing.T) {
	// Arrange — container with no NetworkSettings (created but never inspected
	// post-start). Behaves the same as "not attached".
	m := &NetworkRepairerMock{
		ContainerInspectFunc: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{NetworkSettings: nil}, nil
		},
		NetworkInspectFunc: func(ctx context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{Name: name, ID: "live"}, nil
		},
		NetworkConnectFunc: func(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error {
			return nil
		},
	}

	// Act
	repaired, err := RepairNetworks(t.Context(), m, "c1", []string{"net"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, repaired, []string{"net"})
	assert.Assert(t, cmp.Len(m.NetworkDisconnectCalls(), 0))
}
