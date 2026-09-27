package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestClassifyFiles(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		data  []byte
		photo bool
		mime  string
	}{
		{"shot.png", pngHeader, true, "image/png"},
		{"photo.JPG", []byte("\xff\xd8\xff\xe0 jpeg"), true, "image/jpeg"},
		{"anim.gif", []byte("GIF89a..."), false, "image/gif"},
		{"notes.md", []byte("# notes\n"), false, "text/markdown"},
		{"no-extension", pngHeader, true, "image/png"}, // sniffed
		{"invoice.pdf", []byte("%PDF-1.7"), false, "application/pdf"},
	}
	for _, c := range cases {
		it, err := classify(writeFile(t, dir, c.name, c.data))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if it.photo != c.photo || it.mime != c.mime || it.name != c.name || it.size != int64(len(c.data)) {
			t.Errorf("%s: photo %v mime %q name %q size %d; want photo %v mime %q", c.name, it.photo, it.mime, it.name, it.size, c.photo, c.mime)
		}
	}
	// A photo over Telegram's photo limit goes as a file.
	big := filepath.Join(dir, "huge.jpg")
	f, _ := os.Create(big)
	f.Write([]byte("\xff\xd8\xff"))
	f.Truncate(maxPhotoBytes + 1)
	f.Close()
	if it, err := classify(big); err != nil || it.photo {
		t.Errorf("11 MB jpeg: photo=%v err=%v; want sent as a file", it.photo, err)
	}
	for _, bad := range []string{filepath.Join(dir, "missing.png"), dir, writeFile(t, dir, "empty.txt", nil)} {
		if _, err := classify(bad); err == nil {
			t.Errorf("classify(%s) succeeded; want an error", filepath.Base(bad))
		}
	}
}

func TestSendPlan(t *testing.T) {
	p := func(n string) sendItem { return sendItem{name: n, photo: true} }
	d := func(n string) sendItem { return sendItem{name: n} }
	names := func(bs []sendBatch) string {
		var out []string
		for _, b := range bs {
			var ns []string
			for _, it := range b.items {
				ns = append(ns, it.name)
			}
			out = append(out, "["+strings.Join(ns, " ")+"]")
		}
		return strings.Join(out, "")
	}

	// A doc, then two photos: the doc alone (with the caption), the photos as one album.
	plan := planSend([]sendItem{d("a.pdf"), p("1"), p("2")}, "hello")
	if got := names(plan); got != "[a.pdf][1 2]" {
		t.Errorf("plan = %s", got)
	}
	if plan[0].caption != "hello" || plan[1].caption != "" {
		t.Errorf("captions = %q, %q; want the caption on the first batch only", plan[0].caption, plan[1].caption)
	}
	// Photos split by a file don't join the same album.
	if got := names(planSend([]sendItem{p("1"), d("x"), p("2")}, "")); got != "[1][x][2]" {
		t.Errorf("interrupted run = %s", got)
	}
	// Albums hold at most 10.
	var many []sendItem
	for i := 0; i < 12; i++ {
		many = append(many, p(string(rune('a'+i))))
	}
	plan = planSend(many, "c")
	if len(plan) != 2 || len(plan[0].items) != 10 || len(plan[1].items) != 2 || plan[0].caption != "c" {
		t.Errorf("12 photos = %s", names(plan))
	}
	// Documents never group.
	if got := names(planSend([]sendItem{d("x"), d("y")}, "")); got != "[x][y]" {
		t.Errorf("two docs = %s", got)
	}
	if len(planSend(nil, "text")) != 0 {
		t.Errorf("no files should plan nothing")
	}
}

// Received albums arrive as separate messages sharing a grouped_id; each
// keeps its own photo, and only the captioned one carries text.
func TestConvertMessageAlbum(t *testing.T) {
	ents := entities(testEntities())
	var got []string
	for i, caption := range []string{"from the lake", "", ""} {
		m := msg(100+i, &tg.PeerUser{UserID: 10}, nil, false, caption, 1790000000)
		m.SetGroupedID(555)
		m.Media = photoMedia()
		c := convertMessage(m, ents, 0)
		if len(c.Attachments) != 1 || c.Attachments[0].Kind != "image" {
			t.Errorf("album part %d attachments = %+v", i, c.Attachments)
		}
		got = append(got, c.ID+":"+c.Text)
	}
	if strings.Join(got, "|") != "100:from the lake|101:|102:" {
		t.Errorf("album = %v", got)
	}
}
