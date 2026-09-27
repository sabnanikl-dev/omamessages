package gmessages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"omarchy-omamessages/core"
)

const oldStateJSON = `{
  "status": "connected",
  "account": "me@example.com",
  "phone": "+1 555-000-0000",
  "smsDefault": true,
  "unreadCount": 1,
  "conversations": [
    {"id": "11", "name": "Ada", "lastMessage": "hi", "lastTs": 200, "unread": true, "type": "rcs", "participants": []},
    {"id": "22", "name": "Bob", "lastMessage": "yo", "lastTs": 100, "type": "sms", "participants": []}
  ]
}`

const oldThreadJSON = `{"conversationId": "11", "hasMore": true, "messages": [
  {"id": "m1", "ts": 150, "text": "hello", "status": "received"},
  {"id": "m2", "ts": 200, "fromMe": true, "text": "hi", "status": "read"}
]}`

// snapshotTree records every file under dir with its content hash and mtime.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum := ""
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h := sha256.Sum256(data)
			sum = hex.EncodeToString(h[:])
		}
		out[path] = sum + " " + info.ModTime().String() + " " + info.Mode().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPreviewReadsOnly(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "messages"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "state.json"), []byte(oldStateJSON), 0o600)
	os.WriteFile(filepath.Join(src, "messages", "11.json"), []byte(oldThreadJSON), 0o600)
	os.WriteFile(filepath.Join(src, "session.json"), []byte(`{"secret":"do not touch"}`), 0o600)
	before := snapshotTree(t, src)

	dir := t.TempDir()
	store := core.NewStore(dir, nil)
	p := NewPreview(src)(core.Env{Store: store, Dir: dir, Log: zerolog.Nop(), Notify: func(core.Notification) { t.Error("preview must not notify") }})
	ctx := context.Background()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Stop()
	if err := p.Open(ctx, "11", true); err != nil {
		t.Fatalf("Open: %v", err)
	}
	p.Open(ctx, "22", true)
	p.Refresh(ctx)
	for name, err := range map[string]error{
		"Send":       p.Send(ctx, "11", "hi", nil),
		"Connect":    p.Connect(ctx, ""),
		"Disconnect": p.Disconnect(ctx),
	} {
		if err == nil {
			t.Errorf("%s succeeded in preview; want an error", name)
		}
	}

	if after := snapshotTree(t, src); len(after) != len(before) {
		t.Errorf("source tree changed: %d entries before, %d after", len(before), len(after))
	} else {
		for path, sig := range before {
			if after[path] != sig {
				t.Errorf("source file changed: %s", path)
			}
		}
	}

	snap := store.Snapshot()
	if snap.Status != core.StatusDisconnected || snap.Error != previewNote {
		t.Errorf("status = %q %q; want disconnected with the preview note", snap.Status, snap.Error)
	}
	if len(snap.Conversations) != 2 || snap.Conversations[0].ID != "11" || snap.Conversations[0].Extra["type"] != "rcs" || !snap.Conversations[0].Unread {
		t.Errorf("conversations = %+v; want 11 (rcs, unread) first", snap.Conversations)
	}
	if snap.Account != "me@example.com" || snap.Extra["phone"] != "+1 555-000-0000" || snap.Extra["preview"] != true {
		t.Errorf("account/extra = %q %v", snap.Account, snap.Extra)
	}
	if cm := store.Messages("11"); len(cm.Messages) != 2 || cm.Messages[1].Text != "hi" || cm.HasMore {
		t.Errorf("thread 11 = %+v; want both cached messages and no more pages", cm)
	}
	if cm := store.Messages("22"); cm.Error == "" {
		t.Errorf("thread 22 has no cached file but no error is shown")
	}
	if _, err := os.Stat(filepath.Join(dir, PreviewMarker)); err != nil {
		t.Errorf("preview marker missing from the provider dir: %v", err)
	}
}
