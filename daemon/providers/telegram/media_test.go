package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"omarchy-omamessages/core"
)

func photoMedia() *tg.MessageMediaPhoto {
	return &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 1, AccessHash: 2, FileReference: []byte{3}, Sizes: []tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i"},
		&tg.PhotoSize{Type: "s", W: 90, H: 60, Size: 900},
		&tg.PhotoSize{Type: "m", W: 320, H: 213, Size: 9000},
		&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 853, Sizes: []int{100, 200, 150000}},
	}}}
}

func docMedia(size int64, mimeType string, attrs ...tg.DocumentAttributeClass) *tg.MessageMediaDocument {
	d := &tg.Document{ID: 5, AccessHash: 6, FileReference: []byte{7}, Size: size, MimeType: mimeType, Attributes: attrs}
	return &tg.MessageMediaDocument{Document: d}
}

func TestConvertMessagePhoto(t *testing.T) {
	ents := entities(testEntities())
	m := msg(42, &tg.PeerUser{UserID: 10}, nil, false, "the new desk setup", 1790000000)
	m.Media = photoMedia()
	got := convertMessage(m, ents, 0)
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	a := got.Attachments[0]
	if a.Kind != "image" || a.Width != 320 || a.Height != 213 || a.Mime != "image/jpeg" {
		t.Errorf("photo attachment = %+v; want an image with the largest plain size's dimensions", a)
	}
	if got.Text != "the new desk setup" {
		t.Errorf("caption lost: %q", got.Text)
	}

	// The preview is the "m" size; the full download is the largest.
	if loc, ok := thumbLocation(m.Media); !ok || loc.(*tg.InputPhotoFileLocation).ThumbSize != "m" {
		t.Errorf("thumb location = %+v, %v; want size m", loc, ok)
	}
	if loc, ok := fullLocation(m.Media); !ok || loc.(*tg.InputPhotoFileLocation).ThumbSize != "y" {
		t.Errorf("full location = %+v, %v; want the largest size y", loc, ok)
	}

	// A preview already in the cache is picked up without downloading.
	cache := t.TempDir()
	annotate(cache, "10", 42, &got)
	if got.Attachments[0].ThumbPath != "" {
		t.Errorf("thumbPath set with nothing cached")
	}
	p := thumbPath(cache, "10", 42)
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("jpeg"), 0o600)
	annotate(cache, "10", 42, &got)
	if got.Attachments[0].ThumbPath != p {
		t.Errorf("thumbPath = %q; want the cached %q", got.Attachments[0].ThumbPath, p)
	}
}

func TestConvertMessageDocument(t *testing.T) {
	ents := entities(testEntities())
	cases := []struct {
		media           tg.MessageMediaClass
		kind, name, ext string
		size            int64
	}{
		{docMedia(184000, "application/pdf", &tg.DocumentAttributeFilename{FileName: "invoice-sept.pdf"}), "file", "invoice-sept.pdf", ".pdf", 184000},
		{docMedia(5_000_000, "video/mp4", &tg.DocumentAttributeVideo{W: 640, H: 360}, &tg.DocumentAttributeFilename{FileName: "clip.mp4"}), "video", "clip.mp4", ".mp4", 5_000_000},
		{docMedia(12000, "audio/ogg", &tg.DocumentAttributeAudio{Voice: true}), "audio", "Voice message", ".ogg", 12000},
		{docMedia(3000, "image/webp", &tg.DocumentAttributeSticker{Alt: "😀"}), "image", "Sticker 😀", ".webp", 3000},
		{docMedia(700, "text/plain"), "file", "File", ".txt", 700},
	}
	for _, c := range cases {
		m := msg(7, &tg.PeerUser{UserID: 10}, nil, false, "", 1)
		m.Media = c.media
		got := convertMessage(m, ents, 0)
		if len(got.Attachments) != 1 {
			t.Fatalf("%s: attachments = %+v", c.name, got.Attachments)
		}
		a := got.Attachments[0]
		if a.Kind != c.kind || a.Name != c.name || a.Size != c.size {
			t.Errorf("%s: got kind %q name %q size %d", c.name, a.Kind, a.Name, a.Size)
		}
		if name := fileName(a, 7); !strings.HasSuffix(name, c.ext) {
			t.Errorf("%s: saved as %q; want an extension like %s", c.name, name, c.ext)
		}
		if loc, ok := fullLocation(c.media); !ok || loc.(*tg.InputDocumentFileLocation).ID != 5 {
			t.Errorf("%s: full location = %+v, %v", c.name, loc, ok)
		}
	}
}

func TestMediaPathsAreSafe(t *testing.T) {
	a := core.Attachment{Name: "../../etc/passwd", Kind: "file"}
	p := filePath("/cache", "-1001", 9, 0, a)
	if filepath.Dir(p) != filepath.Join("/cache", "media", "-1001") {
		t.Errorf("file lands in %q; want the chat's own folder", filepath.Dir(p))
	}
	if p1, p2 := filePath("/c", "1", 9, 0, a), filePath("/c", "1", 9, 0, a); p1 != p2 {
		t.Errorf("paths not stable: %q vs %q", p1, p2)
	}
}

func TestDownloadOnce(t *testing.T) {
	ms := newMediaState(func(fn func()) { go fn() })
	path := filepath.Join(t.TempDir(), "f.bin")
	var calls atomic.Int32
	release := make(chan struct{})
	fetch := func() error {
		calls.Add(1)
		<-release
		return os.WriteFile(path, []byte("data"), 0o600)
	}
	// Two opens at once share one download.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ms.once(context.Background(), path, fetch); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	// Opening again finds the file.
	if err := ms.once(context.Background(), path, func() error { calls.Add(1); return nil }); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("downloads = %d; want 1", n)
	}

	// A caller that gives up gets "still downloading"; the download goes on.
	slow := filepath.Join(t.TempDir(), "slow.bin")
	finish := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := ms.once(ctx, slow, func() error { <-finish; return os.WriteFile(slow, []byte("x"), 0o600) })
	if !errors.Is(err, errStillDownloading) {
		t.Errorf("gave-up caller: err = %v; want errStillDownloading", err)
	}
	close(finish)
	if err := ms.once(context.Background(), slow, func() error { t.Error("downloaded twice"); return nil }); err != nil {
		t.Errorf("second open: %v", err)
	}
}
