package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// writeTokens writes a tokens file with the given entries.
func writeTokens(t *testing.T, path string, entries ...Entry) {
	t.Helper()

	data, err := yaml.Marshal(tokensFile{Tokens: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	aliceToken, alice, _ := Generate("alice")
	bobToken, bob, _ := Generate("bob")
	writeTokens(t, path, alice, bob)

	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	for token, want := range map[string]string{aliceToken: "alice", bobToken: "bob"} {
		if name, ok := store.Authenticate(token); !ok || name != want {
			t.Errorf("Authenticate(%s token) = %q, %v", want, name, ok)
		}
	}

	if _, ok := store.Authenticate("khelam_wrong"); ok {
		t.Error("unknown token was accepted")
	}
	if _, ok := store.Authenticate(""); ok {
		t.Error("empty token was accepted")
	}
}

func TestReloadRevokes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	token, alice, _ := Generate("alice")
	writeTokens(t, path, alice)

	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	writeTokens(t, path)
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Authenticate(token); ok || store.Has("alice") {
		t.Fatal("revoked token still valid after reload")
	}
}

func TestReloadKeepsTokensOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	token, alice, _ := Generate("alice")
	writeTokens(t, path, alice)

	store, _ := Load(path)
	os.WriteFile(path, []byte("tokens: [{name: x, sha256: nothex}]"), 0o600)

	if err := store.Reload(); err == nil {
		t.Fatal("expected error for invalid file")
	}
	if _, ok := store.Authenticate(token); !ok {
		t.Fatal("valid tokens were dropped by a failed reload")
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	_, valid, _ := Generate("x")

	tests := map[string]string{
		"duplicate name": "tokens:\n- {name: a, sha256: " + valid.SHA256 + "}\n- {name: a, sha256: " + valid.SHA256 + "}\n",
		"missing name":   "tokens:\n- {sha256: " + valid.SHA256 + "}\n",
		"short hash":     "tokens:\n- {name: a, sha256: abcd}\n",
		"slash in name":  "tokens:\n- {name: a/b, sha256: " + valid.SHA256 + "}\n",
		"unknown field":  "tokens:\n- {name: a, sha256: " + valid.SHA256 + ", token: oops}\n",
	}

	for name, data := range tests {
		if _, err := parse([]byte(data)); err == nil {
			t.Errorf("%s: parse succeeded, want error", name)
		}
	}
}

func TestGenerateIsPrefixedAndUnique(t *testing.T) {
	a, _, _ := Generate("a")
	b, _, _ := Generate("a")

	if !strings.HasPrefix(a, tokenPrefix) || a == b {
		t.Fatalf("tokens %q, %q: want unique and prefixed", a, b)
	}
}

// TestExampleTokensFile keeps examples/tokens.yaml loadable.
func TestExampleTokensFile(t *testing.T) {
	if _, err := Load("../../examples/tokens.yaml"); err != nil {
		t.Fatalf("examples/tokens.yaml: %v", err)
	}
}
