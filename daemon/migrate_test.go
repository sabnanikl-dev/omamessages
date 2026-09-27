package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"omarchy-omamessages/core"
)

func writeOld(t *testing.T) string {
	t.Helper()
	old := t.TempDir()
	os.MkdirAll(filepath.Join(old, "messages"), 0o700)
	os.WriteFile(filepath.Join(old, "session.json"), []byte(`{"secret":"pairing"}`), 0o600)
	os.WriteFile(filepath.Join(old, "state.json"), []byte(`{"status":"connected","account":"me@example.com","phone":"+1 555","smsDefault":true,
		"conversations":[{"id":"11","name":"Ada","lastTs":5,"unread":true,"type":"rcs","participants":[]}]}`), 0o600)
	os.WriteFile(filepath.Join(old, "messages", "11.json"), []byte(`{"conversationId":"11","messages":[{"id":"m1","text":"hi"}]}`), 0o600)
	return old
}

func TestCopiesOnce(t *testing.T) {
	old := writeOld(t)
	before := snapshot(t, old)
	newDir := filepath.Join(t.TempDir(), "gmessages")

	ok, err := MigrateFromGMessages(old, newDir)
	if err != nil || !ok {
		t.Fatalf("first run: %v %v", ok, err)
	}
	if got, _ := os.ReadFile(filepath.Join(newDir, "session.json")); string(got) != `{"secret":"pairing"}` {
		t.Errorf("session not copied: %q", got)
	}
	if info, _ := os.Stat(filepath.Join(newDir, "session.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("session mode %v; want 0600", info.Mode().Perm())
	}
	var st core.ProviderState
	data, _ := os.ReadFile(filepath.Join(newDir, "state.json"))
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Conversations) != 1 || st.Conversations[0].Extra["type"] != "rcs" || st.Account != "me@example.com" || st.Extra["phone"] != "+1 555" {
		t.Errorf("state not converted: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(newDir, "messages", "11.json")); err != nil {
		t.Errorf("thread not copied: %v", err)
	}
	// The old folder is untouched.
	if after := snapshot(t, old); len(after) != len(before) {
		t.Errorf("old dir changed")
	} else {
		for k, v := range before {
			if after[k] != v {
				t.Errorf("old file changed: %s", k)
			}
		}
	}
	// A second run does nothing, and never overwrites the new copy.
	os.WriteFile(filepath.Join(newDir, "session.json"), []byte(`{"secret":"rotated since"}`), 0o600)
	if ok, err := MigrateFromGMessages(old, newDir); ok || err != nil {
		t.Errorf("second run: %v %v; want a no-op", ok, err)
	}
	if got, _ := os.ReadFile(filepath.Join(newDir, "session.json")); string(got) != `{"secret":"rotated since"}` {
		t.Errorf("second run overwrote the migrated session")
	}
}

func TestMigrateReplacesPreview(t *testing.T) {
	old := writeOld(t)
	newDir := filepath.Join(t.TempDir(), "gmessages")
	os.MkdirAll(newDir, 0o700)
	os.WriteFile(filepath.Join(newDir, "PREVIEW"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(newDir, "state.json"), []byte(`{"conversations":[]}`), 0o600)
	if ok, err := MigrateFromGMessages(old, newDir); !ok || err != nil {
		t.Fatalf("over a preview: %v %v; want migrated", ok, err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "PREVIEW")); !os.IsNotExist(err) {
		t.Errorf("preview marker survived")
	}
	if _, err := os.Stat(filepath.Join(newDir, "session.json")); err != nil {
		t.Errorf("session missing after replacing the preview")
	}
	// Nothing to migrate: no old session.
	if ok, err := MigrateFromGMessages(t.TempDir(), filepath.Join(t.TempDir(), "g")); ok || err != nil {
		t.Errorf("empty old dir: %v %v", ok, err)
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(p)
			out[p] = string(data) + info.ModTime().String()
		}
		return nil
	})
	return out
}
