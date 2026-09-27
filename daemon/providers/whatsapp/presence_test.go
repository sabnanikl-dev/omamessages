package whatsapp

import (
	"sync"
	"testing"
	"time"
)

func TestPresenceGoesOfflineWhenIdle(t *testing.T) {
	var mu sync.Mutex
	var sent []bool
	p := &presence{idle: 40 * time.Millisecond, send: func(on bool) error {
		mu.Lock()
		sent = append(sent, on)
		mu.Unlock()
		return nil
	}}
	log := func() []bool { mu.Lock(); defer mu.Unlock(); return append([]bool(nil), sent...) }

	p.touch()
	time.Sleep(20 * time.Millisecond)
	p.touch() // still active: no second "online", and the countdown restarts
	time.Sleep(30 * time.Millisecond)
	if got := log(); len(got) != 1 || !got[0] {
		t.Fatalf("while active: sent %v; want one online", got)
	}
	time.Sleep(40 * time.Millisecond)
	if got := log(); len(got) != 2 || got[1] {
		t.Fatalf("after idle: sent %v; want online then offline", got)
	}
	p.stop() // already offline: nothing more
	if got := log(); len(got) != 2 {
		t.Errorf("stop while offline sent %v", got)
	}
	p.touch()
	p.stop() // disconnect while online goes offline at once
	if got := log(); len(got) != 4 || got[3] {
		t.Errorf("stop while online: sent %v; want a final offline", got)
	}
}
