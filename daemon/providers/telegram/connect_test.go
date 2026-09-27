package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram/auth"

	"omarchy-omamessages/core"
)

// fakeAuth plays Telegram's side of signing in.
type fakeAuth struct {
	mu        sync.Mutex
	twoFA     bool
	calls     []string
	qrScanned chan struct{}
}

func (a *fakeAuth) note(c string) { a.mu.Lock(); a.calls = append(a.calls, c); a.mu.Unlock() }

func (a *fakeAuth) SendCode(ctx context.Context, phone string) (string, string, error) {
	a.note("SendCode " + phone)
	return "hash1", "your Telegram app", nil
}
func (a *fakeAuth) SignIn(ctx context.Context, phone, code, hash string) error {
	a.note("SignIn " + phone + " " + code + " " + hash)
	if code != "12345" {
		return errors.New("wrong code, try again")
	}
	if a.twoFA {
		return auth.ErrPasswordAuthNeeded
	}
	return nil
}
func (a *fakeAuth) Password(ctx context.Context, pw string) error {
	a.note("Password")
	if pw != "hunter2" {
		return errors.New("wrong password, try again")
	}
	return nil
}
func (a *fakeAuth) PasswordHint(ctx context.Context) string { return "cat's name" }
func (a *fakeAuth) QR(ctx context.Context, show func(string, time.Time) error) error {
	if err := show("tg://login?token=abc", time.Now().Add(30*time.Second)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.qrScanned:
	}
	if a.twoFA {
		return auth.ErrPasswordAuthNeeded
	}
	return nil
}
func (a *fakeAuth) Self(ctx context.Context) (string, error) { return "+15550102030 · @ada", nil }

func newFlow(t *testing.T, a *fakeAuth) (*connectFlow, *core.Store, *[]appCreds) {
	t.Helper()
	dir := t.TempDir()
	store := core.NewStore(dir, nil)
	var started []appCreds
	fl := &connectFlow{
		store: store,
		dir:   dir,
		start: func(c appCreds) (authAPI, error) { started = append(started, c); return a, nil },
		spawn: func(fn func()) { go fn() },
	}
	return fl, store, &started
}

func wantStep(t *testing.T, s *core.Store, kind, field string) *core.ConnectStep {
	t.Helper()
	st := s.Snapshot().Connect
	if st == nil || st.Kind != kind || st.Field != field {
		t.Fatalf("step = %+v; want %s:%s", st, kind, field)
	}
	if s.Status() != core.StatusPairing {
		t.Fatalf("status = %q during the flow; want pairing", s.Status())
	}
	return st
}

func TestConnectStepSequence(t *testing.T) {
	ctx := context.Background()
	const hash = "0123456789abcdef0123456789abcdef"
	a := &fakeAuth{twoFA: true}
	fl, s, started := newFlow(t, a)

	fl.begin(ctx, "", nil, nil)
	wantStep(t, s, "input", "api_credentials")
	if err := fl.input(ctx, "not-a-number "+hash); err == nil {
		t.Errorf("bad api_id accepted")
	}
	wantStep(t, s, "input", "api_credentials")
	if err := fl.input(ctx, "1234567 "+hash); err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if len(*started) != 1 || (*started)[0] != (appCreds{ID: 1234567, Hash: hash}) {
		t.Errorf("client started with %v; want the entered credentials", *started)
	}
	if c, ok, err := loadCreds(func(string) string { return "" }, fl.dir); !ok || err != nil || c.ID != 1234567 {
		t.Errorf("credentials not saved to app.json: %v %v %v", c, ok, err)
	}
	if info, err := os.Stat(filepath.Join(fl.dir, "app.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("app.json mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	st := wantStep(t, s, "input", "phone")
	if len(st.Alts) != 1 || st.Alts[0].Method != "qr" {
		t.Errorf("phone step alternatives = %v; want QR", st.Alts)
	}
	if err := fl.input(ctx, "12"); err == nil {
		t.Errorf("too-short phone accepted")
	}
	if err := fl.input(ctx, "+1 (555) 010-2030"); err != nil {
		t.Fatal(err)
	}
	if st := wantStep(t, s, "input", "code"); !strings.Contains(st.Hint, "Telegram app") {
		t.Errorf("code hint = %q; want where the code went", st.Hint)
	}
	if err := fl.input(ctx, "99999"); err == nil {
		t.Errorf("wrong code accepted")
	}
	wantStep(t, s, "input", "code")
	if err := fl.input(ctx, "12 345"); err != nil {
		t.Fatal(err)
	}
	if st := wantStep(t, s, "input", "password"); !st.Secret || !strings.Contains(st.Hint, "cat's name") {
		t.Errorf("password step = %+v; want secret with the hint", st)
	}
	if err := fl.input(ctx, "nope"); err == nil {
		t.Errorf("wrong password accepted")
	}
	if err := fl.input(ctx, "hunter2"); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if snap.Status != core.StatusConnected || snap.Connect != nil || snap.Account != "+15550102030 · @ada" {
		t.Errorf("after password: %q %+v %q; want connected, no step, account", snap.Status, snap.Connect, snap.Account)
	}
	want := []string{"SendCode +15550102030", "SignIn +15550102030 99999 hash1", "SignIn +15550102030 12345 hash1", "Password", "Password"}
	if strings.Join(a.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v; want %v", a.calls, want)
	}
}

func TestConnectWithoutTwoFA(t *testing.T) {
	ctx := context.Background()
	a := &fakeAuth{}
	fl, s, _ := newFlow(t, a)
	fl.begin(ctx, "", a, nil)
	fl.input(ctx, "+15550102030")
	if err := fl.input(ctx, "12345"); err != nil {
		t.Fatal(err)
	}
	if s.Status() != core.StatusConnected {
		t.Errorf("status = %q; want connected straight after the code", s.Status())
	}
}

func TestConnectQRThenPassword(t *testing.T) {
	ctx := context.Background()
	a := &fakeAuth{twoFA: true, qrScanned: make(chan struct{})}
	fl, s, _ := newFlow(t, a)
	fl.begin(ctx, "qr", a, nil)
	waitStep(t, s, "qr")
	if st := s.Snapshot().Connect; st.QRPath == "" || st.QRExpires == 0 {
		t.Fatalf("qr step = %+v; want an image path and expiry", st)
	}
	if _, err := os.Stat(s.Snapshot().Connect.QRPath); err != nil {
		t.Fatalf("qr image: %v", err)
	}
	close(a.qrScanned)
	waitStep(t, s, "input")
	wantStep(t, s, "input", "password")
	if err := fl.input(ctx, "hunter2"); err != nil {
		t.Fatal(err)
	}
	if s.Status() != core.StatusConnected {
		t.Errorf("status = %q; want connected", s.Status())
	}
	if _, err := os.Stat(filepath.Join(fl.dir, "qr.png")); !os.IsNotExist(err) {
		t.Errorf("qr.png left behind after signing in")
	}
}

func waitStep(t *testing.T, s *core.Store, kind string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if st := s.Snapshot().Connect; st != nil && st.Kind == kind {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s step; have %+v", kind, s.Snapshot().Connect)
}

func TestCancelAtEveryStep(t *testing.T) {
	ctx := context.Background()
	for _, stop := range []string{"api_credentials", "phone", "code", "password", "qr"} {
		// Without 2FA a late QR scan would go straight to signed in.
		a := &fakeAuth{twoFA: stop != "qr", qrScanned: make(chan struct{})}
		fl, s, _ := newFlow(t, a)
		if stop == "qr" {
			fl.begin(ctx, "qr", a, nil)
			waitStep(t, s, "qr")
		} else {
			fl.begin(ctx, "", nil, nil)
			answers := []string{"1234567 0123456789abcdef0123456789abcdef", "+15550102030", "12345"}
			for _, field := range []string{"api_credentials", "phone", "code"} {
				if field == stop {
					break
				}
				if err := fl.input(ctx, answers[0]); err != nil {
					t.Fatalf("%s: %v", stop, err)
				}
				answers = answers[1:]
			}
			wantStep(t, s, "input", stop)
		}
		fl.abort()
		if s.Status() != core.StatusDisconnected || s.Snapshot().Connect != nil {
			t.Errorf("cancel at %s: status %q step %+v; want disconnected, no step", stop, s.Status(), s.Snapshot().Connect)
		}
		if stop == "qr" {
			close(a.qrScanned) // a scan after cancelling must not sign in
			time.Sleep(20 * time.Millisecond)
			if s.Status() != core.StatusDisconnected {
				t.Errorf("QR scanned after cancel signed in anyway")
			}
		}
	}
}

func TestCredsFromEnvOverFile(t *testing.T) {
	dir := t.TempDir()
	const fileHash = "ffffffffffffffffffffffffffffffff"
	const envHash = "0123456789abcdef0123456789abcdef"
	if err := saveCreds(dir, appCreds{ID: 111, Hash: fileHash}); err != nil {
		t.Fatal(err)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	c, ok, err := loadCreds(env(nil), dir)
	if !ok || err != nil || c.ID != 111 {
		t.Errorf("file only: %v %v %v; want the file's", c, ok, err)
	}
	c, ok, err = loadCreds(env(map[string]string{envAPIID: " 222 ", envAPIHash: envHash}), dir)
	if !ok || err != nil || c.ID != 222 || c.Hash != envHash {
		t.Errorf("env and file: %v %v %v; want the env's", c, ok, err)
	}
	_, ok, err = loadCreds(env(map[string]string{envAPIID: "abc", envAPIHash: envHash}), dir)
	if ok || err == nil || !strings.Contains(err.Error(), envAPIID) {
		t.Errorf("malformed env id: ok=%v err=%v; want an error naming %s", ok, err, envAPIID)
	}
	_, ok, err = loadCreds(env(map[string]string{envAPIID: "222"}), dir)
	if ok || err == nil {
		t.Errorf("env id without hash: ok=%v err=%v; want an error", ok, err)
	}
	if _, ok, err := loadCreds(env(nil), t.TempDir()); ok || err != nil {
		t.Errorf("nothing anywhere: ok=%v err=%v; want not ok and no error", ok, err)
	}

	// A malformed env value shows up on the credentials step, not as a crash.
	fl, s, _ := newFlow(t, &fakeAuth{})
	_, _, credsErr := loadCreds(env(map[string]string{envAPIID: "abc", envAPIHash: envHash}), dir)
	fl.begin(context.Background(), "", nil, credsErr)
	if st := wantStep(t, s, "input", "api_credentials"); !strings.Contains(st.Prompt, envAPIID) {
		t.Errorf("credentials prompt = %q; want it to explain the bad %s", st.Prompt, envAPIID)
	}
}
