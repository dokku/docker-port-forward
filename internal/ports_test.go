package internal

import "testing"

func TestParsePortSpec(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    PortPair
		wantErr bool
	}{
		{"same port", "8080", PortPair{LocalPort: 8080, RemotePort: 8080, Protocol: ProtocolTCP}, false},
		{"explicit local", "8888:8080", PortPair{LocalPort: 8888, RemotePort: 8080, Protocol: ProtocolTCP}, false},
		{"auto local", ":8080", PortPair{LocalPort: 0, RemotePort: 8080, Protocol: ProtocolTCP}, false},
		{"trims whitespace", "  80:80  ", PortPair{LocalPort: 80, RemotePort: 80, Protocol: ProtocolTCP}, false},
		{"explicit tcp suffix", "80:80/tcp", PortPair{LocalPort: 80, RemotePort: 80, Protocol: ProtocolTCP}, false},
		{"udp suffix same port", "53/udp", PortPair{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP}, false},
		{"udp explicit local", "5353:53/udp", PortPair{LocalPort: 5353, RemotePort: 53, Protocol: ProtocolUDP}, false},
		{"udp auto local", ":53/udp", PortPair{LocalPort: 0, RemotePort: 53, Protocol: ProtocolUDP}, false},
		{"udp uppercase", "53:53/UDP", PortPair{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP}, false},
		{"empty", "", PortPair{}, true},
		{"bad local", "abc:80", PortPair{}, true},
		{"bad remote", "80:abc", PortPair{}, true},
		{"remote zero", "80:0", PortPair{}, true},
		{"remote zero bare", "0", PortPair{}, true},
		{"out of range", "99999", PortPair{}, true},
		{"out of range local", "99999:80", PortPair{}, true},
		{"unknown protocol rejected", "53/sctp", PortPair{}, true},
		{"empty protocol rejected", "53/", PortPair{}, true},
		{"missing port before proto", "/udp", PortPair{}, true},
		{"negative", "-1:80", PortPair{}, true},
		{"ipv4 address", "127.0.0.1:8080:80", PortPair{Address: "127.0.0.1", LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP}, false},
		{"all interfaces", "0.0.0.0:5432:5432", PortPair{Address: "0.0.0.0", LocalPort: 5432, RemotePort: 5432, Protocol: ProtocolTCP}, false},
		{"localhost address", "localhost:8080:80", PortPair{Address: "localhost", LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP}, false},
		{"address auto local", "127.0.0.1::80", PortPair{Address: "127.0.0.1", LocalPort: 0, RemotePort: 80, Protocol: ProtocolTCP}, false},
		{"address udp", "0.0.0.0:5353:53/udp", PortPair{Address: "0.0.0.0", LocalPort: 5353, RemotePort: 53, Protocol: ProtocolUDP}, false},
		{"bracketed ipv6", "[::1]:8080:80", PortPair{Address: "::1", LocalPort: 8080, RemotePort: 80, Protocol: ProtocolTCP}, false},
		{"bracketed ipv6 auto local udp", "[::]::53/udp", PortPair{Address: "::", LocalPort: 0, RemotePort: 53, Protocol: ProtocolUDP}, false},
		{"unbracketed ipv6", "::1:8080:80", PortPair{}, true},
		{"hostname address", "example.com:8080:80", PortPair{}, true},
		{"partial ip address", "1.2.3:80:80", PortPair{}, true},
		{"empty address", ":8080:80", PortPair{}, true},
		{"empty brackets", "[]:8080:80", PortPair{}, true},
		{"bracketed ipv4", "[127.0.0.1]:8080:80", PortPair{}, true},
		{"unclosed bracket", "[::1:8080:80", PortPair{}, true},
		{"bracket without local", "[::1]:80", PortPair{}, true},
		{"too many parts", "1.2.3.4:1:2:3", PortPair{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePortSpec(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %+v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("for %q: got %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParsePortSpecs(t *testing.T) {
	t.Run("multiple including udp", func(t *testing.T) {
		got, err := ParsePortSpecs([]string{"80", "8888:8080", ":5000", "53:53/udp"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []PortPair{
			{LocalPort: 80, RemotePort: 80, Protocol: ProtocolTCP},
			{LocalPort: 8888, RemotePort: 8080, Protocol: ProtocolTCP},
			{LocalPort: 0, RemotePort: 5000, Protocol: ProtocolTCP},
			{LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
		}
		if len(got) != len(want) {
			t.Fatalf("got %d pairs, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("pair %d: got %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, err := ParsePortSpecs(nil)
		if err == nil {
			t.Fatal("expected error on empty specs")
		}
	})

	t.Run("first invalid stops", func(t *testing.T) {
		_, err := ParsePortSpecs([]string{"bad", "80"})
		if err == nil {
			t.Fatal("expected error on bad spec")
		}
	})
}

func TestPortPairString(t *testing.T) {
	cases := map[string]PortPair{
		"80:80":               {LocalPort: 80, RemotePort: 80},
		":8080":               {LocalPort: 0, RemotePort: 8080},
		"53:53/udp":           {LocalPort: 53, RemotePort: 53, Protocol: ProtocolUDP},
		":53/udp":             {LocalPort: 0, RemotePort: 53, Protocol: ProtocolUDP},
		"127.0.0.1:8080:80":   {Address: "127.0.0.1", LocalPort: 8080, RemotePort: 80},
		"0.0.0.0::80":         {Address: "0.0.0.0", LocalPort: 0, RemotePort: 80},
		"[::1]:5353:53/udp":   {Address: "::1", LocalPort: 5353, RemotePort: 53, Protocol: ProtocolUDP},
		"localhost:9000:9000": {Address: "localhost", LocalPort: 9000, RemotePort: 9000},
	}
	for want, in := range cases {
		got := in.String()
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		// Every rendered form parses back to the same pair.
		parsed, err := ParsePortSpec(got)
		if err != nil {
			t.Fatalf("round-trip of %q failed: %v", got, err)
		}
		in.Protocol = NormalizeProtocol(in.Protocol)
		if parsed != in {
			t.Fatalf("round-trip of %q: got %+v, want %+v", got, parsed, in)
		}
	}
}

func TestNormalizeProtocol(t *testing.T) {
	if got := NormalizeProtocol(""); got != ProtocolTCP {
		t.Fatalf("empty → %q, want tcp", got)
	}
	if got := NormalizeProtocol(ProtocolTCP); got != ProtocolTCP {
		t.Fatalf("tcp unchanged, got %q", got)
	}
	if got := NormalizeProtocol(ProtocolUDP); got != ProtocolUDP {
		t.Fatalf("udp unchanged, got %q", got)
	}
}
