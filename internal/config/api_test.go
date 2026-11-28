package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/nickhirras/loot/internal/config"
)

func TestAPITokensYAMLAndFiles(t *testing.T) {
	dir := t.TempDir()
	readFile := filepath.Join(dir, "read.tokens")
	if err := os.WriteFile(readFile, []byte("# comment\n  file-read-1  \n\nfile-read-2\ninline-read\n"), 0o600); err != nil {
		t.Fatalf("write read tokens: %v", err)
	}
	writeFile := filepath.Join(dir, "write.tokens")
	if err := os.WriteFile(writeFile, []byte("file-write-1\n"), 0o600); err != nil {
		t.Fatalf("write write tokens: %v", err)
	}

	cfgPath := writeConfig(t, `
api:
  read_tokens:
    - inline-read
  read_tokens_path: `+readFile+`
  write_tokens_path: `+writeFile+`
`)

	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.API.Configured() {
		t.Fatal("expected API auth to be configured")
	}
	rToks, wToks, err := cfg.API.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(rToks) != 3 {
		t.Fatalf("read tokens = %v, want 3 deduplicated tokens", rToks)
	}
	if len(wToks) != 1 || wToks[0] != "file-write-1" {
		t.Fatalf("write tokens = %v, want [file-write-1]", wToks)
	}
}

func TestAPITokensEnvOverrides(t *testing.T) {
	t.Setenv("LOOT_API_READ_TOKENS", "r1, r2")
	t.Setenv("LOOT_API_WRITE_TOKENS", "w1")

	cfg, err := config.Load("nonexistent.yaml", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rToks, wToks, err := cfg.API.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(rToks) != 2 || rToks[0] != "r1" || rToks[1] != "r2" {
		t.Fatalf("read tokens = %v", rToks)
	}
	if len(wToks) != 1 || wToks[0] != "w1" {
		t.Fatalf("write tokens = %v", wToks)
	}
}

func TestAPITokensMissingPathFailsLoad(t *testing.T) {
	cfgPath := writeConfig(t, "api:\n  read_tokens_path: /does/not/exist/tokens.txt\n")
	if _, err := config.Load(cfgPath, true); err == nil {
		t.Fatal("expected error when api.read_tokens_path does not exist")
	}
}

func TestWebAuthConfigAndHashFile(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cr3t"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	dir := t.TempDir()
	hashFile := filepath.Join(dir, "password.bcrypt")
	if err := os.WriteFile(hashFile, append([]byte("# bcrypt hash\n"), append(hash, '\n')...), 0o600); err != nil {
		t.Fatalf("write hash file: %v", err)
	}

	cfgPath := writeConfig(t, `
auth:
  username: nick
  password_hash_path: `+hashFile+`
  session_ttl: 48h
`)
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := cfg.Auth.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.Enabled || res.Username != "nick" || res.PasswordHash != string(hash) {
		t.Fatalf("resolved = %+v", res)
	}
	if res.SessionTTL != 48*time.Hour {
		t.Fatalf("session_ttl = %v, want 48h", res.SessionTTL)
	}
	if len(res.SessionKey) != 32 {
		t.Fatalf("session_key len = %d, want 32", len(res.SessionKey))
	}
}

func TestWebAuthInvalidBcryptFailsResolve(t *testing.T) {
	cfgPath := writeConfig(t, "auth:\n  password_hash: not-a-bcrypt-hash\n")
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := cfg.Auth.Resolve(); err == nil {
		t.Fatal("expected error for invalid bcrypt hash")
	}
}

func TestWebAuthEnvOverrides(t *testing.T) {
	t.Setenv("LOOT_AUTH_USERNAME", "alice")
	t.Setenv("LOOT_AUTH_PASSWORD", "wonderland")
	t.Setenv("LOOT_AUTH_SESSION_TTL", "24h")

	cfg, err := config.Load("missing.yaml", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := cfg.Auth.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.Enabled || res.Username != "alice" || res.Password != "wonderland" || res.SessionTTL != 24*time.Hour {
		t.Fatalf("resolved = %+v", res)
	}
}
