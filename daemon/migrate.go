package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"omarchy-omamessages/core"
	"omarchy-omamessages/providers/gmessages"
)

// MigrateFromGMessages brings the older gmessages plugin's pairing and
// cached chats into newDir (…/omamessages/gmessages), once, at cutover. It
// copies and never moves: oldDir stays as it was, as a backup and for a
// rollback. It does nothing if newDir already holds a real migration; a
// directory that only holds read-only preview copies (marked PREVIEW) is
// replaced. The old daemon must not be running: two clients on one pairing
// fight.
func MigrateFromGMessages(oldDir, newDir string) (migrated bool, err error) {
	if _, err := os.Stat(filepath.Join(oldDir, "session.json")); err != nil {
		return false, nil // nothing to bring over
	}
	if _, err := os.Stat(newDir); err == nil {
		if _, err := os.Stat(filepath.Join(newDir, gmessages.PreviewMarker)); err != nil {
			return false, nil // already migrated (or set up fresh)
		}
		if err := os.RemoveAll(newDir); err != nil {
			return false, err
		}
	}
	tmp := newDir + ".migrating"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "messages"), 0o700); err != nil {
		return false, err
	}
	if err := copyFile(filepath.Join(oldDir, "session.json"), filepath.Join(tmp, "session.json")); err != nil {
		return false, err
	}
	if data, err := os.ReadFile(filepath.Join(oldDir, "state.json")); err == nil {
		st, err := gmessages.ConvertOldState(data)
		if err != nil {
			return false, err
		}
		if err := core.WriteJSONAtomic(filepath.Join(tmp, "state.json"), st, 0o600); err != nil {
			return false, err
		}
	}
	threads, _ := filepath.Glob(filepath.Join(oldDir, "messages", "*.json"))
	for _, f := range threads {
		if err := copyFile(f, filepath.Join(tmp, "messages", filepath.Base(f))); err != nil {
			return false, err
		}
	}
	// Only a complete copy takes the new name.
	if err := os.Rename(tmp, newDir); err != nil {
		return false, err
	}
	return true, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

var errOldDaemon = errors.New("the old Google Messages plugin is still running")
