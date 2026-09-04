package config

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestFixedTagline(t *testing.T) {
	ui := uiFromVals(map[string]string{"title": "demo"})
	if ui.Tagline != FixedTagline {
		t.Fatalf("tagline=%q want %q", ui.Tagline, FixedTagline)
	}
	ui = uiFromVals(map[string]string{"tagline": "custom slogan"})
	if ui.Tagline != FixedTagline {
		t.Fatalf("tagline must ignore conf: %q", ui.Tagline)
	}
}

func TestDefaultIconPNG(t *testing.T) {
	raw := DefaultIconPNG()
	if len(raw) < 100 {
		t.Fatalf("png too small: %d", len(raw))
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("size=%dx%d", b.Dx(), b.Dy())
	}
}

func TestResolveIcon(t *testing.T) {
	dir := t.TempDir()
	if p := ResolveIcon(dir, ""); p != "" {
		t.Fatalf("empty dir got %q", p)
	}
	custom := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(custom, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := ResolveIcon(dir, "logo.png"); p != custom {
		t.Fatalf("configured: got %q", p)
	}
	fallback := filepath.Join(dir, "icon.jpg")
	if err := os.WriteFile(fallback, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := ResolveIcon(dir, ""); p != fallback {
		t.Fatalf("fallback: got %q", p)
	}
}

func TestParsePositiveInt(t *testing.T) {
	if parsePositiveInt("", 4) != 4 || parsePositiveInt("0", 4) != 4 || parsePositiveInt("x", 4) != 4 {
		t.Fatal("invalid should use default")
	}
	if parsePositiveInt("6", 4) != 6 {
		t.Fatal("want 6")
	}
}
