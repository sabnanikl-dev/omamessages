package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func testEntities() (users []tg.UserClass, chats []tg.ChatClass) {
	users = []tg.UserClass{
		&tg.User{ID: 10, FirstName: "Ada", LastName: "Lovelace", Username: "ada", Phone: "15551112222"},
		&tg.User{ID: 11, FirstName: "", Username: "grace"},
		&tg.User{ID: 12, Deleted: true},
	}
	chats = []tg.ChatClass{
		&tg.Chat{ID: 20, Title: "Book club"},
		&tg.Channel{ID: 30, Title: "Dev chat", Megagroup: true},
		&tg.Channel{ID: 31, Title: "Omarchy news", Broadcast: true, Username: "omarchy"},
	}
	return
}

func msg(id int, p tg.PeerClass, from tg.PeerClass, out bool, text string, date int) *tg.Message {
	m := &tg.Message{ID: id, PeerID: p, Out: out, Message: text, Date: date}
	if from != nil {
		m.SetFromID(from)
	}
	return m
}

func TestPeerIDs(t *testing.T) {
	for _, c := range []struct {
		p    tg.PeerClass
		want string
	}{
		{&tg.PeerUser{UserID: 10}, "10"},
		{&tg.PeerChat{ChatID: 20}, "-20"},
		{&tg.PeerChannel{ChannelID: 30}, "-1000000000030"},
	} {
		if got := nativeID(c.p); got != c.want {
			t.Errorf("nativeID(%T) = %q; want %q", c.p, got, c.want)
		}
	}
}

func TestConvertMessageText(t *testing.T) {
	ents := entities(testEntities())

	// Incoming, private chat: no from_id, the sender is the chat.
	in := convertMessage(msg(1, &tg.PeerUser{UserID: 10}, nil, false, "hi there", 1790000000), ents, 0)
	if in.ID != "1" || in.Ts != 1790000000000 || in.FromMe || in.Text != "hi there" || in.Status != "received" {
		t.Errorf("incoming = %+v", in)
	}
	if in.Sender != "Ada Lovelace" || in.SenderID != "10" {
		t.Errorf("incoming sender = %q/%q; want Ada Lovelace/10", in.Sender, in.SenderID)
	}

	// Ours, read by the other side up to message 5.
	out := convertMessage(msg(5, &tg.PeerUser{UserID: 10}, &tg.PeerUser{UserID: 99}, true, "see you", 1790000100), ents, 5)
	if !out.FromMe || out.Sender != "Me" || out.Status != "read" {
		t.Errorf("outgoing read = %+v", out)
	}
	if o := convertMessage(msg(6, &tg.PeerUser{UserID: 10}, nil, true, "later", 1790000200), ents, 5); o.Status != "sent" {
		t.Errorf("outgoing past readOutboxMax: status %q; want sent", o.Status)
	}

	// Group message: sender from from_id; usernames and deleted accounts.
	g := convertMessage(msg(7, &tg.PeerChannel{ChannelID: 30}, &tg.PeerUser{UserID: 11}, false, "merged", 1790000300), ents, 0)
	if g.Sender != "@grace" || g.SenderID != "11" {
		t.Errorf("group sender = %q/%q; want @grace/11", g.Sender, g.SenderID)
	}
	if d := convertMessage(msg(8, &tg.PeerChat{ChatID: 20}, &tg.PeerUser{UserID: 12}, false, "x", 1), ents, 0); d.Sender != "Deleted account" {
		t.Errorf("deleted sender = %q", d.Sender)
	}

	// Reactions: emoji with a count; custom emoji skipped.
	r := msg(9, &tg.PeerUser{UserID: 10}, nil, false, "nice", 1)
	r.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{
		{Reaction: &tg.ReactionEmoji{Emoticon: "❤"}, Count: 2},
		{Reaction: &tg.ReactionCustomEmoji{DocumentID: 1}, Count: 1},
	}})
	if got := convertMessage(r, ents, 0).Reactions; len(got) != 1 || got[0] != "❤" {
		t.Errorf("reactions = %v; want [❤]", got)
	}

	// Media without text still says what it is.
	ph := msg(10, &tg.PeerUser{UserID: 10}, nil, false, "", 1)
	ph.Media = &tg.MessageMediaPhoto{}
	pm := convertMessage(ph, ents, 0)
	if len(pm.Attachments) != 1 || pm.Attachments[0].Kind != "image" || preview(pm) != "Photo" {
		t.Errorf("photo message = %+v, preview %q", pm, preview(pm))
	}
}

func TestConvertDialog(t *testing.T) {
	ents := entities(testEntities())
	muteSettings := tg.PeerNotifySettings{}
	muteSettings.SetMuteUntil(int(time.Now().Add(time.Hour).Unix()))

	cases := []struct {
		d       *tg.Dialog
		top     *tg.Message
		id      string
		name    string
		group   bool
		unread  bool
		sender  string
		archive bool
	}{
		{&tg.Dialog{Peer: &tg.PeerUser{UserID: 10}, UnreadCount: 2, Pinned: true},
			msg(1, &tg.PeerUser{UserID: 10}, nil, false, "hi", 1790000000), "10", "Ada Lovelace", false, true, "", false},
		{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 30}, NotifySettings: muteSettings},
			msg(2, &tg.PeerChannel{ChannelID: 30}, &tg.PeerUser{UserID: 10}, false, "merged, thanks!", 1790000500), "-1000000000030", "Dev chat", true, false, "Ada", false},
		{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 31}, FolderID: 1, UnreadMark: true},
			nil, "-1000000000031", "Omarchy news", false, true, "", true},
		{&tg.Dialog{Peer: &tg.PeerChat{ChatID: 20}},
			msg(3, &tg.PeerChat{ChatID: 20}, &tg.PeerUser{UserID: 99}, true, "see you all", 1790000600), "-20", "Book club", true, false, "", false},
	}
	for _, c := range cases {
		var top tg.MessageClass
		if c.top != nil {
			top = c.top
		}
		got := convertDialog(c.d, top, ents)
		if got.ID != c.id || got.Name != c.name || got.IsGroup != c.group || got.Unread != c.unread || got.LastSender != c.sender || got.Archived != c.archive {
			t.Errorf("dialog %s = id %q name %q group %v unread %v sender %q archived %v", c.id, got.ID, got.Name, got.IsGroup, got.Unread, got.LastSender, got.Archived)
		}
		if c.top != nil && (got.LastMessage != c.top.Message || got.LastTs != int64(c.top.Date)*1000 || got.LastFromMe != c.top.Out) {
			t.Errorf("dialog %s preview = %q %d %v", c.id, got.LastMessage, got.LastTs, got.LastFromMe)
		}
	}
	if d := convertDialog(cases[0].d, tg.MessageClass(cases[0].top), ents); !d.Pinned || len(d.Participants) != 1 || d.Participants[0].Number != "@ada" {
		t.Errorf("user dialog = pinned %v participants %+v; want pinned with @ada", d.Pinned, d.Participants)
	}
	if d := convertDialog(cases[1].d, tg.MessageClass(cases[1].top), ents); !muted(d) {
		t.Errorf("muted dialog not seen as muted: %v", d.Extra)
	}
	if d := convertDialog(cases[0].d, tg.MessageClass(cases[0].top), ents); muted(d) {
		t.Errorf("unmuted dialog seen as muted")
	}
}

func TestConvertDialogServiceTop(t *testing.T) {
	ents := entities(testEntities())
	svc := &tg.MessageService{ID: 4, PeerID: &tg.PeerUser{UserID: 10}, Date: 1790000700, Action: &tg.MessageActionContactSignUp{}}
	d := convertDialog(&tg.Dialog{Peer: &tg.PeerUser{UserID: 10}, TopMessage: 4}, svc, ents)
	if d.LastTs != 1790000700000 || d.LastMessage != "Joined Telegram" {
		t.Errorf("service top: ts %d preview %q; want its date and a label", d.LastTs, d.LastMessage)
	}
}

func TestSentID(t *testing.T) {
	if id := sentID(&tg.UpdateShortSentMessage{ID: 42}, 7); id != 42 {
		t.Errorf("short sent = %d", id)
	}
	u := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateMessageID{ID: 1, RandomID: 6},
		&tg.UpdateMessageID{ID: 43, RandomID: 7},
	}}
	if id := sentID(u, 7); id != 43 {
		t.Errorf("updates = %d; want the one matching our random id", id)
	}
	if id := sentID(u, 8); id != 0 {
		t.Errorf("no match = %d; want 0", id)
	}
}

func TestPreviewTrims(t *testing.T) {
	ents := entities(testEntities())
	if p := preview(convertMessage(msg(1, &tg.PeerUser{UserID: 10}, nil, false, "  hello \n", 1), ents, 0)); strings.TrimSpace(p) != p {
		t.Errorf("preview not trimmed: %q", p)
	}
}
