package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileUsesDefaults(t *testing.T) {
	cfg, existed, err := Load("/no/such/file.yaml")
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if existed {
		t.Fatal("missing file should report existed=false")
	}
	if cfg.Master != DefaultMaster {
		t.Errorf("master: got %q, want %q", cfg.Master, DefaultMaster)
	}
	// StateDir and DockerSock both stay empty so the runner can apply
	// platform-aware + env-var fallbacks. Hard-coding values here would
	// make ./bin/pp agent fail on non-root macOS hosts where the Linux
	// defaults aren't writable, and would bypass the $DOCKER_SOCK /
	// $DOCKER_HOST chain for ad-hoc CLI use.
	if cfg.StateDir != "" {
		t.Errorf("state_dir should default to empty, got %q", cfg.StateDir)
	}
	if cfg.DockerSock != "" {
		t.Errorf("docker_sock should default to empty, got %q", cfg.DockerSock)
	}
	if cfg.Name != "" {
		t.Errorf("name should default to empty (runner fills in), got %q", cfg.Name)
	}
}

func TestLoad_EmptyPathUsesDefaultPath(t *testing.T) {
	if _, _, err := Load(""); err != nil {
		t.Fatalf("Load(\"\") errored: %v", err)
	}
}

func TestLoad_OverrideViaYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pp-agent.yaml")
	yaml := `
master: "10.0.0.1:7000"
name: "node-A"
state_dir: "/var/lib/pp-agent"
docker_sock: "/run/docker.sock"
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cfg, existed, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !existed {
		t.Fatal("expected existed=true")
	}
	if cfg.Master != "10.0.0.1:7000" {
		t.Errorf("master: got %q", cfg.Master)
	}
	if cfg.Name != "node-A" {
		t.Errorf("name: got %q", cfg.Name)
	}
	if cfg.StateDir != "/var/lib/pp-agent" {
		t.Errorf("state_dir: got %q", cfg.StateDir)
	}
	if cfg.DockerSock != "/run/docker.sock" {
		t.Errorf("docker_sock: got %q", cfg.DockerSock)
	}
}

func TestLoad_PartialYAMLFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pp-agent.yaml")
	if err := os.WriteFile(path, []byte(`master: "10.0.0.1:7000"`), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Master != "10.0.0.1:7000" {
		t.Errorf("master override lost: %q", cfg.Master)
	}
	// StateDir + DockerSock both stay empty so the runner can apply
	// the platform-aware / env-var fallbacks. See applyDefaults.
	if cfg.StateDir != "" {
		t.Errorf("missing state_dir should stay empty, got %q", cfg.StateDir)
	}
	if cfg.DockerSock != "" {
		t.Errorf("missing docker_sock should stay empty (env chain), got %q", cfg.DockerSock)
	}
}

func TestLoad_MalformedYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pp-agent.yaml")
	if err := os.WriteFile(path, []byte("master: :not-valid:yaml ::"), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected parse error for malformed YAML")
	}
}