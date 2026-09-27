package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"omarchy-omamessages/core"
)

// A WhatsApp attachment can only be downloaded with the keys that came in
// its message (where it's stored, how it's encrypted). Nothing on the server
// lets us ask for them again, so each media message is saved next to its
// preview when it arrives:
//
//	<cache>/media/<chat>/<id>.msg              the message's media part (protobuf)
//	<cache>/media/<chat>/<id>-thumb.jpg        the inline preview
//	<cache>/media/<chat>/<id>-<idx>-<name>     the full file, fetched on first open

func (w *WhatsApp) mediaDir(convID string) string {
	return filepath.Join(w.env.CacheDir, "media", core.SafeName(convID))
}

func (w *WhatsApp) keyPath(convID, msgID string) string {
	return filepath.Join(w.mediaDir(convID), core.SafeName(msgID)+".msg")
}

// downloadable is the part of a message that whatsmeow can download.
func downloadable(m *waE2E.Message) (whatsmeow.DownloadableMessage, bool) {
	switch {
	case m.GetImageMessage() != nil:
		return m.GetImageMessage(), true
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage(), true
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage(), true
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage(), true
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage(), true
	}
	return nil, false
}

// saveMediaKey keeps what is needed to download a message's attachment
// later. Messages without one are left alone.
func (w *WhatsApp) saveMediaKey(convID, msgID string, m *waE2E.Message) {
	if m == nil {
		return
	}
	if _, ok := downloadable(m); !ok {
		return
	}
	p := w.keyPath(convID, msgID)
	if _, err := os.Stat(p); err == nil {
		return
	}
	data, err := proto.Marshal(m)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
		_ = os.WriteFile(p, data, 0o600)
	}
}

func (w *WhatsApp) loadMediaKey(convID, msgID string) (whatsmeow.DownloadableMessage, error) {
	data, err := os.ReadFile(w.keyPath(convID, msgID))
	if err != nil {
		return nil, errors.New("this attachment arrived before Omarchy Messages kept download keys; open it on your phone")
	}
	var m waE2E.Message
	if err := proto.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	d, ok := downloadable(&m)
	if !ok {
		return nil, errors.New("this attachment can't be downloaded")
	}
	return d, nil
}

// Go's MIME table lists extensions alphabetically, so the usual ones first.
var preferredExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a",
	"application/pdf": ".pdf", "text/plain": ".txt",
}

func extFor(mimeType string) string {
	if i := strings.IndexByte(mimeType, ';'); i >= 0 {
		mimeType = strings.TrimSpace(mimeType[:i])
	}
	if e, ok := preferredExt[mimeType]; ok {
		return e
	}
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// fileName is what an attachment is saved as: a document keeps its own
// name; photos, videos and voice notes get one from their kind and ID.
func fileName(a core.Attachment, msgID string) string {
	name := a.Name
	if a.Kind != "file" || name == "" || name == "File" {
		base := map[string]string{"image": "photo", "video": "video", "audio": "audio"}[a.Kind]
		if base == "" {
			base = "file"
		}
		name = base + "-" + core.SafeName(msgID)
	}
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, name)
	if filepath.Ext(name) == "" {
		name += extFor(a.Mime)
	}
	return name
}

func (w *WhatsApp) filePath(convID, msgID string, idx int, a core.Attachment) string {
	return filepath.Join(w.mediaDir(convID), fmt.Sprintf("%s-%d-%s", core.SafeName(msgID), idx, fileName(a, msgID)))
}

// downloads shares one download between concurrent opens of the same file.
type downloads struct {
	mu       sync.Mutex
	inflight map[string]chan struct{}
	errs     map[string]error
}

var errStillDownloading = errors.New("still downloading; it opens when you click it again")

// FetchMedia downloads an attachment in full (once) and returns its path.
func (w *WhatsApp) FetchMedia(ctx context.Context, convID, msgID string, idx int) (string, error) {
	cli := w.connected()
	if cli == nil {
		return "", errNotConnected
	}
	var att *core.Attachment
	for _, m := range w.env.Store.Messages(convID).Messages {
		if m.ID == msgID && idx >= 0 && idx < len(m.Attachments) {
			a := m.Attachments[idx]
			att = &a
		}
	}
	if att == nil {
		return "", errors.New("that attachment isn't loaded; open the chat first")
	}
	path := w.filePath(convID, msgID, idx, *att)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		w.setPath(convID, msgID, idx, path)
		return path, nil
	}
	dl, err := w.loadMediaKey(convID, msgID)
	if err != nil {
		return "", err
	}

	w.mu.Lock()
	if w.dls == nil {
		w.dls = &downloads{inflight: map[string]chan struct{}{}, errs: map[string]error{}}
	}
	d := w.dls
	w.mu.Unlock()
	d.mu.Lock()
	done, running := d.inflight[path]
	if !running {
		done = make(chan struct{})
		d.inflight[path] = done
		w.env.Spawn(func() {
			err := errors.New("the download failed")
			defer func() {
				d.mu.Lock()
				d.errs[path] = err
				delete(d.inflight, path)
				d.mu.Unlock()
				close(done)
			}()
			dctx, cancel := context.WithTimeout(w.lifeCtx(), 30*time.Minute)
			defer cancel()
			err = w.download(dctx, cli, dl, path)
		})
	}
	d.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return "", errStillDownloading
	}
	d.mu.Lock()
	err = d.errs[path]
	delete(d.errs, path)
	d.mu.Unlock()
	if err != nil {
		return "", err
	}
	w.setPath(convID, msgID, idx, path)
	return path, nil
}

func (w *WhatsApp) download(ctx context.Context, cli *whatsmeow.Client, dl whatsmeow.DownloadableMessage, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = cli.DownloadToFile(ctx, dl, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
			return errors.New("this file is no longer on WhatsApp's servers; open it on your phone")
		}
		return fmt.Errorf("download failed: %w", err)
	}
	return os.Rename(tmp, path)
}

func (w *WhatsApp) setPath(convID, msgID string, idx int, path string) {
	w.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		for i := range cm.Messages {
			if cm.Messages[i].ID == msgID && idx < len(cm.Messages[i].Attachments) {
				cm.Messages[i].Attachments[idx].Path = path
			}
		}
	})
}
