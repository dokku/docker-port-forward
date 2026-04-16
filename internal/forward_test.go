package internal

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
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
		imagePull: func(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
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
		imagePull: func(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
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
		imagePull: func(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
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
	cfg, hostCfg := buildHelperContainerConfig(
		"target-sha", "alpine/socat", pairs,
		[]string{"127.0.0.1", "::1"},
		"172.17.0.5",
		"port-forward-demo-1234", "sess-xyz",
		map[string]string{"app": "demo"},
		true, // detach
		DefaultUDPTimeout,
	)

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

	for _, key := range []string{LabelPortForward, LabelTarget, LabelSession, LabelName, LabelPorts, LabelAddresses, "app"} {
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

	// Port bindings: same remote has multiple bindings (for 8080 and 9090),
	// each duplicated per address (127.0.0.1 and ::1).
	port80 := "80/tcp"
	for port, bindings := range hostCfg.PortBindings {
		if string(port) == port80 {
			if len(bindings) != 4 {
				t.Fatalf("expected 4 bindings for 80/tcp (2 locals x 2 addrs), got %d: %+v", len(bindings), bindings)
			}
		}
	}

	if hostCfg.AutoRemove {
		t.Fatal("detached helpers must not use AutoRemove")
	}
}

func TestBuildHelperContainerConfig_AttachedAutoRemoves(t *testing.T) {
	cfg, hostCfg := buildHelperContainerConfig(
		"tgt", "alpine/socat",
		[]PortPair{{LocalPort: 8080, RemotePort: 80}},
		[]string{"127.0.0.1"}, "172.17.0.2",
		"name", "sess", nil,
		false, // attached
		DefaultUDPTimeout,
	)
	_ = cfg
	if !hostCfg.AutoRemove {
		t.Fatal("attached helpers should set AutoRemove=true")
	}
}

func TestBuildHelperContainerConfig_UDPSpawnsTimedSocat(t *testing.T) {
	pairs := []PortPair{
		{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
		{LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP},
	}
	cfg, hostCfg := buildHelperContainerConfig(
		"target-sha", "alpine/socat", pairs,
		[]string{"127.0.0.1"}, "172.17.0.5",
		"my-helper", "sess", nil,
		true,
		90*time.Second,
	)

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
		if port.Proto() == "tcp" && port.Int() == 80 {
			foundTCP = true
		}
		if port.Proto() == "udp" && port.Int() == 53 {
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
	cfg, _ := buildHelperContainerConfig(
		"tgt", "alpine/socat", pairs,
		[]string{"127.0.0.1"}, "172.17.0.5",
		"name", "sess", nil,
		true, DefaultUDPTimeout,
	)
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

func TestPickTargetNetwork_PrefersUserDefined(t *testing.T) {
	info := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge":  {IPAddress: "172.17.0.5"},
				"my-net":  {IPAddress: "10.0.0.5"},
				"alt-net": {IPAddress: "10.1.0.5"},
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
				"bridge": {IPAddress: "172.17.0.5"},
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
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
	})
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
				ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{Running: false}},
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
				ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{Running: running}},
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
				ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{Status: "exited", ExitCode: 1}},
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
			labels := options.Filters.Get("label")
			hasForward := false
			hasTarget := false
			for _, v := range labels {
				if v == LabelPortForward+"=true" {
					hasForward = true
				}
				if v == LabelTarget+"=target-sha" {
					hasTarget = true
				}
			}
			if !hasForward || !hasTarget {
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
			labels := options.Filters.Get("label")
			if len(labels) != 1 || labels[0] != LabelPortForward+"=true" {
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

func TestRemoveHelpers_CountsSuccessesAndSuppressesMissing(t *testing.T) {
	logger := &captureLogger{}
	cli := &mockDockerClient{
		containerRemove: func(ctx context.Context, id string, options container.RemoveOptions) error {
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{{ID: "stale-1"}, {ID: "stale-2"}}, nil
		},
		containerRemove: func(ctx context.Context, id string, options container.RemoveOptions) error {
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
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:   id,
			Name: "/target",
			State: &container.State{
				Running: true,
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.5"},
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
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
			if hostConfig.NetworkMode != "bridge" {
				t.Fatalf("expected network 'bridge' as sole option, got %q", hostConfig.NetworkMode)
			}
			if name != "my-name" {
				t.Fatalf("expected helper name my-name, got %q", name)
			}
			return container.CreateResponse{ID: "new-helper"}, nil
		},
		containerStart: func(ctx context.Context, id string, options container.StartOptions) error {
			started = true
			return nil
		},
	}

	result, err := StartForward(context.Background(), ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
		Pairs:       []PortPair{{LocalPort: 0, RemotePort: 80}},
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
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
