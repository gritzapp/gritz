package dockerx

//go:generate go tool moq -pkg dockerx -out network_repairer_moq_test.go . NetworkRepairer

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/network"
)

// NetworkRepairer is the minimal Docker API surface used by RepairNetworks.
type NetworkRepairer interface {
	NetworkInspect(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error)
	NetworkDisconnect(ctx context.Context, networkID, containerID string, force bool) error
	NetworkConnect(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error
}

// RepairNetworks reconciles a container's network attachments, as reported by
// inspecting it, with the live Docker network registry. For each attached
// network, it compares the endpoint's pinned network ID against the live
// network's ID and, if they differ, force-disconnects and reconnects by name.
// An endpoint with no pinned ID belongs to a container that has never started,
// and is left alone.
//
// Returns the names of the networks that were actually repaired (may be
// empty). Recovers from stale endpoint pins left behind after
// `docker compose down && up` recreates a network under the same name with
// a fresh ID.
func RepairNetworks(ctx context.Context, c NetworkRepairer, containerID string, endpoints map[string]*network.EndpointSettings) ([]string, error) {
	var repaired []string
	for _, name := range slices.Sorted(maps.Keys(endpoints)) {
		endpoint := endpoints[name]
		if endpoint == nil || endpoint.NetworkID == "" {
			continue
		}
		live, err := c.NetworkInspect(ctx, name, network.InspectOptions{})
		if err != nil {
			return repaired, fmt.Errorf("inspect network %q: %w", name, err)
		}
		if endpoint.NetworkID == live.ID {
			continue
		}
		if err := c.NetworkDisconnect(ctx, name, containerID, true); err != nil && !isNotConnectedErr(err) {
			return repaired, fmt.Errorf("disconnect %q: %w", name, err)
		}
		if err := c.NetworkConnect(ctx, name, containerID, nil); err != nil {
			return repaired, fmt.Errorf("connect %q: %w", name, err)
		}
		repaired = append(repaired, name)
	}
	return repaired, nil
}

// isNotConnectedErr reports whether err is the daemon's response for
// disconnecting a container that isn't attached to the given network.
// The daemon returns this as plain text; there is no typed sentinel.
func isNotConnectedErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "is not connected")
}
