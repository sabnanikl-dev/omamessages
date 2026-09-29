package gmessages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"omarchy-omamessages/core"
)

// Google Messages attachments are fetched through the phone with the media
// ID and decryption key that come in each message. Those are saved next to
// the thread's media when a message is seen, so an attachment can be
// downloaded later:
//
//	<cache>/media/<chat>/<msg>-<idx>.key          IDs and keys (0600)
//	<cache>/media/<chat>/<msg>-<idx>-thumb.jpg    the preview, fetched in the background
//	<cache>/media/<chat>/<msg>-<idx>-<name>       the full file, fetched on first open

type mediaKey struct {
	MediaID  string `json:"mediaId"`
	Key      []byte `json:"key"`
	ThumbID  string `json:"thumbId,omitempty"`
	ThumbKey []byte `json:"thumbKey,omitempty"`
}

func (g *GMessages) mediaDir(convID string) string {
	return filepath.Join(g.env.CacheDir, "media", core.SafeName(convID))
}

func (g *GMessages) mediaBase(convID, msgID string, idx int) string {
	return filepath.Join(g.mediaDir(convID), core.SafeName(msgID)+"-"+strconv.Itoa(idx))
}

func (g *GMessages) keyPath(convID, msgID string, idx int) string {
	return g.mediaBase(convID, msgID, idx) + ".key"
}

func (g *GMessages) thumbPath(convID, msgID string, idx int) string {
	return g.mediaBase(convID, msgID, idx) + "-thumb.jpg"
}

// Go's MIME table lists extensions alphabetically, so the usual ones first.
var preferredExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "video/3gpp": ".3gp", "audio/amr": ".amr", "audio/ogg": ".ogg",
	"audio/mpeg": ".mp3", "audio/mp4": ".m4a", "application/pdf": ".pdf", "text/plain": ".txt", "text/vcard": ".vcf",
}

func extFor(mimeType string) string {
	if e, ok := preferredExt[mimeType]; ok {
		return e
	}
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// fileName is what an attachment is saved as: its own name when it has a
// real one, else one from its kind and the message.
func fileName(a core.Attachment, msgID string) string {
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, strings.TrimSpace(a.Name))
	if name == "" || name == "." || name == ".." {
		base := map[string]string{"image": "photo", "video": "video", "audio": "audio"}[a.Kind]
		if base == "" {
			base = "file"
		}
		name = base + "-" + core.SafeName(msgID)
	}
	if filepath.Ext(name) == "" {
		name += extFor(a.Mime)
	}
	return name
}

func (g *GMessages) filePath(convID, msgID string, idx int, a core.Attachment) string {
	return g.mediaBase(convID, msgID, idx) + "-" + fileName(a, msgID)
}

// mediaContents lists a message's attachments in the order convertMessage
// turns them into core attachments.
func mediaContents(m *gmproto.Message) []*gmproto.MediaContent {
	var out []*gmproto.MediaContent
	for _, info := range m.GetMessageInfo() {
		if d, ok := info.GetData().(*gmproto.MessageInfo_MediaContent); ok {
			out = append(out, d.MediaContent)
		}
	}
	return out
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Size() > 0
}

// noteMedia keeps what's needed to download a message's attachments, points
// them at files already cached, and queues missing previews. It never calls
// into the store: Open and More run it while holding the store's lock.
func (g *GMessages) noteMedia(convID string, raw *gmproto.Message, m *core.Message) {
	for i, mc := range mediaContents(raw) {
		if i >= len(m.Attachments) || mc.GetMediaID() == "" {
			continue
		}
		g.saveKey(convID, m.ID, i, mc)
		a := &m.Attachments[i]
		if p := g.thumbPath(convID, m.ID, i); fileExists(p) {
			a.ThumbPath = p
		} else if (a.Kind == "image" || a.Kind == "video") && mc.GetThumbnailMediaID() != "" {
			g.queueThumb(convID, m.ID, i, mc.GetThumbnailMediaID(), mc.GetThumbnailDecryptionKey())
		}
		if p := g.filePath(convID, m.ID, i, *a); fileExists(p) {
			a.Path = p
		}
	}
}

func (g *GMessages) saveKey(convID, msgID string, idx int, mc *gmproto.MediaContent) {
	p := g.keyPath(convID, msgID, idx)
	if _, err := os.Stat(p); err == nil {
		return
	}
	data, err := json.Marshal(mediaKey{MediaID: mc.GetMediaID(), Key: mc.GetDecryptionKey(), ThumbID: mc.GetThumbnailMediaID(), ThumbKey: mc.GetThumbnailDecryptionKey()})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
		_ = os.WriteFile(p, data, 0o600)
	}
}

func (g *GMessages) loadKey(convID, msgID string, idx int) (mediaKey, error) {
	var k mediaKey
	data, err := os.ReadFile(g.keyPath(convID, msgID, idx))
	if err != nil {
		return k, errors.New("this attachment hasn't been loaded from the phone yet; reopen the chat")
	}
	if err := json.Unmarshal(data, &k); err != nil {
		return k, err
	}
	if k.MediaID == "" {
		return k, errors.New("this attachment can't be downloaded")
	}
	return k, nil
}

// writeFile saves data through a temporary file, so a half-written file
// never counts as cached.
func writeFile(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// mediaState throttles previews and shares full downloads between clicks.
type mediaState struct {
	slots    chan struct{}
	mu       sync.Mutex
	queued   map[string]bool
	inflight map[string]chan struct{}
	errs     map[string]error
}

func (g *GMessages) media() *mediaState {
	g.mediaInit.Do(func() {
		g.mstate = &mediaState{slots: make(chan struct{}, 3), queued: map[string]bool{}, inflight: map[string]chan struct{}{}, errs: map[string]error{}}
	})
	return g.mstate
}

// queueThumb fetches a preview from the phone in the background and shows
// it once it's there.
func (g *GMessages) queueThumb(convID, msgID string, idx int, thumbID string, key []byte) {
	ms := g.media()
	p := g.thumbPath(convID, msgID, idx)
	ms.mu.Lock()
	if ms.queued[p] {
		ms.mu.Unlock()
		return
	}
	ms.queued[p] = true
	ms.mu.Unlock()
	g.env.Spawn(func() {
		defer func() {
			ms.mu.Lock()
			delete(ms.queued, p)
			ms.mu.Unlock()
		}()
		ms.slots <- struct{}{}
		defer func() { <-ms.slots }()
		cli, err := g.connectedClient()
		if err != nil {
			return
		}
		data, err := cli.DownloadMedia(thumbID, key)
		if err != nil || len(data) == 0 {
			logger.Debug().Err(err).Str("msg", msgID).Msg("Preview download failed")
			return
		}
		if err := writeFile(p, data); err != nil {
			return
		}
		g.setAttachment(convID, msgID, idx, func(a *core.Attachment) { a.ThumbPath = p })
	})
}

func (g *GMessages) setAttachment(convID, msgID string, idx int, fn func(*core.Attachment)) {
	if !g.store.HasMessages(convID) {
		return
	}
	g.store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		for i := range cm.Messages {
			if cm.Messages[i].ID == msgID && idx < len(cm.Messages[i].Attachments) {
				fn(&cm.Messages[i].Attachments[idx])
			}
		}
	})
}

var errStillDownloading = errors.New("still downloading; it opens when you click it again")

// FetchMedia downloads an attachment in full (once) and returns its path.
func (g *GMessages) FetchMedia(ctx context.Context, convID, msgID string, idx int) (string, error) {
	cli, err := g.connectedClient()
	if err != nil {
		return "", err
	}
	var att *core.Attachment
	for _, m := range g.store.Messages(convID).Messages {
		if m.ID == msgID && idx >= 0 && idx < len(m.Attachments) {
			a := m.Attachments[idx]
			att = &a
		}
	}
	if att == nil {
		return "", errors.New("that attachment isn't loaded; open the chat first")
	}
	p := g.filePath(convID, msgID, idx, *att)
	if fileExists(p) {
		g.setAttachment(convID, msgID, idx, func(a *core.Attachment) { a.Path = p })
		return p, nil
	}
	k, err := g.loadKey(convID, msgID, idx)
	if err != nil {
		return "", err
	}

	ms := g.media()
	ms.mu.Lock()
	done, running := ms.inflight[p]
	if !running {
		done = make(chan struct{})
		ms.inflight[p] = done
		g.env.Spawn(func() {
			err := errors.New("the download failed")
			defer func() {
				ms.mu.Lock()
				ms.errs[p] = err
				delete(ms.inflight, p)
				ms.mu.Unlock()
				close(done)
			}()
			var data []byte
			// DownloadMedia takes no context; it goes through the phone.
			if data, err = cli.DownloadMedia(k.MediaID, k.Key); err == nil {
				err = writeFile(p, data)
			}
		})
	}
	ms.mu.Unlock()

	wait, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	select {
	case <-done:
	case <-wait.Done():
		return "", errStillDownloading
	}
	ms.mu.Lock()
	err = ms.errs[p]
	delete(ms.errs, p)
	ms.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	g.setAttachment(convID, msgID, idx, func(a *core.Attachment) { a.Path = p })
	return p, nil
}
