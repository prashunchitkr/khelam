package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/agent"
	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/protocol"
)

func withServerToken(token string) func(*config.ServerConfig) {
	return func(c *config.ServerConfig) { c.Token = token }
}

func withAgentToken(token string) func(*config.AgentConfig) {
	return func(c *config.AgentConfig) { c.Token = token }
}

func TestAuthAcceptsValidToken(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47200, 47209, withServerToken("s3cret"))
	port := waitPort(t, startAgent(t, ctx, serverAddr, startEcho(t), withAgentToken("s3cret")))

	assertEcho(t, port, []byte("authorized"))
}

func TestAuthRejectsBadTokenPermanently(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47210, 47219, withServerToken("s3cret"))
	_, result := startAgentWithResult(t, ctx, serverAddr, startEcho(t), withAgentToken("wrong"))

	// The agent must give up rather than retry forever.
	err := waitResult(t, result)

	var rejected *agent.RejectedError
	if !errors.As(err, &rejected) || rejected.Code != protocol.ErrUnauthorized {
		t.Fatalf("Run() = %v, want unauthorized rejection", err)
	}
}

func TestRejectsUnsupportedVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47220, 47229)

	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(waitTimeout))

	protocol.WriteMessage(conn, protocol.KindRegister, protocol.RegisterMessage{Version: 999, Name: "future"})

	reply, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if msg, ok := reply.(protocol.ErrorMessage); !ok || msg.Code != protocol.ErrUnsupportedVersion {
		t.Fatalf("reply = %#v, want unsupported_version error", reply)
	}
}

// writeTestCert writes a self-signed certificate for 127.0.0.1 and
// returns the certificate and key file paths.
func writeTestCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "khelam test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)

	return certFile, keyFile
}

func withServerTLS(certFile, keyFile string) func(*config.ServerConfig) {
	return func(c *config.ServerConfig) {
		c.TLSCertFile = certFile
		c.TLSKeyFile = keyFile
	}
}

func TestTLSTunnel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	certFile, keyFile := writeTestCert(t)
	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47230, 47239,
		withServerTLS(certFile, keyFile), withServerToken("s3cret"))

	port := waitPort(t, startAgent(t, ctx, serverAddr, startEcho(t), withAgentToken("s3cret"),
		func(c *config.AgentConfig) {
			c.TLS = true
			c.TLSCAFile = certFile
		}))

	assertEcho(t, port, randomBytes(t, 256<<10))
}

func TestTLSRejectsUntrustedServerPermanently(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	certFile, keyFile := writeTestCert(t)
	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47240, 47249, withServerTLS(certFile, keyFile))

	// No CA file: the self-signed certificate is not trusted.
	_, result := startAgentWithResult(t, ctx, serverAddr, startEcho(t),
		func(c *config.AgentConfig) { c.TLS = true })

	err := waitResult(t, result)
	if err == nil {
		t.Fatal("Run() succeeded against an untrusted server")
	}
}

func TestProxyProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// The local service reads the PROXY v2 header for an IPv4 client,
	// reports the source address, then echoes the remaining bytes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	sources := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		header := make([]byte, 16+12)
		if _, err := io.ReadFull(conn, header); err != nil {
			sources <- "read error: " + err.Error()
			return
		}
		if !bytes.Equal(header[:12], []byte("\r\n\r\n\x00\r\nQUIT\n")) || header[13] != 0x11 {
			sources <- fmt.Sprintf("bad header %x", header)
			return
		}
		ip := net.IP(header[16:20])
		port := binary.BigEndian.Uint16(header[24:26])
		sources <- fmt.Sprintf("%s:%d", ip, port)

		io.Copy(conn, conn)
		conn.(*net.TCPConn).CloseWrite()
	}()

	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47250, 47259)
	publicPort := waitPort(t, startAgent(t, ctx, serverAddr, ln.Addr().String(),
		func(c *config.AgentConfig) { c.ProxyProtocol = true }))

	client, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", publicPort))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(waitTimeout))

	client.Write([]byte("payload"))
	client.(*net.TCPConn).CloseWrite()

	select {
	case got := <-sources:
		if want := client.LocalAddr().String(); got != want {
			t.Fatalf("service saw client %s, want %s", got, want)
		}
	case <-time.After(waitTimeout):
		t.Fatal("service never received a connection")
	}

	// The data after the header must be untouched.
	echoed, err := io.ReadAll(client)
	if err != nil || string(echoed) != "payload" {
		t.Fatalf("echo = %q, %v", echoed, err)
	}
}

func TestMaxConnsPerTunnel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47260, 47269,
		func(c *config.ServerConfig) { c.MaxConnsPerTunnel = 1 })
	port := waitPort(t, startAgent(t, ctx, serverAddr, startEcho(t)))
	publicAddr := fmt.Sprintf("127.0.0.1:%d", port)

	// Hold one connection open and prove it is relayed.
	first, err := net.Dial("tcp", publicAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.SetDeadline(time.Now().Add(waitTimeout))
	first.Write([]byte("x"))
	if _, err := io.ReadFull(first, make([]byte, 1)); err != nil {
		t.Fatalf("first connection not relayed: %v", err)
	}

	// The second must be closed by the server straight away.
	second, err := net.Dial("tcp", publicAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetDeadline(time.Now().Add(waitTimeout))
	if n, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatalf("second connection read %d bytes, want it closed", n)
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("second connection was not closed")
	}

	// Releasing the first frees the slot again.
	first.Close()
	deadline := time.Now().Add(waitTimeout)
	for {
		conn, err := net.Dial("tcp", publicAddr)
		if err == nil {
			conn.SetDeadline(time.Now().Add(time.Second))
			conn.Write([]byte("y"))
			_, err = io.ReadFull(conn, make([]byte, 1))
			conn.Close()
			if err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("slot was not released after the first connection closed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPendingHandshakeLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47270, 47279)

	// Fill every handshake slot with connections that never register.
	const limit = 64
	for range limit {
		conn, err := net.Dial("tcp", serverAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}

	extra, err := net.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()

	// The server's own handshake timeout is 5s, so a close well before
	// that means the connection was dropped for being over the limit.
	extra.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := extra.Read(make([]byte, 1)); errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("connection over the handshake limit was not dropped")
	}
}
