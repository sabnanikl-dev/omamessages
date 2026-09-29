package gmessages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"omarchy-omamessages/core"
)

func TestNoteMediaKeepsKeysAndFindsCachedFiles(t *testing.T) {
	cache := t.TempDir()
	g := New(core.Env{Store: core.NewStore(t.TempDir(), nil), CacheDir: cache, Log: zerolog.Nop()}).(*GMessages)
	raw := &gmproto.Message{MessageID: "m9", MessageInfo: []*gmproto.MessageInfo{
		{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: "look"}}},
		{Data: &gmproto.MessageInfo_MediaContent{MediaContent: &gmproto.MediaContent{
			MediaID: "full-1", DecryptionKey: []byte{1}, ThumbnailMediaID: "thumb-1", ThumbnailDecryptionKey: []byte{2},
			MediaName: "IMG_1.jpg", Format: gmproto.MediaFormats_IMAGE_JPEG, MimeType: "image/jpeg",
			Dimensions: &gmproto.Dimensions{Width: 1280, Height: 960},
		}}},
		{Data: &gmproto.MessageInfo_MediaContent{MediaContent: &gmproto.MediaContent{
			MediaID: "full-2", DecryptionKey: []byte{3}, MediaName: "invoice.pdf", Format: gmproto.MediaFormats_APP_PDF,
		}}},
	}}
	m := convertMessage(nil, raw)
	if len(m.Attachments) != 2 || m.Attachments[0].Mime != "image/jpeg" || m.Attachments[0].Width != 1280 || m.Attachments[0].Height != 960 {
		t.Fatalf("attachments = %+v; want the photo's type and size carried over", m.Attachments)
	}

	g.noteMedia("42", raw, &m)
	for i, want := range []string{"full-1", "full-2"} {
		k, err := g.loadKey("42", "m9", i)
		if err != nil || k.MediaID != want {
			t.Errorf("key %d = %+v, %v; want media %s", i, k, err, want)
		}
		if info, err := os.Stat(g.keyPath("42", "m9", i)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("key file %d mode = %v, %v; want 0600", i, info.Mode().Perm(), err)
		}
	}
	if k, _ := g.loadKey("42", "m9", 0); k.ThumbID != "thumb-1" {
		t.Errorf("thumbnail id not kept: %+v", k)
	}
	if m.Attachments[0].ThumbPath != "" || m.Attachments[1].Path != "" {
		t.Errorf("paths set with nothing cached: %+v", m.Attachments)
	}

	// Files already in the cache are picked up without downloading.
	os.WriteFile(g.thumbPath("42", "m9", 0), []byte("jpeg"), 0o600)
	os.WriteFile(g.filePath("42", "m9", 1, m.Attachments[1]), []byte("%PDF"), 0o600)
	m2 := convertMessage(nil, raw)
	g.noteMedia("42", raw, &m2)
	if m2.Attachments[0].ThumbPath != g.thumbPath("42", "m9", 0) {
		t.Errorf("cached preview not used: %q", m2.Attachments[0].ThumbPath)
	}
	if m2.Attachments[1].Path == "" || filepath.Base(m2.Attachments[1].Path) != "m9-1-invoice.pdf" {
		t.Errorf("cached file not used: %q", m2.Attachments[1].Path)
	}
	if _, err := g.loadKey("42", "never", 0); err == nil {
		t.Errorf("missing key didn't error")
	}
}

func TestGMessagesFileNames(t *testing.T) {
	for _, c := range []struct {
		a    core.Attachment
		want string
	}{
		{core.Attachment{Name: "IMG_1.jpg", Kind: "image"}, "IMG_1.jpg"},
		{core.Attachment{Name: "", Kind: "image", Mime: "image/jpeg"}, "photo-m9.jpg"},
		{core.Attachment{Name: "voice", Kind: "audio", Mime: "audio/amr"}, "voice.amr"},
		{core.Attachment{Name: "../../x", Kind: "file"}, ".._.._x"},
	} {
		if got := fileName(c.a, "m9"); got != c.want {
			t.Errorf("fileName(%+v) = %q; want %q", c.a, got, c.want)
		}
	}
}
