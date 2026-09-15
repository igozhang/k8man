package write

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizePodPath(t *testing.T) {
	got, err := sanitizePodPath("/var/log")
	if err != nil || got != "/var/log" {
		t.Fatalf("got %q %v", got, err)
	}
	got, err = sanitizePodPath("/var/../etc/passwd")
	if err != nil || got != "/etc/passwd" {
		t.Fatalf("clean resolve: got %q %v", got, err)
	}
	if _, err := sanitizePodPath("rel"); err == nil {
		t.Fatal("want absolute")
	}
	if _, err := sanitizePodPath(""); err == nil {
		t.Fatal("want empty err")
	}
	if _, err := sanitizePodPath("/tmp/\nfoo"); err == nil {
		t.Fatal("want control char err")
	}
	got, err = sanitizePodPath(" /tmp ")
	if err != nil || got != "/tmp" {
		t.Fatalf("trim: got %q %v", got, err)
	}
}

func TestSplitDirForTar(t *testing.T) {
	p, b, err := splitDirForTar("/var/log")
	if err != nil || p != "/var" || b != "log" {
		t.Fatalf("got %q %q %v", p, b, err)
	}
	p, b, err = splitDirForTar("/foo")
	if err != nil || p != "/" || b != "foo" {
		t.Fatalf("got %q %q %v", p, b, err)
	}
	if _, _, err := splitDirForTar("/"); err == nil {
		t.Fatal("root should fail")
	}
}

func TestNextDirArchiveName(t *testing.T) {
	dlSeqMu.Lock()
	dlSeq = map[string]int{}
	dlSeqMu.Unlock()

	now := time.Date(2026, 9, 15, 15, 30, 0, 0, beijingLoc)
	scope := "c/ns/pod/app/var/log"
	a := nextDirArchiveName(scope, "logs", now)
	b := nextDirArchiveName(scope, "logs", now)
	c := nextDirArchiveName(scope, "logs", now.Add(time.Hour))
	other := nextDirArchiveName("c/ns/other/app/var/log", "logs", now)
	if a != "logs202609151501.tar.gz" {
		t.Fatalf("first: %s", a)
	}
	if b != "logs202609151502.tar.gz" {
		t.Fatalf("second: %s", b)
	}
	if c != "logs202609151601.tar.gz" {
		t.Fatalf("next hour: %s", c)
	}
	if other != "logs202609151501.tar.gz" {
		t.Fatalf("other pod should start at 01: %s", other)
	}
}

func TestContentDisposition(t *testing.T) {
	s := contentDisposition("logs202609151501.tar.gz")
	if !strings.Contains(s, `filename="logs202609151501.tar.gz"`) {
		t.Fatalf("got %s", s)
	}
}
