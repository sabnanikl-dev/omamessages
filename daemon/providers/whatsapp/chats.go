package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"omarchy-omamessages/core"
)

// WhatsApp has no "list my chats" or "fetch history" call for a linked
// device: chats and messages come from the history sync sent once at link
// time and from live messages after that. So everything is kept locally, in
// whatsapp/state.json and whatsapp/messages/<chat>.json.

const keepPerChat = 300 // newest messages kept per chat from the history sync

// contactNames names people from whatsmeow's contact store and groups from
// what we already know about them.
type contactNames struct {
	w   *WhatsApp
	cli *whatsmeow.Client
}

func (n contactNames) Name(jid types.JID) string {
	ctx := n.w.lifeCtx()
	jid = jid.ToNonAD()
	if jid.Server == types.GroupServer {
		if c := n.w.env.Store.Conversation(jid.String()); c != nil && c.Name != "" && c.Name != "Group" {
			return c.Name
		}
		return fallbackName(jid)
	}
	if jid.Server == types.HiddenUserServer {
		if pn, err := n.cli.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			jid = pn
		}
	}
	if ci, err := n.cli.Store.Contacts.GetContact(ctx, jid); err == nil && ci.Found {
		for _, s := range []string{ci.FullName, ci.FirstName, ci.BusinessName, ci.PushName} {
			if s != "" {
				return s
			}
		}
	}
	return fallbackName(jid)
}

// chatID picks the conversation for a chat JID, preferring the phone-number
// JID over a hidden "@lid" one.
func (w *WhatsApp) chatID(cli *whatsmeow.Client, jid types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server == types.HiddenUserServer {
		if pn, err := cli.Store.LIDs.GetPNForLID(w.lifeCtx(), jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid
}

func (w *WhatsApp) thumbPath(convID, msgID string) string {
	return filepath.Join(w.env.CacheDir, "media", core.SafeName(convID), core.SafeName(msgID)+"-thumb.jpg")
}

// saveThumb writes an inline preview (once) and points the attachment at
// it. Messages from the history sync often carry no inline preview, only a
// thumbnail on WhatsApp's servers: that one is fetched in the background.
func (w *WhatsApp) saveThumb(cli *whatsmeow.Client, cv *converted) {
	if len(cv.msg.Attachments) == 0 {
		return
	}
	p := w.thumbPath(cv.convID, cv.msg.ID)
	if _, err := os.Stat(p); err == nil {
		cv.msg.Attachments[0].ThumbPath = p
		return
	}
	if len(cv.thumb) == 0 {
		if d, ok := downloadable(cv.raw); ok {
			if t, ok := d.(whatsmeow.DownloadableThumbnail); ok && t.GetThumbnailDirectPath() != "" {
				w.queueThumb(cli, cv.convID, cv.msg.ID, t)
			}
		}
		return
	}
	if _, err := os.Stat(p); err != nil {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return
		}
		if err := os.WriteFile(p, cv.thumb, 0o600); err != nil {
			return
		}
	}
	cv.msg.Attachments[0].ThumbPath = p
}

// queueThumb downloads a server-side thumbnail, a few at a time, and shows
// it once it's there.
func (w *WhatsApp) queueThumb(cli *whatsmeow.Client, convID, msgID string, t whatsmeow.DownloadableThumbnail) {
	w.mu.Lock()
	if w.thumbSlots == nil {
		w.thumbSlots = make(chan struct{}, 3)
	}
	slots := w.thumbSlots
	w.mu.Unlock()
	w.env.Spawn(func() {
		slots <- struct{}{}
		defer func() { <-slots }()
		ctx, cancel := context.WithTimeout(w.lifeCtx(), time.Minute)
		defer cancel()
		data, err := cli.DownloadThumbnail(ctx, t)
		if err != nil || len(data) == 0 {
			w.env.Log.Debug().Err(err).Str("msg", msgID).Msg("Thumbnail download failed")
			return
		}
		p := w.thumbPath(convID, msgID)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return
		}
		w.env.Store.Messages(convID)
		w.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
			for i := range cm.Messages {
				if cm.Messages[i].ID == msgID && len(cm.Messages[i].Attachments) > 0 {
					cm.Messages[i].Attachments[0].ThumbPath = p
				}
			}
		})
	})
}

// --- names ----------------------------------------------------------------------

// isPlaceholderName: a chat still named only by its number (or "Group").
func isPlaceholderName(c *core.Conversation, chat types.JID) bool {
	return c.Name == "" || c.Name == fallbackName(chat)
}

// rename gives a chat a better name; only placeholder names are replaced
// unless force is set (a contact's saved name, a group rename).
func (w *WhatsApp) rename(convID, name string, force bool) {
	if name == "" {
		return
	}
	c := w.conversationCopy(convID)
	if c == nil || c.Name == name {
		return
	}
	chat, err := types.ParseJID(convID)
	if err != nil || (!force && !isPlaceholderName(c, chat)) {
		return
	}
	c.Name = name
	for i := range c.Participants {
		c.Participants[i].Name = name
	}
	w.env.Store.UpsertConversation(c)
	w.env.Store.Flush()
}

// renameSweep names every chat still showing only a number, from what the
// contact store knows now (contacts reach a linked device after the history).
func (w *WhatsApp) renameSweep(cli *whatsmeow.Client) {
	names := contactNames{w: w, cli: cli}
	n := 0
	for _, c := range w.env.Store.Snapshot().Conversations {
		chat, err := types.ParseJID(c.ID)
		if err != nil || !isPlaceholderName(&c, chat) {
			continue
		}
		name := names.Name(chat)
		if chat.Server == types.GroupServer && name == fallbackName(chat) {
			w.learnGroupName(cli, chat)
			continue
		}
		if name != fallbackName(chat) {
			c.Name = name
			for i := range c.Participants {
				c.Participants[i].Name = name
			}
			cc := c
			w.env.Store.UpsertConversation(&cc)
			n++
		}
	}
	if n > 0 {
		w.env.Store.Flush()
		w.env.Log.Info().Int("renamed", n).Msg("Named chats from contacts")
	}
}

func (w *WhatsApp) conversationCopy(id string) *core.Conversation {
	c := w.env.Store.Conversation(id)
	if c == nil {
		return nil
	}
	cp := *c
	cp.Extra = map[string]any{}
	for k, v := range c.Extra {
		cp.Extra[k] = v
	}
	return &cp
}

func extraInt64(c *core.Conversation, k string) int64 {
	switch v := c.Extra[k].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func muted(c *core.Conversation) bool {
	return extraInt64(c, "muteUntil") > time.Now().UnixMilli()
}

// --- history sync ---------------------------------------------------------------

func (w *WhatsApp) onHistory(cli *whatsmeow.Client, e *events.HistorySync) {
	names := contactNames{w: w, cli: cli}
	n := 0
	for _, hc := range e.Data.GetConversations() {
		jid, err := types.ParseJID(hc.GetID())
		if err != nil {
			continue
		}
		if jid.Server == types.HiddenUserServer && hc.GetPnJID() != "" {
			if pn, err := types.ParseJID(hc.GetPnJID()); err == nil {
				jid = pn
			}
		}
		chat := w.chatID(cli, jid)
		c, msgs := convertHistory(hc, chat, names, func(hm *waHistorySync.HistorySyncMsg) (*events.Message, error) {
			return cli.ParseWebMessage(chat, hm.GetMessage())
		})
		if c == nil {
			continue
		}
		// Keep what a newer chunk or a live message already told us.
		if old := w.conversationCopy(c.ID); old != nil {
			if c.Name == fallbackName(chat) && old.Name != "" {
				c.Name = old.Name
			}
			if old.LastTs > c.LastTs {
				c.LastMessage, c.LastFromMe, c.LastSender, c.LastTs = old.LastMessage, old.LastFromMe, old.LastSender, old.LastTs
			}
		}
		w.env.Store.UpsertConversation(c)
		if len(msgs) > 0 {
			for i := range msgs {
				w.saveThumb(cli, &msgs[i])
				w.saveMediaKey(msgs[i].convID, msgs[i].msg.ID, msgs[i].raw)
			}
			w.mergeMessages(c.ID, msgs)
		}
		n++
	}
	w.env.Store.Flush()
	w.env.Log.Info().Str("type", e.Data.GetSyncType().String()).Int("conversations", n).Msg("History sync")
}

// mergeMessages adds a batch to a chat's thread with one write, keeping the
// newest keepPerChat.
func (w *WhatsApp) mergeMessages(convID string, batch []converted) {
	w.env.Store.Messages(convID)
	w.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		byID := map[string]core.Message{}
		for _, m := range cm.Messages {
			byID[m.ID] = m
		}
		for _, cv := range batch {
			byID[cv.msg.ID] = cv.msg
		}
		out := make([]core.Message, 0, len(byID))
		for _, m := range byID {
			out = append(out, m)
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Ts < out[j].Ts })
		if len(out) > keepPerChat {
			out = out[len(out)-keepPerChat:]
		}
		cm.Messages = out
		cm.HasMore = false
	})
}

// --- live events ---------------------------------------------------------------

func (w *WhatsApp) onMessage(cli *whatsmeow.Client, e *events.Message) {
	chat := w.chatID(cli, e.Info.Chat)
	convID := chat.String()
	if ch, ok := protocol(e); ok {
		w.applyChange(convID, ch)
		return
	}
	names := contactNames{w: w, cli: cli}
	cv, ok := convertEvent(e, chat, names)
	if !ok {
		return
	}
	w.saveThumb(cli, &cv)
	w.saveMediaKey(convID, cv.msg.ID, cv.raw)
	w.env.Store.MarkAlive()
	w.env.Store.Messages(convID)
	w.env.Store.UpsertMessage(convID, cv.msg)

	c := w.conversationCopy(convID)
	if c == nil {
		c = &core.Conversation{ID: convID, Name: names.Name(chat), IsGroup: chat.Server == types.GroupServer, Extra: map[string]any{}}
		if !c.IsGroup {
			c.Participants = []core.Participant{{ID: convID, Name: c.Name, Number: fallbackName(chat)}}
		}
		if c.IsGroup {
			w.env.Spawn(func() { w.learnGroupName(cli, chat) })
		}
	}
	if cv.msg.Ts >= c.LastTs {
		c.LastMessage, c.LastFromMe, c.LastTs = preview(cv.msg), cv.msg.FromMe, cv.msg.Ts
		c.LastSender = ""
		if c.IsGroup && !cv.msg.FromMe {
			c.LastSender = firstName(cv.msg.Sender)
		}
	}
	if !cv.msg.FromMe {
		c.Unread = true
		c.Extra["unreadCount"] = extraInt64(c, "unreadCount") + 1
	}
	w.env.Store.UpsertConversation(c)
	w.env.Store.Flush()

	if !cv.msg.FromMe && !muted(c) {
		title := cv.msg.Sender
		if c.IsGroup {
			title = c.Name + " · " + cv.msg.Sender
		}
		w.env.Notify(core.Notification{ConvID: convID, Title: title, Body: preview(cv.msg)})
	}
}

// learnGroupName asks WhatsApp for a group's name the first time a
// message arrives from a group the history sync didn't name.
func (w *WhatsApp) learnGroupName(cli *whatsmeow.Client, chat types.JID) {
	ctx, cancel := context.WithTimeout(w.lifeCtx(), 20*time.Second)
	defer cancel()
	info, err := cli.GetGroupInfo(ctx, chat)
	if err != nil || info.Name == "" {
		return
	}
	if c := w.conversationCopy(chat.String()); c != nil {
		c.Name = info.Name
		w.env.Store.UpsertConversation(c)
		w.env.Store.Flush()
	}
}

// applyChange edits or blanks out an earlier message.
func (w *WhatsApp) applyChange(convID string, ch protocolChange) {
	if !w.env.Store.HasMessages(convID) {
		w.env.Store.Messages(convID)
	}
	w.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		for i := range cm.Messages {
			m := &cm.Messages[i]
			if m.ID != ch.targetID {
				continue
			}
			if ch.deleted {
				m.Text, m.Attachments = "This message was deleted", nil
			} else {
				m.Text = ch.newText
				m.StatusText = "edited"
			}
		}
	})
}

// onReceipt: delivered/read ticks on our messages, or we read a chat on
// another device.
func (w *WhatsApp) onReceipt(cli *whatsmeow.Client, e *events.Receipt) {
	convID := w.chatID(cli, e.Chat).String()
	switch e.Type {
	case types.ReceiptTypeReadSelf:
		if c := w.conversationCopy(convID); c != nil && c.Unread {
			c.Unread = false
			c.Extra["unreadCount"] = 0
			w.env.Store.UpsertConversation(c)
			w.env.Store.Flush()
		}
		return
	case types.ReceiptTypeDelivered, types.ReceiptTypeRead, types.ReceiptTypePlayed:
	default:
		return
	}
	status := "delivered"
	if e.Type != types.ReceiptTypeDelivered {
		status = "read"
	}
	ids := map[string]bool{}
	for _, id := range e.MessageIDs {
		ids[id] = true
	}
	w.env.Store.Messages(convID)
	w.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		for i := range cm.Messages {
			m := &cm.Messages[i]
			if ids[m.ID] && m.FromMe && m.Status != "read" {
				m.Status = status
			}
		}
	})
}

// onMarkRead: the chat was marked read or unread on the phone.
func (w *WhatsApp) onMarkRead(cli *whatsmeow.Client, e *events.MarkChatAsRead) {
	c := w.conversationCopy(w.chatID(cli, e.JID).String())
	if c == nil {
		return
	}
	read := e.Action.GetRead()
	c.Unread = !read
	if read {
		c.Extra["unreadCount"] = 0
		c.Extra["readUpTo"] = e.Timestamp.UnixMilli()
	}
	w.env.Store.UpsertConversation(c)
	w.env.Store.Flush()
}

// --- commands ----------------------------------------------------------------------

func (w *WhatsApp) Refresh(ctx context.Context) error {
	if w.connected() != nil {
		w.env.Store.MarkAlive()
	}
	return nil
}

func (w *WhatsApp) connected() *whatsmeow.Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.client != nil && w.linked {
		return w.client
	}
	return nil
}

// Open shows the locally kept thread and marks it read on the phone.
func (w *WhatsApp) Open(ctx context.Context, convID string, markRead bool) error {
	w.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		cm.Loading, cm.Error, cm.HasMore = false, "", false
	})
	c := w.conversationCopy(convID)
	cli := w.connected()
	chat, err := types.ParseJID(convID)
	if err != nil {
		return err
	}
	if cli != nil {
		w.active(ctx, chat)
	}
	if !markRead || c == nil || !c.Unread || cli == nil {
		return nil
	}
	readUpTo := extraInt64(c, "readUpTo")
	// Receipts go per sender (required in groups); only for messages not
	// already reported read.
	bySender := map[string][]types.MessageID{}
	var newest int64
	for _, m := range w.env.Store.Messages(convID).Messages {
		if m.FromMe || m.Ts <= readUpTo {
			continue
		}
		bySender[m.SenderID] = append(bySender[m.SenderID], m.ID)
		if m.Ts > newest {
			newest = m.Ts
		}
	}
	for sender, ids := range bySender {
		var sjid types.JID
		if chat.Server == types.GroupServer {
			if sjid, err = types.ParseJID(sender); err != nil {
				continue
			}
		}
		if err := cli.MarkRead(ctx, ids, time.Now(), chat, sjid); err != nil {
			w.env.Log.Warn().Err(err).Msg("Marking read failed")
			return nil
		}
	}
	c.Unread = false
	c.Extra["unreadCount"] = 0
	if newest > readUpTo {
		c.Extra["readUpTo"] = newest
	}
	w.env.Store.UpsertConversation(c)
	w.env.Store.Flush()
	return nil
}

// More has nothing to fetch: a linked device gets history only once.
func (w *WhatsApp) More(ctx context.Context, convID string) error {
	w.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) { cm.Loading, cm.HasMore = false, false })
	return nil
}
