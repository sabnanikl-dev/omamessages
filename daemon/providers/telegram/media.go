package telegram

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"omarchy-omamessages/core"
)

// Received photos and files live in the cache at fixed paths:
//
//	<cache>/media/<chat>/<msg>-thumb.jpg      small preview, fetched when the message loads
//	<cache>/media/<chat>/<msg>-<idx>-<name>   the full file, fetched on first open
//
// Converting a message points at whatever is already there, so nothing is
// downloaded twice, across restarts too.

func mediaDir(cacheDir, convID string) string {
	return filepath.Join(cacheDir, "media", core.SafeName(convID))
}

func thumbPath(cacheDir, convID string, msgID int) string {
	return filepath.Join(mediaDir(cacheDir, convID), strconv.Itoa(msgID)+"-thumb.jpg")
}

// fileName is the name an attachment is saved under: its own name, with an
// extension from its type when it has none.
func fileName(a core.Attachment, msgID int) string {
	name := strings.TrimSpace(a.Name)
	if a.Kind == "image" && name == "Photo" {
		return "photo-" + strconv.Itoa(msgID) + ".jpg"
	}
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == 0 || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	if filepath.Ext(name) == "" && a.Mime != "" {
		name += extFor(a.Mime)
	}
	return name
}

// Go's MIME table lists extensions alphabetically (".asc" for text/plain),
// so the usual ones come first.
var preferredExt = map[string]string{
	"text/plain": ".txt", "image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "video/quicktime": ".mov", "audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a",
	"application/pdf": ".pdf", "application/zip": ".zip",
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

func filePath(cacheDir, convID string, msgID, idx int, a core.Attachment) string {
	return filepath.Join(mediaDir(cacheDir, convID), strconv.Itoa(msgID)+"-"+strconv.Itoa(idx)+"-"+fileName(a, msgID))
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// annotate points m's attachments at files already in the cache.
func annotate(cacheDir, convID string, msgID int, m *core.Message) {
	for i := range m.Attachments {
		a := &m.Attachments[i]
		if p := thumbPath(cacheDir, convID, msgID); i == 0 && exists(p) {
			a.ThumbPath = p
		}
		if p := filePath(cacheDir, convID, msgID, i, *a); exists(p) {
			a.Path = p
		}
	}
}

// thumbLocation picks a small preview of a photo, or of a video or image
// document; ok is false when there is none worth fetching.
func thumbLocation(media tg.MessageMediaClass) (tg.InputFileLocationClass, bool) {
	switch v := media.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := v.Photo.(*tg.Photo)
		if !ok {
			return nil, false
		}
		if t := pickSize(p.Sizes, false); t != "" {
			return &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: t}, true
		}
	case *tg.MessageMediaDocument:
		d, ok := v.Document.(*tg.Document)
		if !ok {
			return nil, false
		}
		if t := pickSize(d.Thumbs, false); t != "" {
			return &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference, ThumbSize: t}, true
		}
	}
	return nil, false
}

// fullLocation is the whole photo (its largest size) or document.
func fullLocation(media tg.MessageMediaClass) (tg.InputFileLocationClass, bool) {
	switch v := media.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := v.Photo.(*tg.Photo)
		if !ok {
			return nil, false
		}
		if t := pickSize(p.Sizes, true); t != "" {
			return &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: t}, true
		}
	case *tg.MessageMediaDocument:
		d, ok := v.Document.(*tg.Document)
		if !ok {
			return nil, false
		}
		return &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}, true
	}
	return nil, false
}

// pickSize chooses a downloadable size: the largest, or for a preview the
// "m" size (about 320 px) when there is one, else the smallest real size.
// Stripped and path sizes are inline hints, not files.
func pickSize(sizes []tg.PhotoSizeClass, largest bool) string {
	best, bestW := "", 0
	for _, s := range sizes {
		var typ string
		var w int
		switch v := s.(type) {
		case *tg.PhotoSize:
			typ, w = v.Type, v.W
		case *tg.PhotoSizeProgressive:
			typ, w = v.Type, v.W
		default:
			continue
		}
		if !largest && typ == "m" {
			return typ
		}
		if best == "" || (largest && w > bestW) || (!largest && w < bestW) {
			best, bestW = typ, w
		}
	}
	return best
}

// --- downloading ---------------------------------------------------------------------

// mediaState is the session's memory of which media each message carries,
// plus the downloads in flight.
type mediaState struct {
	mu       sync.Mutex
	byMsg    map[string]tg.MessageMediaClass // "<chat>/<msg>"
	inflight map[string]*download            // by destination path
	slots    chan struct{}                   // limits concurrent thumbnail downloads
	spawn    func(func())                    // the hub's panic-isolated goroutine
}

type download struct {
	done chan struct{}
	err  error
}

func newMediaState(spawn func(func())) *mediaState {
	return &mediaState{byMsg: map[string]tg.MessageMediaClass{}, inflight: map[string]*download{}, slots: make(chan struct{}, 3), spawn: spawn}
}

func mediaKey(convID string, msgID int) string { return convID + "/" + strconv.Itoa(msgID) }

func (ms *mediaState) remember(convID string, msgID int, media tg.MessageMediaClass) {
	if media == nil {
		return
	}
	ms.mu.Lock()
	ms.byMsg[mediaKey(convID, msgID)] = media
	ms.mu.Unlock()
}

func (ms *mediaState) lookup(convID string, msgID int) tg.MessageMediaClass {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.byMsg[mediaKey(convID, msgID)]
}

// once runs fetch for path unless the file is already there; concurrent
// callers for the same path share one download.
func (ms *mediaState) once(ctx context.Context, path string, fetch func() error) error {
	if exists(path) {
		return nil
	}
	ms.mu.Lock()
	d, running := ms.inflight[path]
	// A download that finished since the check above has removed itself
	// from inflight only after writing the file, so look again.
	if !running && exists(path) {
		ms.mu.Unlock()
		return nil
	}
	if !running {
		d = &download{done: make(chan struct{})}
		ms.inflight[path] = d
	}
	ms.mu.Unlock()
	if !running {
		ms.spawn(func() {
			// Waiters are released even if fetch panics.
			d.err = errors.New("the download failed")
			defer func() {
				ms.mu.Lock()
				delete(ms.inflight, path)
				ms.mu.Unlock()
				close(d.done)
			}()
			d.err = fetch()
		})
	}
	select {
	case <-d.done:
		return d.err
	case <-ctx.Done():
		return errStillDownloading
	}
}

var errStillDownloading = errors.New("still downloading; it opens when you click it again")

// saveTo downloads loc to path through a temporary file, so a half-written
// file never counts as cached.
func saveTo(ctx context.Context, api *tg.Client, loc tg.InputFileLocationClass, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".part"
	if _, err := downloader.NewDownloader().Download(api, loc).ToPath(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func referenceExpired(err error) bool {
	return tgerr.Is(err, "FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID", "FILE_REFERENCE_EMPTY")
}

// refreshMedia fetches a message again, for its current file reference.
func (t *Telegram) refreshMedia(ctx context.Context, s *liveSession, convID string, msgID int) (tg.MessageMediaClass, error) {
	in, err := s.inputPeer(ctx, convID)
	if err != nil {
		return nil, err
	}
	res, err := s.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: in, OffsetID: msgID + 1, Limit: 1})
	if err != nil {
		return nil, err
	}
	msgs, _, _ := history(res)
	for _, mc := range msgs {
		if m, ok := mc.(*tg.Message); ok && m.ID == msgID && m.Media != nil {
			s.media.remember(convID, msgID, m.Media)
			return m.Media, nil
		}
	}
	return nil, errors.New("that message is gone")
}

// withMedia runs get with the message's media, fetching the message when it
// isn't known yet, and once more with a fresh file reference if Telegram
// says the one we had has expired.
func (t *Telegram) withMedia(ctx context.Context, s *liveSession, convID string, msgID int, get func(tg.MessageMediaClass) error) error {
	media := s.media.lookup(convID, msgID)
	if media == nil {
		var err error
		if media, err = t.refreshMedia(ctx, s, convID, msgID); err != nil {
			return err
		}
	}
	err := get(media)
	if referenceExpired(err) {
		if media, err = t.refreshMedia(ctx, s, convID, msgID); err != nil {
			return err
		}
		err = get(media)
	}
	return err
}

// queueThumb fetches a message's preview in the background and shows it in
// the thread once it's there.
// media may be nil when the message is only in the cache; it is then
// looked up again.
func (t *Telegram) queueThumb(s *liveSession, convID string, msgID int, media tg.MessageMediaClass) {
	if media != nil {
		if _, ok := thumbLocation(media); !ok {
			return
		}
		s.media.remember(convID, msgID, media)
	}
	path := thumbPath(t.env.CacheDir, convID, msgID)
	if exists(path) {
		return
	}
	t.env.Spawn(func() {
		ctx, cancel := context.WithTimeout(t.lifeCtx(), 2*time.Minute)
		defer cancel()
		s.media.slots <- struct{}{}
		defer func() { <-s.media.slots }()
		err := s.media.once(ctx, path, func() error {
			return t.withMedia(ctx, s, convID, msgID, func(m tg.MessageMediaClass) error {
				loc, ok := thumbLocation(m)
				if !ok {
					return errors.New("no preview")
				}
				return saveTo(ctx, s.api, loc, path)
			})
		})
		if err != nil {
			t.env.Log.Warn().Err(err).Str("conv", convID).Int("msg", msgID).Msg("Preview download failed")
			return
		}
		t.setAttachment(convID, msgID, 0, func(a *core.Attachment) { a.ThumbPath = path })
	})
}

// setAttachment edits one attachment of a cached message, if it's loaded.
func (t *Telegram) setAttachment(convID string, msgID, idx int, fn func(*core.Attachment)) {
	if !t.env.Store.HasMessages(convID) {
		return
	}
	id := strconv.Itoa(msgID)
	t.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		for i := range cm.Messages {
			if cm.Messages[i].ID == id && idx < len(cm.Messages[i].Attachments) {
				fn(&cm.Messages[i].Attachments[idx])
			}
		}
	})
}

// FetchMedia downloads an attachment in full (once) and returns its path.
func (t *Telegram) FetchMedia(ctx context.Context, convID, msgID string, idx int) (string, error) {
	s := t.live()
	if s == nil {
		return "", errNotConnected
	}
	id, err := strconv.Atoi(msgID)
	if err != nil {
		return "", fmt.Errorf("bad message id %q", msgID)
	}
	// The attachment as the thread shows it: its name decides the file name.
	var att *core.Attachment
	for _, m := range t.env.Store.Messages(convID).Messages {
		if m.ID == msgID && idx >= 0 && idx < len(m.Attachments) {
			a := m.Attachments[idx]
			att = &a
		}
	}
	if att == nil {
		return "", errors.New("that attachment isn't loaded; open the chat first")
	}
	path := filePath(t.env.CacheDir, convID, id, idx, *att)
	// The download outlives this request, so a big file still finishes
	// (and opens on the next click) if the panel gives up waiting.
	err = s.media.once(ctx, path, func() error {
		dctx, cancel := context.WithTimeout(t.lifeCtx(), 30*time.Minute)
		defer cancel()
		return t.withMedia(dctx, s, convID, id, func(m tg.MessageMediaClass) error {
			loc, ok := fullLocation(m)
			if !ok {
				return errors.New("this attachment can't be downloaded")
			}
			return saveTo(dctx, s.api, loc, path)
		})
	})
	if err != nil {
		if errors.Is(err, errStillDownloading) {
			return "", err
		}
		return "", friendly(err)
	}
	t.setAttachment(convID, id, idx, func(a *core.Attachment) { a.Path = path })
	return path, nil
}

// catchUpThumbs queues previews for photos and videos that were cached
// before their preview was fetched (older pages, or an earlier version).
// Only the newest few are worth a request each.
func (t *Telegram) catchUpThumbs(s *liveSession, convID string) {
	const limit = 40
	var ids []int
	for _, m := range t.env.Store.Messages(convID).Messages {
		if len(m.Attachments) == 0 || m.Attachments[0].ThumbPath != "" {
			continue
		}
		if k := m.Attachments[0].Kind; k != "image" && k != "video" {
			continue
		}
		if id, err := strconv.Atoi(m.ID); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) > limit {
		ids = ids[len(ids)-limit:]
	}
	for _, id := range ids {
		t.queueThumb(s, convID, id, s.media.lookup(convID, id))
	}
}
