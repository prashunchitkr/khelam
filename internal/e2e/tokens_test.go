package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/prashunchitkr/khelam/internal/agent"
	"github.com/prashunchitkr/khelam/internal/auth"
	"github.com/prashunchitkr/khelam/internal/config"
	"github.com/prashunchitkr/khelam/internal/protocol"
	"go.yaml.in/yaml/v3"
)

// writeTokensFile writes a server tokens file accepting entries.
func writeTokensFile(t *testing.T, path string, entries ...auth.Entry) {
	t.Helper()

	data, err := yaml.Marshal(map[string][]auth.Entry{"tokens": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTokensIsolateAgentIDs checks that an agent ID is scoped to its
// token: an agent reusing another token holder's ID neither replaces
// that agent nor gets its ports.
func TestTokensIsolateAgentIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	tokensPath := filepath.Join(t.TempDir(), "tokens.yaml")
	aliceToken, alice, _ := auth.Generate("alice")
	malloryToken, mallory, _ := auth.Generate("mallory")
	writeTokensFile(t, tokensPath, alice, mallory)

	srv := startServer(t, ctx, "127.0.0.1:0", 47500, 47509,
		func(c *config.ServerConfig) { c.TokensFile = tokensPath })

	aliceCfg := agentConfig(t, srv, startEcho(t))
	aliceCfg.Token = aliceToken
	aliceAssignments, aliceResult := startAgent(t, ctx, aliceCfg)
	alicePort := waitPort(t, aliceAssignments)

	// Mallory knows alice's agent ID but holds a different token.
	malloryCfg := aliceCfg
	malloryCfg.Token = malloryToken
	malloryAssignments, _ := startAgent(t, ctx, malloryCfg)
	if port := waitPort(t, malloryAssignments); port == alicePort {
		t.Fatalf("agent with another token got alice's port %d", alicePort)
	}

	select {
	case err := <-aliceResult:
		t.Fatalf("alice was disconnected by another token's agent: %v", err)
	default:
	}
	assertEcho(t, alicePort, []byte("alice still connected"))
}

func TestTokenRevocationOnReload(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	tokensPath := filepath.Join(t.TempDir(), "tokens.yaml")
	aliceToken, alice, _ := auth.Generate("alice")
	bobToken, bob, _ := auth.Generate("bob")
	writeTokensFile(t, tokensPath, alice, bob)

	srv := startServer(t, ctx, "127.0.0.1:0", 47510, 47519,
		func(c *config.ServerConfig) { c.TokensFile = tokensPath })

	aliceCfg := agentConfig(t, srv, startEcho(t))
	aliceCfg.Token = aliceToken
	aliceAssignments, aliceResult := startAgent(t, ctx, aliceCfg)
	waitPort(t, aliceAssignments)

	bobCfg := agentConfig(t, srv, startEcho(t))
	bobCfg.Token = bobToken
	bobAssignments, bobResult := startAgent(t, ctx, bobCfg)
	bobPort := waitPort(t, bobAssignments)

	// Revoke alice only.
	writeTokensFile(t, tokensPath, bob)
	if err := srv.srv.ReloadTokens(); err != nil {
		t.Fatal(err)
	}

	// Alice is dropped, then rejected on reconnect, and gives up.
	err := waitResult(t, aliceResult)
	var rejected *agent.RejectedError
	if !errors.As(err, &rejected) || rejected.Code != protocol.ErrUnauthorized {
		t.Fatalf("revoked agent Run() = %v, want unauthorized rejection", err)
	}

	select {
	case err := <-bobResult:
		t.Fatalf("bob was disconnected by alice's revocation: %v", err)
	default:
	}
	assertEcho(t, bobPort, []byte("bob unaffected"))
}
