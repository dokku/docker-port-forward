package portforward

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/dokku/docker-port-forward/internal"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// fakeClient implements client.APIClient by embedding the interface and
// overriding only the methods Forward and Cleanup use. It records calls so
// tests can assert on what was sent to Docker.
type fakeClient struct {
	client.APIClient

	containers map[string]container.InspectResponse
	list       []container.Summary
	removeErr  map[string]error

	calls       int
	closed      bool
	listFilters []client.Filters
	created     []client.ContainerCreateOptions
	removed     []string
}

func (f *fakeClient) Close() error {
	f.closed = true
	return nil
}

func (f *fakeClient) ContainerInspect(ctx context.Context, id string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	f.calls++
	info, ok := f.containers[id]
	if !ok {
		return client.ContainerInspectResult{}, fmt.Errorf("No such container: %s", id)
	}
	return client.ContainerInspectResult{Container: info}, nil
}

func (f *fakeClient) ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error) {
	f.calls++
	f.listFilters = append(f.listFilters, options.Filters)
	return client.ContainerListResult{Items: f.list}, nil
}

func (f *fakeClient) ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.calls++
	f.created = append(f.created, options)
	return client.ContainerCreateResult{ID: "helper-id"}, nil
}

func (f *fakeClient) ContainerStart(ctx context.Context, id string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
	f.calls++
	return client.ContainerStartResult{}, nil
}

func (f *fakeClient) ContainerRemove(ctx context.Context, id string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.calls++
	if err := f.removeErr[id]; err != nil {
		return client.ContainerRemoveResult{}, err
	}
	f.removed = append(f.removed, id)
	return client.ContainerRemoveResult{}, nil
}

func (f *fakeClient) ImageInspect(ctx context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	f.calls++
	return client.ImageInspectResult{}, nil
}

func (f *fakeClient) ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	f.calls++
	return nil, errors.New("unexpected image pull")
}

// newTargetClient returns a fake with a running target container named "web"
// on the default bridge network, and a helper that reports running once
// created.
func newTargetClient() *fakeClient {
	target := container.InspectResponse{
		ID:    "web-id",
		Name:  "/web",
		State: &container.State{Running: true},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: netip.MustParseAddr("172.17.0.5")},
			},
		},
	}
	return &fakeClient{
		containers: map[string]container.InspectResponse{
			"web":       target,
			"web-id":    target,
			"helper-id": {ID: "helper-id", State: &container.State{Running: true}},
		},
	}
}

type recordingLogger struct {
	info []string
	warn []string
}

func (l *recordingLogger) Info(m string)  { l.info = append(l.info, m) }
func (l *recordingLogger) Warn(m string)  { l.warn = append(l.warn, m) }
func (l *recordingLogger) Error(m string) {}

func TestForward_ValidationErrorsBeforeDocker(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{
			name:    "invalid pull",
			opts:    Options{Target: "web", Ports: []string{"80"}, Pull: "sometimes"},
			wantErr: `invalid --pull value "sometimes": must be one of: always, missing, never`,
		},
		{
			name:    "empty target",
			opts:    Options{Ports: []string{"80"}},
			wantErr: "target is required",
		},
		{
			name:    "non-numeric port",
			opts:    Options{Target: "web", Ports: []string{"abc"}},
			wantErr: `invalid remote port in "abc": not a valid port number: "abc"`,
		},
		{
			name:    "unsupported protocol",
			opts:    Options{Target: "web", Ports: []string{"80/sctp"}},
			wantErr: `invalid port spec "80/sctp": unsupported protocol "sctp" (expected tcp or udp)`,
		},
		{
			name:    "restart policy without detach",
			opts:    Options{Target: "web", Ports: []string{"80"}, RestartPolicy: RestartAlways},
			wantErr: "conflicting options: cannot specify both --restart and an attached (auto-removed) helper; use --detach",
		},
		{
			name:    "invalid restart policy",
			opts:    Options{Target: "web", Ports: []string{"80"}, Detach: true, RestartPolicy: "sometimes"},
			wantErr: "invalid restart policy: unknown policy 'sometimes'; use one of 'no', 'always', 'on-failure', or 'unless-stopped'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newTargetClient()
			tc.opts.Client = fake
			_, err := Forward(context.Background(), tc.opts)
			if err == nil {
				t.Fatalf("expected error %q", tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("got error %q, want %q", err.Error(), tc.wantErr)
			}
			if fake.calls != 0 {
				t.Fatalf("expected no Docker calls, got %d", fake.calls)
			}
		})
	}
}

func TestForward_DefaultsCreateDetachedHelper(t *testing.T) {
	fake := newTargetClient()
	result, err := Forward(context.Background(), Options{
		Target: "web",
		Ports:  []string{":80"},
		Detach: true,
		Client: fake,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.created) != 1 {
		t.Fatalf("expected one helper to be created, got %d", len(fake.created))
	}
	created := fake.created[0]
	if created.Config.Image != DefaultHelperImage {
		t.Fatalf("expected image %q, got %q", DefaultHelperImage, created.Config.Image)
	}
	if created.HostConfig.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("expected unless-stopped, got %q", created.HostConfig.RestartPolicy.Name)
	}
	if created.HostConfig.AutoRemove {
		t.Fatal("detached helper must not auto-remove")
	}

	hostIPs := map[string]bool{}
	for _, bindings := range created.HostConfig.PortBindings {
		for _, b := range bindings {
			hostIPs[b.HostIP.String()] = true
		}
	}
	if !hostIPs["127.0.0.1"] || !hostIPs["::1"] {
		t.Fatalf("expected bindings on 127.0.0.1 and ::1, got %v", hostIPs)
	}

	if result.HelperID != "helper-id" || result.Existing {
		t.Fatalf("unexpected result: %+v", result)
	}
	if fake.closed {
		t.Fatal("a caller-supplied client must not be closed")
	}
}

func TestForward_RestartPolicyPassedThrough(t *testing.T) {
	fake := newTargetClient()
	_, err := Forward(context.Background(), Options{
		Target:        "web",
		Ports:         []string{":80"},
		Addresses:     []string{"127.0.0.1"},
		Detach:        true,
		RestartPolicy: "on-failure:3",
		Client:        fake,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := fake.created[0].HostConfig.RestartPolicy
	if got.Name != container.RestartPolicyOnFailure || got.MaximumRetryCount != 3 {
		t.Fatalf("unexpected restart policy: %+v", got)
	}
}

func TestForward_IdempotentOnOverlap(t *testing.T) {
	fake := newTargetClient()
	fake.list = []container.Summary{{
		ID:     "existing-id",
		Names:  []string{"/port-forward-web-abcd"},
		State:  "running",
		Labels: map[string]string{internal.LabelPorts: "8080:80"},
	}}
	result, err := Forward(context.Background(), Options{
		Target: "web",
		Ports:  []string{"8080:80"},
		Detach: true,
		Client: fake,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Existing || result.HelperID != "existing-id" || result.HelperName != "port-forward-web-abcd" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(fake.created) != 0 {
		t.Fatal("no helper should be created on overlap")
	}
}

func TestForward_ResultPorts(t *testing.T) {
	fake := newTargetClient()
	// Bind only 127.0.0.1: the OS-assigned port is only guaranteed free there.
	result, err := Forward(context.Background(), Options{
		Target:    "web",
		Ports:     []string{":5000", ":5353/udp"},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Client:    fake,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %+v", result.Ports)
	}
	tcp, udp := result.Ports[0], result.Ports[1]
	if tcp.Local == 0 || tcp.Remote != 5000 || tcp.Protocol != "tcp" {
		t.Fatalf("unexpected tcp port: %+v", tcp)
	}
	if udp.Local == 0 || udp.Remote != 5353 || udp.Protocol != "udp" {
		t.Fatalf("unexpected udp port: %+v", udp)
	}
}

func helperSummaries() []container.Summary {
	return []container.Summary{
		{
			ID:     "h1-id",
			Names:  []string{"/pf-1"},
			Labels: map[string]string{internal.LabelPortForward: "true", internal.LabelTarget: "web-id", internal.LabelPorts: "8080:80"},
		},
		{
			ID:     "h2-id",
			Names:  []string{"/pf-2"},
			Labels: map[string]string{internal.LabelPortForward: "true", internal.LabelTarget: "db-id", internal.LabelPorts: "5432:5432"},
		},
	}
}

func TestCleanup_DryRunRemovesNothing(t *testing.T) {
	fake := &fakeClient{list: helperSummaries()}
	result, err := Cleanup(context.Background(), CleanupOptions{DryRun: true, Client: fake})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Helpers) != 2 || result.Removed != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Helpers[0] != (Helper{ID: "h1-id", Name: "pf-1", Target: "web-id", Ports: "8080:80"}) {
		t.Fatalf("unexpected helper: %+v", result.Helpers[0])
	}
	if len(fake.removed) != 0 {
		t.Fatalf("dry run removed %v", fake.removed)
	}
}

func TestCleanup_ByNameRejectsNonHelper(t *testing.T) {
	fake := &fakeClient{containers: map[string]container.InspectResponse{
		"other": {ID: "other-id", Name: "/other", Config: &container.Config{}},
	}}
	_, err := Cleanup(context.Background(), CleanupOptions{Name: "other", Client: fake})
	if err == nil || err.Error() != `container "other" is not a port-forward helper` {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.removed) != 0 {
		t.Fatalf("removed %v", fake.removed)
	}
}

func TestCleanup_ByNameRemovesOnlyThatHelper(t *testing.T) {
	fake := &fakeClient{
		list: helperSummaries(),
		containers: map[string]container.InspectResponse{
			"pf-1": {
				ID:     "h1-id",
				Name:   "/pf-1",
				Config: &container.Config{Labels: map[string]string{internal.LabelPortForward: "true", internal.LabelTarget: "web-id"}},
			},
		},
	}
	result, err := Cleanup(context.Background(), CleanupOptions{Name: "pf-1", Client: fake})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Removed != 1 || len(fake.removed) != 1 || fake.removed[0] != "h1-id" {
		t.Fatalf("unexpected removal: result=%+v removed=%v", result, fake.removed)
	}
}

func TestCleanup_ByTargetFiltersList(t *testing.T) {
	fake := &fakeClient{
		list:       helperSummaries()[:1],
		containers: map[string]container.InspectResponse{"web": {ID: "web-id"}},
	}
	result, err := Cleanup(context.Background(), CleanupOptions{Target: "web", Client: fake})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.listFilters) != 1 || !fake.listFilters[0]["label"][internal.LabelTarget+"=web-id"] {
		t.Fatalf("expected list filtered by target label, got %v", fake.listFilters)
	}
	if result.Removed != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestCleanup_RemovesAllAndCountsMissingAsRemoved(t *testing.T) {
	fake := &fakeClient{
		list:      helperSummaries(),
		removeErr: map[string]error{"h2-id": errors.New("Error response from daemon: No such container: h2-id")},
	}
	result, err := Cleanup(context.Background(), CleanupOptions{Client: fake})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Removed != 2 {
		t.Fatalf("expected 2 removed, got %+v", result)
	}
}

func TestCleanup_RemovalFailureIsLoggedNotReturned(t *testing.T) {
	fake := &fakeClient{
		list:      helperSummaries(),
		removeErr: map[string]error{"h2-id": errors.New("permission denied")},
	}
	logger := &recordingLogger{}
	result, err := Cleanup(context.Background(), CleanupOptions{Client: fake, Logger: logger})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Removed != 1 || len(result.Helpers) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(logger.warn) != 1 || !strings.Contains(logger.warn[0], "permission denied") {
		t.Fatalf("expected removal warning, got %v", logger.warn)
	}
}

func TestCleanup_NilLoggerDoesNotPanic(t *testing.T) {
	fake := &fakeClient{
		list:      helperSummaries(),
		removeErr: map[string]error{"h2-id": errors.New("permission denied")},
	}
	if _, err := Cleanup(context.Background(), CleanupOptions{Client: fake}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.closed {
		t.Fatal("a caller-supplied client must not be closed")
	}
}

// Compile-time check that the fake satisfies the interface Options expects.
var _ client.APIClient = (*fakeClient)(nil)

func TestForward_LogOptionsReachHelper(t *testing.T) {
	fake := newTargetClient()
	_, err := Forward(context.Background(), Options{
		Target:    "web",
		Ports:     []string{":80"},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		LogDriver: "json-file",
		LogOpts:   map[string]string{"max-size": "10m", "max-file": "3"},
		Client:    fake,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	logConfig := fake.created[0].HostConfig.LogConfig
	if logConfig.Type != "json-file" || logConfig.Config["max-size"] != "10m" || logConfig.Config["max-file"] != "3" {
		t.Fatalf("unexpected log config: %+v", logConfig)
	}
}

func TestForward_LogOptsWithNoneDriverFailsBeforeDocker(t *testing.T) {
	fake := newTargetClient()
	_, err := Forward(context.Background(), Options{
		Target:    "web",
		Ports:     []string{"80"},
		LogDriver: "none",
		LogOpts:   map[string]string{"a": "b"},
		Client:    fake,
	})
	if err == nil || err.Error() != "invalid logging opts for driver none" {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("expected no Docker calls, got %d", fake.calls)
	}
}

func TestForward_PerPortAddresses(t *testing.T) {
	fake := newTargetClient()
	result, err := Forward(context.Background(), Options{
		Target:    "web",
		Ports:     []string{"127.0.0.1::80", ":5432"},
		Addresses: []string{"127.0.0.1"},
		Detach:    true,
		Client:    fake,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.Ports[0].Addresses; len(got) != 1 || got[0] != "127.0.0.1" {
		t.Fatalf("unexpected addresses for port 80: %v", got)
	}
	labels := fake.created[0].Config.Labels
	want := fmt.Sprintf("127.0.0.1:%d:80,127.0.0.1:%d:5432", result.Ports[0].Local, result.Ports[1].Local)
	if labels[internal.LabelBindings] != want {
		t.Fatalf("got bindings label %q, want %q", labels[internal.LabelBindings], want)
	}
	if labels[internal.LabelTargetNetwork] != "bridge" || labels[internal.LabelTargetAddress] != "172.17.0.5" {
		t.Fatalf("unexpected target labels: %v", labels)
	}
}

func TestForward_InvalidSpecAddress(t *testing.T) {
	fake := newTargetClient()
	_, err := Forward(context.Background(), Options{
		Target: "web",
		Ports:  []string{"example.com:8080:80"},
		Client: fake,
	})
	if err == nil || err.Error() != `invalid port spec "example.com:8080:80": invalid address "example.com"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

// driftClient returns a fake with two helpers for target "web-id" on the
// default bridge: h1 dials the target's current IP and h2 an old one.
func driftClient() *fakeClient {
	fake := newTargetClient()
	fake.list = []container.Summary{
		{
			ID:    "h1-id",
			Names: []string{"/pf-1"},
			State: "running",
			Labels: map[string]string{
				internal.LabelPortForward:   "true",
				internal.LabelTarget:        "web-id",
				internal.LabelTargetName:    "web",
				internal.LabelTargetNetwork: "bridge",
				internal.LabelTargetAddress: "172.17.0.5",
				internal.LabelPorts:         "8080:80",
				internal.LabelBindings:      "127.0.0.1:8080:80",
			},
		},
		{
			ID:    "h2-id",
			Names: []string{"/pf-2"},
			State: "running",
			Labels: map[string]string{
				internal.LabelPortForward:   "true",
				internal.LabelTarget:        "web-id",
				internal.LabelTargetName:    "web",
				internal.LabelTargetNetwork: "bridge",
				internal.LabelTargetAddress: "172.17.0.9",
				internal.LabelPorts:         "9090:80",
				internal.LabelBindings:      "127.0.0.1:9090:80",
			},
		},
	}
	return fake
}

func TestList_ReportsStaleness(t *testing.T) {
	fake := driftClient()
	helpers, err := List(context.Background(), ListOptions{Client: fake})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(helpers) != 2 {
		t.Fatalf("expected 2 helpers, got %+v", helpers)
	}
	fresh, stale := helpers[0], helpers[1]
	if fresh.Stale || fresh.TargetNetwork != "bridge" || fresh.TargetAddress != "172.17.0.5" || fresh.TargetName != "web" || fresh.Bindings != "127.0.0.1:8080:80" {
		t.Fatalf("unexpected fresh helper: %+v", fresh)
	}
	if !stale.Stale || stale.StaleReason != "target IP changed from 172.17.0.9 to 172.17.0.5" {
		t.Fatalf("unexpected stale helper: %+v", stale)
	}
	if fake.closed {
		t.Fatal("a caller-supplied client must not be closed")
	}
}

func TestList_StaleOnly(t *testing.T) {
	helpers, err := List(context.Background(), ListOptions{Stale: true, Client: driftClient()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(helpers) != 1 || helpers[0].ID != "h2-id" {
		t.Fatalf("expected only the stale helper, got %+v", helpers)
	}
}

func TestCleanup_StaleOnlyRemovesStale(t *testing.T) {
	fake := driftClient()
	result, err := Cleanup(context.Background(), CleanupOptions{Stale: true, Client: fake})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Removed != 1 || len(fake.removed) != 1 || fake.removed[0] != "h2-id" {
		t.Fatalf("expected only h2 removed, got result=%+v removed=%v", result, fake.removed)
	}
}

func TestForward_SkipPreflightAllowsBusyPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen failed: %v", err)
	}
	defer l.Close()
	busy := fmt.Sprintf("127.0.0.1:%d:80", l.Addr().(*net.TCPAddr).Port)

	// Without SkipPreflight the busy port is rejected before any helper is
	// created.
	fake := newTargetClient()
	if _, err := Forward(context.Background(), Options{Target: "web", Ports: []string{busy}, Detach: true, Client: fake}); err == nil || !strings.Contains(err.Error(), "is not available") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	if len(fake.created) != 0 {
		t.Fatal("no helper should be created when preflight fails")
	}

	fake = newTargetClient()
	if _, err := Forward(context.Background(), Options{Target: "web", Ports: []string{busy}, Detach: true, SkipPreflight: true, Client: fake}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.created) != 1 {
		t.Fatalf("expected one helper to be created, got %d", len(fake.created))
	}
}
