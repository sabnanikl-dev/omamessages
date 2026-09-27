package fake

import (
	"context"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"omarchy-omamessages/core"
)

func newTestFake(t *testing.T, dir string) (*Fake, *core.Store) {
	t.Helper()
	store := core.NewStore(dir, nil)
	f := Named("fake", "Fake")(core.Env{Store: store, Dir: dir, Log: zerolog.Nop(), Notify: func(core.Notification) {}}).(*Fake)
	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Stop)
	return f, store
}

func step(s *core.Store) *core.ConnectStep { return s.Snapshot().Connect }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestConnectInputSequence(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	f, s := newTestFake(t, dir)
	if err := f.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st != core.StatusDisconnected || len(s.Snapshot().Conversations) != 0 {
		t.Fatalf("after Disconnect: status %q, %d conversations", st, len(s.Snapshot().Conversations))
	}
	// Signed out survives a restart.
	f2, s2 := newTestFake(t, dir)
	if s2.Status() != core.StatusDisconnected {
		t.Fatalf("restart after Disconnect: status %q; want disconnected", s2.Status())
	}

	if err := f2.Connect(ctx, "input"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value, wantField string
		wantErr          bool
	}{
		{"12", "phone", true},
		{"+15550102030", "code", false},
		{"000000", "code", true},
		{"123456", "password", false},
	} {
		err := f2.ConnectInput(ctx, tc.value)
		if (err != nil) != tc.wantErr {
			t.Errorf("ConnectInput(%q) err = %v; wantErr %v", tc.value, err, tc.wantErr)
		}
		st := step(s2)
		if st == nil || st.Kind != "input" || st.Field != tc.wantField {
			t.Fatalf("after %q: step = %+v; want input:%s", tc.value, st, tc.wantField)
		}
		if s2.Status() != core.StatusPairing {
			t.Errorf("status during flow = %q; want pairing", s2.Status())
		}
	}
	if !step(s2).Secret {
		t.Errorf("password step is not marked secret")
	}
	if err := f2.ConnectInput(ctx, "hunter2"); err != nil {
		t.Fatal(err)
	}
	if s2.Status() != core.StatusConnected || step(s2) != nil || len(s2.Snapshot().Conversations) == 0 {
		t.Errorf("after password: status %q step %+v convs %d; want connected, no step, seeded", s2.Status(), step(s2), len(s2.Snapshot().Conversations))
	}
	if _, err := os.Stat(dir + "/" + signedOutFile); !os.IsNotExist(err) {
		t.Errorf("signed-out marker still there after connecting")
	}
}

func TestConnectCancelAtEveryStep(t *testing.T) {
	ctx := context.Background()
	defer func(a, b, c time.Duration) { signInDelay, autoConnect, qrRefreshEach = a, b, c }(signInDelay, autoConnect, qrRefreshEach)
	signInDelay, autoConnect, qrRefreshEach = 10*time.Millisecond, 60*time.Millisecond, 30*time.Millisecond
	for _, method := range []string{"emoji", "qr", "code", "input"} {
		f, s := newTestFake(t, t.TempDir())
		f.Disconnect(ctx)
		if err := f.Connect(ctx, method); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		waitFor(t, method+" step", func() bool { st := step(s); return st != nil && st.Kind != "waiting" })
		st := step(s)
		if len(st.Alts) != 3 {
			t.Errorf("%s: %d alternatives; want the other 3 methods", method, len(st.Alts))
		}
		if method == "qr" {
			fh, err := os.Open(st.QRPath)
			if err != nil {
				t.Fatalf("qr: %v", err)
			}
			if _, err := png.Decode(fh); err != nil {
				t.Errorf("qr.png is not a PNG: %v", err)
			}
			fh.Close()
		}
		f.CancelConnect()
		if s.Status() != core.StatusDisconnected || step(s) != nil {
			t.Errorf("%s: after cancel status %q step %+v; want disconnected, no step", method, s.Status(), step(s))
		}
		// A cancelled flow must not complete (or redraw) later on its own.
		time.Sleep(4 * autoConnect)
		if s.Status() != core.StatusDisconnected || step(s) != nil {
			t.Errorf("%s: after cancel and a wait, status %q step %+v; want still disconnected", method, s.Status(), step(s))
		}
	}
}
