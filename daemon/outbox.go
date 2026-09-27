package main

import (
	"os"
	"path/filepath"
	"time"
)

// Pasted screenshots are staged in <cache>/outbox before they're sent, and
// nothing needs them afterwards. cleanOutbox removes those older than keep,
// at daemon start; it never touches anything outside the outbox.
func cleanOutbox(cacheDir string, keep time.Duration) (removed int) {
	dir := filepath.Join(cacheDir, "outbox")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-keep)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}
