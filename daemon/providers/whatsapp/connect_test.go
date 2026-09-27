package whatsapp

import (
	"context"
	"errors"
	"image/png"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"

	"omarchy-omamessages/core"
)

type fakeLinker struct {
	items    chan whatsmeow.QRChannelItem
	mu       sync.Mutex
	phones   []string
	stopped  bool
	failPair bool
}

func (l *fakeLinker) Start(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return l.items, nil
}
func (l *fakeLinker) PairPhone(ctx context.Context, phone string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.phones = append(l.phones, phone)
	if l.failPair {
		return "", errors.New("bad number")
	}
	return "ABCD-EFGH", nil
}
func (l *fakeLinker) Stop() { l.mu.Lock(); l.stopped = true; l.mu.Unlock() }

func newLink(t *testing.T) (*linkFlow, *fakeLinker, *core.Store) {
	t.Helper()
	// Not t.TempDir: a flow that just failed may still be writing its status
	// file when the test ends, which t.TempDir's cleanup reports as an error.
	dir, err := os.MkdirTemp("", "walink")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 50 && os.RemoveAll(dir) != nil; i++ {
			time.Sleep(10 * time.Millisecond)
		}
	})
	s := core.NewStore(dir, nil)
	l := &fakeLinker{items: make(chan whatsmeow.QRChannelItem)}
	return &linkFlow{store: s, dir: dir, link: l, spawn: func(fn func()) { go fn() }}, l, s
}

func stepIs(s *core.Store, kind, field string) bool {
	st := s.Snapshot().Connect
	return st != nil && st.Kind == kind && st.Field == field
}

func waitStep(t *testing.T, s *core.Store, kind, field string) *core.ConnectStep {
	t.Helper()
	for i := 0; i < 200; i++ {
		if stepIs(s, kind, field) {
			return s.Snapshot().Connect
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s:%s step; have %+v (status %q)", kind, field, s.Snapshot().Connect, s.Status())
	return nil
}

func code(c string) whatsmeow.QRChannelItem {
	return whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: c, Timeout: 20 * time.Second}
}

func TestLinkQRThenSuccess(t *testing.T) {
	f, l, s := newLink(t)
	if err := f.begin(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	l.items <- code("2@first,code")
	st := waitStep(t, s, "qr", "")
	if len(st.Alts) != 1 || st.Alts[0].Method != "phone" {
		t.Errorf("qr alternatives = %v; want phone number", st.Alts)
	}
	if d := time.Until(time.UnixMilli(st.QRExpires)); d < 15*time.Second || d > 21*time.Second {
		t.Errorf("qr expires in %v; want about the channel's 20 s", d)
	}
	fh, err := os.Open(st.QRPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(fh); err != nil {
		t.Errorf("qr.png: %v", err)
	}
	fh.Close()
	l.items <- whatsmeow.QRChannelItem{Event: "success"}
	st = waitStep(t, s, "waiting", "")
	if !strings.Contains(st.Prompt, "Linked") {
		t.Errorf("after success: %q", st.Prompt)
	}
	f.done()
	if _, err := os.Stat(f.dir + "/qr.png"); !os.IsNotExist(err) {
		t.Errorf("qr.png left behind")
	}
}

func TestLinkPhoneCodeStaysUp(t *testing.T) {
	f, l, s := newLink(t)
	if err := f.begin(context.Background(), "phone"); err != nil {
		t.Fatal(err)
	}
	waitStep(t, s, "input", "phone")
	if err := f.input(context.Background(), "12"); err == nil {
		t.Errorf("short number accepted")
	}
	if err := f.input(context.Background(), "+1 (555) 010-2030"); err != nil {
		t.Fatal(err)
	}
	st := waitStep(t, s, "code", "")
	if st.Value != "ABCD-EFGH" || l.phones[len(l.phones)-1] != "15550102030" {
		t.Errorf("code step %q for %v; want ABCD-EFGH for 15550102030", st.Value, l.phones)
	}
	// The QR channel keeps producing codes; they must not cover the pairing code.
	l.items <- code("2@later,code")
	time.Sleep(20 * time.Millisecond)
	if !stepIs(s, "code", "") {
		t.Errorf("a fresh QR code replaced the pairing code: %+v", s.Snapshot().Connect)
	}
	// Switching back to QR shows the next code.
	f.switchTo("qr")
	l.items <- code("2@next,code")
	waitStep(t, s, "qr", "")
}

func TestLinkCancel(t *testing.T) {
	f, l, s := newLink(t)
	f.begin(context.Background(), "")
	l.items <- code("2@a,b")
	waitStep(t, s, "qr", "")
	f.abort()
	if s.Status() != core.StatusDisconnected || s.Snapshot().Connect != nil {
		t.Errorf("after cancel: %q %+v", s.Status(), s.Snapshot().Connect)
	}
	if !l.stopped {
		t.Errorf("cancel didn't disconnect the unfinished link")
	}
	select {
	case l.items <- code("2@late,code"):
	case <-time.After(20 * time.Millisecond):
	}
	if s.Snapshot().Connect != nil {
		t.Errorf("a QR code after cancel drew a step")
	}
}

func TestLinkFailures(t *testing.T) {
	for _, c := range []struct {
		item whatsmeow.QRChannelItem
		want string
	}{
		{whatsmeow.QRChannelItem{Event: "timeout"}, "expired"},
		{whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventPasskeyRequest}, "passkey"},
		{whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: errors.New("boom")}, "boom"},
		{whatsmeow.QRChannelItem{Event: "err-client-outdated"}, "outdated"},
	} {
		f, l, s := newLink(t)
		f.begin(context.Background(), "")
		l.items <- c.item
		for i := 0; i < 200 && s.Status() != core.StatusDisconnected; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		if snap := s.Snapshot(); snap.Status != core.StatusDisconnected || !strings.Contains(snap.Error, c.want) || snap.Connect != nil {
			t.Errorf("%s: status %q error %q step %+v; want disconnected mentioning %q", c.item.Event, snap.Status, snap.Error, snap.Connect, c.want)
		}
	}
}

func TestPairDigits(t *testing.T) {
	for in, want := range map[string]string{"+1 555 010 2030": "15550102030", "+44 (20) 7946-0958": "442079460958", "4915112345678": "4915112345678"} {
		if got, err := pairDigits(in); err != nil || got != want {
			t.Errorf("pairDigits(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"12345", "+1 555 abc", ""} {
		if _, err := pairDigits(bad); err == nil {
			t.Errorf("pairDigits(%q) accepted", bad)
		}
	}
}
