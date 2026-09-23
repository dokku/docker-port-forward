package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	dockerClient "github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestExpandAddresses(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"default", nil, []string{"127.0.0.1", "::1"}},
		{"localhost expansion", []string{"localhost"}, []string{"127.0.0.1", "::1"}},
		{"bind all", []string{"0.0.0.0"}, []string{"0.0.0.0"}},
		{"dedupe", []string{"localhost", "127.0.0.1"}, []string{"127.0.0.1", "::1"}},
		{"strips empty", []string{"", "127.0.0.1"}, []string{"127.0.0.1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandAddresses(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestEnsureHelperImage_AlwaysPulls(t *testing.T) {
	pulled := false
	cli := &mockDockerClient{
		imagePull: func(ctx context.Context, ref string, options dockerClient.ImagePullOptions) (io.ReadCloser, error) {
			pulled = true
			if ref != "alpine/socat" {
				t.Fatalf("expected ref alpine/socat, got %q", ref)
			}
			return io.NopCloser(strings.NewReader("")), nil
		},
	}
	if err := ensureHelperImage(context.Background(), cli, "alpine/socat", PullAlways, &captureLogger{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pulled {
		t.Fatal("expected pull to be called for PullAlways")
	}
}

func TestEnsureHelperImage_NeverMissing(t *testing.T) {
	cli := &mockDockerClient{
		imageInspect: func(ctx context.Context, id string) (image.InspectResponse, error) {
			return image.InspectResponse{}, errors.New("no such image")
		},
	}
	err := ensureHelperImage(context.Background(), cli, "alpine/socat", PullNever, &captureLogger{})
	if err == nil {
		t.Fatal("expected error when image missing and PullNever")
	}
}

func TestEnsureHelperImage_MissingPullsIfAbsent(t *testing.T) {
	pulled := false
	cli := &mockDockerClient{
		imageInspect: func(ctx context.Context, id string) (image.InspectResponse, error) {
			return image.InspectResponse{}, errors.New("no such image")
		},
		imagePull: func(ctx context.Context, ref string, options dockerClient.ImagePullOptions) (io.ReadCloser, error) {
			pulled = true
			return io.NopCloser(strings.NewReader("")), nil
		},
	}
	if err := ensureHelperImage(context.Background(), cli, "alpine/socat", PullMissing, &captureLogger{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pulled {
		t.Fatal("expected pull to run when image missing")
	}
}

func TestEnsureHelperImage_MissingSkipsIfPresent(t *testing.T) {
	pulled := false
	cli := &mockDockerClient{
		imageInspect: func(ctx context.Context, id string) (image.InspectResponse, error) {
			return image.InspectResponse{ID: "sha"}, nil
		},
		imagePull: func(ctx context.Context, ref string, options dockerClient.ImagePullOptions) (io.ReadCloser, error) {
			pulled = true
			return io.NopCloser(strings.NewReader("")), nil
		},
	}
	if err := ensureHelperImage(context.Background(), cli, "alpine/socat", PullMissing, &captureLogger{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pulled {
		t.Fatal("expected no pull when image already present")
	}
}

func TestEnsureHelperImage_InvalidPolicy(t *testing.T) {
	err := ensureHelperImage(context.Background(), &mockDockerClient{}, "alpine/socat", "bogus", &captureLogger{})
	if err == nil {
		t.Fatal("expected error on invalid pull policy")
	}
}

func TestBuildHelperContainerConfig(t *testing.T) {
	pairs := []PortPair{
		{LocalPort: 8080, RemotePort: 80},
		{LocalPort: 9090, RemotePort: 80},
		{LocalPort: 5432, RemotePort: 5432},
	}
	cfg, hostCfg := buildHelperContainerConfig(helperConfig{
		TargetID:      "target-sha",
		TargetName:    "demo",
		TargetNetwork: "bridge",
		TargetAddress: "172.17.0.5",
		Image:         "alpine/socat",
		Pairs:         pairs,
		Addresses:     []string{"127.0.0.1", "::1"},
		Name:          "port-forward-demo-1234",
		Session:       "sess-xyz",
		ExtraLabels:   map[string]string{"app": "demo"},
		Detach:        true,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		UDPTimeout:    DefaultUDPTimeout,
	})

	if cfg.Image != "alpine/socat" {
		t.Fatalf("unexpected image: %q", cfg.Image)
	}
	if len(cfg.Entrypoint) != 2 || cfg.Entrypoint[0] != "sh" || cfg.Entrypoint[1] != "-c" {
		t.Fatalf("unexpected entrypoint: %v", cfg.Entrypoint)
	}
	if len(cfg.Cmd) != 1 {
		t.Fatalf("expected single sh-c command, got %v", cfg.Cmd)
	}
	shCmd := cfg.Cmd[0]
	// One socat per DISTINCT remote port.
	if strings.Count(shCmd, "socat TCP-LISTEN") != 2 {
		t.Fatalf("expected 2 socat invocations (distinct remotes 80, 5432), got: %s", shCmd)
	}
	if !strings.Contains(shCmd, "TCP:172.17.0.5:80") || !strings.Contains(shCmd, "TCP:172.17.0.5:5432") {
		t.Fatalf("missing expected TCP: targets: %s", shCmd)
	}
	if !strings.Contains(shCmd, "trap 'kill 0' EXIT") {
		t.Fatalf("expected trap to propagate signals: %s", shCmd)
	}

	for _, key := range []string{LabelPortForward, LabelTarget, LabelSession, LabelName, LabelPorts, LabelAddresses, LabelBindings, LabelTargetName, LabelTargetNetwork, LabelTargetAddress, "app"} {
		if _, ok := cfg.Labels[key]; !ok {
			t.Fatalf("missing label %q", key)
		}
	}
	if cfg.Labels[LabelTarget] != "target-sha" {
		t.Fatalf("wrong target label: %q", cfg.Labels[LabelTarget])
	}
	if cfg.Labels[LabelName] != "port-forward-demo-1234" {
		t.Fatalf("wrong name label: %q", cfg.Labels[LabelName])
	}
	if cfg.Labels["app"] != "demo" {
		t.Fatalf("expected extra label app=demo")
	}
	if cfg.Labels[LabelTargetNetwork] != "bridge" || cfg.Labels[LabelTargetAddress] != "172.17.0.5" || cfg.Labels[LabelTargetName] != "demo" {
		t.Fatalf("unexpected target labels: %v", cfg.Labels)
	}
	if cfg.Labels[LabelAddresses] != "127.0.0.1,::1" {
		t.Fatalf("unexpected addresses label: %q", cfg.Labels[LabelAddresses])
	}
	if hostCfg.NetworkMode != "bridge" {
		t.Fatalf("expected network mode bridge, got %q", hostCfg.NetworkMode)
	}

	// Port bindings: same remote has multiple bindings (for 8080 and 9090),
	// each duplicated per address (127.0.0.1 and ::1).
	port80 := "80/tcp"
	for port, bindings := range hostCfg.PortBindings {
		if port.String() == port80 {
			if len(bindings) != 4 {
				t.Fatalf("expected 4 bindings for 80/tcp (2 locals x 2 addrs), got %d: %+v", len(bindings), bindings)
			}
		}
	}

	if hostCfg.AutoRemove {
		t.Fatal("detached helpers must not use AutoRemove")
	}
	if hostCfg.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("expected unless-stopped restart policy, got %q", hostCfg.RestartPolicy.Name)
	}
}

func TestBuildHelperContainerConfig_AttachedAutoRemoves(t *testing.T) {
	cfg, hostCfg := buildHelperContainerConfig(helperConfig{
		TargetID:      "tgt",
		TargetNetwork: "bridge",
		TargetAddress: "172.17.0.2",
		Image:         "alpine/socat",
		Pairs:         []PortPair{{LocalPort: 8080, RemotePort: 80}},
		Addresses:     []string{"127.0.0.1"},
		Name:          "name",
		Session:       "sess",
		Detach:        false,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyAlways},
		UDPTimeout:    DefaultUDPTimeout,
	})
	_ = cfg
	if !hostCfg.AutoRemove {
		t.Fatal("attached helpers should set AutoRemove=true")
	}
	if hostCfg.RestartPolicy.Name != container.RestartPolicyDisabled {
		t.Fatalf("attached helpers must use restart policy \"no\", got %q", hostCfg.RestartPolicy.Name)
	}
}

func TestBuildHelperContainerConfig_DetachedUsesRestartPolicy(t *testing.T) {
	_, hostCfg := buildHelperContainerConfig(helperConfig{
		TargetID:      "tgt",
		TargetNetwork: "my-net",
		TargetAddress: "target",
		Image:         "alpine/socat",
		Pairs:         []PortPair{{LocalPort: 8080, RemotePort: 80}},
		Addresses:     []string{"127.0.0.1"},
		Name:          "name",
		Session:       "sess",
		Detach:        true,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3},
		LogConfig:     container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m"}},
		UDPTimeout:    DefaultUDPTimeout,
	})
	if hostCfg.RestartPolicy.Name != container.RestartPolicyOnFailure || hostCfg.RestartPolicy.MaximumRetryCount != 3 {
		t.Fatalf("unexpected restart policy: %+v", hostCfg.RestartPolicy)
	}
	if hostCfg.LogConfig.Type != "json-file" || hostCfg.LogConfig.Config["max-size"] != "10m" {
		t.Fatalf("unexpected log config: %+v", hostCfg.LogConfig)
	}
}

func TestBuildHelperContainerConfig_TargetsByName(t *testing.T) {
	cfg, _ := buildHelperContainerConfig(helperConfig{
		TargetID:      "tgt",
		TargetNetwork: "my-net",
		TargetAddress: "my-target",
		Image:         "alpine/socat",
		Pairs:         []PortPair{{LocalPort: 8080, RemotePort: 80}, {LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP}},
		Addresses:     []string{"127.0.0.1"},
		Name:          "name",
		Session:       "sess",
		Detach:        true,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		UDPTimeout:    DefaultUDPTimeout,
	})
	shCmd := cfg.Cmd[0]
	if !strings.Contains(shCmd, "TCP:my-target:80") || !strings.Contains(shCmd, "UDP:my-target:53") {
		t.Fatalf("expected socat to target the container name: %s", shCmd)
	}
}

func TestBuildHelperContainerConfig_UDPSpawnsTimedSocat(t *testing.T) {
	pairs := []PortPair{
		{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
		{LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP},
	}
	cfg, hostCfg := buildHelperContainerConfig(helperConfig{
		TargetID:      "target-sha",
		TargetNetwork: "bridge",
		TargetAddress: "172.17.0.5",
		Image:         "alpine/socat",
		Pairs:         pairs,
		Addresses:     []string{"127.0.0.1"},
		Name:          "my-helper",
		Session:       "sess",
		Detach:        true,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		UDPTimeout:    90 * time.Second,
	})

	shCmd := cfg.Cmd[0]
	if !strings.Contains(shCmd, "socat TCP-LISTEN:80,fork,reuseaddr TCP:172.17.0.5:80") {
		t.Fatalf("missing TCP socat invocation: %q", shCmd)
	}
	if !strings.Contains(shCmd, "socat -T 90 UDP-LISTEN:53,fork,reuseaddr UDP:172.17.0.5:53") {
		t.Fatalf("missing UDP socat invocation with custom timeout: %q", shCmd)
	}

	foundTCP := false
	foundUDP := false
	for port := range hostCfg.PortBindings {
		if port.Proto() == "tcp" && int(port.Num()) == 80 {
			foundTCP = true
		}
		if port.Proto() == "udp" && int(port.Num()) == 53 {
			foundUDP = true
		}
	}
	if !foundTCP || !foundUDP {
		t.Fatalf("expected both tcp/80 and udp/53 bindings, got: %+v", hostCfg.PortBindings)
	}
}

func TestBuildHelperContainerConfig_UDPPortsLabelRoundTrip(t *testing.T) {
	pairs := []PortPair{
		{LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP},
		{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
	}
	cfg, _ := buildHelperContainerConfig(helperConfig{
		TargetID:      "tgt",
		TargetNetwork: "bridge",
		TargetAddress: "172.17.0.5",
		Image:         "alpine/socat",
		Pairs:         pairs,
		Addresses:     []string{"127.0.0.1"},
		Name:          "name",
		Session:       "sess",
		Detach:        true,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		UDPTimeout:    DefaultUDPTimeout,
	})
	decoded, err := DecodePortPairs(cfg.Labels[LabelPorts])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(pairs) {
		t.Fatalf("expected %d pairs, got %d (%s)", len(pairs), len(decoded), cfg.Labels[LabelPorts])
	}
	for i := range pairs {
		if decoded[i] != pairs[i] {
			t.Fatalf("pair %d round-trip mismatch: got %+v, want %+v", i, decoded[i], pairs[i])
		}
	}
}

func TestPickTargetNetwork_UserDefinedUsesContainerName(t *testing.T) {
	info := container.InspectResponse{
		Name: "/my-target",
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: netip.MustParseAddr("172.17.0.5")},
				"my-net": {IPAddress: netip.MustParseAddr("10.0.0.5")},
			},
		},
	}
	name, addr, err := pickTargetNetwork(info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "my-net" || addr != "my-target" {
		t.Fatalf("expected my-net/my-target, got %q %q", name, addr)
	}
}

func TestPickTargetNetwork_BridgeUsesIPEvenWithName(t *testing.T) {
	info := container.InspectResponse{
		Name: "/my-target",
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: netip.MustParseAddr("172.17.0.5")},
			},
		},
	}
	name, addr, err := pickTargetNetwork(info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "bridge" || addr != "172.17.0.5" {
		t.Fatalf("got %q %q", name, addr)
	}
}

func TestPickTargetNetwork_PrefersUserDefined(t *testing.T) {
	info := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge":  {IPAddress: netip.MustParseAddr("172.17.0.5")},
				"my-net":  {IPAddress: netip.MustParseAddr("10.0.0.5")},
				"alt-net": {IPAddress: netip.MustParseAddr("10.1.0.5")},
			},
		},
	}
	name, addr, err := pickTargetNetwork(info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "alt-net" && name != "my-net" {
		t.Fatalf("expected a user-defined network, got %q", name)
	}
	if addr == "172.17.0.5" {
		t.Fatalf("bridge IP picked when user-defined networks exist")
	}
}

func TestPickTargetNetwork_BridgeFallback(t *testing.T) {
	info := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: netip.MustParseAddr("172.17.0.5")},
			},
		},
	}
	name, addr, err := pickTargetNetwork(info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "bridge" || addr != "172.17.0.5" {
		t.Fatalf("got %q %q", name, addr)
	}
}

func TestPickTargetNetwork_NoUsable(t *testing.T) {
	info := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{},
		},
	}
	if _, _, err := pickTargetNetwork(info); err == nil {
		t.Fatal("expected error with no networks")
	}
}

func TestFindOverlappingHelper_FindsSharedPair(t *testing.T) {
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{
					ID:     "existing-1",
					Names:  []string{"/port-forward-demo-1111"},
					State:  "running",
					Labels: map[string]string{LabelPorts: "8080:80,5432:5432"},
				},
			}, nil
		},
	}
	requested := []PortPair{{LocalPort: 6379, RemotePort: 6379}, {LocalPort: 8080, RemotePort: 80}}
	got, ok, err := findOverlappingHelper(context.Background(), cli, "target-sha", requested)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected an overlap match")
	}
	if got.ID != "existing-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestFindOverlappingHelper_IgnoresStopped(t *testing.T) {
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{
					ID:     "stopped",
					State:  "exited",
					Labels: map[string]string{LabelPorts: "8080:80"},
				},
			}, nil
		},
	}
	_, ok, err := findOverlappingHelper(context.Background(), cli, "t", []PortPair{{LocalPort: 8080, RemotePort: 80}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("stopped helpers should not count as overlap")
	}
}

func TestFindOverlappingHelper_NoMatchWhenPortsDiffer(t *testing.T) {
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{
					ID:     "existing",
					State:  "running",
					Labels: map[string]string{LabelPorts: "9090:80"},
				},
			}, nil
		},
	}
	_, ok, err := findOverlappingHelper(context.Background(), cli, "t", []PortPair{{LocalPort: 8080, RemotePort: 80}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("different local ports should not overlap")
	}
}

func TestPreflightHostPorts_Ok(t *testing.T) {
	if err := preflightHostPorts([]string{"127.0.0.1"}, []PortPair{{LocalPort: 0, RemotePort: 80}}); err == nil {
		// 0 would auto-assign, but preflight will bind-test it and succeed.
	}
	// Find an unused port the "real" way, then run preflight against it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	if err := preflightHostPorts([]string{"127.0.0.1"}, []PortPair{{LocalPort: port, RemotePort: 80}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflightHostPorts_DetectsConflict(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	err = preflightHostPorts([]string{"127.0.0.1"}, []PortPair{{LocalPort: port, RemotePort: 80}})
	if err == nil {
		t.Fatal("expected preflight error on bound port")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflightHostPorts_FailsWhenIPv4IsBusyEvenIfIPv6IsFree(t *testing.T) {
	// IPv4 conflict alone must fail preflight: the tolerance is only for
	// IPv6-unavailable errors, not "address already in use".
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	err = preflightHostPorts([]string{"127.0.0.1", "::1"}, []PortPair{{LocalPort: port, RemotePort: 80}})
	if err == nil {
		t.Fatal("expected preflight to fail when IPv4 address is busy")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflightHostPorts_DetectsUDPConflict(t *testing.T) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup udp listen failed: %v", err)
	}
	defer c.Close()
	port := c.LocalAddr().(*net.UDPAddr).Port

	err = preflightHostPorts([]string{"127.0.0.1"}, []PortPair{{LocalPort: port, RemotePort: 53, Protocol: ProtocolUDP}})
	if err == nil {
		t.Fatal("expected preflight failure when UDP port is busy")
	}
	if !strings.Contains(err.Error(), "udp") {
		t.Fatalf("expected 'udp' in error, got: %v", err)
	}
}

func TestPreflightHostPorts_TCPAndUDPSamePortDontCollide(t *testing.T) {
	// Pick one port, occupy only its TCP side, and ask preflight to check UDP.
	// They are distinct socket namespaces and must not collide.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup tcp listen failed: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	if err := preflightHostPorts([]string{"127.0.0.1"}, []PortPair{{LocalPort: port, RemotePort: 53, Protocol: ProtocolUDP}}); err != nil {
		t.Fatalf("UDP preflight should succeed even when TCP side is busy, got: %v", err)
	}
}

func TestIsIPv6Unavailable(t *testing.T) {
	cases := map[string]bool{
		"listen tcp [::1]:80: bind: address family not supported by protocol": true,
		"listen tcp [::1]:80: bind: cannot assign requested address":          true,
		"listen tcp 127.0.0.1:80: bind: address already in use":               false,
		"listen tcp 127.0.0.1:80: bind: permission denied":                    false,
	}
	for msg, want := range cases {
		got := isIPv6Unavailable(errors.New(msg))
		if got != want {
			t.Errorf("isIPv6Unavailable(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestResolveAutoPorts_AssignsNonZero(t *testing.T) {
	pairs, err := resolveAutoPorts([]PortPair{
		{LocalPort: 0, RemotePort: 80},
		{LocalPort: 8080, RemotePort: 8080},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pairs[0].LocalPort == 0 {
		t.Fatal("expected auto-allocated port to be non-zero")
	}
	if pairs[1].LocalPort != 8080 {
		t.Fatalf("explicit port mangled: %d", pairs[1].LocalPort)
	}
}

func TestResolveAutoPorts_KeepsAddressAndAllocatesOnIt(t *testing.T) {
	pairs, err := resolveAutoPorts([]PortPair{
		{Address: "127.0.0.1", LocalPort: 0, RemotePort: 80},
		{Address: "127.0.0.1", LocalPort: 0, RemotePort: 53, Protocol: ProtocolUDP},
	}, []string{"192.0.2.1"}) // unassignable default: allocation must use the pair's address
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, p := range pairs {
		if p.Address != "127.0.0.1" || p.LocalPort == 0 {
			t.Fatalf("unexpected pair: %+v", p)
		}
	}
}

func TestPairAddresses(t *testing.T) {
	defaults := []string{"localhost"}
	if got := PairAddresses(PortPair{RemotePort: 80}, defaults); len(got) != 2 || got[0] != "127.0.0.1" || got[1] != "::1" {
		t.Fatalf("default addresses: got %v", got)
	}
	if got := PairAddresses(PortPair{Address: "0.0.0.0", RemotePort: 80}, defaults); len(got) != 1 || got[0] != "0.0.0.0" {
		t.Fatalf("pair address: got %v", got)
	}
}

func TestBuildHelperContainerConfig_PerPairAddresses(t *testing.T) {
	_, hostCfg := buildHelperContainerConfig(helperConfig{
		TargetID:      "tgt",
		TargetNetwork: "bridge",
		TargetAddress: "172.17.0.5",
		Image:         "alpine/socat",
		Pairs: []PortPair{
			{Address: "0.0.0.0", LocalPort: 8080, RemotePort: 80},
			{LocalPort: 5432, RemotePort: 5432},
		},
		Addresses:     []string{"127.0.0.1"},
		Name:          "name",
		Session:       "sess",
		Detach:        true,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
	})
	for port, bindings := range hostCfg.PortBindings {
		if len(bindings) != 1 {
			t.Fatalf("expected one binding for %s, got %+v", port, bindings)
		}
		switch port.Num() {
		case 80:
			if bindings[0].HostIP.String() != "0.0.0.0" || bindings[0].HostPort != "8080" {
				t.Fatalf("unexpected binding for 80: %+v", bindings[0])
			}
		case 5432:
			if bindings[0].HostIP.String() != "127.0.0.1" || bindings[0].HostPort != "5432" {
				t.Fatalf("unexpected binding for 5432: %+v", bindings[0])
			}
		}
	}
}

func TestPreflightHostPorts_UsesPairAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	// The default address is busy, but the pair binds elsewhere.
	if err := preflightHostPorts([]string{"127.0.0.1"}, []PortPair{{Address: "::1", LocalPort: port, RemotePort: 80}}); err != nil && !isIPv6Unavailable(err) {
		t.Fatalf("pair address should be checked instead of defaults: %v", err)
	}
	if err := preflightHostPorts(nil, []PortPair{{Address: "127.0.0.1", LocalPort: port, RemotePort: 80}}); err == nil {
		t.Fatal("expected conflict on the pair's own address")
	}
}

func TestEncodeDecodeBindings(t *testing.T) {
	pairs := []PortPair{
		{Address: "0.0.0.0", LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP},
		{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
	}
	encoded := EncodeBindings(pairs, []string{"localhost"})
	if encoded != "0.0.0.0:8080:80,127.0.0.1:53:53/udp,[::1]:53:53/udp" {
		t.Fatalf("unexpected encoding: %q", encoded)
	}
	decoded := DecodeBindings(encoded)
	want := []PortPair{
		{Address: "0.0.0.0", LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP},
		{Address: "127.0.0.1", LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
		{Address: "::1", LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
	}
	if len(decoded) != len(want) {
		t.Fatalf("got %+v, want %+v", decoded, want)
	}
	for i := range want {
		if decoded[i] != want[i] {
			t.Fatalf("binding %d: got %+v, want %+v", i, decoded[i], want[i])
		}
	}
	if DecodeBindings("") != nil {
		t.Fatal("expected nil for empty label")
	}
}

func TestEncodeDecodePortPairs(t *testing.T) {
	pairs := []PortPair{
		{LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP},
		{LocalPort: 5432, RemotePort: 5432, Protocol: ProtocolTCP},
		{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
	}
	encoded := EncodePortPairs(pairs)
	if encoded != "8080:80,5432:5432,53:53/udp" {
		t.Fatalf("got %q", encoded)
	}
	decoded, err := DecodePortPairs(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decoded) != len(pairs) {
		t.Fatalf("round-trip changed length: %v", decoded)
	}
	for i := range decoded {
		if decoded[i] != pairs[i] {
			t.Fatalf("mismatch at %d: %+v vs %+v", i, decoded[i], pairs[i])
		}
	}
}

func TestDecodePortPairs_LegacyTCPEntriesAreDefaultProtocol(t *testing.T) {
	// Helpers created before UDP support wrote labels without a suffix; the
	// decoder must still read them as TCP.
	decoded, err := DecodePortPairs("8080:80,5432:5432")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("expected 2 pairs, got %+v", decoded)
	}
	for _, p := range decoded {
		if p.Protocol != ProtocolTCP {
			t.Fatalf("legacy entry %+v should be tcp", p)
		}
	}
}

func TestDecodePortPairs_SkipsBadTokens(t *testing.T) {
	decoded, _ := DecodePortPairs("8080:80,garbage,5432:5432,,abc:def,100:200/sctp")
	if len(decoded) != 2 {
		t.Fatalf("expected 2 valid pairs, got %v", decoded)
	}
}

func TestAutoName_TrimsAndSuffixes(t *testing.T) {
	got := autoName("/an-extremely-long-container-name-that-exceeds-sixteen-chars")
	// base name truncated to 16 chars, plus 8 random hex chars after the trailing dash.
	if !strings.HasPrefix(got, "port-forward-an-extremely-lon-") {
		t.Fatalf("unexpected name %q", got)
	}
	if len(got) != len("port-forward-an-extremely-lon-")+8 {
		t.Fatalf("expected suffix of 8 random chars; got %q", got)
	}
}

func TestWaitForRunning_TimesOut(t *testing.T) {
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{
				State: &container.State{Running: false},
			}, nil
		},
	}
	err := waitForRunning(context.Background(), cli, "id", 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitForRunning_Succeeds(t *testing.T) {
	var calls int
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			calls++
			running := calls > 2
			return container.InspectResponse{
				State: &container.State{Running: running},
			}, nil
		},
	}
	if err := waitForRunning(context.Background(), cli, "id", time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForRunning_DetectsExitedContainer(t *testing.T) {
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{
				State: &container.State{Status: "exited", ExitCode: 1},
			}, nil
		},
	}
	err := waitForRunning(context.Background(), cli, "id", time.Second)
	if err == nil {
		t.Fatal("expected error for exited helper")
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Fatalf("expected 'exited' in error, got: %v", err)
	}
}

func TestListHelpers_FiltersByTarget(t *testing.T) {
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			labels := options.Filters["label"]
			if !labels[LabelPortForward+"=true"] || !labels[LabelTarget+"=target-sha"] {
				t.Fatalf("missing expected label filters: %v", labels)
			}
			return []container.Summary{{ID: "h-1"}}, nil
		},
	}
	list, err := ListHelpers(context.Background(), cli, "target-sha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].ID != "h-1" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestListHelpers_NoTargetFilter(t *testing.T) {
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			labels := options.Filters["label"]
			if len(labels) != 1 || !labels[LabelPortForward+"=true"] {
				t.Fatalf("expected only the port-forward label, got %v", labels)
			}
			return []container.Summary{{ID: "h-1"}, {ID: "h-2"}}, nil
		},
	}
	list, err := ListHelpers(context.Background(), cli, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(list))
	}
}

// TestListHelpers_SetsAllAndLabelFilter locks in the migration to the
// github.com/moby/moby/client Filters map: ListHelpers must request all
// containers and populate the "label" filter term with the port-forward label.
func TestListHelpers_SetsAllAndLabelFilter(t *testing.T) {
	var got dockerClient.ContainerListOptions
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			got = options
			return nil, nil
		},
	}
	if _, err := ListHelpers(context.Background(), cli, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.All {
		t.Fatal("expected ListHelpers to request all containers")
	}
	if !got.Filters["label"][LabelPortForward+"=true"] {
		t.Fatalf("expected port-forward label filter to be set, got %+v", got.Filters)
	}
}

func TestRemoveHelpers_CountsSuccessesAndSuppressesMissing(t *testing.T) {
	logger := &captureLogger{}
	cli := &mockDockerClient{
		containerRemove: func(ctx context.Context, id string, options dockerClient.ContainerRemoveOptions) error {
			switch id {
			case "gone":
				return errors.New("Error: No such container: gone")
			case "in-progress":
				return errors.New("removal already in progress")
			case "boom":
				return errors.New("something unexpected")
			}
			return nil
		},
	}
	removed := RemoveHelpers(context.Background(), cli, []string{"ok-1", "gone", "in-progress", "boom", "ok-2"}, logger)
	if removed != 4 {
		t.Fatalf("expected 4 removed, got %d", removed)
	}
	if len(logger.warn) != 1 || !strings.Contains(logger.warn[0], "boom") {
		t.Fatalf("expected exactly one warning, got %v", logger.warn)
	}
}

func TestCleanupStaleHelpers_RemovesAllListed(t *testing.T) {
	removedIDs := []string{}
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			return []container.Summary{{ID: "stale-1"}, {ID: "stale-2"}}, nil
		},
		containerRemove: func(ctx context.Context, id string, options dockerClient.ContainerRemoveOptions) error {
			removedIDs = append(removedIDs, id)
			return nil
		},
	}
	CleanupStaleHelpers(context.Background(), cli, "target-sha", &captureLogger{})
	if len(removedIDs) != 2 {
		t.Fatalf("expected 2 removals, got %v", removedIDs)
	}
}

// newInspectResponseForNetwork returns a ContainerInspectResponse with a bridge
// network so network selection during StartForward can succeed.
func newInspectResponseForNetwork(id string) container.InspectResponse {
	return container.InspectResponse{
		ID:   id,
		Name: "/target",
		State: &container.State{
			Running: true,
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: netip.MustParseAddr("172.17.0.5")},
			},
		},
	}
}

func TestStartForward_IdempotentOnOverlap(t *testing.T) {
	listCalls := 0
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponseForNetwork(id), nil
		},
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			listCalls++
			return []container.Summary{
				{
					ID:     "existing-helper",
					Names:  []string{"/port-forward-target-abcd"},
					State:  "running",
					Labels: map[string]string{LabelPorts: "8080:80"},
				},
			}, nil
		},
		containerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
			t.Fatalf("no new helper should be created on overlap")
			return container.CreateResponse{}, nil
		},
	}

	logger := &captureLogger{}
	result, err := StartForward(context.Background(), ForwardInput{
		Client:    cli,
		Target:    ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
		Pairs:     []PortPair{{LocalPort: 8080, RemotePort: 80}},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Existing {
		t.Fatal("expected Existing=true for overlap")
	}
	if result.HelperID != "existing-helper" {
		t.Fatalf("got helper id %q", result.HelperID)
	}
	if listCalls != 1 {
		t.Fatalf("expected ListHelpers called once, got %d", listCalls)
	}
}

func TestStartForward_DetachedCreatesHelper(t *testing.T) {
	created := false
	started := false
	inspectCount := 0
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			inspectCount++
			info := newInspectResponseForNetwork(id)
			// First inspect (target resolution / network pick) returns running.
			// Subsequent inspects after create() are the helper's; always running.
			return info, nil
		},
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			return nil, nil
		},
		imageInspect: func(ctx context.Context, id string) (image.InspectResponse, error) {
			return image.InspectResponse{ID: "sha"}, nil
		},
		containerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
			created = true
			if hostConfig.AutoRemove {
				t.Fatal("detached helper should not auto-remove")
			}
			if hostConfig.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
				t.Fatalf("expected default restart policy unless-stopped, got %q", hostConfig.RestartPolicy.Name)
			}
			if hostConfig.NetworkMode != "bridge" {
				t.Fatalf("expected network 'bridge' as sole option, got %q", hostConfig.NetworkMode)
			}
			if name != "my-name" {
				t.Fatalf("expected helper name my-name, got %q", name)
			}
			return container.CreateResponse{ID: "new-helper"}, nil
		},
		containerStart: func(ctx context.Context, id string, options dockerClient.ContainerStartOptions) error {
			started = true
			return nil
		},
	}

	requested := []PortPair{{LocalPort: 0, RemotePort: 80}}
	result, err := StartForward(context.Background(), ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
		Pairs:       requested,
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		Name:        "my-name",
		ExtraLabels: map[string]string{"env": "dev"},
		Logger:      &captureLogger{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created || !started {
		t.Fatalf("helper not created/started (created=%v started=%v)", created, started)
	}
	if result.Existing {
		t.Fatal("unexpected Existing=true for fresh helper")
	}
	if result.HelperID != "new-helper" {
		t.Fatalf("unexpected helper id %q", result.HelperID)
	}
	// Auto-allocated local port should be resolved to a non-zero value.
	if result.Pairs[0].LocalPort == 0 {
		t.Fatal("auto-local port should have been resolved")
	}
	if requested[0].Protocol != "" || requested[0].LocalPort != 0 {
		t.Fatalf("caller's pairs were mutated: %+v", requested[0])
	}
}

func TestStartForward_PreflightFailsOnBusyPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	defer l.Close()
	busyPort := l.Addr().(*net.TCPAddr).Port

	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponseForNetwork(id), nil
		},
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			return nil, nil
		},
		containerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
			t.Fatal("no helper should be created when preflight fails")
			return container.CreateResponse{}, nil
		},
	}

	_, err = StartForward(context.Background(), ForwardInput{
		Client:    cli,
		Target:    ResolvedTarget{ContainerID: "target-sha"},
		Pairs:     []PortPair{{LocalPort: busyPort, RemotePort: 80}},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Logger:    &captureLogger{},
	})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStartForward_SkipPreflightAllowsBusyPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	defer l.Close()
	busyPort := l.Addr().(*net.TCPAddr).Port

	created, started := false, false
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return newInspectResponseForNetwork(id), nil
		},
		containerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
			created = true
			return container.CreateResponse{ID: "new-helper"}, nil
		},
		containerStart: func(ctx context.Context, id string, options dockerClient.ContainerStartOptions) error {
			started = true
			return nil
		},
	}

	_, err = StartForward(context.Background(), ForwardInput{
		Client:        cli,
		Target:        ResolvedTarget{ContainerID: "target-sha"},
		Pairs:         []PortPair{{LocalPort: busyPort, RemotePort: 80}},
		Addresses:     []string{"127.0.0.1"},
		Detach:        true,
		SkipPreflight: true,
		Logger:        &captureLogger{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created || !started {
		t.Fatalf("expected helper created and started (created=%v started=%v)", created, started)
	}
}

func TestStartWithPortRetry(t *testing.T) {
	allocated := errors.New("Error response from daemon: driver failed programming external connectivity: Bind for 127.0.0.1:80 failed: port is already allocated")

	t.Run("retries port conflicts until success", func(t *testing.T) {
		calls := 0
		cli := &mockDockerClient{
			containerStart: func(ctx context.Context, id string, options dockerClient.ContainerStartOptions) error {
				calls++
				if calls < 3 {
					return allocated
				}
				return nil
			},
		}
		if err := startWithPortRetry(context.Background(), cli, "id", true, 5*time.Second); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 3 {
			t.Fatalf("expected 3 start attempts, got %d", calls)
		}
	})

	t.Run("no retry when disabled", func(t *testing.T) {
		calls := 0
		cli := &mockDockerClient{
			containerStart: func(ctx context.Context, id string, options dockerClient.ContainerStartOptions) error {
				calls++
				return allocated
			},
		}
		if err := startWithPortRetry(context.Background(), cli, "id", false, 5*time.Second); err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Fatalf("expected 1 start attempt, got %d", calls)
		}
	})

	t.Run("no retry for unrelated errors", func(t *testing.T) {
		calls := 0
		cli := &mockDockerClient{
			containerStart: func(ctx context.Context, id string, options dockerClient.ContainerStartOptions) error {
				calls++
				return errors.New("no such image")
			},
		}
		if err := startWithPortRetry(context.Background(), cli, "id", true, 5*time.Second); err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Fatalf("expected 1 start attempt, got %d", calls)
		}
	})

	t.Run("gives up at the deadline", func(t *testing.T) {
		cli := &mockDockerClient{
			containerStart: func(ctx context.Context, id string, options dockerClient.ContainerStartOptions) error {
				return errors.New("listen tcp4 0.0.0.0:80: bind: address already in use")
			},
		}
		if err := startWithPortRetry(context.Background(), cli, "id", true, 300*time.Millisecond); err == nil {
			t.Fatal("expected error after timeout")
		}
	})
}

func TestStartForward_SkipPreflightRetriesStartAfterReplacingStaleHelper(t *testing.T) {
	port := freeTCPPort(t)
	stale := helperFor("bridge", "172.17.0.9")
	stale.Labels[LabelPorts] = fmt.Sprintf("%d:80", port)

	removed := map[string]bool{}
	startCalls := 0
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
			return container.CreateResponse{ID: "new-helper"}, nil
		},
		containerStart: func(ctx context.Context, id string, options dockerClient.ContainerStartOptions) error {
			startCalls++
			if startCalls == 1 {
				return errors.New("port is already allocated")
			}
			return nil
		},
	}

	result, err := StartForward(context.Background(), ForwardInput{
		Client:        cli,
		Target:        ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
		Pairs:         []PortPair{{LocalPort: port, RemotePort: 80}},
		Addresses:     []string{"127.0.0.1"},
		Detach:        true,
		SkipPreflight: true,
		Logger:        &captureLogger{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !removed[stale.ID] || startCalls != 2 || result.HelperID != "new-helper" {
		t.Fatalf("expected replacement with one retried start (removed=%v startCalls=%d result=%+v)", removed, startCalls, result)
	}
}
