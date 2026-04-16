package internal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
)

// Listener is a detected listening socket inside the target's netns.
type Listener struct {
	Port     int
	Protocol Protocol
}

// probeSections lists the /proc files scraped by the probe, each prefixed
// with a marker so the parser can attach the correct protocol.
var probeSections = []struct {
	marker string
	path   string
	proto  Protocol
}{
	{"===tcp===", "/proc/net/tcp", ProtocolTCP},
	{"===tcp6===", "/proc/net/tcp6", ProtocolTCP},
	{"===udp===", "/proc/net/udp", ProtocolUDP},
	{"===udp6===", "/proc/net/udp6", ProtocolUDP},
}

// ProbeListeners starts a short-lived probe container that shares the
// target's network namespace (so `/proc/net/{tcp,tcp6,udp,udp6}` reflect the
// target's sockets) and returns the non-loopback listening sockets it sees.
//
// For TCP a listener is state 0A (TCP_LISTEN). For UDP it is state 07
// (TCP_CLOSE; this is how the kernel reports bound-but-unconnected UDP
// sockets, i.e. the ones `ss -uln` shows).
func ProbeListeners(ctx context.Context, cli DockerClientInterface, targetID, helperImage, pullPolicy string, logger Logger) ([]Listener, error) {
	if err := ensureHelperImage(ctx, cli, helperImage, pullPolicy, logger); err != nil {
		return nil, err
	}

	var shCmd strings.Builder
	for _, s := range probeSections {
		fmt.Fprintf(&shCmd, "echo '%s'; cat %s 2>/dev/null || true; ", s.marker, s.path)
	}
	shCmd.WriteString("true")

	cfg := &container.Config{
		Image:        helperImage,
		Entrypoint:   []string{"sh", "-c"},
		Cmd:          []string{shCmd.String()},
		AttachStdout: true,
		AttachStderr: true,
	}
	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode("container:" + targetID),
		AutoRemove:  false,
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil, "")
	if err != nil {
		return nil, fmt.Errorf("error creating port probe: %v", err)
	}
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cli.ContainerRemove(rmCtx, resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("error starting port probe: %v", err)
	}

	waitCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case <-waitCh:
	case waitErr := <-errCh:
		if waitErr != nil {
			return nil, fmt.Errorf("error waiting for port probe: %v", waitErr)
		}
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("port probe timed out")
	}

	logs, err := cli.ContainerLogs(ctx, resp.ID, container.LogsOptions{ShowStdout: true, ShowStderr: false})
	if err != nil {
		return nil, fmt.Errorf("error reading port probe logs: %v", err)
	}
	defer logs.Close()

	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, logs); err != nil {
		return nil, fmt.Errorf("error demuxing port probe logs: %v", err)
	}

	return ExtractListeners(stdout.String()), nil
}

// ExtractListeners parses the combined /proc output produced by
// ProbeListeners. Sections are demarcated by the probeSections markers, which
// let the parser attribute each entry to the correct protocol. Loopback
// addresses and port zero entries are dropped.
func ExtractListeners(text string) []Listener {
	type key struct {
		Port  int
		Proto Protocol
	}
	seen := map[key]struct{}{}

	markerProto := map[string]Protocol{}
	for _, s := range probeSections {
		markerProto[s.marker] = s.proto
	}

	current := Protocol("")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if p, ok := markerProto[line]; ok {
			current = p
			continue
		}
		if current == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// State 0A = TCP_LISTEN (TCP), 07 = TCP_CLOSE (UDP "bound").
		wantState := "0A"
		if current == ProtocolUDP {
			wantState = "07"
		}
		if fields[3] != wantState {
			continue
		}

		addrPort := fields[1]
		colon := strings.LastIndex(addrPort, ":")
		if colon < 0 {
			continue
		}
		ip := parseHexAddr(addrPort[:colon])
		if ip == nil || ip.IsLoopback() {
			continue
		}
		port, err := strconv.ParseInt(addrPort[colon+1:], 16, 32)
		if err != nil || port <= 0 {
			continue
		}
		seen[key{Port: int(port), Proto: current}] = struct{}{}
	}

	out := make([]Listener, 0, len(seen))
	for k := range seen {
		out = append(out, Listener{Port: k.Port, Protocol: k.Proto})
	}
	// Stable ordering: TCP first, then UDP; within a protocol, ascending port.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Protocol != out[j].Protocol {
			return out[i].Protocol < out[j].Protocol
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// parseHexAddr decodes a /proc/net/{tcp,tcp6,udp,udp6} address. The format
// is a sequence of 4-byte words each written as a little-endian hex number;
// 8 hex chars for IPv4 and 32 for IPv6.
func parseHexAddr(hexStr string) net.IP {
	switch len(hexStr) {
	case 8:
		out := make([]byte, 4)
		for i := 0; i < 4; i++ {
			v, err := strconv.ParseUint(hexStr[i*2:i*2+2], 16, 8)
			if err != nil {
				return nil
			}
			out[3-i] = byte(v)
		}
		return net.IP(out)
	case 32:
		out := make([]byte, 16)
		for w := 0; w < 4; w++ {
			for b := 0; b < 4; b++ {
				v, err := strconv.ParseUint(hexStr[w*8+b*2:w*8+b*2+2], 16, 8)
				if err != nil {
					return nil
				}
				out[w*4+3-b] = byte(v)
			}
		}
		return net.IP(out)
	}
	return nil
}
