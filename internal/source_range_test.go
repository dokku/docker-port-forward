package internal

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	dockerClient "github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestParseSourceRange(t *testing.T) {
	valid := map[string]string{
		"":               "",
		"10.0.0.0/8":     "10.0.0.0/8",
		"10.1.2.3/8":     "10.0.0.0/8",
		"192.0.2.1":      "192.0.2.1/32",
		"0.0.0.0/0":      "0.0.0.0/0",
		"2001:db8::/32":  "2001:db8::/32",
		"2001:db8::1":    "2001:db8::1/128",
		"fe80::1%eth0":   "fe80::1/128",
		"::ffff:1.2.3.4": "::ffff:1.2.3.4/128",
	}
	for value, expected := range valid {
		prefix, err := ParseSourceRange(value)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", value, err)
			continue
		}
		if got := sourceRangeLabel(prefix); got != expected {
			t.Errorf("%q: expected %q, got %q", value, expected, got)
		}
	}

	for _, value := range []string{"localhost", "10.0.0.0/33", "a.b.c.d", "10.0.0.0/8,192.168.0.0/16", "[::1]", "*", "10.0.0.0/"} {
		_, err := ParseSourceRange(value)
		if err == nil {
			t.Errorf("%q: expected an error", value)
			continue
		}
		if !strings.Contains(err.Error(), "invalid --source-range value") {
			t.Errorf("%q: unexpected error %q", value, err)
		}
	}
}

func TestSocatRangeOptions(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"10.0.0.0/8":    ",pf=ip4,range=10.0.0.0/8",
		"192.0.2.1":     ",pf=ip4,range=192.0.2.1/32",
		"2001:db8::/32": ",pf=ip6,range=[2001:db8::]/32",
		"::1":           ",pf=ip6,range=[::1]/128",
	}
	for value, expected := range cases {
		prefix, err := ParseSourceRange(value)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", value, err)
		}
		if got := socatRangeOptions(prefix); got != expected {
			t.Errorf("%q: expected %q, got %q", value, expected, got)
		}
	}
}

func TestBuildHelperContainerConfig_SourceRange(t *testing.T) {
	build := func(sourceRange netip.Prefix) *container.Config {
		cfg, _ := buildHelperContainerConfig(helperConfig{
			TargetID:      "tgt",
			TargetNetwork: "bridge",
			TargetAddress: "172.17.0.5",
			Image:         "alpine/socat",
			Pairs: []PortPair{
				{LocalPort: 8080, RemotePort: 80},
				{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
			},
			Addresses:     []string{"127.0.0.1"},
			Name:          "name",
			Session:       "sess",
			Detach:        true,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			UDPTimeout:    DefaultUDPTimeout,
			SourceRange:   sourceRange,
		})
		return cfg
	}

	cfg := build(netip.MustParsePrefix("10.0.0.0/8"))
	shCmd := cfg.Cmd[0]
	if !strings.Contains(shCmd, "socat TCP-LISTEN:80,fork,reuseaddr,pf=ip4,range=10.0.0.0/8 TCP:172.17.0.5:80") {
		t.Fatalf("expected the range on the TCP listener: %s", shCmd)
	}
	if !strings.Contains(shCmd, "socat -T 60 UDP-LISTEN:53,fork,reuseaddr,pf=ip4,range=10.0.0.0/8 UDP:172.17.0.5:53") {
		t.Fatalf("expected the range on the UDP listener: %s", shCmd)
	}
	if cfg.Labels[LabelSourceRange] != "10.0.0.0/8" {
		t.Fatalf("expected the source range label, got %q", cfg.Labels[LabelSourceRange])
	}

	cfg = build(netip.MustParsePrefix("2001:db8::/32"))
	if !strings.Contains(cfg.Cmd[0], "TCP-LISTEN:80,fork,reuseaddr,pf=ip6,range=[2001:db8::]/32 ") {
		t.Fatalf("expected an IPv6 range on the TCP listener: %s", cfg.Cmd[0])
	}

	cfg = build(netip.Prefix{})
	if strings.Contains(cfg.Cmd[0], "range=") || strings.Contains(cfg.Cmd[0], "pf=") {
		t.Fatalf("expected no range when unset: %s", cfg.Cmd[0])
	}
	if _, ok := cfg.Labels[LabelSourceRange]; ok {
		t.Fatalf("expected no source range label when unset, got %q", cfg.Labels[LabelSourceRange])
	}
}

// startForwardOverHelper runs StartForward against a target that already has
// a helper forwarding the requested port, which still reaches the target, and
// reports whether that helper was removed and a new one created.
func startForwardOverHelper(t *testing.T, helperRange string, requested netip.Prefix) (removed bool, created *container.Config, logger *captureLogger) {
	t.Helper()

	port := freeTCPPort(t)
	existing := helperFor("bridge", "172.17.0.5")
	existing.Labels[LabelPorts] = fmt.Sprintf("%d:80", port)
	if helperRange != "" {
		existing.Labels[LabelSourceRange] = helperRange
	}

	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			if id == "target-sha" {
				return targetOn("bridge", "172.17.0.5", true), nil
			}
			return container.InspectResponse{ID: id, State: &container.State{Running: true}}, nil
		},
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			if removed {
				return nil, nil
			}
			return []container.Summary{existing}, nil
		},
		containerRemove: func(ctx context.Context, id string, options dockerClient.ContainerRemoveOptions) error {
			removed = true
			return nil
		},
		containerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
			created = config
			return container.CreateResponse{ID: "new-helper"}, nil
		},
	}

	logger = &captureLogger{}
	if _, err := StartForward(context.Background(), ForwardInput{
		Client:      cli,
		Target:      ResolvedTarget{ContainerID: "target-sha", ContainerName: "target"},
		Pairs:       []PortPair{{LocalPort: port, RemotePort: 80}},
		Addresses:   []string{"127.0.0.1"},
		Detach:      true,
		SourceRange: requested,
		Logger:      logger,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	return removed, created, logger
}

func TestStartForward_ReplacesHelperWithDifferentSourceRange(t *testing.T) {
	cases := []struct {
		name        string
		helperRange string
		requested   netip.Prefix
		logLine     string
	}{
		{
			name:      "a range where there was none",
			requested: netip.MustParsePrefix("10.0.0.0/8"),
			logLine:   `Replacing helper "helper": source range changed from any address to 10.0.0.0/8`,
		},
		{
			name:        "a different range",
			helperRange: "192.0.2.0/24",
			requested:   netip.MustParsePrefix("10.0.0.0/8"),
			logLine:     `Replacing helper "helper": source range changed from 192.0.2.0/24 to 10.0.0.0/8`,
		},
		{
			name:        "no range where there was one",
			helperRange: "192.0.2.0/24",
			logLine:     `Replacing helper "helper": source range changed from 192.0.2.0/24 to any address`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			removed, created, logger := startForwardOverHelper(t, tc.helperRange, tc.requested)
			if !removed || created == nil {
				t.Fatalf("expected the helper to be replaced (removed=%v created=%v)", removed, created != nil)
			}
			if got := created.Labels[LabelSourceRange]; got != sourceRangeLabel(tc.requested) {
				t.Fatalf("expected the new helper to carry range %q, got %q", sourceRangeLabel(tc.requested), got)
			}

			found := false
			for _, m := range logger.info {
				if strings.Contains(m, tc.logLine) {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %q, got %v", tc.logLine, logger.info)
			}
		})
	}
}

func TestStartForward_ReusesHelperWithSameSourceRange(t *testing.T) {
	for _, sourceRange := range []string{"", "10.0.0.0/8"} {
		requested := netip.Prefix{}
		if sourceRange != "" {
			requested = netip.MustParsePrefix(sourceRange)
		}

		removed, created, _ := startForwardOverHelper(t, sourceRange, requested)
		if removed || created != nil {
			t.Fatalf("%q: expected the helper to be reused (removed=%v created=%v)", sourceRange, removed, created != nil)
		}
	}
}
