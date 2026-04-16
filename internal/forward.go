package internal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

// Defaults and well-known labels set on helper containers.
const (
	DefaultHelperImage = "alpine/socat"
	// DefaultUDPTimeout is the default idle timeout applied to each UDP
	// socat invocation (socat's -T flag). A UDP pseudo-session with no
	// traffic for this long is dropped inside the helper. TCP is unaffected.
	DefaultUDPTimeout = 60 * time.Second

	LabelPortForward = "com.dokku.port-forward"
	LabelTarget      = "com.dokku.port-forward.target"
	LabelSession     = "com.dokku.port-forward.session"
	LabelName        = "com.dokku.port-forward.name"
	LabelPorts       = "com.dokku.port-forward.ports"
	LabelAddresses   = "com.dokku.port-forward.addresses"
)

// Pull policies for the helper image.
const (
	PullAlways  = "always"
	PullMissing = "missing"
	PullNever   = "never"
)

// Logger is the minimal structured logger used by the forwarder.
type Logger interface {
	Info(message string)
	Warn(message string)
	Error(message string)
}

// ForwardInput bundles inputs to StartForward.
type ForwardInput struct {
	Client         DockerClientInterface
	Target         ResolvedTarget
	Pairs          []PortPair
	Addresses      []string
	HelperImage    string
	PullPolicy     string
	RunningTimeout time.Duration
	Detach         bool
	// Name is the helper container name. If empty, a name is auto-generated.
	Name string
	// ExtraLabels are user-supplied labels added to the helper container.
	ExtraLabels map[string]string
	// UDPTimeout is the socat -T value for UDP forwards (idle pseudo-session
	// timeout). Ignored when no UDP pairs are requested. Zero falls back to
	// DefaultUDPTimeout.
	UDPTimeout time.Duration
	Logger     Logger
}

// ForwardResult describes what StartForward produced.
type ForwardResult struct {
	HelperID   string
	HelperName string
	Pairs      []PortPair
	// Existing is true when an existing helper already covered the request and
	// no new helper was created (idempotent no-op).
	Existing bool
}

// StartForward either reuses an existing helper that already covers any of
// the requested (local, remote) pairs for the same target, or creates a new
// helper container that binds each host port and forwards to the target.
//
// In detached mode it returns immediately after the helper is running. In
// attached mode it blocks until ctx is canceled, then stops the helper.
func StartForward(ctx context.Context, in ForwardInput) (ForwardResult, error) {
	if in.Client == nil {
		return ForwardResult{}, errors.New("docker client is required")
	}
	if in.Logger == nil {
		return ForwardResult{}, errors.New("logger is required")
	}
	if in.Target.ContainerID == "" {
		return ForwardResult{}, errors.New("target container id is required")
	}
	if len(in.Pairs) == 0 {
		return ForwardResult{}, errors.New("at least one port pair is required")
	}
	if in.HelperImage == "" {
		in.HelperImage = DefaultHelperImage
	}
	if in.PullPolicy == "" {
		in.PullPolicy = PullMissing
	}
	if in.RunningTimeout <= 0 {
		in.RunningTimeout = time.Minute
	}
	if in.UDPTimeout <= 0 {
		in.UDPTimeout = DefaultUDPTimeout
	}
	addresses := expandAddresses(in.Addresses)
	if len(addresses) == 0 {
		return ForwardResult{}, errors.New("no valid listen addresses")
	}
	// Normalize empty protocols to TCP so equality + label encoding are consistent.
	for i := range in.Pairs {
		in.Pairs[i].Protocol = NormalizeProtocol(in.Pairs[i].Protocol)
	}

	// Resolve any auto-allocated local ports (LocalPort == 0) by briefly
	// binding a TCP listener on "" to have the OS assign a free port. This
	// lets us pass a concrete port to Docker's -p binding and ensures both
	// IPv4 and IPv6 bindings for "localhost" share the same port number.
	pairs, err := resolveAutoPorts(in.Pairs)
	if err != nil {
		return ForwardResult{}, err
	}

	// 1. Idempotency: if an existing helper for this target shares any
	// (local, remote) pair with our request, exit 0 and reuse it.
	if existing, match, err := findOverlappingHelper(ctx, in.Client, in.Target.ContainerID, pairs); err != nil {
		return ForwardResult{}, err
	} else if match {
		name := strings.TrimPrefix(helperName(existing), "/")
		in.Logger.Info(fmt.Sprintf("Existing helper %q already forwards %s for target %s; no action taken.",
			name, renderPairs(pairs), shortID(in.Target.ContainerID)))
		return ForwardResult{
			HelperID:   existing.ID,
			HelperName: name,
			Pairs:      pairs,
			Existing:   true,
		}, nil
	}

	// 2. Preflight: ensure each requested (address, local_port) is free. Skip
	// checks where LocalPort == 0 was already resolved in resolveAutoPorts.
	if err := preflightHostPorts(addresses, pairs); err != nil {
		return ForwardResult{}, err
	}

	if err := ensureHelperImage(ctx, in.Client, in.HelperImage, in.PullPolicy, in.Logger); err != nil {
		return ForwardResult{}, err
	}

	// 3. Pick a network the helper will attach to and learn the target's IP
	// on that network; socat talks to that IP.
	info, err := in.Client.ContainerInspect(ctx, in.Target.ContainerID)
	if err != nil {
		return ForwardResult{}, fmt.Errorf("error inspecting target: %v", err)
	}
	networkName, targetAddr, err := pickTargetNetwork(info)
	if err != nil {
		return ForwardResult{}, err
	}

	// 4. Build the helper and start it.
	name := in.Name
	if name == "" {
		name = autoName(info.Name)
	}
	session := randomHex(8)
	cfg, hostCfg := buildHelperContainerConfig(
		in.Target.ContainerID,
		in.HelperImage,
		pairs,
		addresses,
		targetAddr,
		name,
		session,
		in.ExtraLabels,
		in.Detach,
		in.UDPTimeout,
	)
	hostCfg.NetworkMode = container.NetworkMode(networkName)

	resp, err := in.Client.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil, name)
	if err != nil {
		return ForwardResult{}, fmt.Errorf("error creating helper container: %v", err)
	}
	if err := in.Client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = in.Client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return ForwardResult{}, fmt.Errorf("error starting helper container: %v", err)
	}

	if err := waitForRunning(ctx, in.Client, resp.ID, in.RunningTimeout); err != nil {
		_ = in.Client.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
		return ForwardResult{}, fmt.Errorf("helper container did not start: %v", err)
	}

	for _, addr := range addresses {
		for _, p := range pairs {
			proto := NormalizeProtocol(p.Protocol)
			in.Logger.Info(fmt.Sprintf("Forwarding %s:%d -> %s:%d/%s (container %s)",
				addr, p.LocalPort, in.Target.ContainerName, p.RemotePort, proto, shortID(in.Target.ContainerID)))
		}
	}

	result := ForwardResult{HelperID: resp.ID, HelperName: name, Pairs: pairs}
	if in.Detach {
		in.Logger.Info(fmt.Sprintf("Started detached helper %q (id %s); use `docker port-forward cleanup --name %s` to remove.",
			name, shortID(resp.ID), name))
		return result, nil
	}

	// Attached: block until context is canceled, then tear down.
	<-ctx.Done()
	stopAndRemove(in.Client, resp.ID, in.Logger)
	return result, nil
}

// resolveAutoPorts replaces any LocalPort == 0 with an OS-assigned free port
// so the same port can be published to all requested bind addresses. The
// auto-allocation happens against the spec's protocol (TCP or UDP) so the
// returned port is guaranteed to be free for the relevant socket type.
func resolveAutoPorts(in []PortPair) ([]PortPair, error) {
	out := make([]PortPair, len(in))
	for i, p := range in {
		if p.LocalPort != 0 {
			out[i] = p
			continue
		}
		proto := NormalizeProtocol(p.Protocol)
		var port int
		switch proto {
		case ProtocolTCP:
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, fmt.Errorf("error allocating local tcp port: %v", err)
			}
			port = l.Addr().(*net.TCPAddr).Port
			_ = l.Close()
		case ProtocolUDP:
			c, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				return nil, fmt.Errorf("error allocating local udp port: %v", err)
			}
			port = c.LocalAddr().(*net.UDPAddr).Port
			_ = c.Close()
		default:
			return nil, fmt.Errorf("unsupported protocol %q", proto)
		}
		out[i] = PortPair{LocalPort: port, RemotePort: p.RemotePort, Protocol: proto}
	}
	return out, nil
}

// expandAddresses returns the concrete list of IPs to bind, expanding
// "localhost" into ["127.0.0.1", "::1"] to match kubectl's behavior.
func expandAddresses(addrs []string) []string {
	if len(addrs) == 0 {
		addrs = []string{"localhost"}
	}
	out := make([]string, 0, len(addrs))
	seen := map[string]bool{}
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if a == "localhost" {
			add("127.0.0.1")
			add("::1")
			continue
		}
		add(a)
	}
	return out
}

// preflightHostPorts attempts to bind every (address, local_port, protocol)
// triple briefly to detect conflicts before asking Docker to publish them.
// TCP uses net.Listen; UDP uses net.ListenPacket. A real conflict (address
// already in use, permission denied) always fails the preflight. Only
// "IPv6 is unavailable"-class errors are tolerated so that a default
// --address localhost (127.0.0.1 + ::1) still works on hosts without IPv6.
//
// Because TCP and UDP sockets don't collide, a single host port may be
// requested for both protocols without false positives.
func preflightHostPorts(addresses []string, pairs []PortPair) error {
	for _, p := range pairs {
		proto := NormalizeProtocol(p.Protocol)
		for _, addr := range addresses {
			host := addr
			if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
				host = "[" + host + "]"
			}
			bind := fmt.Sprintf("%s:%d", host, p.LocalPort)
			var err error
			switch proto {
			case ProtocolTCP:
				l, e := net.Listen("tcp", bind)
				if e == nil {
					_ = l.Close()
				}
				err = e
			case ProtocolUDP:
				c, e := net.ListenPacket("udp", bind)
				if e == nil {
					_ = c.Close()
				}
				err = e
			default:
				return fmt.Errorf("unsupported protocol %q", proto)
			}
			if err == nil {
				continue
			}
			if isIPv6Unavailable(err) {
				// Quietly skip ::1 on IPv4-only hosts.
				continue
			}
			return fmt.Errorf("host port %s/%s is not available: %v", bind, proto, err)
		}
	}
	return nil
}

// isIPv6Unavailable returns true when a listen failure is due to the host
// lacking IPv6 support (rather than a real conflict with another process).
func isIPv6Unavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address family not supported") ||
		strings.Contains(msg, "cannot assign requested address")
}

// ensureHelperImage pulls the helper image according to the pull policy.
func ensureHelperImage(ctx context.Context, cli DockerClientInterface, ref, policy string, logger Logger) error {
	switch policy {
	case PullAlways:
		return pullImage(ctx, cli, ref, logger)
	case PullNever:
		if _, err := cli.ImageInspect(ctx, ref); err != nil {
			return fmt.Errorf("helper image %s is not present locally and --pull=never was set: %v", ref, err)
		}
		return nil
	case PullMissing, "":
		if _, err := cli.ImageInspect(ctx, ref); err == nil {
			return nil
		}
		return pullImage(ctx, cli, ref, logger)
	default:
		return fmt.Errorf("invalid --pull value %q: must be one of: always, missing, never", policy)
	}
}

func pullImage(ctx context.Context, cli DockerClientInterface, ref string, logger Logger) error {
	logger.Info(fmt.Sprintf("Pulling helper image %s", ref))
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("error pulling helper image %s: %v", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("error reading image pull response: %v", err)
	}
	return nil
}

// buildHelperContainerConfig prepares the Docker Config + HostConfig for the
// sidecar helper. The helper runs one socat process per distinct
// (remote_port, protocol) pair, supervised by sh. TCP uses
// `socat TCP-LISTEN:<r>,fork,reuseaddr TCP:<target>:<r>`; UDP adds `-T <timeout>`
// and swaps in `UDP-LISTEN` / `UDP:` addresses.
func buildHelperContainerConfig(
	targetID, helperImage string,
	pairs []PortPair,
	addresses []string,
	targetAddr, name, session string,
	extraLabels map[string]string,
	detach bool,
	udpTimeout time.Duration,
) (*container.Config, *container.HostConfig) {
	type socatKey struct {
		remote int
		proto  Protocol
	}
	seen := map[socatKey]struct{}{}
	keys := make([]socatKey, 0, len(pairs))
	for _, p := range pairs {
		k := socatKey{remote: p.RemotePort, proto: NormalizeProtocol(p.Protocol)}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].proto != keys[j].proto {
			return keys[i].proto < keys[j].proto
		}
		return keys[i].remote < keys[j].remote
	})

	udpSeconds := int(udpTimeout.Seconds())
	if udpSeconds <= 0 {
		udpSeconds = int(DefaultUDPTimeout.Seconds())
	}

	var shCmd strings.Builder
	shCmd.WriteString("trap 'kill 0' EXIT; ")
	for _, k := range keys {
		switch k.proto {
		case ProtocolUDP:
			fmt.Fprintf(&shCmd, "socat -T %d UDP-LISTEN:%d,fork,reuseaddr UDP:%s:%d & ",
				udpSeconds, k.remote, targetAddr, k.remote)
		default:
			fmt.Fprintf(&shCmd, "socat TCP-LISTEN:%d,fork,reuseaddr TCP:%s:%d & ",
				k.remote, targetAddr, k.remote)
		}
	}
	shCmd.WriteString("wait")

	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range pairs {
		proto := string(NormalizeProtocol(p.Protocol))
		port, _ := nat.NewPort(proto, strconv.Itoa(p.RemotePort))
		exposed[port] = struct{}{}
		for _, addr := range addresses {
			bindings[port] = append(bindings[port], nat.PortBinding{
				HostIP:   addr,
				HostPort: strconv.Itoa(p.LocalPort),
			})
		}
	}

	labels := map[string]string{
		LabelPortForward: "true",
		LabelTarget:      targetID,
		LabelSession:     session,
		LabelName:        name,
		LabelPorts:       EncodePortPairs(pairs),
		LabelAddresses:   strings.Join(addresses, ","),
	}
	for k, v := range extraLabels {
		labels[k] = v
	}

	cfg := &container.Config{
		Image:        helperImage,
		Entrypoint:   []string{"sh", "-c"},
		Cmd:          []string{shCmd.String()},
		Labels:       labels,
		ExposedPorts: exposed,
	}
	hostCfg := &container.HostConfig{
		PortBindings: bindings,
		// Attached helpers are ephemeral; detached helpers need to survive
		// CLI exit so they can be cleaned up explicitly later.
		AutoRemove: !detach,
		RestartPolicy: container.RestartPolicy{
			Name: "no",
		},
	}
	return cfg, hostCfg
}

// pickTargetNetwork chooses a network to attach the helper to and returns the
// target's IP address on that network. User-defined networks are preferred
// over the default bridge so Docker's embedded DNS is available as a fallback.
func pickTargetNetwork(info container.InspectResponse) (networkName string, targetAddr string, err error) {
	if info.NetworkSettings == nil || len(info.NetworkSettings.Networks) == 0 {
		return "", "", errors.New("target container has no networks")
	}
	var userDefined []string
	for name := range info.NetworkSettings.Networks {
		if name == "bridge" || name == "host" || name == "none" {
			continue
		}
		userDefined = append(userDefined, name)
	}
	sort.Strings(userDefined)
	if len(userDefined) > 0 {
		for _, name := range userDefined {
			endpoint := info.NetworkSettings.Networks[name]
			if endpoint != nil && endpoint.IPAddress != "" {
				return name, endpoint.IPAddress, nil
			}
		}
	}
	if endpoint, ok := info.NetworkSettings.Networks["bridge"]; ok && endpoint != nil && endpoint.IPAddress != "" {
		return "bridge", endpoint.IPAddress, nil
	}
	return "", "", errors.New("target container has no routable IP address")
}

// findOverlappingHelper looks for an existing helper container for the same
// target that shares at least one (local, remote, protocol) pair with the
// request. Protocol is normalized on both sides so callers can pass pairs
// with the zero-value protocol (treated as TCP).
func findOverlappingHelper(ctx context.Context, cli DockerClientInterface, targetID string, requested []PortPair) (container.Summary, bool, error) {
	list, err := ListHelpers(ctx, cli, targetID)
	if err != nil {
		return container.Summary{}, false, err
	}
	want := make(map[PortPair]struct{}, len(requested))
	for _, p := range requested {
		p.Protocol = NormalizeProtocol(p.Protocol)
		want[p] = struct{}{}
	}
	for _, c := range list {
		if c.State != "running" {
			continue
		}
		existingPairs, _ := DecodePortPairs(c.Labels[LabelPorts])
		for _, p := range existingPairs {
			p.Protocol = NormalizeProtocol(p.Protocol)
			if _, ok := want[p]; ok {
				return c, true, nil
			}
		}
	}
	return container.Summary{}, false, nil
}

// waitForRunning polls ContainerInspect until the helper is running or the
// timeout elapses.
func waitForRunning(ctx context.Context, cli DockerClientInterface, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		info, err := cli.ContainerInspect(ctx, id)
		if err == nil && info.State != nil && info.State.Running {
			return nil
		}
		if err == nil && info.State != nil && info.State.ExitCode != 0 && !info.State.Running && info.State.Status == "exited" {
			return fmt.Errorf("helper container exited with code %d", info.State.ExitCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for helper container to start")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func stopAndRemove(cli DockerClientInterface, id string, logger Logger) {
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopTimeout := 1
	if err := cli.ContainerStop(stopCtx, id, container.StopOptions{Timeout: &stopTimeout}); err != nil {
		if !isNoSuchContainer(err) {
			logger.Warn(fmt.Sprintf("error stopping helper container %s: %v", shortID(id), err))
		}
	}
	if err := cli.ContainerRemove(stopCtx, id, container.RemoveOptions{Force: true}); err != nil {
		if !isNoSuchContainer(err) && !strings.Contains(strings.ToLower(err.Error()), "already in progress") {
			logger.Warn(fmt.Sprintf("error removing helper container %s: %v", shortID(id), err))
		}
	}
}

// ListHelpers returns helper containers created by this plugin, optionally
// filtered to a single target id.
func ListHelpers(ctx context.Context, cli DockerClientInterface, targetID string) ([]container.Summary, error) {
	f := filters.NewArgs()
	f.Add("label", LabelPortForward+"=true")
	if targetID != "" {
		f.Add("label", LabelTarget+"="+targetID)
	}
	return cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
}

// RemoveHelpers force-removes the given helper containers. "No such container"
// and "already in progress" errors are treated as successes for idempotency.
func RemoveHelpers(ctx context.Context, cli DockerClientInterface, ids []string, logger Logger) int {
	removed := 0
	for _, id := range ids {
		err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		if err != nil {
			if isNoSuchContainer(err) || strings.Contains(strings.ToLower(err.Error()), "already in progress") {
				removed++
				continue
			}
			logger.Warn(fmt.Sprintf("error removing helper container %s: %v", shortID(id), err))
			continue
		}
		removed++
	}
	return removed
}

// CleanupStaleHelpers removes leftover helper containers for the given target.
// Best effort; errors are logged, not returned.
func CleanupStaleHelpers(ctx context.Context, cli DockerClientInterface, targetID string, logger Logger) {
	list, err := ListHelpers(ctx, cli, targetID)
	if err != nil {
		return
	}
	ids := make([]string, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	RemoveHelpers(ctx, cli, ids, logger)
}

// EncodePortPairs serializes port pairs as "LOCAL:REMOTE[/PROTO],..." so the
// helper container's labels are self-describing. TCP pairs omit the suffix
// to keep older label values round-trippable without migration.
func EncodePortPairs(pairs []PortPair) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		proto := NormalizeProtocol(p.Protocol)
		suffix := ""
		if proto != ProtocolTCP {
			suffix = "/" + string(proto)
		}
		parts = append(parts, fmt.Sprintf("%d:%d%s", p.LocalPort, p.RemotePort, suffix))
	}
	return strings.Join(parts, ",")
}

// DecodePortPairs parses an encoded pair list. Invalid entries are skipped.
// Entries without a protocol suffix are treated as TCP for backward
// compatibility with helpers created before UDP support.
func DecodePortPairs(s string) ([]PortPair, error) {
	if s == "" {
		return nil, nil
	}
	out := make([]PortPair, 0, strings.Count(s, ",")+1)
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		proto := ProtocolTCP
		if slash := strings.LastIndex(tok, "/"); slash >= 0 {
			ps := Protocol(strings.ToLower(tok[slash+1:]))
			if ps != ProtocolTCP && ps != ProtocolUDP {
				continue
			}
			proto = ps
			tok = tok[:slash]
		}
		parts := strings.SplitN(tok, ":", 2)
		if len(parts) != 2 {
			continue
		}
		l, err1 := strconv.Atoi(parts[0])
		r, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, PortPair{LocalPort: l, RemotePort: r, Protocol: proto})
	}
	return out, nil
}

func autoName(containerName string) string {
	base := strings.TrimPrefix(containerName, "/")
	if base == "" {
		base = "target"
	}
	if len(base) > 16 {
		base = base[:16]
	}
	return fmt.Sprintf("port-forward-%s-%s", base, randomHex(4))
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func renderPairs(pairs []PortPair) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, ",")
}

func helperName(c container.Summary) string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return c.ID
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func isNoSuchContainer(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such container")
}
