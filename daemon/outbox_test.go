package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanOutbox(t *testing.T) {
	cache := t.TempDir()
	out := filepath.Join(cache, "outbox")
	os.MkdirAll(filepath.Join(out, "keep-dir"), 0o700)
	old := filepath.Join(out, "paste-old.png")
	fresh := filepath.Join(out, "paste-new.png")
	elsewhere := filepath.Join(cache, "media-old.jpg")
	for _, p := range []string{old, fresh, elsewhere} {
		os.WriteFile(p, []byte("x"), 0o600)
	}
	week := 7 * 24 * time.Hour
	past := time.Now().Add(-week - time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(elsewhere, past, past)

	if n := cleanOutbox(cache, week); n != 1 {
		t.Errorf("removed %d; want 1", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old paste kept")
	}
	for _, p := range []string{fresh, elsewhere, filepath.Join(out, "keep-dir")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed; only old files in the outbox should go", filepath.Base(p))
		}
	}
	if n := cleanOutbox(t.TempDir(), week); n != 0 {
		t.Errorf("no outbox: removed %d", n)
	}
}
