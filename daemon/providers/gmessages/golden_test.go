package gmessages

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"omarchy-omamessages/core"
)

// testdata/convert.golden.json is what the old gmessagesd produced for the
// shared fixture, captured from a scratch copy before its code moved here. The
// one intended difference: SMS/RCS moved from a top-level "type" into
// extra.type, so it is moved back before comparing.
func TestConvertMessage(t *testing.T) {
	var got struct {
		Conversations []*core.Conversation `json:"conversations"`
		Messages      []core.Message       `json:"messages"`
	}
	convs := fixtureConversations()
	converted := make([]*core.Conversation, len(convs))
	for i, c := range convs {
		converted[i] = convertConversation(c)
		got.Conversations = append(got.Conversations, converted[i])
	}
	for _, fm := range fixtureMessages() {
		var conv *core.Conversation
		if fm.Conv >= 0 {
			conv = converted[fm.Conv]
		}
		got.Messages = append(got.Messages, convertMessage(conv, fm.Msg))
	}

	gotDoc := roundTrip(t, got)
	for _, c := range gotDoc["conversations"].([]any) {
		conv := c.(map[string]any)
		extra, _ := conv["extra"].(map[string]any)
		if extra == nil || extra["type"] == nil {
			t.Fatalf("conversation %v has no extra.type", conv["id"])
		}
		conv["type"] = extra["type"]
		delete(conv, "extra")
	}

	data, err := os.ReadFile("testdata/convert.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"conversations", "messages"} {
		g, w := gotDoc[key].([]any), want[key].([]any)
		if len(g) != len(w) {
			t.Fatalf("%s: got %d, golden has %d", key, len(g), len(w))
		}
		for i := range w {
			if !reflect.DeepEqual(g[i], w[i]) {
				gj, _ := json.MarshalIndent(g[i], "", "  ")
				wj, _ := json.MarshalIndent(w[i], "", "  ")
				t.Errorf("%s[%d] differs from golden\ngot:  %s\nwant: %s", key, i, gj, wj)
			}
		}
	}
}

func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
