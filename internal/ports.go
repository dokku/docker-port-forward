package internal

import (
	"fmt"
	"net/netip"
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
	// Address is the host address to bind LocalPort on: an IP literal or
	// "localhost". Empty means the forward's default addresses are used.
	Address string
	// LocalPort is the host port to listen on. Zero means the OS should
	// assign a free port when the listener is opened.
	LocalPort int
	// RemotePort is the container port to forward connections to.
	RemotePort int
	// Protocol is the transport protocol. Empty is treated as TCP.
	Protocol Protocol
}

// String returns the Docker-style representation of the port pair,
// ([ADDRESS:]LOCAL:REMOTE[/proto]), omitting the protocol suffix when it is
// the default (tcp).
func (p PortPair) String() string {
	proto := NormalizeProtocol(p.Protocol)
	suffix := ""
	if proto != ProtocolTCP {
		suffix = "/" + string(proto)
	}
	local := ""
	if p.LocalPort != 0 {
		local = strconv.Itoa(p.LocalPort)
	}
	if p.Address != "" {
		return fmt.Sprintf("%s:%s:%d%s", FormatAddress(p.Address), local, p.RemotePort, suffix)
	}
	if local == "" {
		return fmt.Sprintf(":%d%s", p.RemotePort, suffix)
	}
	return fmt.Sprintf("%s:%d%s", local, p.RemotePort, suffix)
}

// FormatAddress returns addr in the form used in port specs and labels,
// wrapping IPv6 literals in brackets.
func FormatAddress(addr string) string {
	if strings.Contains(addr, ":") {
		return "[" + addr + "]"
	}
	return addr
}

// AllInterfaces is the bind address that publishes on every IPv4 and IPv6
// interface, like `docker run -p LOCAL:REMOTE` with no host IP. It maps to
// the zero HostIP in the helper's port bindings.
const AllInterfaces = "*"

// ValidateAddress reports whether addr is usable as a bind address: an IP
// literal, "localhost", or AllInterfaces.
func ValidateAddress(addr string) error {
	if !isValidAddress(addr) {
		return fmt.Errorf("invalid address %q: must be an IP address, \"localhost\" or %q", addr, AllInterfaces)
	}
	return nil
}

func isValidAddress(addr string) bool {
	if addr == "localhost" || addr == AllInterfaces {
		return true
	}
	_, err := netip.ParseAddr(addr)
	return err == nil
}

// ValidateAddresses checks a list of default bind addresses (--address):
// every entry must be valid (see ValidateAddress), and AllInterfaces can't be
// combined with other entries, since the bindings would overlap on the same
// host port.
func ValidateAddresses(addrs []string) error {
	for _, addr := range addrs {
		if !isValidAddress(addr) {
			return fmt.Errorf("invalid --address value %q: must be an IP address, \"localhost\" or %q", addr, AllInterfaces)
		}
		if addr == AllInterfaces && len(addrs) > 1 {
			return fmt.Errorf("invalid --address value %q: cannot be combined with other addresses", AllInterfaces)
		}
	}
	return nil
}

// ParsePortSpec parses a single port-forward spec, following the
// `docker run -p` syntax:
//
//	"REMOTE"                  -> local=remote, tcp
//	"LOCAL:REMOTE"            -> explicit local, tcp
//	":REMOTE"                 -> local auto-allocated (0), tcp
//	"ADDRESS:LOCAL:REMOTE"    -> bind LOCAL on ADDRESS
//	"ADDRESS::REMOTE"         -> bind an auto-allocated port on ADDRESS
//	"[IPV6]:LOCAL:REMOTE"     -> IPv6 addresses must be bracketed
//	":LOCAL:REMOTE"           -> Docker's empty host IP: all interfaces,
//	                             the same as "*:LOCAL:REMOTE"
//
// Any form may end in "/tcp" or "/udp". The protocol suffix is optional and
// case-insensitive; any other suffix is rejected. ADDRESS must be an IP
// literal, "localhost" or "*" (AllInterfaces). Specs without an address
// leave Address empty.
func ParsePortSpec(spec string) (PortPair, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return PortPair{}, fmt.Errorf("empty port spec")
	}
	orig := spec

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
			return PortPair{}, fmt.Errorf("invalid port spec %q: unsupported protocol %q (expected tcp or udp)", orig, protoStr)
		}
		if ports == "" {
			return PortPair{}, fmt.Errorf("invalid port spec %q: missing port before protocol", orig)
		}
		spec = ports
	}

	var address, localStr, remoteStr string
	if strings.HasPrefix(spec, "[") {
		end := strings.Index(spec, "]")
		if end < 0 {
			return PortPair{}, fmt.Errorf("invalid port spec %q: missing closing bracket in address", orig)
		}
		address = spec[1:end]
		rest, ok := strings.CutPrefix(spec[end+1:], ":")
		if !ok {
			return PortPair{}, fmt.Errorf("invalid port spec %q: expected [ADDRESS]:LOCAL:REMOTE", orig)
		}
		var found bool
		localStr, remoteStr, found = strings.Cut(rest, ":")
		if !found {
			return PortPair{}, fmt.Errorf("invalid port spec %q: expected [ADDRESS]:LOCAL:REMOTE", orig)
		}
		if address == "" || !strings.Contains(address, ":") {
			return PortPair{}, fmt.Errorf("invalid port spec %q: invalid address %q", orig, address)
		}
	} else {
		parts := strings.Split(spec, ":")
		switch len(parts) {
		case 1:
			localStr, remoteStr = parts[0], parts[0]
		case 2:
			localStr, remoteStr = parts[0], parts[1]
		case 3:
			address, localStr, remoteStr = parts[0], parts[1], parts[2]
			if address == "" {
				// An empty host IP publishes on all interfaces, as in
				// `docker run -p :LOCAL:REMOTE`.
				address = AllInterfaces
			}
		default:
			return PortPair{}, fmt.Errorf("invalid port spec %q: IPv6 addresses must be enclosed in brackets", orig)
		}
	}

	if address != "" {
		if err := ValidateAddress(address); err != nil {
			return PortPair{}, fmt.Errorf("invalid port spec %q: %v", orig, err)
		}
	}

	remote, err := parsePortNumber(remoteStr)
	if err != nil {
		return PortPair{}, fmt.Errorf("invalid remote port in %q: %v", orig, err)
	}
	if remote == 0 {
		return PortPair{}, fmt.Errorf("invalid port spec %q: remote port must be non-zero", orig)
	}

	local := 0
	if localStr != "" {
		local, err = parsePortNumber(localStr)
		if err != nil {
			return PortPair{}, fmt.Errorf("invalid local port in %q: %v", orig, err)
		}
	}

	return PortPair{Address: address, LocalPort: local, RemotePort: remote, Protocol: proto}, nil
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
