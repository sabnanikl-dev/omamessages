package gmessages

// Hand-built libgm values covering every branch of convertConversation and
// convertMessage. This file is shared verbatim (apart from the package line)
// between the golden capture in a scratch copy of the old gmessagesd and the
// golden test in providers/gmessages, so both convert the same input.

import "go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

func str(s string) *string { return &s }

func participant(id, number, full, first, formatted string, me bool) *gmproto.Participant {
	return &gmproto.Participant{
		ID:              &gmproto.SmallInfo{ParticipantID: id, Number: number},
		FullName:        full,
		FirstName:       first,
		FormattedNumber: formatted,
		IsMe:            me,
	}
}

func text(t string) *gmproto.MessageInfo {
	return &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: t}}}
}

func media(name string, format gmproto.MediaFormats, size int64) *gmproto.MessageInfo {
	return &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MediaContent{MediaContent: &gmproto.MediaContent{MediaName: name, Format: format, Size: size}}}
}

func status(st gmproto.MessageStatusType, errMsg string) *gmproto.MessageStatus {
	return &gmproto.MessageStatus{Status: st, ErrMsg: errMsg}
}

type fixtureMessage struct {
	Conv int // index into fixtureConversations(), or -1 for no known conversation
	Msg  *gmproto.Message
}

func fixtureConversations() []*gmproto.Conversation {
	me := participant("p-me", "+15550000000", "", "", "", true)
	return []*gmproto.Conversation{
		{ // 0: 1:1 SMS, named, unread, pinned
			ConversationID:       "11",
			Name:                 "  Ada Lovelace ",
			LatestMessage:        &gmproto.LatestMessage{DisplayContent: " Are you coming Sunday? ", FromMe: 0, DisplayName: "Ada"},
			LastMessageTimestamp: 1790000000123456,
			Unread:               true,
			Pinned:               true,
			DefaultOutgoingID:    "p-me",
			Type:                 gmproto.ConversationType_SMS,
			Status:               gmproto.ConversationStatus_ACTIVE,
			Participants:         []*gmproto.Participant{me, participant("p-ada", "+15551112222", "Ada Lovelace", "Ada", "(555) 111-2222", false)},
		},
		{ // 1: RCS group with no name, from me, names from full/first/formatted/number
			ConversationID:       "22",
			LatestMessage:        &gmproto.LatestMessage{DisplayContent: "merged, thanks!", FromMe: 1, DisplayName: "You"},
			LastMessageTimestamp: 1790000500000000,
			IsGroupChat:          true,
			DefaultOutgoingID:    "p-me",
			Type:                 gmproto.ConversationType_RCS,
			Participants: []*gmproto.Participant{
				me,
				participant("p-grace", "+15553334444", "Grace Hopper", "Grace", "", false),
				participant("p-linus", "+15556667777", "", "Linus", "", false),
				participant("p-fmt", "+15558889999", "", "", "(555) 888-9999", false),
				participant("p-num", "+15550001111", "", "", "", false),
			},
		},
		{ // 2: archived, no name, no other participants -> "Conversation"
			ConversationID:       "33",
			LastMessageTimestamp: 1780000000000000,
			Status:               gmproto.ConversationStatus_ARCHIVED,
			Type:                 gmproto.ConversationType_UNKNOWN_CONVERSATION_TYPE,
			Participants:         []*gmproto.Participant{me},
		},
	}
}

func fixtureMessages() []fixtureMessage {
	return []fixtureMessage{
		{0, &gmproto.Message{MessageID: "m1", Timestamp: 1790000000123456, ParticipantID: "p-ada",
			MessageStatus: status(gmproto.MessageStatusType_INCOMING_COMPLETE, ""), MessageInfo: []*gmproto.MessageInfo{text("Are you coming Sunday?")}}},
		{0, &gmproto.Message{MessageID: "m2", Timestamp: 1790000100000000, ParticipantID: "p-me", TmpID: "tmp_abc",
			MessageStatus: status(gmproto.MessageStatusType_OUTGOING_COMPLETE, ""), MessageInfo: []*gmproto.MessageInfo{text("yes"), text("see you")}}},
		{0, &gmproto.Message{MessageID: "m3", Timestamp: 1790000200000000, ParticipantID: "p-me",
			MessageStatus: status(gmproto.MessageStatusType_OUTGOING_DELIVERED, "")}},
		{0, &gmproto.Message{MessageID: "m4", Timestamp: 1790000300000000, ParticipantID: "p-me",
			MessageStatus: status(gmproto.MessageStatusType_OUTGOING_DISPLAYED, ""), MessageInfo: []*gmproto.MessageInfo{text("read one")}}},
		{0, &gmproto.Message{MessageID: "m5", Timestamp: 1790000400000000, ParticipantID: "p-me",
			MessageStatus: status(gmproto.MessageStatusType_OUTGOING_SENDING, ""), MessageInfo: []*gmproto.MessageInfo{text("sending")}}},
		{0, &gmproto.Message{MessageID: "m6", Timestamp: 1790000500000000, ParticipantID: "p-me",
			MessageStatus: status(gmproto.MessageStatusType_OUTGOING_FAILED_GENERIC, "  No service  "), MessageInfo: []*gmproto.MessageInfo{text("failed")}}},
		{0, &gmproto.Message{MessageID: "m7", Timestamp: 1790000600000000, ParticipantID: "p-me",
			MessageStatus: status(gmproto.MessageStatusType_OUTGOING_FAILED_TOO_LARGE, "")}},
		{0, &gmproto.Message{MessageID: "m8", Timestamp: 1790000700000000, ParticipantID: "p-me",
			MessageStatus: status(gmproto.MessageStatusType_OUTGOING_CANCELED, "")}},
		{1, &gmproto.Message{MessageID: "g1", Timestamp: 1790000800000000, ParticipantID: "p-grace",
			MessageStatus: status(gmproto.MessageStatusType_INCOMING_AUTO_DOWNLOADING, ""),
			MessageInfo: []*gmproto.MessageInfo{
				media("IMG_1.jpg", gmproto.MediaFormats_IMAGE_JPEG, 123456),
				media("clip", gmproto.MediaFormats_VIDEO_MP4, 999),
				media("voice", gmproto.MediaFormats_AUDIO_OGG, 42),
				media("scan.PNG", gmproto.MediaFormats_UNSPECIFIED_TYPE, 7),
				media("movie.MOV", gmproto.MediaFormats_UNSPECIFIED_TYPE, 8),
				media("invoice.pdf", gmproto.MediaFormats_APP_PDF, 184000),
				text("photos from the lake"),
			},
			Reactions: []*gmproto.ReactionEntry{
				{Data: &gmproto.ReactionData{Unicode: "❤️"}},
				{Data: &gmproto.ReactionData{Unicode: ""}},
				{Data: &gmproto.ReactionData{Unicode: "😂"}},
			}}},
		{1, &gmproto.Message{MessageID: "g2", Timestamp: 1790000900000000, ParticipantID: "p-me",
			MessageStatus: status(gmproto.MessageStatusType_OUTGOING_COMPLETE, ""), MessageInfo: []*gmproto.MessageInfo{text("merged, thanks!")}}},
		{1, &gmproto.Message{MessageID: "g3", Timestamp: 1790001000000000, ParticipantID: "p-stranger",
			SenderParticipant: participant("p-stranger", "+15559990000", "", "Sam", "", false),
			MessageStatus:     status(gmproto.MessageStatusType_INCOMING_COMPLETE, ""), Subject: str("  Subject only  ")}},
		{1, &gmproto.Message{MessageID: "g4", Timestamp: 1790001100000000, ParticipantID: "p-linus",
			MessageStatus: status(gmproto.MessageStatusType_INCOMING_COMPLETE, ""), Subject: str("ignored subject"), MessageInfo: []*gmproto.MessageInfo{text("body wins")}}},
		// An incoming status from a participant marked as me: FromMe comes from the participant.
		{1, &gmproto.Message{MessageID: "g5", Timestamp: 1790001200000000, ParticipantID: "p-me",
			MessageStatus: status(gmproto.MessageStatusType_INCOMING_COMPLETE, ""), MessageInfo: []*gmproto.MessageInfo{text("sent from the phone")}}},
		{-1, &gmproto.Message{MessageID: "x1", Timestamp: 1790001300000000, ParticipantID: "p-unknown",
			MessageStatus: status(gmproto.MessageStatusType_INCOMING_COMPLETE, ""), MessageInfo: []*gmproto.MessageInfo{text("no conversation known")}}},
		{-1, &gmproto.Message{MessageID: "x2", Timestamp: 1790001400000000}},
	}
}
