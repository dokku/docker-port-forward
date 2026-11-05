package internal

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	dockerClient "github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// inspectByKey returns a containerInspect mock that serves the given
// containers by id or name and reports everything else as missing.
func inspectByKey(containers map[string]container.InspectResponse) func(ctx context.Context, id string) (container.InspectResponse, error) {
	return func(ctx context.Context, id string) (container.InspectResponse, error) {
		info, ok := containers[id]
		if !ok {
			return container.InspectResponse{}, fmt.Errorf("Error response from daemon: No such container: %s", id)
		}
		return info, nil
	}
}

func targetOn(networkName, ip string, running bool) container.InspectResponse {
	return container.InspectResponse{
		ID:    "target-sha",
		Name:  "/target",
		State: &container.State{Running: running},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				networkName: {IPAddress: netip.MustParseAddr(ip)},
			},
		},
	}
}

func helperFor(networkName, address string) container.Summary {
	return container.Summary{
		ID:    "helper-id",
		Names: []string{"/helper"},
		State: "running",
		Labels: map[string]string{
			LabelPortForward:   "true",
			LabelTarget:        "target-sha",
			LabelTargetNetwork: networkName,
			LabelTargetAddress: address,
			LabelPorts:         "8080:80",
		},
	}
}

func TestCheckHelper(t *testing.T) {
	cases := []struct {
		name       string
		helper     container.Summary
		containers map[string]container.InspectResponse
		internal   []string
		wantStale  bool
		wantReason string
	}{
		{
			name:   "legacy helper without labels",
			helper: container.Summary{Labels: map[string]string{LabelTarget: "target-sha"}},
		},
		{
			name:       "ip unchanged",
			helper:     helperFor("bridge", "172.17.0.5"),
			containers: map[string]container.InspectResponse{"target-sha": targetOn("bridge", "172.17.0.5", true)},
		},
		{
			name:       "ip changed",
			helper:     helperFor("bridge", "172.17.0.5"),
			containers: map[string]container.InspectResponse{"target-sha": targetOn("bridge", "172.17.0.9", true)},
			wantStale:  true,
			wantReason: "target IP changed from 172.17.0.5 to 172.17.0.9",
		},
		{
			name:       "ip target stopped",
			helper:     helperFor("bridge", "172.17.0.5"),
			containers: map[string]container.InspectResponse{"target-sha": targetOn("bridge", "172.17.0.9", false)},
		},
		{
			name:       "ip target removed",
			helper:     helperFor("bridge", "172.17.0.5"),
			wantStale:  true,
			wantReason: "target container no longer exists",
		},
		{
			name:       "ip target left network",
			helper:     helperFor("bridge", "172.17.0.5"),
			containers: map[string]container.InspectResponse{"target-sha": targetOn("other", "10.0.0.5", true)},
			wantStale:  true,
			wantReason: `target is no longer attached to network "bridge"`,
		},
		{
			name:       "name resolves",
			helper:     helperFor("my-net", "target"),
			containers: map[string]container.InspectResponse{"target": targetOn("my-net", "10.0.0.9", true)},
		},
		{
			name:       "name stopped",
			helper:     helperFor("my-net", "target"),
			containers: map[string]container.InspectResponse{"target": targetOn("my-net", "10.0.0.9", false)},
		},
		{
			name:       "name removed",
			helper:     helperFor("my-net", "target"),
			wantStale:  true,
			wantReason: `target container "target" no longer exists`,
		},
		{
			name:       "name left network",
			helper:     helperFor("my-net", "target"),
			containers: map[string]container.InspectResponse{"target": targetOn("other", "10.1.0.9", true)},
			wantStale:  true,
			wantReason: `target is no longer attached to network "my-net"`,
		},
		{
			name:       "name on internal network",
			helper:     helperFor("my-net", "target"),
			containers: map[string]container.InspectResponse{"target": targetOn("my-net", "10.0.0.9", true)},
			internal:   []string{"my-net"},
			wantStale:  true,
			wantReason: `network "my-net" is internal, so none of its ports are published`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := internalNetworks(tc.internal...)
			cli.containerInspect = inspectByKey(tc.containers)
			stale, reason, err := CheckHelper(context.Background(), cli, tc.helper)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if stale != tc.wantStale || reason != tc.wantReason {
				t.Fatalf("got stale=%v reason=%q, want stale=%v reason=%q", stale, reason, tc.wantStale, tc.wantReason)
			}
		})
	}
}

func TestCheckHelper_InspectErrorIsReturned(t *testing.T) {
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{}, fmt.Errorf("connection refused")
		},
	}
	if _, _, err := CheckHelper(context.Background(), cli, helperFor("bridge", "172.17.0.5")); err == nil {
		t.Fatal("expected inspect error to be returned")
	}
}

func TestCheckHelper_NetworkInspectErrorIsReturned(t *testing.T) {
	cli := &mockDockerClient{
		networkInspect: func(ctx context.Context, id string) (network.Inspect, error) {
			return network.Inspect{}, fmt.Errorf("connection refused")
		},
	}
	if _, _, err := CheckHelper(context.Background(), cli, helperFor("my-net", "target")); err == nil {
		t.Fatal("expected network inspect error to be returned")
	}
}

func TestCheckHelper_MissingNetworkIsLeftToAttachment(t *testing.T) {
	cli := &mockDockerClient{
		networkInspect: func(ctx context.Context, id string) (network.Inspect, error) {
			return network.Inspect{}, cerrdefs.ErrNotFound
		},
		containerInspect: inspectByKey(map[string]container.InspectResponse{"target": targetOn("other", "10.1.0.9", true)}),
	}
	stale, reason, err := CheckHelper(context.Background(), cli, helperFor("my-net", "target"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale || reason != `target is no longer attached to network "my-net"` {
		t.Fatalf("got stale=%v reason=%q", stale, reason)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func TestStartForward_ReplacesStaleOverlappingHelper(t *testing.T) {
	port := freeTCPPort(t)
	stale := helperFor("bridge", "172.17.0.9")
	stale.Labels[LabelPorts] = fmt.Sprintf("%d:80", port)

	removed := map[string]bool{}
	created := false
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			if id == "target-sha" {
				return targetOn("bridge", "172.17.0.5", true), nil
			}
			return container.InspectResponse{ID: id, State: &container.State{Running: true}}, nil
		},
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			if removed[stale.ID] {
				return nil, nil
			}
			return []container.Summary{stale}, nil
		},
		containerRemove: func(ctx context.Context, id string, options dockerClient.ContainerRemoveOptions) error {
			removed[id] = true
			return nil
		},
		containerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
			created = true
			if config.Labels[LabelTargetAddress] != "172.17.0.5" {
				t.Fatalf("new helper should dial the current IP, got %q", config.Labels[LabelTargetAddress])
			}
			return container.CreateResponse{ID: "new-helper"}, nil
		},
	}

	logger := &captureLogger{}
	result, err := StartForward(context.Background(), ForwardInput{
		Client:    cli,
		Target:    ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
		Pairs:     []PortPair{{LocalPort: port, RemotePort: 80}},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !removed[stale.ID] || !created {
		t.Fatalf("expected stale helper replaced (removed=%v created=%v)", removed, created)
	}
	if result.Existing || result.HelperID != "new-helper" {
		t.Fatalf("unexpected result: %+v", result)
	}
	found := false
	for _, m := range logger.info {
		if strings.Contains(m, `Replacing stale helper "helper": target IP changed from 172.17.0.9 to 172.17.0.5`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected replacement log line, got %v", logger.info)
	}
}

func TestStartForward_RequestedNetwork(t *testing.T) {
	cases := []struct {
		name         string
		helperNet    string
		requested    string
		wantReplaced bool
		logLine      string
	}{
		{
			name:      "same network is reused",
			helperNet: "bridge",
			requested: "bridge",
			logLine:   `Existing helper "helper" already forwards`,
		},
		{
			name:         "other network is replaced",
			helperNet:    "my-net",
			requested:    "bridge",
			wantReplaced: true,
			logLine:      `Replacing helper "helper": network changed from "my-net" to "bridge"`,
		},
		{
			name:      "no network requested reuses any",
			helperNet: "my-net",
			logLine:   `Existing helper "helper" already forwards`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := freeTCPPort(t)
			address := "target"
			if tc.helperNet == "bridge" {
				address = "172.17.0.5"
			}
			existing := helperFor(tc.helperNet, address)
			existing.Labels[LabelPorts] = fmt.Sprintf("%d:80", port)

			info := targetOn("bridge", "172.17.0.5", true)
			info.NetworkSettings.Networks["my-net"] = &network.EndpointSettings{IPAddress: netip.MustParseAddr("10.0.0.5")}

			removed := map[string]bool{}
			var created *container.Config
			var createdHost *container.HostConfig
			cli := &mockDockerClient{
				containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
					if id == "target-sha" || id == "target" {
						return info, nil
					}
					return container.InspectResponse{ID: id, State: &container.State{Running: true}}, nil
				},
				containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
					if removed[existing.ID] {
						return nil, nil
					}
					return []container.Summary{existing}, nil
				},
				containerRemove: func(ctx context.Context, id string, options dockerClient.ContainerRemoveOptions) error {
					removed[id] = true
					return nil
				},
				containerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
					created, createdHost = config, hostConfig
					return container.CreateResponse{ID: "new-helper"}, nil
				},
			}

			logger := &captureLogger{}
			result, err := StartForward(context.Background(), ForwardInput{
				Client:    cli,
				Target:    ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
				Pairs:     []PortPair{{LocalPort: port, RemotePort: 80}},
				Addresses: []string{"127.0.0.1"},
				Detach:    true,
				Network:   tc.requested,
				Logger:    logger,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantReplaced != removed[existing.ID] || tc.wantReplaced != (created != nil) || result.Existing == tc.wantReplaced {
				t.Fatalf("expected replaced=%v, got removed=%v created=%v result=%+v", tc.wantReplaced, removed, created != nil, result)
			}
			if tc.wantReplaced {
				if created.Labels[LabelTargetNetwork] != tc.requested || string(createdHost.NetworkMode) != tc.requested {
					t.Fatalf("expected the new helper on %q, got label %q mode %q", tc.requested, created.Labels[LabelTargetNetwork], createdHost.NetworkMode)
				}
			}
			found := false
			for _, m := range logger.info {
				if strings.Contains(m, tc.logLine) {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected log line %q, got %v", tc.logLine, logger.info)
			}
		})
	}
}

func TestStartForward_UnusableNetworkLeavesHelper(t *testing.T) {
	removed := false
	cli := internalNetworks("my-net")
	cli.containerInspect = func(ctx context.Context, id string) (container.InspectResponse, error) {
		return targetOn("my-net", "10.0.0.5", true), nil
	}
	cli.containerList = func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
		return []container.Summary{helperFor("bridge", "172.17.0.5")}, nil
	}
	cli.containerRemove = func(ctx context.Context, id string, options dockerClient.ContainerRemoveOptions) error {
		removed = true
		return nil
	}

	_, err := StartForward(context.Background(), ForwardInput{
		Client:    cli,
		Target:    ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
		Pairs:     []PortPair{{LocalPort: 8080, RemotePort: 80}},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Logger:    &captureLogger{},
	})
	if err == nil || !strings.Contains(err.Error(), "only on internal networks (my-net)") {
		t.Fatalf("expected an internal networks error, got %v", err)
	}
	if removed {
		t.Fatal("expected the existing helper to be left in place")
	}
}

func TestStartForward_RemovesOrphanedCollidingHelperOnly(t *testing.T) {
	port := freeTCPPort(t)
	orphan := container.Summary{
		ID:     "orphan-id",
		Names:  []string{"/orphan"},
		State:  "running",
		Labels: map[string]string{LabelPortForward: "true", LabelTarget: "gone-sha", LabelPorts: fmt.Sprintf("%d:80", port)},
	}
	live := container.Summary{
		ID:     "live-id",
		Names:  []string{"/live"},
		State:  "running",
		Labels: map[string]string{LabelPortForward: "true", LabelTarget: "other-sha", LabelPorts: fmt.Sprintf("%d:5432", port)},
	}

	var removedIDs []string
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			switch id {
			case "gone-sha":
				return container.InspectResponse{}, fmt.Errorf("No such container: %s", id)
			case "target-sha":
				return targetOn("bridge", "172.17.0.5", true), nil
			}
			return container.InspectResponse{ID: id, State: &container.State{Running: true}}, nil
		},
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			if options.Filters["label"][LabelTarget+"=target-sha"] {
				return nil, nil
			}
			return []container.Summary{orphan, live}, nil
		},
		containerRemove: func(ctx context.Context, id string, options dockerClient.ContainerRemoveOptions) error {
			removedIDs = append(removedIDs, id)
			return nil
		},
		containerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "new-helper"}, nil
		},
	}

	_, err := StartForward(context.Background(), ForwardInput{
		Client:    cli,
		Target:    ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
		Pairs:     []PortPair{{LocalPort: port, RemotePort: 80}},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Logger:    &captureLogger{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removedIDs) != 1 || removedIDs[0] != "orphan-id" {
		t.Fatalf("expected only the orphaned helper removed, got %v", removedIDs)
	}
}

func TestValidateLogConfig(t *testing.T) {
	if err := ValidateLogConfig("", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidateLogConfig("json-file", map[string]string{"max-size": "10m"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidateLogConfig("none", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := ValidateLogConfig("none", map[string]string{"a": "b"})
	if err == nil || err.Error() != "invalid logging opts for driver none" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForHostPorts_SucceedsOncePortIsReleased(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = l.Close()
	}()

	pairs := []PortPair{{Address: "127.0.0.1", LocalPort: port, RemotePort: 80}}
	if err := waitForHostPorts(context.Background(), nil, pairs, 5*time.Second); err != nil {
		t.Fatalf("expected port to become available: %v", err)
	}
}

func TestWaitForHostPorts_TimesOut(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	pairs := []PortPair{{Address: "127.0.0.1", LocalPort: port, RemotePort: 80}}
	if err := waitForHostPorts(context.Background(), nil, pairs, 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout error while the port stays busy")
	}
}
