package whatsapp

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"omarchy-omamessages/core"
)

// Conversation IDs are chat JIDs: "15551234567@s.whatsapp.net" for a
// person, "1203…@g.us" for a group. WhatsApp's hidden "@lid" addresses are
// mapped to the phone-number JID where whatsmeow knows it (see chatID), so a
// person is one conversation whichever way their messages are addressed.

// nameSource names people and groups.
type nameSource interface {
	Name(jid types.JID) string
}

// fallbackName is what shows when nothing better is known: "+number".
func fallbackName(jid types.JID) string {
	if jid.Server == types.DefaultUserServer {
		return "+" + jid.User
	}
	if jid.Server == types.GroupServer {
		return "Group"
	}
	return jid.User
}

// skippedChat reports chats the panel doesn't show: status updates,
// channels (newsletters) and broadcast lists.
func skippedChat(jid types.JID) bool {
	return jid.Server == types.BroadcastServer || jid.Server == types.NewsletterServer || jid.User == "status"
}

// mediaText is the attachment and caption of a media message, if it is one.
type mediaText struct {
	att     core.Attachment
	caption string
	thumb   []byte // WhatsApp embeds a small JPEG preview in the message
}

func media(m *waE2E.Message) (mediaText, bool) {
	switch {
	case m.GetImageMessage() != nil:
		v := m.GetImageMessage()
		return mediaText{core.Attachment{Name: "Photo", Kind: "image", Size: int64(v.GetFileLength()), Mime: v.GetMimetype(), Width: int(v.GetWidth()), Height: int(v.GetHeight())}, v.GetCaption(), v.GetJPEGThumbnail()}, true
	case m.GetVideoMessage() != nil:
		v := m.GetVideoMessage()
		return mediaText{core.Attachment{Name: "Video", Kind: "video", Size: int64(v.GetFileLength()), Mime: v.GetMimetype(), Width: int(v.GetWidth()), Height: int(v.GetHeight())}, v.GetCaption(), v.GetJPEGThumbnail()}, true
	case m.GetAudioMessage() != nil:
		v := m.GetAudioMessage()
		name := "Audio"
		if v.GetPTT() {
			name = "Voice message"
		}
		if s := v.GetSeconds(); s > 0 {
			name += fmt.Sprintf(" (%d:%02d)", s/60, s%60)
		}
		return mediaText{att: core.Attachment{Name: name, Kind: "audio", Size: int64(v.GetFileLength()), Mime: v.GetMimetype()}}, true
	case m.GetDocumentMessage() != nil:
		v := m.GetDocumentMessage()
		name := v.GetFileName()
		if name == "" {
			name = v.GetTitle()
		}
		if name == "" {
			name = "File"
		}
		return mediaText{core.Attachment{Name: name, Kind: "file", Size: int64(v.GetFileLength()), Mime: v.GetMimetype()}, v.GetCaption(), v.GetJPEGThumbnail()}, true
	case m.GetStickerMessage() != nil:
		v := m.GetStickerMessage()
		return mediaText{att: core.Attachment{Name: "Sticker", Kind: "image", Size: int64(v.GetFileLength()), Mime: v.GetMimetype(), Width: int(v.GetWidth()), Height: int(v.GetHeight())}}, true
	case m.GetContactMessage() != nil:
		return mediaText{att: core.Attachment{Name: strings.TrimSpace("Contact " + m.GetContactMessage().GetDisplayName()), Kind: "file"}}, true
	case m.GetLocationMessage() != nil, m.GetLiveLocationMessage() != nil:
		return mediaText{att: core.Attachment{Name: "Location", Kind: "file"}}, true
	case m.GetPollCreationMessage() != nil:
		return mediaText{att: core.Attachment{Name: "Poll: " + m.GetPollCreationMessage().GetName(), Kind: "file"}}, true
	case m.GetPollCreationMessageV3() != nil:
		return mediaText{att: core.Attachment{Name: "Poll: " + m.GetPollCreationMessageV3().GetName(), Kind: "file"}}, true
	}
	return mediaText{}, false
}

// text is the plain text of a message, if it has any.
func text(m *waE2E.Message) string {
	if t := m.GetConversation(); t != "" {
		return t
	}
	return m.GetExtendedTextMessage().GetText()
}

// converted is one message ready for the store, plus what else it carries.
type converted struct {
	convID string
	msg    core.Message
	thumb  []byte         // inline JPEG preview of the first attachment
	raw    *waE2E.Message // the message, kept when it carries downloadable media
}

// convertEvent maps a message event. ok is false for anything that isn't a
// message to show (reactions, protocol messages, status updates…).
func convertEvent(evt *events.Message, chat types.JID, names nameSource) (converted, bool) {
	if evt.Message == nil || skippedChat(chat) {
		return converted{}, false
	}
	info := evt.Info
	m := core.Message{
		ID:       info.ID,
		Ts:       info.Timestamp.UnixMilli(),
		FromMe:   info.IsFromMe,
		SenderID: info.Sender.ToNonAD().String(),
		Status:   "received",
	}
	if info.IsFromMe {
		m.Sender = "Me"
		m.Status = "sent"
	} else {
		m.Sender = names.Name(info.Sender.ToNonAD())
		if m.Sender == fallbackName(info.Sender.ToNonAD()) && info.PushName != "" {
			m.Sender = info.PushName
		}
	}
	out := converted{convID: chat.String()}
	if md, ok := media(evt.Message); ok {
		m.Attachments = []core.Attachment{md.att}
		m.Text = md.caption
		out.thumb = md.thumb
		out.raw = evt.Message
	} else if t := text(evt.Message); t != "" {
		m.Text = t
	} else {
		return converted{}, false
	}
	out.msg = m
	return out, true
}

// protocolChange is an edit or a delete-for-everyone of an earlier message.
type protocolChange struct {
	targetID string
	deleted  bool
	newText  string
}

func protocol(evt *events.Message) (protocolChange, bool) {
	p := evt.Message.GetProtocolMessage()
	if p == nil {
		return protocolChange{}, false
	}
	switch p.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		return protocolChange{targetID: p.GetKey().GetID(), deleted: true}, true
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		if t := text(p.GetEditedMessage()); t != "" {
			return protocolChange{targetID: p.GetKey().GetID(), newText: t}, true
		}
	}
	return protocolChange{}, false
}

// preview is the conversation list's one line for a message.
func preview(m core.Message) string {
	if t := strings.TrimSpace(m.Text); t != "" {
		return t
	}
	if len(m.Attachments) > 0 {
		return m.Attachments[0].Name
	}
	return ""
}

func firstName(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// convertHistory maps one conversation from the link-time history sync.
// parse turns a stored message into an event (whatsmeow's ParseWebMessage);
// messages it can't parse, or that aren't worth showing, are skipped.
func convertHistory(hc *waHistorySync.Conversation, chat types.JID, names nameSource, parse func(*waHistorySync.HistorySyncMsg) (*events.Message, error)) (*core.Conversation, []converted) {
	if skippedChat(chat) {
		return nil, nil
	}
	c := &core.Conversation{
		ID:       chat.String(),
		Name:     hc.GetName(),
		IsGroup:  chat.Server == types.GroupServer,
		Unread:   hc.GetUnreadCount() > 0,
		Archived: hc.GetArchived(),
		Pinned:   hc.GetPinned() > 0,
		Extra:    map[string]any{"unreadCount": int(hc.GetUnreadCount())},
	}
	if c.Name == "" {
		c.Name = names.Name(chat)
	}
	if mu := hc.GetMuteEndTime(); mu > 0 {
		c.Extra["muteUntil"] = int64(mu) * 1000
	}
	if !c.IsGroup {
		c.Participants = []core.Participant{{ID: chat.String(), Name: c.Name, Number: fallbackName(chat)}}
	}
	var msgs []converted
	for _, hm := range hc.GetMessages() {
		evt, err := parse(hm)
		if err != nil || evt == nil {
			continue
		}
		if cv, ok := convertEvent(evt, chat, names); ok {
			msgs = append(msgs, cv)
		}
	}
	// History comes newest first; the store wants oldest first.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	if n := len(msgs); n > 0 {
		last := msgs[n-1].msg
		c.LastMessage, c.LastFromMe, c.LastTs = preview(last), last.FromMe, last.Ts
		if c.IsGroup && !last.FromMe {
			c.LastSender = firstName(last.Sender)
		}
	} else if ts := hc.GetConversationTimestamp(); ts > 0 {
		c.LastTs = int64(ts) * 1000
	}
	return c, msgs
}
