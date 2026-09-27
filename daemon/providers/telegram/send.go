package telegram

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"omarchy-omamessages/core"
)

// Sending photos and files. Photos (JPEG, PNG, WebP up to 10 MB) go as
// Telegram photos, several in a row as one album (up to 10 each); anything
// else goes as a file. The typed text is the caption of the first message.

const (
	maxPhotoBytes = 10 << 20 // Telegram's limit for a compressed photo
	maxFileBytes  = 2 << 30  // for a file, on a regular account
	maxAlbum      = 10
)

type sendItem struct {
	path, name, mime string
	size             int64
	photo            bool
}

type sendBatch struct {
	items   []sendItem
	caption string
}

// classify checks a file can be sent and decides how.
func classify(path string) (sendItem, error) {
	info, err := os.Stat(path)
	if err != nil {
		return sendItem{}, fmt.Errorf("can't read %s", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return sendItem{}, fmt.Errorf("%s is not a file", filepath.Base(path))
	}
	if info.Size() == 0 {
		return sendItem{}, fmt.Errorf("%s is empty", filepath.Base(path))
	}
	if info.Size() > maxFileBytes {
		return sendItem{}, fmt.Errorf("%s is larger than Telegram's 2 GB limit", filepath.Base(path))
	}
	it := sendItem{path: path, name: filepath.Base(path), size: info.Size()}
	it.mime = mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if it.mime == "" {
		it.mime = sniff(path)
	}
	if i := strings.IndexByte(it.mime, ';'); i >= 0 {
		it.mime = it.mime[:i]
	}
	switch it.mime {
	case "image/jpeg", "image/png", "image/webp":
		it.photo = it.size <= maxPhotoBytes
	}
	return it, nil
}

func sniff(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return http.DetectContentType(buf[:n])
}

// planSend groups files in order: runs of photos become albums of up to 10,
// every other file goes alone. The caption rides on the first batch.
func planSend(items []sendItem, caption string) []sendBatch {
	var out []sendBatch
	for _, it := range items {
		n := len(out)
		if it.photo && n > 0 && out[n-1].items[0].photo && len(out[n-1].items) < maxAlbum {
			out[n-1].items = append(out[n-1].items, it)
			continue
		}
		out = append(out, sendBatch{items: []sendItem{it}})
	}
	if len(out) > 0 {
		out[0].caption = caption
	}
	return out
}

func kindOf(it sendItem) string {
	switch {
	case strings.HasPrefix(it.mime, "image/"):
		return "image"
	case strings.HasPrefix(it.mime, "video/"):
		return "video"
	case strings.HasPrefix(it.mime, "audio/"):
		return "audio"
	}
	return "file"
}

// sendWithFiles shows one "sending" message with every attachment at once
// (photos preview from the local file), then uploads and sends in the
// background: big files take longer than the panel waits for an answer. A
// failure marks that message failed with the reason; on success it goes
// away and the real messages arrive through the update stream.
func (t *Telegram) sendWithFiles(ctx context.Context, s *liveSession, convID string, in tg.InputPeerClass, text string, files []string) error {
	var items []sendItem
	for _, f := range files {
		it, err := classify(f)
		if err != nil {
			return err
		}
		items = append(items, it)
	}
	tmp := "tmp_" + strconv.FormatInt(randomID(), 36)
	now := time.Now().UnixMilli()
	pending := core.Message{ID: tmp, TmpID: tmp, Ts: now, FromMe: true, Sender: "Me", Text: text, Status: "sending"}
	for _, it := range items {
		a := core.Attachment{Name: it.name, Kind: kindOf(it), Size: it.size, Mime: it.mime, Path: it.path}
		if it.photo {
			a.ThumbPath = it.path
		}
		pending.Attachments = append(pending.Attachments, a)
	}
	if t.env.Store.HasMessages(convID) {
		t.env.Store.UpsertMessage(convID, pending)
	}
	if c := t.conversationCopy(convID); c != nil {
		c.LastMessage, c.LastFromMe, c.LastSender, c.LastTs = preview(pending), true, "", now
		t.env.Store.UpsertConversation(c)
		t.env.Store.Flush()
	}

	t.env.Spawn(func() {
		sctx, cancel := context.WithTimeout(t.lifeCtx(), 30*time.Minute)
		defer cancel()
		err := t.sendBatches(sctx, s, in, planSend(items, text))
		if !t.env.Store.HasMessages(convID) {
			return
		}
		if err != nil {
			pending.Status, pending.StatusText = "failed", friendly(err).Error()
			t.env.Store.UpsertMessage(convID, pending)
			return
		}
		t.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
			kept := cm.Messages[:0]
			for _, m := range cm.Messages {
				if m.ID != tmp {
					kept = append(kept, m)
				}
			}
			cm.Messages = kept
		})
	})
	return nil
}

func (t *Telegram) sendBatches(ctx context.Context, s *liveSession, in tg.InputPeerClass, batches []sendBatch) error {
	up := uploader.NewUploader(s.api)
	for _, b := range batches {
		var upd tg.UpdatesClass
		var err error
		if len(b.items) == 1 {
			upd, err = t.sendOne(ctx, s, up, in, b.items[0], b.caption)
		} else {
			upd, err = t.sendAlbum(ctx, s, up, in, b)
		}
		if err != nil {
			return err
		}
		if err := s.gaps.Handle(ctx, upd); err != nil {
			t.env.Log.Debug().Err(err).Msg("Updates manager rejected the send result")
		}
	}
	return nil
}

func (t *Telegram) sendOne(ctx context.Context, s *liveSession, up *uploader.Uploader, in tg.InputPeerClass, it sendItem, caption string) (tg.UpdatesClass, error) {
	file, err := up.FromPath(ctx, it.path)
	if err != nil {
		return nil, fmt.Errorf("uploading %s: %w", it.name, err)
	}
	var media tg.InputMediaClass
	if it.photo {
		media = &tg.InputMediaUploadedPhoto{File: file}
	} else {
		media = &tg.InputMediaUploadedDocument{
			File:       file,
			MimeType:   it.mime,
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: it.name}},
		}
	}
	return s.api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: in, Media: media, Message: caption, RandomID: randomID()})
}

// sendAlbum uploads each photo to Telegram first (an album can only refer
// to media already on the server), then sends them as one group.
func (t *Telegram) sendAlbum(ctx context.Context, s *liveSession, up *uploader.Uploader, in tg.InputPeerClass, b sendBatch) (tg.UpdatesClass, error) {
	var multi []tg.InputSingleMedia
	for i, it := range b.items {
		file, err := up.FromPath(ctx, it.path)
		if err != nil {
			return nil, fmt.Errorf("uploading %s: %w", it.name, err)
		}
		res, err := s.api.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{Peer: in, Media: &tg.InputMediaUploadedPhoto{File: file}})
		if err != nil {
			return nil, fmt.Errorf("uploading %s: %w", it.name, err)
		}
		mp, ok := res.(*tg.MessageMediaPhoto)
		if !ok {
			return nil, errors.New("Telegram didn't take " + it.name + " as a photo")
		}
		photo, ok := mp.Photo.(*tg.Photo)
		if !ok {
			return nil, errors.New("Telegram didn't take " + it.name + " as a photo")
		}
		sm := tg.InputSingleMedia{
			Media:    &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference}},
			RandomID: randomID(),
		}
		if i == 0 {
			sm.Message = b.caption
		}
		multi = append(multi, sm)
	}
	return s.api.MessagesSendMultiMedia(ctx, &tg.MessagesSendMultiMediaRequest{Peer: in, MultiMedia: multi})
}
