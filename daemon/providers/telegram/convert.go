package telegram

import (
	"strconv"
	"strings"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"omarchy-omamessages/core"
)

// Conversation IDs are TDLib/Bot-API style peer IDs: users positive, basic
// groups negative, channels and supergroups -100…, so "telegram:-1001234".

func peerID(p tg.PeerClass) int64 {
	var id constant.TDLibPeerID
	switch v := p.(type) {
	case *tg.PeerUser:
		id.User(v.UserID)
	case *tg.PeerChat:
		id.Chat(v.ChatID)
	case *tg.PeerChannel:
		id.Channel(v.ChannelID)
	}
	return int64(id)
}

func nativeID(p tg.PeerClass) string { return strconv.FormatInt(peerID(p), 10) }

// entities builds a lookup from the users and chats a response carries.
func entities(users []tg.UserClass, chats []tg.ChatClass) peer.Entities {
	us := map[int64]*tg.User{}
	cs := map[int64]*tg.Chat{}
	chs := map[int64]*tg.Channel{}
	for _, u := range users {
		if v, ok := u.(*tg.User); ok {
			us[v.ID] = v
		}
	}
	for _, c := range chats {
		switch v := c.(type) {
		case *tg.Chat:
			cs[v.ID] = v
		case *tg.Channel:
			chs[v.ID] = v
		}
	}
	return peer.NewEntities(us, cs, chs)
}

func userName(u *tg.User) string {
	if u == nil {
		return ""
	}
	if u.Deleted {
		return "Deleted account"
	}
	if n := strings.TrimSpace(u.FirstName + " " + u.LastName); n != "" {
		return n
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	if u.Phone != "" {
		return "+" + u.Phone
	}
	return "Telegram user"
}

// peerInfo names a chat and says what kind it is: "user", "group" (basic
// groups and supergroups) or "channel" (broadcast).
func peerInfo(p tg.PeerClass, ents peer.Entities) (name, kind string) {
	switch v := p.(type) {
	case *tg.PeerUser:
		return userName(ents.Users()[v.UserID]), "user"
	case *tg.PeerChat:
		if c, ok := ents.Chat(v.ChatID); ok {
			return c.Title, "group"
		}
		return "Group", "group"
	case *tg.PeerChannel:
		if c, ok := ents.Channel(v.ChannelID); ok {
			if c.Broadcast {
				return c.Title, "channel"
			}
			return c.Title, "group"
		}
		return "Channel", "channel"
	}
	return "", ""
}

// senderOf names who wrote m. Incoming messages in a private chat have no
// from_id: the sender is the chat itself.
func senderOf(m *tg.Message, ents peer.Entities) (id, name string) {
	from, ok := m.GetFromID()
	if !ok {
		from = m.PeerID
	}
	name, _ = peerInfo(from, ents)
	return nativeID(from), name
}

// convertMessage maps one message. readOutboxMax is the highest of our
// messages the other side has read (0 if unknown).
func convertMessage(m *tg.Message, ents peer.Entities, readOutboxMax int) core.Message {
	out := core.Message{
		ID:          strconv.Itoa(m.ID),
		Ts:          int64(m.Date) * 1000,
		FromMe:      m.Out,
		Text:        m.Message,
		Attachments: mediaAttachment(m.Media),
		Status:      "received",
	}
	if m.Out {
		out.Status = "sent"
		if readOutboxMax > 0 && m.ID <= readOutboxMax {
			out.Status = "read"
		}
	}
	out.SenderID, out.Sender = senderOf(m, ents)
	if m.Out {
		out.Sender = "Me"
	}
	if r, ok := m.GetReactions(); ok {
		for _, rc := range r.Results {
			if e, ok := rc.Reaction.(*tg.ReactionEmoji); ok && rc.Count > 0 {
				out.Reactions = append(out.Reactions, e.Emoticon)
			}
		}
	}
	return out
}

// mediaAttachment describes what a message carries besides text. Thumbnails
// and downloads come in a later slice; this is enough to label it.
func mediaAttachment(media tg.MessageMediaClass) []core.Attachment {
	switch v := media.(type) {
	case nil, *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
		return nil
	case *tg.MessageMediaPhoto:
		a := core.Attachment{Name: "Photo", Kind: "image", Mime: "image/jpeg"}
		if p, ok := v.Photo.(*tg.Photo); ok {
			for _, s := range p.Sizes {
				if ps, ok := s.(*tg.PhotoSize); ok && ps.W > a.Width {
					a.Width, a.Height, a.Size = ps.W, ps.H, int64(ps.Size)
				}
			}
		}
		return []core.Attachment{a}
	case *tg.MessageMediaDocument:
		doc, ok := v.Document.(*tg.Document)
		if !ok {
			return []core.Attachment{{Name: "File", Kind: "file"}}
		}
		a := core.Attachment{Name: "File", Kind: "file", Size: doc.Size, Mime: doc.MimeType}
		for _, attr := range doc.Attributes {
			switch at := attr.(type) {
			case *tg.DocumentAttributeFilename:
				a.Name = at.FileName
			case *tg.DocumentAttributeVideo:
				a.Kind, a.Width, a.Height = "video", at.W, at.H
				if at.RoundMessage {
					a.Name = "Video message"
				} else if a.Name == "File" {
					a.Name = "Video"
				}
			case *tg.DocumentAttributeAudio:
				a.Kind = "audio"
				if at.Voice {
					a.Name = "Voice message"
				} else if at.Title != "" {
					a.Name = strings.TrimSpace(at.Performer + " – " + at.Title)
				}
			case *tg.DocumentAttributeSticker:
				a.Kind = "image"
				a.Name = strings.TrimSpace("Sticker " + at.Alt)
			case *tg.DocumentAttributeAnimated:
				a.Kind = "video"
				a.Name = "GIF"
			case *tg.DocumentAttributeImageSize:
				a.Width, a.Height = at.W, at.H
				if a.Kind == "file" {
					a.Kind = "image"
				}
			}
		}
		return []core.Attachment{a}
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return []core.Attachment{{Name: "Location", Kind: "file"}}
	case *tg.MessageMediaContact:
		return []core.Attachment{{Name: strings.TrimSpace("Contact " + v.FirstName + " " + v.LastName), Kind: "file"}}
	case *tg.MessageMediaPoll:
		return []core.Attachment{{Name: "Poll: " + v.Poll.Question.Text, Kind: "file"}}
	}
	return []core.Attachment{{Name: "Attachment", Kind: "file"}}
}

// preview is the one line the conversation list shows for a message.
func preview(m core.Message) string {
	if t := strings.TrimSpace(m.Text); t != "" {
		return t
	}
	if len(m.Attachments) > 0 {
		return m.Attachments[0].Name
	}
	return ""
}

// serviceText labels a service message ("joined Telegram" and the like) for
// the conversation list.
func serviceText(a tg.MessageActionClass) string {
	switch v := a.(type) {
	case *tg.MessageActionContactSignUp:
		return "Joined Telegram"
	case *tg.MessageActionChatCreate, *tg.MessageActionChannelCreate:
		return "Created the group"
	case *tg.MessageActionChatAddUser:
		return "Added members"
	case *tg.MessageActionChatJoinedByLink, *tg.MessageActionChatJoinedByRequest:
		return "Joined by link"
	case *tg.MessageActionChatDeleteUser:
		return "Left the group"
	case *tg.MessageActionChatEditTitle:
		return "Renamed the group to " + v.Title
	case *tg.MessageActionChatEditPhoto:
		return "Changed the group photo"
	case *tg.MessageActionPinMessage:
		return "Pinned a message"
	case *tg.MessageActionPhoneCall:
		if v.Video {
			return "Video call"
		}
		return "Call"
	case *tg.MessageActionHistoryClear:
		return "History cleared"
	}
	return ""
}

// convertDialog maps one dialog, using its top message (which may be a
// service message) for the preview and time.
func convertDialog(d *tg.Dialog, topMsg tg.MessageClass, ents peer.Entities) *core.Conversation {
	name, kind := peerInfo(d.Peer, ents)
	c := &core.Conversation{
		ID:      nativeID(d.Peer),
		Name:    name,
		Unread:  d.UnreadCount > 0 || d.UnreadMark,
		IsGroup: kind == "group",
		Pinned:  d.Pinned,
		// Folder 1 is Telegram's Archive.
		Archived: d.FolderID == 1,
		Extra:    map[string]any{"kind": kind, "unreadCount": d.UnreadCount, "readOutboxMax": d.ReadOutboxMaxID},
	}
	if mu, ok := d.NotifySettings.GetMuteUntil(); ok && mu > 0 {
		c.Extra["muteUntil"] = int64(mu) * 1000
	}
	if u, ok := d.Peer.(*tg.PeerUser); ok {
		if user, ok := ents.Users()[u.UserID]; ok {
			c.Participants = []core.Participant{{ID: c.ID, Name: name, Number: userHandle(user)}}
		}
	}
	if svc, ok := topMsg.(*tg.MessageService); ok {
		c.LastMessage = serviceText(svc.Action)
		c.LastTs = int64(svc.Date) * 1000
		c.LastFromMe = svc.Out
	}
	if top, ok := topMsg.(*tg.Message); ok {
		m := convertMessage(top, ents, d.ReadOutboxMaxID)
		c.LastMessage = preview(m)
		c.LastFromMe = m.FromMe
		c.LastTs = m.Ts
		if kind == "group" && !m.FromMe {
			c.LastSender = firstName(m.Sender)
		}
	}
	return c
}

// userHandle is how a user can be reached: @username, else +phone.
func userHandle(u *tg.User) string {
	if u.Username != "" {
		return "@" + u.Username
	}
	if u.Phone != "" {
		return "+" + u.Phone
	}
	return ""
}

func firstName(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
