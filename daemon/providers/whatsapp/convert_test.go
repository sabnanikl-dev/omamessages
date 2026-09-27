package whatsapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type fakeNames map[string]string

func (f fakeNames) Name(jid types.JID) string {
	if n, ok := f[jid.String()]; ok {
		return n
	}
	return fallbackName(jid)
}

var (
	ada   = types.NewJID("15551112222", types.DefaultUserServer)
	me    = types.NewJID("15550000000", types.DefaultUserServer)
	group = types.NewJID("120363000000000001", types.GroupServer)
	names = fakeNames{ada.String(): "Ada Lovelace", group.String(): "Book club"}
	t0    = time.Unix(1790000000, 0)
)

func event(id string, chat, sender types.JID, fromMe bool, m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer},
			ID:            id,
			Timestamp:     t0,
		},
		Message: m,
	}
}

func TestConvertEventText(t *testing.T) {
	in, ok := convertEvent(event("A1", ada, ada, false, &waE2E.Message{Conversation: proto.String("Are you coming Sunday?")}), ada, names)
	if !ok || in.convID != ada.String() || in.msg.ID != "A1" || in.msg.Ts != t0.UnixMilli() || in.msg.FromMe || in.msg.Status != "received" {
		t.Errorf("incoming = %+v %v", in, ok)
	}
	if in.msg.Text != "Are you coming Sunday?" || in.msg.Sender != "Ada Lovelace" || in.msg.SenderID != ada.String() {
		t.Errorf("incoming text/sender = %q %q %q", in.msg.Text, in.msg.Sender, in.msg.SenderID)
	}
	// Extended text (links, replies) and our own messages.
	out, ok := convertEvent(event("A2", ada, me, true, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("see https://x.y")}}), ada, names)
	if !ok || !out.msg.FromMe || out.msg.Sender != "Me" || out.msg.Status != "sent" || out.msg.Text != "see https://x.y" {
		t.Errorf("outgoing = %+v", out.msg)
	}
	// Unknown sender: their push name beats a bare number.
	e := event("A3", types.NewJID("15559990000", types.DefaultUserServer), types.NewJID("15559990000", types.DefaultUserServer), false, &waE2E.Message{Conversation: proto.String("hi")})
	e.Info.PushName = "Sam"
	if cv, _ := convertEvent(e, e.Info.Chat, names); cv.msg.Sender != "Sam" {
		t.Errorf("unknown sender = %q; want the push name", cv.msg.Sender)
	}
	// Not messages to show: reactions, status updates, channels, empty.
	skips := []struct {
		name string
		evt  *events.Message
		chat types.JID
	}{
		{"reaction", event("R", ada, ada, false, &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("❤")}}), ada},
		{"status", event("S", types.StatusBroadcastJID, ada, false, &waE2E.Message{Conversation: proto.String("my status")}), types.StatusBroadcastJID},
		{"channel", event("N", types.NewJID("1", types.NewsletterServer), ada, false, &waE2E.Message{Conversation: proto.String("news")}), types.NewJID("1", types.NewsletterServer)},
		{"empty", event("E", ada, ada, false, &waE2E.Message{}), ada},
	}
	for _, sk := range skips {
		if _, ok := convertEvent(sk.evt, sk.chat, names); ok {
			t.Errorf("%s was converted; want skipped", sk.name)
		}
	}
}

func TestConvertEventImage(t *testing.T) {
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("the lake"), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(123456),
		Width: proto.Uint32(1280), Height: proto.Uint32(960), JPEGThumbnail: []byte("\xff\xd8\xffthumb"),
	}}
	cv, ok := convertEvent(event("I1", ada, ada, false, img), ada, names)
	if !ok || len(cv.msg.Attachments) != 1 {
		t.Fatalf("image = %+v %v", cv, ok)
	}
	a := cv.msg.Attachments[0]
	if a.Kind != "image" || a.Size != 123456 || a.Width != 1280 || a.Height != 960 || a.Mime != "image/jpeg" {
		t.Errorf("attachment = %+v", a)
	}
	if cv.msg.Text != "the lake" || string(cv.thumb) != "\xff\xd8\xffthumb" {
		t.Errorf("caption %q, thumb %q", cv.msg.Text, cv.thumb)
	}
	voice := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true), Seconds: proto.Uint32(75)}}
	if cv, _ := convertEvent(event("V", ada, ada, false, voice), ada, names); preview(cv.msg) != "Voice message (1:15)" {
		t.Errorf("voice preview = %q", preview(cv.msg))
	}
	doc := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("invoice.pdf"), FileLength: proto.Uint64(184000), Mimetype: proto.String("application/pdf")}}
	if cv, _ := convertEvent(event("D", ada, ada, false, doc), ada, names); cv.msg.Attachments[0].Name != "invoice.pdf" || cv.msg.Attachments[0].Kind != "file" {
		t.Errorf("document = %+v", cv.msg.Attachments)
	}
}

func TestConvertEventGroup(t *testing.T) {
	cv, ok := convertEvent(event("G1", group, ada, false, &waE2E.Message{Conversation: proto.String("merged, thanks!")}), group, names)
	if !ok || cv.convID != group.String() || cv.msg.Sender != "Ada Lovelace" || cv.msg.SenderID != ada.String() {
		t.Errorf("group message = %+v", cv)
	}
	// Device-specific sender JIDs collapse to the person.
	dev := ada
	dev.Device = 3
	if cv, _ := convertEvent(event("G2", group, dev, false, &waE2E.Message{Conversation: proto.String("x")}), group, names); cv.msg.SenderID != ada.String() || cv.msg.Sender != "Ada Lovelace" {
		t.Errorf("device sender = %q %q", cv.msg.SenderID, cv.msg.Sender)
	}
}

func TestProtocolChanges(t *testing.T) {
	revoke := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("A1")}}}
	if ch, ok := protocol(event("P1", ada, ada, false, revoke)); !ok || !ch.deleted || ch.targetID != "A1" {
		t.Errorf("revoke = %+v %v", ch, ok)
	}
	edit := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("A1")},
		EditedMessage: &waE2E.Message{Conversation: proto.String("fixed typo")}}}
	if ch, ok := protocol(event("P2", ada, ada, false, edit)); !ok || ch.deleted || ch.newText != "fixed typo" {
		t.Errorf("edit = %+v %v", ch, ok)
	}
	if _, ok := protocol(event("P3", ada, ada, false, &waE2E.Message{Conversation: proto.String("plain")})); ok {
		t.Errorf("plain message taken as a protocol change")
	}
}

func TestConvertHistory(t *testing.T) {
	// History arrives newest first; parse is whatsmeow's ParseWebMessage,
	// faked here from the order id.
	hms := []*waHistorySync.HistorySyncMsg{{MsgOrderID: proto.Uint64(3)}, {MsgOrderID: proto.Uint64(2)}, {MsgOrderID: proto.Uint64(1)}, {MsgOrderID: proto.Uint64(99)}}
	parse := func(hm *waHistorySync.HistorySyncMsg) (*events.Message, error) {
		switch n := hm.GetMsgOrderID(); n {
		case 99:
			return nil, errors.New("undecryptable")
		default:
			e := event("H"+string(rune('0'+n)), group, ada, n == 2, &waE2E.Message{Conversation: proto.String("msg " + string(rune('0'+n)))})
			e.Info.Timestamp = t0.Add(time.Duration(n) * time.Minute)
			return e, nil
		}
	}
	hc := &waHistorySync.Conversation{
		ID: proto.String(group.String()), Name: proto.String("Book club"), UnreadCount: proto.Uint32(2),
		Pinned: proto.Uint32(1), MuteEndTime: proto.Uint64(uint64(time.Now().Add(time.Hour).Unix())), Messages: hms,
	}
	c, msgs := convertHistory(hc, group, names, parse)
	if c == nil || c.ID != group.String() || c.Name != "Book club" || !c.IsGroup || !c.Unread || !c.Pinned || !muted(c) {
		t.Fatalf("conversation = %+v", c)
	}
	var ids []string
	for _, m := range msgs {
		ids = append(ids, m.msg.ID)
	}
	if strings.Join(ids, " ") != "H1 H2 H3" {
		t.Errorf("messages = %v; want oldest first, the unparseable one skipped", ids)
	}
	if c.LastMessage != "msg 3" || c.LastTs != t0.Add(3*time.Minute).UnixMilli() || c.LastSender != "Ada" {
		t.Errorf("preview = %q %d %q", c.LastMessage, c.LastTs, c.LastSender)
	}
	// A person's chat with no name in the sync is named from contacts.
	pc, _ := convertHistory(&waHistorySync.Conversation{ID: proto.String(ada.String())}, ada, names, parse)
	if pc.Name != "Ada Lovelace" || pc.IsGroup || len(pc.Participants) != 1 || pc.Participants[0].Number != "+15551112222" {
		t.Errorf("person chat = %+v", pc)
	}
	if c, _ := convertHistory(&waHistorySync.Conversation{ID: proto.String("status@broadcast")}, types.StatusBroadcastJID, names, parse); c != nil {
		t.Errorf("status broadcast became a chat")
	}
}
