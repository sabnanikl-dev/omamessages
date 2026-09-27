package core

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStoreUpsertPlaceholder(t *testing.T) {
	s := NewStore(t.TempDir(), nil)
	s.Messages("c")
	now := time.Now().UnixMilli()

	// Echo carrying our tmpID replaces the placeholder in place.
	s.UpsertMessage("c", Message{ID: "tmp_1", TmpID: "tmp_1", Ts: now, FromMe: true, Text: "hi", Status: "sending"})
	s.UpsertMessage("c", Message{ID: "real1", TmpID: "tmp_1", Ts: now + 10, FromMe: true, Text: "hi", Status: "sent"})
	// Echo with the service's own IDs and no tmpID is matched by text and time.
	s.UpsertMessage("c", Message{ID: "tmp_2", TmpID: "tmp_2", Ts: now + 20, FromMe: true, Text: "second", Status: "sending"})
	s.UpsertMessage("c", Message{ID: "real2", Ts: now + 30, FromMe: true, Text: "second", Status: "delivered"})
	// Updating an existing message by ID doesn't duplicate it.
	s.UpsertMessage("c", Message{ID: "real2", Ts: now + 30, FromMe: true, Text: "second", Status: "read"})
	// Same text from the other side is a new message, not a confirmation.
	s.UpsertMessage("c", Message{ID: "in1", Ts: now + 40, Text: "second", Status: "received"})

	got := s.Messages("c").Messages
	var ids, statuses []string
	for _, m := range got {
		ids = append(ids, m.ID)
		statuses = append(statuses, m.Status)
	}
	if want := []string{"real1", "real2", "in1"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v; want %v", ids, want)
	}
	if want := []string{"sent", "read", "received"}; !reflect.DeepEqual(statuses, want) {
		t.Errorf("statuses = %v; want %v", statuses, want)
	}
}

func TestStoreLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, nil)
	s.UpsertConversation(&Conversation{ID: "a", Name: "A", LastTs: 5, Unread: true, Extra: map[string]any{"type": "rcs"}})
	s.UpsertConversation(&Conversation{ID: "b", Name: "B", LastTs: 9})
	s.SetAccount("me@example.com")
	s.SetExtra("phone", "+15550000000")
	s.SetConnect(&ConnectStep{Kind: "emoji", Value: "🦊"})
	s.Flush()

	r := NewStore(dir, nil)
	r.Load()
	got := r.Snapshot()
	if len(got.Conversations) != 2 || got.Conversations[0].ID != "b" || got.Conversations[1].ID != "a" {
		t.Fatalf("conversations = %+v; want b then a", got.Conversations)
	}
	if !got.Conversations[1].Unread || got.Conversations[1].Extra["type"] != "rcs" {
		t.Errorf("conversation a lost fields: %+v", got.Conversations[1])
	}
	if got.Account != "me@example.com" || got.Extra["phone"] != "+15550000000" {
		t.Errorf("account/extra = %q %v; want restored", got.Account, got.Extra)
	}
	// A connect step belongs to the process that was running it.
	if got.Connect != nil || got.Status != StatusDisconnected {
		t.Errorf("status/connect = %q %+v; want disconnected and no step", got.Status, got.Connect)
	}
}

func TestStoreFlushEmptyThread(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, nil)
	s.Messages("c")
	s.FlushMessages("c")
	data, err := os.ReadFile(s.MessagesPath("c"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"messages": []`) {
		t.Errorf("empty thread written as %s; want \"messages\": []", data)
	}
}
