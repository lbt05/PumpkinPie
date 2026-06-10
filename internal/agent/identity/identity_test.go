package identity

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestLoad_FirstRunGenerates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !hex16.MatchString(id) {
		t.Errorf("expected 16-hex id, got %q", id)
	}
	info, err := os.Stat(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %v, want 0600", mode)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %v, want 0700", mode)
	}
}

func TestLoad_ReuseExisting(t *testing.T) {
	dir := t.TempDir()
	id1, err := Load(dir)
	if err != nil {
		t.Fatalf("Load 1: %v", err)
	}
	id2, err := Load(dir)
	if err != nil {
		t.Fatalf("Load 2: %v", err)
	}
	if id1 != id2 {
		t.Errorf("expected reuse, got %q then %q", id1, id2)
	}
}

func TestLoad_EmptyFileRegenerates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !hex16.MatchString(id) {
		t.Errorf("expected 16-hex id, got %q", id)
	}
}

func TestLoad_WhitespaceFileRegenerates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte("   \n\t "), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !hex16.MatchString(id) {
		t.Errorf("expected regenerated id, got %q", id)
	}
}

func TestLoad_AcceptsCustomString(t *testing.T) {
	dir := t.TempDir()
	custom := "prod-rack3-node7"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(custom+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if id != custom {
		t.Errorf("expected %q, got %q", custom, id)
	}
}

func TestLoad_TrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("  abc123def456abcd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if id != "abc123def456abcd" {
		t.Errorf("expected trimmed id, got %q", id)
	}
}

func TestLoad_CreatesMissingParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if _, err := Load(dir); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, filename)); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestLoad_EmptyStateDirError(t *testing.T) {
	if _, err := Load(""); err == nil {
		t.Fatal("expected error for empty state dir")
	}
}

func TestLoad_UnwritableDirFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	// Force file creation under the read-only parent.
	stateDir := filepath.Join(parent, "sub")
	_, err := Load(stateDir)
	if err == nil {
		t.Fatal("expected error when state dir cannot be created")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("expected mkdir error, got %v", err)
	}
}

func TestLoad_AtomicWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".machine-id-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
