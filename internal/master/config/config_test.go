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
	if cfg.HTTP != ":8080" || cfg.GRPC != ":7000" || cfg.DB != "pumpkinpie.db" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	if cfg.Greptime.URL != "" {
		t.Errorf("greptime.url should default to empty, got %q", cfg.Greptime.URL)
	}
	if cfg.Greptime.Database != "public" || cfg.Greptime.Table != "node_metrics" {
		t.Errorf("greptime defaults wrong: %+v", cfg.Greptime)
	}
}

func TestLoad_EmptyPathUsesDefaultPath(t *testing.T) {
	// Sanity: Load("") should not panic and should fall back to the
	// missing-file path (DefaultPath is unlikely to exist in tests).
	if _, _, err := Load(""); err != nil {
		t.Fatalf("Load(\"\") errored: %v", err)
	}
}

func TestLoad_OverrideViaYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pp-master.yaml")
	yaml := `
http: ":18080"
grpc: ":17000"
db: "/tmp/test.db"
greptime:
  url: "greptime.test:4001"
  database: "metrics"
  table: "nodes"
  username: "alice"
  password: "s3cret"
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
	if cfg.HTTP != ":18080" || cfg.GRPC != ":17000" || cfg.DB != "/tmp/test.db" {
		t.Fatalf("scalar fields wrong: %+v", cfg)
	}
	if cfg.Greptime.URL != "greptime.test:4001" {
		t.Errorf("greptime.url: got %q", cfg.Greptime.URL)
	}
	if cfg.Greptime.Database != "metrics" || cfg.Greptime.Table != "nodes" {
		t.Errorf("greptime db/table: got %+v", cfg.Greptime)
	}
	if cfg.Greptime.Username != "alice" || cfg.Greptime.Password != "s3cret" {
		t.Errorf("greptime auth: got %+v", cfg.Greptime)
	}
}

func TestLoad_PartialYAMLFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pp-master.yaml")
	if err := os.WriteFile(path, []byte(`http: ":9999"`), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP != ":9999" {
		t.Errorf("HTTP override lost: %q", cfg.HTTP)
	}
	if cfg.GRPC != ":7000" || cfg.DB != "pumpkinpie.db" {
		t.Errorf("missing fields should keep defaults, got grpc=%q db=%q", cfg.GRPC, cfg.DB)
	}
}

func TestLoad_MalformedYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pp-master.yaml")
	if err := os.WriteFile(path, []byte("http: :not-valid:yaml ::"), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected parse error for malformed YAML")
	}
}