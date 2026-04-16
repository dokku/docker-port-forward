package internal

import (
	"reflect"
	"testing"
)

func TestParseHexAddrIPv4(t *testing.T) {
	cases := map[string]string{
		"0100007F": "127.0.0.1",
		"00000000": "0.0.0.0",
		"0500A8C0": "192.168.0.5",
	}
	for hex, want := range cases {
		got := parseHexAddr(hex)
		if got == nil || got.String() != want {
			t.Fatalf("parseHexAddr(%q) = %v, want %s", hex, got, want)
		}
	}
}

func TestParseHexAddrIPv6(t *testing.T) {
	got := parseHexAddr("00000000000000000000000001000000")
	if got == nil || got.String() != "::1" {
		t.Fatalf("expected ::1, got %v", got)
	}
}

func TestParseHexAddr_BadLength(t *testing.T) {
	if parseHexAddr("ABC") != nil {
		t.Fatal("expected nil for bad input")
	}
}

func TestExtractListeners_ParsesTCPv4(t *testing.T) {
	// Header line, a LISTEN on 0.0.0.0:8080 (1F90),
	// a LISTEN on 127.0.0.1:5432 (skipped — loopback),
	// an ESTABLISHED on 0.0.0.0:443 (skipped — state != 0A).
	text := "===tcp===\n" +
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 123 ...\n" +
		"   1: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 124 ...\n" +
		"   2: 00000000:01BB 01020304:1234 01 00000000:00000000 00:00000000 00000000     0        0 125 ...\n"
	got := ExtractListeners(text)
	want := []Listener{{Port: 8080, Protocol: ProtocolTCP}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestExtractListeners_ParsesTCPv6(t *testing.T) {
	text := "===tcp6===\n" +
		"   0: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A ...\n" +
		"   1: 00000000000000000000000001000000:2328 00000000000000000000000000000000:0000 0A ...\n"
	got := ExtractListeners(text)
	want := []Listener{{Port: 3000, Protocol: ProtocolTCP}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestExtractListeners_ParsesUDPListenersOnlyInState07(t *testing.T) {
	// Two UDP entries: 0.0.0.0:53 (state 07 = bound/listening) and
	// 192.168.1.5:40000 (state 01 = connected UDP client — skip).
	text := "===udp===\n" +
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 321 ...\n" +
		"   1: 0501A8C0:9C40 08080808:0035 01 00000000:00000000 00:00000000 00000000     0        0 322 ...\n"
	got := ExtractListeners(text)
	want := []Listener{{Port: 53, Protocol: ProtocolUDP}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestExtractListeners_SkipsLoopbackUDP(t *testing.T) {
	text := "===udp===\n" +
		"   0: 0100007F:1538 00000000:0000 07 ...\n"
	got := ExtractListeners(text)
	if len(got) != 0 {
		t.Fatalf("loopback UDP must be skipped, got %+v", got)
	}
}

func TestExtractListeners_AllFourSectionsMerged(t *testing.T) {
	text := "===tcp===\n" +
		"   0: 00000000:1F90 00000000:0000 0A ...\n" +
		"===tcp6===\n" +
		"   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A ...\n" +
		"===udp===\n" +
		"   0: 00000000:0035 00000000:0000 07 ...\n" +
		"===udp6===\n" +
		"   0: 00000000000000000000000000000000:0035 00000000000000000000000000000000:0000 07 ...\n"
	got := ExtractListeners(text)
	want := []Listener{
		{Port: 8080, Protocol: ProtocolTCP},
		{Port: 53, Protocol: ProtocolUDP},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestExtractListeners_NoEntriesWithoutSectionMarker(t *testing.T) {
	text := "   0: 00000000:1F90 00000000:0000 0A ...\n"
	got := ExtractListeners(text)
	if len(got) != 0 {
		t.Fatalf("expected no listeners without section marker, got %+v", got)
	}
}
