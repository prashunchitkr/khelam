package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prashunchitkr/khelam/internal/agent"
	"github.com/prashunchitkr/khelam/internal/config"
	"github.com/prashunchitkr/khelam/internal/protocol"
	"github.com/prashunchitkr/khelam/internal/tlsutil"
	"github.com/prashunchitkr/khelam/internal/transport"
)

func withServerToken(token string) func(*config.ServerConfig) {
	return func(c *config.ServerConfig) { c.Token = token }
}

func TestAuthAcceptsValidToken(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47300, 47309, withServerToken("s3cret"))
	cfg := agentConfig(t, srv, startEcho(t))
	cfg.Token = "s3cret"
	assignments, _ := startAgent(t, ctx, cfg)

	assertEcho(t, waitPort(t, assignments), []byte("authorized"))
}

func TestAuthRejectsBadTokenPermanently(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47310, 47319, withServerToken("s3cret"))
	cfg := agentConfig(t, srv, startEcho(t))
	cfg.Token = "wrong"
	_, result := startAgent(t, ctx, cfg)

	// The agent must give up rather than retry forever.
	err := waitResult(t, result)

	var rejected *agent.RejectedError
	if !errors.As(err, &rejected) || rejected.Code != protocol.ErrUnauthorized {
		t.Fatalf("Run() = %v, want unauthorized rejection", err)
	}
}

func TestRejectsInvalidRegistrationPermanently(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47320, 47329)

	// Duplicate names cannot come from the CLI, but a buggy or hostile
	// agent could send them.
	cfg := agentConfig(t, srv, "dup=tcp://"+startEcho(t))
	cfg.Tunnels = append(cfg.Tunnels, cfg.Tunnels[0])
	_, result := startAgent(t, ctx, cfg)

	err := waitResult(t, result)

	var rejected *agent.RejectedError
	if !errors.As(err, &rejected) || rejected.Code != protocol.ErrBadRequest {
		t.Fatalf("Run() = %v, want bad_request rejection", err)
	}
}

func TestRejectsUnsupportedVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47330, 47339)

	session, err := transport.Dial(ctx, srv.addr, tlsutil.PinnedConfig(srv.fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(transport.CloseNormal, "")

	control, err := session.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	control.SetDeadline(time.Now().Add(waitTimeout))

	protocol.WriteMessage(control, protocol.KindRegister, protocol.RegisterMessage{
		Version: 999,
		AgentID: "future",
		Tunnels: []protocol.TunnelRequest{{Name: "t", Protocol: protocol.ProtocolTCP}},
	})

	reply, err := protocol.ReadMessage(control)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if msg, ok := reply.(protocol.ErrorMessage); !ok || msg.Code != protocol.ErrUnsupportedVersion {
		t.Fatalf("reply = %#v, want unsupported_version error", reply)
	}
}

// writeTestCert writes a CA-signed-style self-signed certificate for
// 127.0.0.1 and returns the certificate and key file paths.
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

func TestTrustsServerViaCAFile(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	certFile, keyFile := writeTestCert(t)
	srv := startServer(t, ctx, "127.0.0.1:0", 47340, 47349, func(c *config.ServerConfig) {
		c.TLSCertFile = certFile
		c.TLSKeyFile = keyFile
	})

	cfg := agentConfig(t, srv, startEcho(t))
	cfg.Fingerprint = ""
	cfg.TLSCAFile = certFile
	assignments, _ := startAgent(t, ctx, cfg)

	assertEcho(t, waitPort(t, assignments), randomBytes(t, 256<<10))
}

func TestRejectsUntrustedServerPermanently(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47350, 47359)

	// No fingerprint or CA: the self-signed certificate is not trusted.
	cfg := agentConfig(t, srv, startEcho(t))
	cfg.Fingerprint = ""
	_, result := startAgent(t, ctx, cfg)

	err := waitResult(t, result)
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); !ok {
		t.Fatalf("Run() = %v, want certificate verification error", err)
	}
}

func TestRejectsFingerprintMismatchPermanently(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47360, 47369)

	cfg := agentConfig(t, srv, startEcho(t))
	cfg.Fingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	_, result := startAgent(t, ctx, cfg)

	err := waitResult(t, result)
	if _, ok := errors.AsType[*tlsutil.FingerprintMismatchError](err); !ok {
		t.Fatalf("Run() = %v, want fingerprint mismatch", err)
	}
}

func TestPendingHandshakeLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47370, 47379)
	tlsConf := tlsutil.PinnedConfig(srv.fingerprint)

	// Fill every handshake slot with sessions that never register.
	const limit = 64
	for range limit {
		session, err := transport.Dial(ctx, srv.addr, tlsConf)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close(transport.CloseNormal, "")
	}

	extra, err := transport.Dial(ctx, srv.addr, tlsConf)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close(transport.CloseNormal, "")

	// The server's own handshake timeout is 5s, so a close well before
	// that means the session was dropped for being over the limit.
	select {
	case <-extra.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session over the handshake limit was not dropped")
	}
}
