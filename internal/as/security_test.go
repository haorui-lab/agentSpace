package as

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRedactPath(t *testing.T) {
	if got := redactPath("/i/ic_super-secret/manifest.json"); got != "/i/:invite/manifest.json" {
		t.Fatalf("got %s", got)
	}
	if got := redactPath("/api/v1/invitations/ic_super-secret/join"); got != "/api/v1/invitations/:invite/join" {
		t.Fatalf("got %s", got)
	}
	if got := redactPath("/healthz"); got != "/healthz" {
		t.Fatalf("got %s", got)
	}
}

func TestSafeAssetPath(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{AssetDir: dir}}
	if _, err := s.safeAssetPath("../etc/passwd"); err == nil {
		t.Fatal("traversal key was accepted")
	}
	key := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	path, err := s.safeAssetPath(key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || len(rel) >= 2 && rel[:2] == ".." {
		t.Fatalf("path escaped: %s", abs)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeFilename(t *testing.T) {
	name, ext, err := sanitizeFilename(`../../etc/passwd.png`)
	if err != nil || name != "passwd.png" || ext != ".png" {
		t.Fatalf("name=%s ext=%s err=%v", name, ext, err)
	}
	if _, _, err := sanitizeFilename("payload.html"); err == nil {
		t.Fatal("html extension was accepted")
	}
	if _, _, err := sanitizeFilename("note.md"); err != nil {
		t.Fatal(err)
	}
}
