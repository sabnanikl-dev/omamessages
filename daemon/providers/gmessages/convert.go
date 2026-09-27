package gmessages

import (
	"strings"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"omarchy-omamessages/core"
)

func participantName(p *gmproto.Participant) string {
	if p == nil {
		return ""
	}
	if n := strings.TrimSpace(p.GetFullName()); n != "" {
		return n
	}
	if n := strings.TrimSpace(p.GetFirstName()); n != "" {
		return n
	}
	if n := strings.TrimSpace(p.GetFormattedNumber()); n != "" {
		return n
	}
	return p.GetID().GetNumber()
}

// convertConversation maps a libgm conversation to the shared type. SMS vs RCS
// is Google-only, so it goes in Extra["type"].
func convertConversation(c *gmproto.Conversation) *core.Conversation {
	out := &core.Conversation{
		ID:          c.GetConversationID(),
		Name:        strings.TrimSpace(c.GetName()),
		LastMessage: strings.TrimSpace(c.GetLatestMessage().GetDisplayContent()),
		LastFromMe:  c.GetLatestMessage().GetFromMe() != 0,
		LastSender:  strings.TrimSpace(c.GetLatestMessage().GetDisplayName()),
		LastTs:      c.GetLastMessageTimestamp() / 1000,
		Unread:      c.GetUnread(),
		IsGroup:     c.GetIsGroupChat(),
		Archived:    c.GetStatus() == gmproto.ConversationStatus_ARCHIVED,
		Pinned:      c.GetPinned(),
		OutgoingID:  c.GetDefaultOutgoingID(),
		Extra:       map[string]any{"type": "sms"},
	}
	if c.GetType() == gmproto.ConversationType_RCS {
		out.Extra["type"] = "rcs"
	}
	for _, p := range c.GetParticipants() {
		out.Participants = append(out.Participants, core.Participant{
			ID:     p.GetID().GetParticipantID(),
			Name:   participantName(p),
			Number: p.GetID().GetNumber(),
			IsMe:   p.GetIsMe(),
		})
	}
	if out.Name == "" {
		var names []string
		for _, p := range out.Participants {
			if !p.IsMe {
				names = append(names, p.Name)
			}
		}
		out.Name = strings.Join(names, ", ")
	}
	if out.Name == "" {
		out.Name = "Conversation"
	}
	return out
}

func statusString(st gmproto.MessageStatusType) (string, bool) {
	code := int(st)
	if code >= 100 && code < 200 {
		return "received", false
	}
	switch st {
	case gmproto.MessageStatusType_OUTGOING_COMPLETE:
		return "sent", true
	case gmproto.MessageStatusType_OUTGOING_DELIVERED:
		return "delivered", true
	case gmproto.MessageStatusType_OUTGOING_DISPLAYED:
		return "read", true
	case gmproto.MessageStatusType_OUTGOING_YET_TO_SEND, gmproto.MessageStatusType_OUTGOING_SENDING,
		gmproto.MessageStatusType_OUTGOING_RESENDING, gmproto.MessageStatusType_OUTGOING_AWAITING_RETRY,
		gmproto.MessageStatusType_OUTGOING_SEND_AFTER_PROCESSING, gmproto.MessageStatusType_OUTGOING_VALIDATING,
		gmproto.MessageStatusType_OUTGOING_NOT_DELIVERED_YET, gmproto.MessageStatusType_OUTGOING_DRAFT:
		return "sending", true
	case gmproto.MessageStatusType_OUTGOING_DELETED, gmproto.MessageStatusType_OUTGOING_CANCELED:
		return "deleted", true
	}
	if strings.HasPrefix(st.String(), "OUTGOING_FAILED") || strings.Contains(st.String(), "FAILED") {
		return "failed", true
	}
	return "sent", true
}

func mediaKind(format gmproto.MediaFormats, name string) string {
	f := strings.ToLower(format.String())
	switch {
	case strings.Contains(f, "image"), strings.Contains(f, "gif"), strings.Contains(f, "png"), strings.Contains(f, "jpeg"), strings.Contains(f, "webp"):
		return "image"
	case strings.Contains(f, "video"), strings.Contains(f, "mp4"):
		return "video"
	case strings.Contains(f, "audio"), strings.Contains(f, "amr"), strings.Contains(f, "ogg"), strings.Contains(f, "mp3"):
		return "audio"
	}
	lower := strings.ToLower(name)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic"} {
		if strings.HasSuffix(lower, ext) {
			return "image"
		}
	}
	for _, ext := range []string{".mp4", ".mov", ".3gp", ".webm"} {
		if strings.HasSuffix(lower, ext) {
			return "video"
		}
	}
	return "file"
}

func convertMessage(conv *core.Conversation, m *gmproto.Message) core.Message {
	out := core.Message{
		ID:       m.GetMessageID(),
		Ts:       m.GetTimestamp() / 1000,
		SenderID: m.GetParticipantID(),
		TmpID:    m.GetTmpID(),
	}
	status, outgoing := statusString(m.GetMessageStatus().GetStatus())
	out.Status = status
	out.StatusText = strings.TrimSpace(m.GetMessageStatus().GetErrMsg())
	out.FromMe = outgoing
	if conv != nil {
		for _, p := range conv.Participants {
			if p.ID == out.SenderID {
				out.Sender = p.Name
				if p.IsMe {
					out.FromMe = true
				}
				break
			}
		}
	}
	if out.Sender == "" && m.GetSenderParticipant() != nil {
		out.Sender = participantName(m.GetSenderParticipant())
	}
	var parts []string
	for _, info := range m.GetMessageInfo() {
		switch d := info.GetData().(type) {
		case *gmproto.MessageInfo_MessageContent:
			if t := d.MessageContent.GetContent(); t != "" {
				parts = append(parts, t)
			}
		case *gmproto.MessageInfo_MediaContent:
			out.Attachments = append(out.Attachments, core.Attachment{
				Name: d.MediaContent.GetMediaName(),
				Kind: mediaKind(d.MediaContent.GetFormat(), d.MediaContent.GetMediaName()),
				Size: d.MediaContent.GetSize(),
			})
		}
	}
	out.Text = strings.Join(parts, "\n")
	if s := strings.TrimSpace(m.GetSubject()); s != "" && out.Text == "" {
		out.Text = s
	}
	for _, r := range m.GetReactions() {
		if e := r.GetData().GetUnicode(); e != "" {
			out.Reactions = append(out.Reactions, e)
		}
	}
	return out
}
