package hostsfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDefaultLayout(t *testing.T) {
	dir := t.TempDir()
	f := New(dir, nil, nil)
	if err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	idx := strings.Index(text, docMarker)
	if idx != 0 {
		t.Fatalf("comment tail should start the empty file, idx=%d\n%s", idx, text)
	}
	if !strings.Contains(text, "10.10.10.10 igozhang.cn") || !strings.Contains(text, "示例") {
		t.Fatalf("missing example tail:\n%s", text)
	}
	// Second ensure must not rewrite.
	if err := os.WriteFile(f.Path(), []byte("10.1.1.1 kept.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(f.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "10.1.1.1 kept.example\n" {
		t.Fatalf("existing file was overwritten: %q", raw)
	}
}

func TestCreateInsertsAboveCommentTail(t *testing.T) {
	dir := t.TempDir()
	f := New(dir, nil, nil)
	if _, err := f.Create("b.example", "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Create("a.example", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "hosts"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	mark := strings.Index(text, docMarker)
	b := strings.Index(text, "10.0.0.2 b.example")
	a := strings.Index(text, "10.0.0.1 a.example")
	if b < 0 || a < 0 || mark < 0 || b > mark || a > mark || b > a {
		t.Fatalf("entries should stay above the comment tail in insert order:\n%s", text)
	}
}

func TestCreateAppendsWhenNoMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte("# legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := New(dir, nil, nil)
	if _, err := f.Create("legacy.example", "10.9.9.9"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# legacy\n10.9.9.9 legacy.example\n" {
		t.Fatalf("got %q", raw)
	}
}
