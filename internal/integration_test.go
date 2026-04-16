//go:build integration

// Integration tests that exercise StartForward, ProbeListeners, and the
// cleanup flow against a real Docker daemon. Run with:
//
//	go test -tags=integration -v ./internal/...
//
// These tests require:
//   - A reachable Docker daemon (DOCKER_HOST or default socket).
//   - Permission to pull nginx:alpine and alpine/socat.
//   - Free host ports (tests use OS-assigned :0 ports where possible).
package internal

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"
)

const (
	integrationNginxImage = "nginx:alpine"
	integrationPrefix     = "dpf-integration-"
)

// testLogger writes to t.Log; methods match Logger.
type testLogger struct{ t *testing.T }

func (l *testLogger) Info(m string)  { l.t.Helper(); l.t.Logf("INFO:  %s", m) }
func (l *testLogger) Warn(m string)  { l.t.Helper(); l.t.Logf("WARN:  %s", m) }
func (l *testLogger) Error(m string) { l.t.Helper(); l.t.Logf("ERROR: %s", m) }

// setupIntegration returns a Docker client and a context; it also registers a
// cleanup that removes any containers this test created (by label) regardless
// of how the test exits.
func setupIntegration(t *testing.T) (context.Context, DockerClientInterface) {
	t.Helper()
	cli, err := NewDockerClient()
	if err != nil {
		t.Skipf("integration test skipped: Docker client not available: %v", err)
	}
	ctx := context.Background()

	// Fast failure if the daemon isn't reachable.
	if _, err := cli.ContainerList(ctx, container.ListOptions{Limit: 1}); err != nil {
		t.Skipf("integration test skipped: Docker daemon not reachable: %v", err)
	}

	ensureImage(t, ctx, cli, integrationNginxImage)
	ensureImage(t, ctx, cli, DefaultHelperImage)

	t.Cleanup(func() {
		cleanupByLabel(t, ctx, cli, "dpf-integration=true")
		_ = cli.Close()
	})
	return ctx, cli
}

func ensureImage(t *testing.T, ctx context.Context, cli DockerClientInterface, ref string) {
	t.Helper()
	if _, err := cli.ImageInspect(ctx, ref); err == nil {
		return
	}
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		t.Fatalf("failed to pull %s: %v", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatalf("failed to read pull response for %s: %v", ref, err)
	}
}

// startNginxTarget creates a running nginx container that will serve as the
// target of port-forwards. It returns the container's ID.
func startNginxTarget(t *testing.T, ctx context.Context, cli DockerClientInterface) string {
	t.Helper()
	name := fmt.Sprintf("%sngx-%s", integrationPrefix, randomHex(4))
	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:  integrationNginxImage,
			Labels: map[string]string{"dpf-integration": "true"},
		},
		&container.HostConfig{AutoRemove: false},
		&network.NetworkingConfig{},
		nil,
		name,
	)
	if err != nil {
		t.Fatalf("failed to create nginx target: %v", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		t.Fatalf("failed to start nginx target: %v", err)
	}

	// Wait for it to be actually running.
	deadline := time.Now().Add(15 * time.Second)
	for {
		info, err := cli.ContainerInspect(ctx, resp.ID)
		if err == nil && info.State != nil && info.State.Running {
			return resp.ID
		}
		if time.Now().After(deadline) {
			_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
			t.Fatalf("nginx target did not reach running state in time")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// cleanupByLabel force-removes any container carrying a given label. Used as
// both teardown and a safety net.
func cleanupByLabel(t *testing.T, ctx context.Context, cli DockerClientInterface, label string) {
	t.Helper()
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return
	}
	key, val, _ := strings.Cut(label, "=")
	for _, c := range list {
		if c.Labels[key] == val || c.Labels[LabelPortForward] == "true" {
			_ = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
		}
	}
}

// httpGetWithRetry dials the URL and retries briefly until it gets a 200,
// tolerating the window between port bind and socat's TCP-LISTEN being ready.
func httpGetWithRetry(t *testing.T, url string, timeout time.Duration) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		resp, err := client.Get(url)
		if err == nil {
			return resp
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf("http GET %s never succeeded: %v", url, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Detached mode + TCP connectivity
// ---------------------------------------------------------------------------

func TestIntegration_DetachedForwardIsReachable(t *testing.T) {
	ctx, cli := setupIntegration(t)
	targetID := startNginxTarget(t, ctx, cli)

	// Pick a free local port up front so we can curl it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	logger := &testLogger{t: t}
	result, err := StartForward(ctx, ForwardInput{
		Client:    cli,
		Target:    ResolvedTarget{ContainerID: targetID, ContainerName: "nginx"},
		Pairs:     []PortPair{{LocalPort: port, RemotePort: 80}},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Name:      integrationPrefix + "detach-reachable-" + randomHex(4),
		ExtraLabels: map[string]string{
			"dpf-integration": "true",
		},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("StartForward returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(ctx, result.HelperID, container.RemoveOptions{Force: true})
	})

	if result.Existing {
		t.Fatal("expected a new helper to be created")
	}

	// The helper should still be running after StartForward returns because
	// we asked for Detach.
	info, err := cli.ContainerInspect(ctx, result.HelperID)
	if err != nil {
		t.Fatalf("helper disappeared: %v", err)
	}
	if info.State == nil || !info.State.Running {
		t.Fatalf("expected helper to still be running, state=%+v", info.State)
	}

	// Actually reach the nginx default page through the forward.
	resp := httpGetWithRetry(t, fmt.Sprintf("http://127.0.0.1:%d/", port), 10*time.Second)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from nginx through forward, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "nginx") {
		t.Fatalf("response body did not mention nginx: %q", string(body))
	}
}

// ---------------------------------------------------------------------------
// Extra labels
// ---------------------------------------------------------------------------

func TestIntegration_DetachedAppliesExtraLabels(t *testing.T) {
	ctx, cli := setupIntegration(t)
	targetID := startNginxTarget(t, ctx, cli)

	result, err := StartForward(ctx, ForwardInput{
		Client:    cli,
		Target:    ResolvedTarget{ContainerID: targetID},
		Pairs:     []PortPair{{LocalPort: 0, RemotePort: 80}},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Name:      integrationPrefix + "labels-" + randomHex(4),
		ExtraLabels: map[string]string{
			"dpf-integration": "true",
			"team":            "backend",
			"env":             "dev",
		},
		Logger: &testLogger{t: t},
	})
	if err != nil {
		t.Fatalf("StartForward returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(ctx, result.HelperID, container.RemoveOptions{Force: true})
	})

	info, err := cli.ContainerInspect(ctx, result.HelperID)
	if err != nil {
		t.Fatalf("inspect helper: %v", err)
	}
	if info.Config == nil {
		t.Fatal("helper config is nil")
	}
	for k, want := range map[string]string{
		"team":             "backend",
		"env":              "dev",
		LabelPortForward:   "true",
		LabelTarget:        targetID,
		LabelName:          result.HelperName,
	} {
		if got := info.Config.Labels[k]; got != want {
			t.Errorf("label %q: got %q, want %q", k, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Idempotent exit 0 on overlap
// ---------------------------------------------------------------------------

func TestIntegration_IdempotentOverlapReusesHelper(t *testing.T) {
	ctx, cli := setupIntegration(t)
	targetID := startNginxTarget(t, ctx, cli)

	// Allocate two free ports.
	ports := func(n int) []int {
		t.Helper()
		out := make([]int, 0, n)
		for i := 0; i < n; i++ {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("allocate port %d: %v", i, err)
			}
			out = append(out, l.Addr().(*net.TCPAddr).Port)
			_ = l.Close()
		}
		return out
	}(2)
	portA, portB := ports[0], ports[1]

	logger := &testLogger{t: t}

	// First call: creates a helper covering (portA, 80).
	first, err := StartForward(ctx, ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: targetID},
		Pairs:       []PortPair{{LocalPort: portA, RemotePort: 80}},
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		Name:        integrationPrefix + "idem-" + randomHex(4),
		ExtraLabels: map[string]string{"dpf-integration": "true"},
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("first StartForward: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(ctx, first.HelperID, container.RemoveOptions{Force: true})
	})
	if first.Existing {
		t.Fatal("first call should create a new helper")
	}

	// Second call: different local port but same (portA, 80) included → overlap.
	second, err := StartForward(ctx, ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: targetID},
		Pairs:       []PortPair{{LocalPort: portA, RemotePort: 80}, {LocalPort: portB, RemotePort: 8080}},
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		Name:        integrationPrefix + "idem-dup-" + randomHex(4),
		ExtraLabels: map[string]string{"dpf-integration": "true"},
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("second StartForward: %v", err)
	}
	if !second.Existing {
		t.Fatalf("second call with overlapping pair should be idempotent; got new helper %s", second.HelperID)
	}
	if second.HelperID != first.HelperID {
		t.Fatalf("idempotent reply should point at first helper; got %s vs %s", second.HelperID, first.HelperID)
	}

	// Exactly one helper should exist for this target.
	helpers, err := ListHelpers(ctx, cli, targetID)
	if err != nil {
		t.Fatalf("ListHelpers: %v", err)
	}
	running := 0
	for _, h := range helpers {
		if h.State == "running" {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("expected exactly 1 running helper, got %d", running)
	}
}

// ---------------------------------------------------------------------------
// Preflight host-port conflict
// ---------------------------------------------------------------------------

func TestIntegration_PreflightRejectsBusyHostPort(t *testing.T) {
	ctx, cli := setupIntegration(t)
	targetID := startNginxTarget(t, ctx, cli)

	// Occupy a host port, then ask the forwarder to bind the same one.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate busy port: %v", err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	result, err := StartForward(ctx, ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: targetID},
		Pairs:       []PortPair{{LocalPort: busyPort, RemotePort: 80}},
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		Name:        integrationPrefix + "busy-" + randomHex(4),
		ExtraLabels: map[string]string{"dpf-integration": "true"},
		Logger:      &testLogger{t: t},
	})
	if err == nil {
		// Defensive: if a helper did get created, tear it down before failing.
		if result.HelperID != "" {
			_ = cli.ContainerRemove(ctx, result.HelperID, container.RemoveOptions{Force: true})
		}
		t.Fatal("expected preflight to reject conflicting host port, got no error")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected 'not available' error, got: %v", err)
	}

	// No helper should have been created.
	helpers, listErr := ListHelpers(ctx, cli, targetID)
	if listErr != nil {
		t.Fatalf("ListHelpers: %v", listErr)
	}
	if len(helpers) != 0 {
		t.Fatalf("expected no helper to exist; got %d", len(helpers))
	}
}

// ---------------------------------------------------------------------------
// Multi-port: single helper, multiple socat listeners
// ---------------------------------------------------------------------------

func TestIntegration_MultiplePortsOneHelper(t *testing.T) {
	ctx, cli := setupIntegration(t)
	targetID := startNginxTarget(t, ctx, cli)

	// nginx:alpine only listens on 80. Forward it twice from different local
	// ports. Both must hit the same target port through a single helper with
	// a single socat process (per distinct remote), but two port bindings.
	ports := allocatePorts(t, 2)
	result, err := StartForward(ctx, ForwardInput{
		Client: cli,
		Target: ResolvedTarget{ContainerID: targetID},
		Pairs: []PortPair{
			{LocalPort: ports[0], RemotePort: 80},
			{LocalPort: ports[1], RemotePort: 80},
		},
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		Name:        integrationPrefix + "multi-" + randomHex(4),
		ExtraLabels: map[string]string{"dpf-integration": "true"},
		Logger:      &testLogger{t: t},
	})
	if err != nil {
		t.Fatalf("StartForward: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(ctx, result.HelperID, container.RemoveOptions{Force: true})
	})

	for _, p := range ports {
		resp := httpGetWithRetry(t, fmt.Sprintf("http://127.0.0.1:%d/", p), 10*time.Second)
		if resp.StatusCode != 200 {
			t.Errorf("port %d: expected 200, got %d", p, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// A single helper should have ports 80/tcp exposed, with both host ports
	// bound to it.
	info, err := cli.ContainerInspect(ctx, result.HelperID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if info.HostConfig == nil {
		t.Fatal("missing HostConfig")
	}
	bindings := info.HostConfig.PortBindings["80/tcp"]
	if len(bindings) != 2 {
		t.Fatalf("expected 2 bindings for 80/tcp, got %d: %+v", len(bindings), bindings)
	}
	bound := map[string]bool{}
	for _, b := range bindings {
		bound[b.HostPort] = true
	}
	for _, p := range ports {
		if !bound[strconv.Itoa(p)] {
			t.Fatalf("port %d not found in bindings: %+v", p, bindings)
		}
	}
}

// ---------------------------------------------------------------------------
// Auto-detection: nginx listens on 80, probe must report it
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// UDP: round-trip a datagram through a helper-published UDP forward.
// ---------------------------------------------------------------------------

func TestIntegration_UDPForwardRoundTrip(t *testing.T) {
	ctx, cli := setupIntegration(t)

	// Start a UDP echo server in its own container using socat's own image.
	// `-u` is unidirectional; we want the default bidirectional mode so
	// socat writes the reply back with UDP-SENDTO.
	echoName := fmt.Sprintf("%sudp-echo-%s", integrationPrefix, randomHex(4))
	echoResp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: DefaultHelperImage,
			// alpine/socat ENTRYPOINT is `socat`; pass its arguments via Cmd.
			Cmd:    []string{"-T", "3600", "UDP-RECVFROM:9999,fork,reuseaddr", "SYSTEM:cat"},
			Labels: map[string]string{"dpf-integration": "true"},
		},
		&container.HostConfig{AutoRemove: false},
		&network.NetworkingConfig{},
		nil,
		echoName,
	)
	if err != nil {
		t.Fatalf("create udp echo: %v", err)
	}
	defer func() {
		_ = cli.ContainerRemove(ctx, echoResp.ID, container.RemoveOptions{Force: true})
	}()
	if err := cli.ContainerStart(ctx, echoResp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start udp echo: %v", err)
	}

	// Wait for the echo container to be running.
	deadline := time.Now().Add(10 * time.Second)
	for {
		info, err := cli.ContainerInspect(ctx, echoResp.ID)
		if err == nil && info.State != nil && info.State.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("udp echo did not reach running state")
		}
		time.Sleep(100 * time.Millisecond)
	}

	port := allocatePorts(t, 1)[0]
	logger := &testLogger{t: t}

	result, err := StartForward(ctx, ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: echoResp.ID, ContainerName: echoName},
		Pairs:       []PortPair{{LocalPort: port, RemotePort: 9999, Protocol: ProtocolUDP}},
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		Name:        integrationPrefix + "udp-fwd-" + randomHex(4),
		ExtraLabels: map[string]string{"dpf-integration": "true"},
		UDPTimeout:  30 * time.Second,
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("StartForward UDP: %v", err)
	}
	defer func() {
		_ = cli.ContainerRemove(ctx, result.HelperID, container.RemoveOptions{Force: true})
	}()

	// Helper config must expose udp, not tcp.
	info, err := cli.ContainerInspect(ctx, result.HelperID)
	if err != nil {
		t.Fatalf("inspect helper: %v", err)
	}
	foundUDP := false
	for p := range info.HostConfig.PortBindings {
		if p.Proto() == "udp" && p.Int() == 9999 {
			foundUDP = true
		}
	}
	if !foundUDP {
		t.Fatalf("expected udp/9999 binding, got %+v", info.HostConfig.PortBindings)
	}

	// Send a datagram and expect the same bytes back. Retry a few times to
	// tolerate the brief startup window where socat hasn't begun listening.
	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer conn.Close()

	var received []byte
	payload := []byte("dpf-udp-test\n")
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("udp write: %v", err)
		}
		buf := make([]byte, 1024)
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, err := conn.Read(buf)
		if err == nil && n > 0 {
			received = buf[:n]
			break
		}
	}
	if len(received) == 0 {
		t.Fatal("never received UDP echo reply through the forward")
	}
	if string(received) != string(payload) {
		t.Fatalf("echo mismatch: got %q, want %q", received, payload)
	}
}

func TestIntegration_ProbeListenersFindsNginx(t *testing.T) {
	ctx, cli := setupIntegration(t)
	targetID := startNginxTarget(t, ctx, cli)

	logger := &testLogger{t: t}
	listeners, err := ProbeListeners(ctx, cli, targetID, DefaultHelperImage, PullMissing, logger)
	if err != nil {
		t.Fatalf("ProbeListeners: %v", err)
	}
	if len(listeners) == 0 {
		t.Fatal("expected at least one listening port")
	}
	found := false
	for _, l := range listeners {
		if l.Port == 80 && l.Protocol == ProtocolTCP {
			found = true
		}
	}
	if !found {
		t.Fatalf("nginx's tcp/80 not reported: %v", listeners)
	}
}

// ---------------------------------------------------------------------------
// Named cleanup: remove one named helper, leave others running
// ---------------------------------------------------------------------------

func TestIntegration_CleanupByNameRemovesOnlyThatHelper(t *testing.T) {
	ctx, cli := setupIntegration(t)
	targetID := startNginxTarget(t, ctx, cli)

	ports := allocatePorts(t, 2)
	logger := &testLogger{t: t}

	nameKeep := integrationPrefix + "keep-" + randomHex(4)
	keep, err := StartForward(ctx, ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: targetID},
		Pairs:       []PortPair{{LocalPort: ports[0], RemotePort: 80}},
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		Name:        nameKeep,
		ExtraLabels: map[string]string{"dpf-integration": "true"},
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("first StartForward: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(ctx, keep.HelperID, container.RemoveOptions{Force: true})
	})

	nameDrop := integrationPrefix + "drop-" + randomHex(4)
	drop, err := StartForward(ctx, ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: targetID},
		Pairs:       []PortPair{{LocalPort: ports[1], RemotePort: 80}},
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		Name:        nameDrop,
		ExtraLabels: map[string]string{"dpf-integration": "true"},
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("second StartForward: %v", err)
	}

	// Remove by name (mimics what the cleanup command does after inspect).
	removed := RemoveHelpers(ctx, cli, []string{drop.HelperID}, logger)
	if removed != 1 {
		t.Fatalf("expected 1 helper removed, got %d", removed)
	}

	// Dropped helper should be gone.
	if _, err := cli.ContainerInspect(ctx, drop.HelperID); err == nil {
		t.Fatal("expected dropped helper to be gone")
	} else if !isNoSuchContainer(err) {
		t.Fatalf("unexpected inspect error: %v", err)
	}

	// Kept helper should still be running.
	info, err := cli.ContainerInspect(ctx, keep.HelperID)
	if err != nil {
		t.Fatalf("expected kept helper to still exist: %v", err)
	}
	if info.State == nil || !info.State.Running {
		t.Fatal("expected kept helper to still be running")
	}
}

// ---------------------------------------------------------------------------
// Helper: allocate N free local ports for a test.
// ---------------------------------------------------------------------------

func allocatePorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, 0, n)
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("allocate port %d: %v", i, err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports
}

// Compile-time guard to make sure we pulled in all the packages we actually
// use in the build tagged above. Prevents accidental removal.
var _ = dockerClient.ErrorConnectionFailed
