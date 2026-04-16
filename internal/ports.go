package internal

import (
	"fmt"
	"strconv"
	"strings"
)

// Protocol is the transport protocol for a port forward.
type Protocol string

const (
	// ProtocolTCP is the default protocol when none is specified.
	ProtocolTCP Protocol = "tcp"
	// ProtocolUDP is the UDP datagram protocol.
	ProtocolUDP Protocol = "udp"
)

// NormalizeProtocol coerces zero/empty protocol values to the TCP default
// and returns the normalized value. Any other value is returned as-is so
// callers can validate explicitly.
func NormalizeProtocol(p Protocol) Protocol {
	switch p {
	case "", ProtocolTCP:
		return ProtocolTCP
	case ProtocolUDP:
		return ProtocolUDP
	default:
		return p
	}
}

// PortPair represents a single local-to-remote port mapping for forwarding.
type PortPair struct {
	// LocalPort is the host port to listen on. Zero means the OS should
	// assign a free port when the listener is opened.
	LocalPort int
	// RemotePort is the container port to forward connections to.
	RemotePort int
	// Protocol is the transport protocol. Empty is treated as TCP.
	Protocol Protocol
}

// String returns the kubectl/Docker-style representation of the port pair,
// omitting the protocol suffix when it is the default (tcp).
func (p PortPair) String() string {
	proto := NormalizeProtocol(p.Protocol)
	suffix := ""
	if proto != ProtocolTCP {
		suffix = "/" + string(proto)
	}
	if p.LocalPort == 0 {
		return fmt.Sprintf(":%d%s", p.RemotePort, suffix)
	}
	return fmt.Sprintf("%d:%d%s", p.LocalPort, p.RemotePort, suffix)
}

// ParsePortSpec parses a single port-forward spec:
//
//	"REMOTE"            -> local=remote, tcp
//	"LOCAL:REMOTE"      -> explicit local, tcp
//	":REMOTE"           -> local auto-allocated (0), tcp
//	"REMOTE/udp"        -> same port both sides, udp
//	"LOCAL:REMOTE/udp"  -> explicit local, udp
//	":REMOTE/udp"       -> auto local, udp
//
// The protocol suffix is optional and case-insensitive. Only `tcp` and `udp`
// are supported; any other suffix is rejected.
func ParsePortSpec(spec string) (PortPair, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return PortPair{}, fmt.Errorf("empty port spec")
	}

	proto := ProtocolTCP
	if idx := strings.LastIndex(spec, "/"); idx >= 0 {
		protoStr := strings.ToLower(strings.TrimSpace(spec[idx+1:]))
		ports := strings.TrimSpace(spec[:idx])
		switch Protocol(protoStr) {
		case ProtocolTCP:
			proto = ProtocolTCP
		case ProtocolUDP:
			proto = ProtocolUDP
		default:
			return PortPair{}, fmt.Errorf("invalid port spec %q: unsupported protocol %q (expected tcp or udp)", spec, protoStr)
		}
		if ports == "" {
			return PortPair{}, fmt.Errorf("invalid port spec %q: missing port before protocol", spec)
		}
		spec = ports
	}

	var localStr, remoteStr string
	if idx := strings.Index(spec, ":"); idx >= 0 {
		localStr = spec[:idx]
		remoteStr = spec[idx+1:]
	} else {
		localStr = spec
		remoteStr = spec
	}

	remote, err := parsePortNumber(remoteStr)
	if err != nil {
		return PortPair{}, fmt.Errorf("invalid remote port in %q: %v", spec, err)
	}
	if remote == 0 {
		return PortPair{}, fmt.Errorf("invalid port spec %q: remote port must be non-zero", spec)
	}

	local := 0
	if localStr != "" {
		local, err = parsePortNumber(localStr)
		if err != nil {
			return PortPair{}, fmt.Errorf("invalid local port in %q: %v", spec, err)
		}
	}

	return PortPair{LocalPort: local, RemotePort: remote, Protocol: proto}, nil
}

// ParsePortSpecs parses multiple specs and returns the parsed port pairs.
// Returns a single wrapped error if any spec is invalid.
func ParsePortSpecs(specs []string) ([]PortPair, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("at least one port spec is required")
	}
	pairs := make([]PortPair, 0, len(specs))
	for _, s := range specs {
		p, err := ParsePortSpec(s)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return pairs, nil
}

func parsePortNumber(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not a valid port number: %q", s)
	}
	if n < 0 || n > 65535 {
		return 0, fmt.Errorf("port %d out of range 0-65535", n)
	}
	return n, nil
}
