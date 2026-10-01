package proxyproto

import (
	"bytes"
	"net"
	"testing"
)

func TestWriteV2IPv4(t *testing.T) {
	var buf bytes.Buffer
	err := WriteV2(&buf,
		&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 51234},
		&net.TCPAddr{IP: net.ParseIP("198.51.100.1"), Port: 41000},
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []byte{
		0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a, // signature
		0x21,       // v2, PROXY
		0x11,       // TCP over IPv4
		0x00, 0x0c, // 12 bytes of addresses
		203, 0, 113, 7, // src IP
		198, 51, 100, 1, // dst IP
		0xc8, 0x22, // src port 51234
		0xa0, 0x28, // dst port 41000
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("header mismatch\n got %x\nwant %x", buf.Bytes(), want)
	}
}

func TestWriteV2MixedFamiliesUsesIPv6(t *testing.T) {
	var buf bytes.Buffer
	err := WriteV2(&buf,
		&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 1},
		&net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 2},
	)
	if err != nil {
		t.Fatal(err)
	}

	b := buf.Bytes()
	if b[13] != familyTCP6 {
		t.Fatalf("family = %#x, want TCP6", b[13])
	}
	if got := int(b[14])<<8 | int(b[15]); got != 36 || len(b) != 16+36 {
		t.Fatalf("address length = %d (total %d), want 36", got, len(b))
	}
	if !net.IP(b[32:48]).Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("dst = %v, want IPv4-mapped 192.0.2.1", net.IP(b[32:48]))
	}
}
