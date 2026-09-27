package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"omarchy-omamessages/core"
	"omarchy-omamessages/providers/fake"
)

func testHub(t *testing.T, enabled ...core.ProviderID) *Hub {
	t.Helper()
	factories := map[core.ProviderID]core.Factory{
		"alpha": fake.Named("alpha", "Alpha"),
		"beta":  fake.Named("beta", "Beta"),
		"gamma": fake.Named("gamma", "Gamma"),
	}
	return NewHub(t.TempDir(), t.TempDir(), factories, enabled, false)
}

func seed(t *testing.T, h *Hub, id core.ProviderID, convs ...core.Conversation) {
	t.Helper()
	h.mu.Lock()
	s := h.slots[id]
	h.mu.Unlock()
	if s == nil {
		t.Fatalf("provider %s not enabled", id)
	}
	for i := range convs {
		s.store.UpsertConversation(&convs[i])
	}
}

func TestMergeSortsAndNamespaces(t *testing.T) {
	h := testHub(t, "alpha", "beta")
	seed(t, h, "alpha",
		core.Conversation{ID: "a1", Name: "A1", LastTs: 300, Unread: true},
		core.Conversation{ID: "a2", Name: "A2", LastTs: 100},
	)
	seed(t, h, "beta",
		core.Conversation{ID: "b1", Name: "B1", LastTs: 200, Unread: true},
		core.Conversation{ID: "b:2", Name: "B2", LastTs: 400, Unread: true},
	)

	m := h.Merge()

	var ids []string
	for _, c := range m.Conversations {
		ids = append(ids, c.ID)
		if !strings.HasPrefix(c.ID, string(c.Provider)+":") {
			t.Errorf("conversation %q is not namespaced by its provider %q", c.ID, c.Provider)
		}
	}
	want := "beta:b:2 alpha:a1 beta:b1 alpha:a2"
	if got := strings.Join(ids, " "); got != want {
		t.Errorf("merged order = %q; want %q", got, want)
	}
	if m.UnreadCount != 3 {
		t.Errorf("unreadCount = %d; want 3", m.UnreadCount)
	}
	unread := map[core.ProviderID]int{}
	enabled := map[core.ProviderID]bool{}
	for _, p := range m.Providers {
		unread[p.ID] = p.Unread
		enabled[p.ID] = p.Enabled
	}
	if unread["alpha"] != 1 || unread["beta"] != 2 {
		t.Errorf("per-provider unread = %v; want alpha 1, beta 2", unread)
	}
	if !enabled["alpha"] || !enabled["beta"] || enabled["gamma"] {
		t.Errorf("enabled = %v; want alpha and beta only", enabled)
	}
	if _, listed := enabled["gamma"]; !listed {
		t.Errorf("disabled provider gamma missing from providers[]; the panel needs it to offer Connect")
	}
}

func TestRouteUnknownProvider(t *testing.T) {
	h := testHub(t, "alpha")
	if _, _, err := h.Route("signal:1"); err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Errorf("Route(signal:1) err = %v; want unknown service", err)
	}
	if _, _, err := h.Route("gamma:1"); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("Route(gamma:1) err = %v; want not enabled", err)
	}
	if _, _, err := h.Route("no-colon"); err == nil {
		t.Errorf("Route(no-colon) succeeded; want an error")
	}
	p, native, err := h.Route("alpha:x:y")
	if err != nil || p.ID() != "alpha" || native != "x:y" {
		t.Errorf("Route(alpha:x:y) = %v, %q, %v; want alpha, x:y, nil", p, native, err)
	}
}

// panicky panics in Open; everything else is the fake's.
type panicky struct{ core.Provider }

func (panicky) Open(context.Context, string, bool) error { panic("boom") }

// recorder notes every Send and can claim attachment support.
type recorder struct {
	core.Provider
	attachments bool
	sends       [][]string
}

func (r *recorder) Caps() core.Caps { return core.Caps{Attachments: r.attachments} }
func (r *recorder) Send(_ context.Context, _, _ string, files []string) error {
	r.sends = append(r.sends, files)
	return nil
}

func hubWith(t *testing.T, factories map[core.ProviderID]core.Factory) *Hub {
	t.Helper()
	var ids []core.ProviderID
	for id := range factories {
		ids = append(ids, id)
	}
	return NewHub(t.TempDir(), t.TempDir(), factories, ids, false)
}

func providerStatus(h *Hub, id core.ProviderID) (core.Status, string) {
	for _, p := range h.Merge().Providers {
		if p.ID == id {
			return p.Status, p.Error
		}
	}
	return "", ""
}

func TestProviderPanicIsolated(t *testing.T) {
	h := hubWith(t, map[core.ProviderID]core.Factory{
		"alpha": func(env core.Env) core.Provider { return panicky{fake.Named("alpha", "Alpha")(env)} },
		"beta":  fake.Named("beta", "Beta"),
	})
	ctx := context.Background()
	seed(t, h, "alpha", core.Conversation{ID: "a1"})
	seed(t, h, "beta", core.Conversation{ID: "b1"})

	err := h.DoConv("alpha:a1", func(p core.Provider, native string) error { return p.Open(ctx, native, true) })
	if err == nil || !strings.Contains(err.Error(), "crashed") {
		t.Fatalf("Open on panicking provider: err = %v; want a crashed error", err)
	}
	if st, msg := providerStatus(h, "alpha"); st != core.StatusError || !strings.Contains(msg, "boom") {
		t.Errorf("alpha status = %q %q; want error mentioning the panic", st, msg)
	}
	if err := h.DoConv("beta:b1", func(p core.Provider, native string) error { return p.Open(ctx, native, true) }); err != nil {
		t.Errorf("beta Open after alpha crashed: %v", err)
	}
	if st, _ := providerStatus(h, "beta"); st == core.StatusError {
		t.Errorf("beta status = error; the crash leaked across providers")
	}
}

func TestSpawnPanicIsolated(t *testing.T) {
	var env core.Env
	h := hubWith(t, map[core.ProviderID]core.Factory{
		"alpha": func(e core.Env) core.Provider { env = e; return fake.Named("alpha", "Alpha")(e) },
	})
	done := make(chan struct{})
	env.Spawn(func() {
		defer close(done)
		panic("background boom")
	})
	<-done
	// recoverInto runs after fn's own defers; give it a moment to record.
	for i := 0; i < 100; i++ {
		if st, _ := providerStatus(h, "alpha"); st == core.StatusError {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, msg := providerStatus(h, "alpha")
	t.Errorf("alpha status after a panicking background goroutine = %q %q; want error", st, msg)
}

func TestSendFilesUnsupported(t *testing.T) {
	rec := &recorder{}
	h := hubWith(t, map[core.ProviderID]core.Factory{
		"alpha": func(env core.Env) core.Provider { rec.Provider = fake.Named("alpha", "Alpha")(env); return rec },
	})
	err := h.Send(context.Background(), "alpha:a1", "hi", []string{"/tmp/a.png"})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("Send with files to a provider without attachments: err = %v; want ErrUnsupported", err)
	}
	if len(rec.sends) != 0 {
		t.Errorf("provider Send was called %d times; want 0", len(rec.sends))
	}
	if err := h.Send(context.Background(), "alpha:a1", "hi", []string{}); err != nil || len(rec.sends) != 1 || rec.sends[0] != nil {
		t.Errorf("text-only Send: err = %v, sends = %v; want one call with nil files", err, rec.sends)
	}
}

func TestHandleArgsFilesList(t *testing.T) {
	rec := &recorder{attachments: true}
	h := hubWith(t, map[core.ProviderID]core.Factory{
		"alpha": func(env core.Env) core.Provider { rec.Provider = fake.Named("alpha", "Alpha")(env); return rec },
	})
	var req Request
	line := `{"cmd":"send","args":{"id":"alpha:a1","text":"caption","files":["a","b"]}}`
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatal(err)
	}
	if resp := handle(h, req, func() {}); !resp.OK {
		t.Fatalf("send failed: %s", resp.Error)
	}
	if len(rec.sends) != 1 || !reflect.DeepEqual(rec.sends[0], []string{"a", "b"}) {
		t.Errorf("files reaching Send = %v; want [[a b]]", rec.sends)
	}
}

// typer records typing calls on top of the fake.
type typer struct {
	core.Provider
	calls []string
}

func (t *typer) SetTyping(_ context.Context, convID string, on bool) error {
	t.calls = append(t.calls, convID+"="+map[bool]string{true: "on", false: "off"}[on])
	return nil
}

func TestSetTypingOptional(t *testing.T) {
	ty := &typer{}
	h := hubWith(t, map[core.ProviderID]core.Factory{
		"alpha": func(env core.Env) core.Provider { ty.Provider = fake.Named("alpha", "Alpha")(env); return ty },
		"beta":  fake.Named("beta", "Beta"),
	})
	ctx := context.Background()
	if err := h.SetTyping(ctx, "alpha:c1", true); err != nil {
		t.Fatal(err)
	}
	h.SetTyping(ctx, "alpha:c1", false)
	if strings.Join(ty.calls, " ") != "c1=on c1=off" {
		t.Errorf("typer calls = %v", ty.calls)
	}
	// A provider without typing support is simply skipped.
	if err := h.SetTyping(ctx, "beta:c1", true); err != nil {
		t.Errorf("provider without Typer: %v", err)
	}
	if err := h.SetTyping(ctx, "gamma:c1", true); err == nil {
		t.Errorf("unknown service accepted")
	}
}
