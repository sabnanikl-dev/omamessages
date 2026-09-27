package whatsapp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"omarchy-omamessages/core"
)

func TestDownloadableKinds(t *testing.T) {
	yes := map[string]*waE2E.Message{
		"image":    {ImageMessage: &waE2E.ImageMessage{}},
		"video":    {VideoMessage: &waE2E.VideoMessage{}},
		"voice":    {AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}},
		"document": {DocumentMessage: &waE2E.DocumentMessage{}},
		"sticker":  {StickerMessage: &waE2E.StickerMessage{}},
	}
	for name, m := range yes {
		if _, ok := downloadable(m); !ok {
			t.Errorf("%s not downloadable", name)
		}
	}
	no := map[string]*waE2E.Message{
		"text":     {Conversation: proto.String("hi")},
		"contact":  {ContactMessage: &waE2E.ContactMessage{}},
		"location": {LocationMessage: &waE2E.LocationMessage{}},
	}
	for name, m := range no {
		if _, ok := downloadable(m); ok {
			t.Errorf("%s taken as downloadable", name)
		}
	}
}

func TestMediaKeyRoundTrip(t *testing.T) {
	w := &WhatsApp{env: core.Env{CacheDir: t.TempDir()}}
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		DirectPath: proto.String("/v/t62.7118-24/abc"), MediaKey: []byte{1, 2, 3}, FileSHA256: []byte{4}, FileEncSHA256: []byte{5},
	}}
	w.saveMediaKey("15551112222@s.whatsapp.net", "3EB0ABC", img)
	w.saveMediaKey("15551112222@s.whatsapp.net", "TEXT1", &waE2E.Message{Conversation: proto.String("hi")})

	if _, err := os.Stat(w.keyPath("15551112222@s.whatsapp.net", "TEXT1")); !os.IsNotExist(err) {
		t.Errorf("a key file was written for a text message")
	}
	info, err := os.Stat(w.keyPath("15551112222@s.whatsapp.net", "3EB0ABC"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v; want 0600", info, err)
	}
	d, err := w.loadMediaKey("15551112222@s.whatsapp.net", "3EB0ABC")
	if err != nil {
		t.Fatal(err)
	}
	if d.GetDirectPath() != "/v/t62.7118-24/abc" || !bytes.Equal(d.GetMediaKey(), []byte{1, 2, 3}) {
		t.Errorf("reloaded key = %q %v", d.GetDirectPath(), d.GetMediaKey())
	}
	if _, err := w.loadMediaKey("15551112222@s.whatsapp.net", "NEVER-SEEN"); err == nil || !strings.Contains(err.Error(), "phone") {
		t.Errorf("missing key: err = %v; want a hint to open it on the phone", err)
	}
}

func TestFileNames(t *testing.T) {
	w := &WhatsApp{env: core.Env{CacheDir: "/c"}}
	cases := []struct {
		a    core.Attachment
		want string
	}{
		{core.Attachment{Name: "Photo", Kind: "image", Mime: "image/jpeg"}, "photo-3EB0.jpg"},
		{core.Attachment{Name: "Video", Kind: "video", Mime: "video/mp4"}, "video-3EB0.mp4"},
		{core.Attachment{Name: "Voice message (0:12)", Kind: "audio", Mime: "audio/ogg; codecs=opus"}, "audio-3EB0.ogg"},
		{core.Attachment{Name: "invoice.pdf", Kind: "file", Mime: "application/pdf"}, "invoice.pdf"},
		{core.Attachment{Name: "notes", Kind: "file", Mime: "text/plain"}, "notes.txt"},
		{core.Attachment{Name: "../../evil.sh", Kind: "file"}, ".._.._evil.sh"},
	}
	for _, c := range cases {
		if got := fileName(c.a, "3EB0"); got != c.want {
			t.Errorf("fileName(%+v) = %q; want %q", c.a, got, c.want)
		}
		p := w.filePath("123@g.us", "3EB0", 0, c.a)
		if filepath.Dir(p) != filepath.Join("/c", "media", "123_g_us") {
			t.Errorf("%q lands outside the chat's folder", p)
		}
	}
}

func TestRenameOnlyPlaceholders(t *testing.T) {
	store := core.NewStore(t.TempDir(), nil)
	w := &WhatsApp{env: core.Env{Store: store}}
	num := "15557776666@s.whatsapp.net"
	saved := "15551112222@s.whatsapp.net"
	store.UpsertConversation(&core.Conversation{ID: num, Name: "+15557776666", Participants: []core.Participant{{ID: num, Name: "+15557776666"}}})
	store.UpsertConversation(&core.Conversation{ID: saved, Name: "Mom"})

	w.rename(num, "Priya", false) // a profile name fills in a bare number
	if c := store.Conversation(num); c.Name != "Priya" || c.Participants[0].Name != "Priya" {
		t.Errorf("placeholder chat = %q / %+v; want renamed", c.Name, c.Participants)
	}
	w.rename(saved, "Mary S.", false) // ...but never a name you saved
	if c := store.Conversation(saved); c.Name != "Mom" {
		t.Errorf("saved name replaced by a profile name: %q", c.Name)
	}
	w.rename(saved, "Mum", true) // a contact edit or group rename does
	if c := store.Conversation(saved); c.Name != "Mum" {
		t.Errorf("forced rename = %q; want Mum", c.Name)
	}
	w.rename("0000@s.whatsapp.net", "Nobody", true) // unknown chats aren't created
	if store.Conversation("0000@s.whatsapp.net") != nil {
		t.Errorf("rename created a chat")
	}
}
