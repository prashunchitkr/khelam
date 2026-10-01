// Package proxyproto writes PROXY protocol v2 headers, which tell a
// local service the real address of a tunnelled client.
//
// Spec: https://www.haproxy.org/download/2.9/doc/proxy-protocol.txt
package proxyproto

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
)

// signature opens every v2 header.
var signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

const (
	versionCommandProxy = 0x21 // version 2, PROXY command
	familyTCP4          = 0x11 // AF_INET, STREAM
	familyTCP6          = 0x21 // AF_INET6, STREAM
)

// WriteV2 writes a PROXY v2 header describing a TCP connection from src
// to dst. If the two addresses are of different IP families, both are
// sent as IPv6.
func WriteV2(w io.Writer, src, dst *net.TCPAddr) error {
	if src == nil || dst == nil {
		return errors.New("proxyproto: source and destination are required")
	}

	srcIP4, dstIP4 := src.IP.To4(), dst.IP.To4()

	var family byte
	var srcIP, dstIP net.IP
	if srcIP4 != nil && dstIP4 != nil {
		family, srcIP, dstIP = familyTCP4, srcIP4, dstIP4
	} else {
		family, srcIP, dstIP = familyTCP6, src.IP.To16(), dst.IP.To16()
		if srcIP == nil || dstIP == nil {
			return errors.New("proxyproto: invalid IP address")
		}
	}

	addrLen := 2*len(srcIP) + 4
	header := make([]byte, 0, len(signature)+4+addrLen)
	header = append(header, signature...)
	header = append(header, versionCommandProxy, family)
	header = binary.BigEndian.AppendUint16(header, uint16(addrLen))
	header = append(header, srcIP...)
	header = append(header, dstIP...)
	header = binary.BigEndian.AppendUint16(header, uint16(src.Port))
	header = binary.BigEndian.AppendUint16(header, uint16(dst.Port))

	_, err := w.Write(header)
	return err
}
