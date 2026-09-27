package internal

import (
	"fmt"
	"net/netip"
	"strings"
)

// ParseSourceRange parses a --source-range value: an IP address or a CIDR.
// A bare address is a range of one (/32 or /128), and host bits are masked
// off, so "10.1.2.3/8" and "10.0.0.0/8" name the same range. An empty value
// means no range and is returned as the zero Prefix.
//
// Only one range is accepted because socat, which enforces it in the helper,
// honors a single range option per listener.
func ParseSourceRange(value string) (netip.Prefix, error) {
	if value == "" {
		return netip.Prefix{}, nil
	}

	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid --source-range value %q: must be an IP address or CIDR", value)
		}
		return prefix.Masked(), nil
	}

	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid --source-range value %q: must be an IP address or CIDR", value)
	}
	addr = addr.WithZone("")
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// socatRangeOptions returns the socat listen options that restrict a
// listener to clients in prefix, with a leading comma, or "" for the zero
// Prefix. socat needs the listener's address family to parse a range, and
// takes an IPv6 range with the address in brackets.
func socatRangeOptions(prefix netip.Prefix) string {
	if !prefix.IsValid() {
		return ""
	}

	if prefix.Addr().Is4() {
		return fmt.Sprintf(",pf=ip4,range=%s", prefix)
	}

	return fmt.Sprintf(",pf=ip6,range=[%s]/%d", prefix.Addr(), prefix.Bits())
}
