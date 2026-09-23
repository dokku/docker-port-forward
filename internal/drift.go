package internal

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/moby/moby/api/types/container"
)

// CheckHelper reports whether a helper can no longer reach its target, based
// on the target-network and target-address labels recorded when it was
// created.
//
//   - Helpers without a target-address label (created by older versions)
//     are never reported stale, since their target can't be checked.
//   - When the helper dials a container name (user-defined networks), it is
//     stale if no container with that name exists or the container is no
//     longer attached to the network.
//   - When the helper dials an IP (the default bridge), it is stale if the
//     target container no longer exists, is no longer attached to the
//     network, or is running with a different IP.
//
// A stopped target is not stale: a name still resolves once it restarts,
// and a new IP isn't known until it does.
func CheckHelper(ctx context.Context, cli DockerClientInterface, helper container.Summary) (bool, string, error) {
	address := helper.Labels[LabelTargetAddress]
	networkName := helper.Labels[LabelTargetNetwork]
	if address == "" || networkName == "" {
		return false, "", nil
	}

	targetIP, err := netip.ParseAddr(address)
	if err != nil {
		// Name-based: follow the name, as Docker's DNS does.
		info, err := cli.ContainerInspect(ctx, address)
		if err != nil {
			if isNotFound(err) {
				return true, fmt.Sprintf("target container %q no longer exists", address), nil
			}
			return false, "", fmt.Errorf("error inspecting target %q: %v", address, err)
		}
		if !attachedTo(info, networkName) {
			return true, fmt.Sprintf("target is no longer attached to network %q", networkName), nil
		}
		return false, "", nil
	}

	targetID := helper.Labels[LabelTarget]
	info, err := cli.ContainerInspect(ctx, targetID)
	if err != nil {
		if isNotFound(err) {
			return true, "target container no longer exists", nil
		}
		return false, "", fmt.Errorf("error inspecting target %s: %v", shortID(targetID), err)
	}
	if info.State == nil || !info.State.Running {
		return false, "", nil
	}
	if !attachedTo(info, networkName) {
		return true, fmt.Sprintf("target is no longer attached to network %q", networkName), nil
	}
	current := info.NetworkSettings.Networks[networkName].IPAddress
	if current.IsValid() && current != targetIP {
		return true, fmt.Sprintf("target IP changed from %s to %s", targetIP, current), nil
	}
	return false, "", nil
}

func attachedTo(info container.InspectResponse, networkName string) bool {
	if info.NetworkSettings == nil {
		return false
	}
	endpoint, ok := info.NetworkSettings.Networks[networkName]
	return ok && endpoint != nil
}

// removeOrphanedHelpers removes running helpers for targets other than
// targetID that hold any requested (local port, protocol) and whose target
// container no longer exists, and returns how many were removed. Helpers
// whose target still exists are left alone, so preflight reports the
// conflict as before.
func removeOrphanedHelpers(ctx context.Context, cli DockerClientInterface, targetID string, requested []PortPair, logger Logger) (int, error) {
	type hostPort struct {
		port  int
		proto Protocol
	}
	want := make(map[hostPort]struct{}, len(requested))
	for _, p := range requested {
		want[hostPort{p.LocalPort, NormalizeProtocol(p.Protocol)}] = struct{}{}
	}

	list, err := ListHelpers(ctx, cli, "")
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, c := range list {
		if c.State != "running" || c.Labels[LabelTarget] == targetID {
			continue
		}
		pairs, _ := DecodePortPairs(c.Labels[LabelPorts])
		collides := false
		for _, p := range pairs {
			if _, ok := want[hostPort{p.LocalPort, NormalizeProtocol(p.Protocol)}]; ok {
				collides = true
				break
			}
		}
		if !collides {
			continue
		}
		if _, err := cli.ContainerInspect(ctx, c.Labels[LabelTarget]); err == nil || !isNotFound(err) {
			continue
		}
		name := strings.TrimPrefix(helperName(c), "/")
		logger.Info(fmt.Sprintf("Replacing stale helper %q: target container no longer exists", name))
		if RemoveHelpers(ctx, cli, []string{c.ID}, logger) != 1 {
			return removed, fmt.Errorf("error removing stale helper %q", name)
		}
		removed++
	}
	return removed, nil
}
