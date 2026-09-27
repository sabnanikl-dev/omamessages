package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	"omarchy-omamessages/core"
)

const (
	pageSize    = 40
	dialogLimit = 60
)

var errNotConnected = errors.New("Telegram is not connected")

// router lets the client's update handler change after it is built: raw
// updates go to the dispatcher while signing in (the QR login needs them),
// then through the peers hook and the gap-filling updates manager.
type router struct {
	mu sync.RWMutex
	h  telegram.UpdateHandler
}

func (r *router) Handle(ctx context.Context, u tg.UpdatesClass) error {
	r.mu.RLock()
	h := r.h
	r.mu.RUnlock()
	return h.Handle(ctx, u)
}

func (r *router) set(h telegram.UpdateHandler) {
	r.mu.Lock()
	r.h = h
	r.mu.Unlock()
}

// liveSession is everything that exists only while signed in.
type liveSession struct {
	client *telegram.Client
	api    *tg.Client
	peers  *peers.Manager
	gaps   *updates.Manager

	mu     sync.Mutex
	inputs map[string]tg.InputPeerClass // native conversation ID → how to address it
	oldest map[string]int               // oldest loaded message ID per conversation, for More
	media  *mediaState
}

func (s *liveSession) remember(ents peer.Entities, p tg.PeerClass) {
	in, err := ents.ExtractPeer(p)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.inputs[nativeID(p)] = in
	s.mu.Unlock()
}

// inputPeer addresses a conversation, from what dialogs and updates have
// shown us, else by asking Telegram.
func (s *liveSession) inputPeer(ctx context.Context, convID string) (tg.InputPeerClass, error) {
	s.mu.Lock()
	in, ok := s.inputs[convID]
	s.mu.Unlock()
	if ok {
		return in, nil
	}
	n, err := strconv.ParseInt(convID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("not a Telegram chat id: %q", convID)
	}
	p, err := s.peers.ResolveTDLibID(ctx, constant.TDLibPeerID(n))
	if err != nil {
		return nil, fmt.Errorf("could not find that chat: %w", err)
	}
	in = p.InputPeer()
	s.mu.Lock()
	s.inputs[convID] = in
	s.mu.Unlock()
	return in, nil
}

func (t *Telegram) live() *liveSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sess
}

// startSession begins receiving live updates for the signed-in client and
// loads the chat list. It does nothing if one is already running for it.
func (t *Telegram) startSession() {
	t.mu.Lock()
	cli, r, d, ctx := t.client, t.router, t.dispatcher, t.runCtx
	if cli == nil || (t.sess != nil && t.sess.client == cli) {
		t.mu.Unlock()
		return
	}
	api := cli.API()
	s := &liveSession{
		client: cli,
		api:    api,
		peers:  peers.Options{}.Build(api),
		gaps:   updates.New(updates.Config{Handler: d}),
		inputs: map[string]tg.InputPeerClass{},
		oldest: map[string]int{},
		media:  newMediaState(t.env.Spawn),
	}
	t.sess = s
	t.mu.Unlock()
	r.set(s.peers.UpdateHook(s.gaps))

	t.env.Spawn(func() {
		self, err := cli.Self(ctx)
		if err != nil {
			t.env.Log.Warn().Err(err).Msg("Could not look up the signed-in account")
			return
		}
		// Update state is kept in memory only: after a restart the chat list
		// is fetched afresh instead of replaying what was missed.
		err = s.gaps.Run(ctx, api, self.ID, updates.AuthOptions{
			Forget: true,
			OnStart: func(context.Context) {
				t.env.Spawn(func() {
					if err := t.Refresh(ctx); err != nil {
						t.env.Log.Warn().Err(err).Msg("Loading Telegram chats failed")
					}
				})
			},
		})
		if err != nil && ctx.Err() == nil {
			t.env.Log.Warn().Err(err).Msg("Telegram updates stopped")
		}
	})
}

// --- updates ---------------------------------------------------------------------

// handleUpdates registers what the panel cares about. Each handler runs
// under the hub's panic isolation: gotd calls them on its own goroutines.
func (t *Telegram) handleUpdates(d tg.UpdateDispatcher) {
	guard := func(fn func()) error { t.env.Protect(fn); return nil }
	d.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		return guard(func() { t.onMessage(e, u.Message, true) })
	})
	d.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		return guard(func() { t.onMessage(e, u.Message, true) })
	})
	d.OnEditMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditMessage) error {
		return guard(func() { t.onMessage(e, u.Message, false) })
	})
	d.OnEditChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		return guard(func() { t.onMessage(e, u.Message, false) })
	})
	d.OnUserTyping(func(ctx context.Context, e tg.Entities, u *tg.UpdateUserTyping) error {
		return guard(func() { t.onTyping(&tg.PeerUser{UserID: u.UserID}, u.Action) })
	})
	d.OnChatUserTyping(func(ctx context.Context, e tg.Entities, u *tg.UpdateChatUserTyping) error {
		return guard(func() { t.onTyping(&tg.PeerChat{ChatID: u.ChatID}, u.Action) })
	})
	d.OnChannelUserTyping(func(ctx context.Context, e tg.Entities, u *tg.UpdateChannelUserTyping) error {
		return guard(func() { t.onTyping(&tg.PeerChannel{ChannelID: u.ChannelID}, u.Action) })
	})
	d.OnReadHistoryInbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadHistoryInbox) error {
		return guard(func() { t.onReadInbox(nativeID(u.Peer), u.StillUnreadCount) })
	})
	d.OnReadChannelInbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadChannelInbox) error {
		return guard(func() { t.onReadInbox(nativeID(&tg.PeerChannel{ChannelID: u.ChannelID}), u.StillUnreadCount) })
	})
	d.OnReadHistoryOutbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadHistoryOutbox) error {
		return guard(func() { t.onReadOutbox(nativeID(u.Peer), u.MaxID) })
	})
	d.OnReadChannelOutbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadChannelOutbox) error {
		return guard(func() { t.onReadOutbox(nativeID(&tg.PeerChannel{ChannelID: u.ChannelID}), u.MaxID) })
	})
}

// conversationCopy returns a copy safe to modify, or nil.
func (t *Telegram) conversationCopy(id string) *core.Conversation {
	c := t.env.Store.Conversation(id)
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

func extraInt(c *core.Conversation, k string) int {
	switch v := c.Extra[k].(type) {
	case int:
		return v
	case float64: // after a round trip through state.json
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

func muted(c *core.Conversation) bool {
	switch v := c.Extra["muteUntil"].(type) {
	case int64:
		return v > time.Now().UnixMilli()
	case float64:
		return int64(v) > time.Now().UnixMilli()
	}
	return false
}

// onMessage takes a new or edited message from an update.
func (t *Telegram) onMessage(e tg.Entities, mc tg.MessageClass, isNew bool) {
	m, ok := mc.(*tg.Message)
	if !ok {
		return
	}
	ents := peer.EntitiesFromUpdate(e)
	convID := nativeID(m.PeerID)
	if s := t.live(); s != nil {
		s.remember(ents, m.PeerID)
	}
	t.env.Store.MarkAlive()
	c := t.conversationCopy(convID)
	readMax := 0
	if c != nil {
		readMax = extraInt(c, "readOutboxMax")
	}
	msg := convertMessage(m, ents, readMax)
	annotate(t.env.CacheDir, convID, m.ID, &msg)
	if t.env.Store.HasMessages(convID) {
		t.env.Store.UpsertMessage(convID, msg)
		if s := t.live(); s != nil && m.Media != nil {
			s.media.remember(convID, m.ID, m.Media)
			t.queueThumb(s, convID, m.ID, m.Media)
		}
	}
	if !isNew {
		return
	}
	if c == nil {
		name, kind := peerInfo(m.PeerID, ents)
		c = &core.Conversation{ID: convID, Name: name, IsGroup: kind == "group", Extra: map[string]any{"kind": kind}}
	}
	if msg.Ts >= c.LastTs {
		c.LastMessage = preview(msg)
		c.LastFromMe = msg.FromMe
		c.LastTs = msg.Ts
		c.LastSender = ""
		if c.IsGroup && !msg.FromMe {
			c.LastSender = firstName(msg.Sender)
		}
	}
	if !m.Out {
		c.Unread = true
		c.Extra["unreadCount"] = extraInt(c, "unreadCount") + 1
	}
	t.env.Store.SetTyping(convID, time.Now())
	t.env.Store.UpsertConversation(c)
	t.env.Store.Flush()

	if !m.Out && !muted(c) {
		title := msg.Sender
		if c.IsGroup {
			title = c.Name + " · " + msg.Sender
		} else if title == "" {
			title = c.Name
		}
		t.env.Notify(core.Notification{ConvID: convID, Title: title, Body: preview(msg)})
	}
}

func (t *Telegram) onTyping(p tg.PeerClass, action tg.SendMessageActionClass) {
	until := time.Now().Add(6 * time.Second)
	if _, ok := action.(*tg.SendMessageCancelAction); ok {
		until = time.Now()
	}
	t.env.Store.SetTyping(nativeID(p), until)
}

// onReadInbox: we read the chat somewhere else.
func (t *Telegram) onReadInbox(convID string, stillUnread int) {
	c := t.conversationCopy(convID)
	if c == nil {
		return
	}
	c.Unread = stillUnread > 0
	c.Extra["unreadCount"] = stillUnread
	t.env.Store.UpsertConversation(c)
	t.env.Store.Flush()
}

// onReadOutbox: they read our messages up to maxID.
func (t *Telegram) onReadOutbox(convID string, maxID int) {
	c := t.conversationCopy(convID)
	if c == nil {
		return
	}
	c.Extra["readOutboxMax"] = maxID
	t.env.Store.UpsertConversation(c)
	t.env.Store.Flush()
	if t.env.Store.HasMessages(convID) {
		t.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
			for i := range cm.Messages {
				m := &cm.Messages[i]
				if id, err := strconv.Atoi(m.ID); err == nil && m.FromMe && id <= maxID && m.Status == "sent" {
					m.Status = "read"
				}
			}
		})
	}
}

// --- commands --------------------------------------------------------------------

func (t *Telegram) Refresh(ctx context.Context) error {
	s := t.live()
	if s == nil {
		return nil // not signed in: nothing to list
	}
	res, err := s.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: dialogLimit})
	if err != nil {
		return friendly(err)
	}
	var (
		dialogs []tg.DialogClass
		msgs    []tg.MessageClass
		users   []tg.UserClass
		chats   []tg.ChatClass
	)
	switch v := res.(type) {
	case *tg.MessagesDialogs:
		dialogs, msgs, users, chats = v.Dialogs, v.Messages, v.Users, v.Chats
	case *tg.MessagesDialogsSlice:
		dialogs, msgs, users, chats = v.Dialogs, v.Messages, v.Users, v.Chats
	default:
		return nil
	}
	_ = s.peers.Apply(ctx, users, chats)
	ents := entities(users, chats)
	top := map[string]tg.MessageClass{}
	for _, mc := range msgs {
		switch m := mc.(type) {
		case *tg.Message:
			top[nativeID(m.PeerID)+"/"+strconv.Itoa(m.ID)] = m
		case *tg.MessageService:
			top[nativeID(m.PeerID)+"/"+strconv.Itoa(m.ID)] = m
		}
	}
	for _, dc := range dialogs {
		d, ok := dc.(*tg.Dialog)
		if !ok {
			continue
		}
		s.remember(ents, d.Peer)
		c := convertDialog(d, top[nativeID(d.Peer)+"/"+strconv.Itoa(d.TopMessage)], ents)
		t.env.Store.UpsertConversation(c)
	}
	t.env.Store.MarkAlive()
	t.env.Store.Flush()
	return nil
}

// history splits the several shapes messages.getHistory can answer with.
func history(res tg.MessagesMessagesClass) (msgs []tg.MessageClass, users []tg.UserClass, chats []tg.ChatClass) {
	switch v := res.(type) {
	case *tg.MessagesMessages:
		return v.Messages, v.Users, v.Chats
	case *tg.MessagesMessagesSlice:
		return v.Messages, v.Users, v.Chats
	case *tg.MessagesChannelMessages:
		return v.Messages, v.Users, v.Chats
	}
	return nil, nil, nil
}

// fetch loads one page of a conversation, newest first from offsetID (0 for
// the newest), and merges it into the cached thread.
func (t *Telegram) fetch(ctx context.Context, s *liveSession, convID string, offsetID int) (newest int, err error) {
	in, err := s.inputPeer(ctx, convID)
	if err != nil {
		return 0, err
	}
	res, err := s.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: in, OffsetID: offsetID, Limit: pageSize})
	if err != nil {
		return 0, friendly(err)
	}
	msgs, users, chats := history(res)
	_ = s.peers.Apply(ctx, users, chats)
	ents := entities(users, chats)
	readMax := 0
	if c := t.env.Store.Conversation(convID); c != nil {
		readMax = extraInt(c, "readOutboxMax")
	}
	oldest := 0
	var page []core.Message
	for _, mc := range msgs {
		m, ok := mc.(*tg.Message)
		if !ok {
			continue
		}
		cm := convertMessage(m, ents, readMax)
		annotate(t.env.CacheDir, convID, m.ID, &cm)
		page = append(page, cm)
		if m.Media != nil {
			s.media.remember(convID, m.ID, m.Media)
			defer t.queueThumb(s, convID, m.ID, m.Media) // after the page is in the store
		}
		if oldest == 0 || m.ID < oldest {
			oldest = m.ID
		}
		if m.ID > newest {
			newest = m.ID
		}
	}
	s.mu.Lock()
	if oldest > 0 && (s.oldest[convID] == 0 || oldest < s.oldest[convID]) {
		s.oldest[convID] = oldest
	}
	s.mu.Unlock()
	t.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		cm.Loading = false
		cm.Error = ""
		cm.HasMore = len(msgs) >= pageSize
		byID := map[string]core.Message{}
		for _, m := range cm.Messages {
			byID[m.ID] = m
		}
		for _, m := range page {
			byID[m.ID] = m
		}
		cm.Messages = cm.Messages[:0]
		for _, m := range byID {
			cm.Messages = append(cm.Messages, m)
		}
		cm.Messages = core.DropStalePlaceholders(cm.Messages)
		sortMessages(cm.Messages)
	})
	return newest, nil
}

func sortMessages(ms []core.Message) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].Ts < ms[j-1].Ts; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

// Open loads the newest page of a conversation and marks it read.
func (t *Telegram) Open(ctx context.Context, convID string, markRead bool) error {
	s := t.live()
	if s == nil {
		return errNotConnected
	}
	t.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) { cm.Loading = true; cm.Error = "" })
	newest, err := t.fetch(ctx, s, convID, 0)
	if err != nil {
		t.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) { cm.Loading = false; cm.Error = err.Error() })
		return err
	}
	t.catchUpThumbs(s, convID)
	c := t.conversationCopy(convID)
	if markRead && c != nil && c.Unread && newest > 0 {
		if err := t.markRead(ctx, s, convID, newest); err != nil {
			t.env.Log.Warn().Err(err).Msg("Marking read failed")
		} else {
			c.Unread = false
			c.Extra["unreadCount"] = 0
			t.env.Store.UpsertConversation(c)
			t.env.Store.Flush()
		}
	}
	return nil
}

func (t *Telegram) markRead(ctx context.Context, s *liveSession, convID string, maxID int) error {
	in, err := s.inputPeer(ctx, convID)
	if err != nil {
		return err
	}
	if ch, ok := in.(*tg.InputPeerChannel); ok {
		_, err := s.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			MaxID:   maxID,
		})
		return err
	}
	affected, err := s.api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: in, MaxID: maxID})
	if err != nil {
		return err
	}
	return s.gaps.HandleAffected(ctx, 0, affected.Pts, affected.PtsCount)
}

// More loads the page before the oldest message loaded so far.
func (t *Telegram) More(ctx context.Context, convID string) error {
	s := t.live()
	if s == nil {
		return errNotConnected
	}
	s.mu.Lock()
	oldest := s.oldest[convID]
	s.mu.Unlock()
	if oldest == 0 {
		return t.Open(ctx, convID, false)
	}
	t.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) { cm.Loading = true })
	if _, err := t.fetch(ctx, s, convID, oldest); err != nil {
		t.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) { cm.Loading = false; cm.Error = err.Error() })
		return err
	}
	return nil
}

func randomID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1)
}

// sentID finds our message's ID in what messages.sendMessage returned.
func sentID(u tg.UpdatesClass, rid int64) int {
	switch v := u.(type) {
	case *tg.UpdateShortSentMessage:
		return v.ID
	case *tg.Updates:
		for _, up := range v.Updates {
			if m, ok := up.(*tg.UpdateMessageID); ok && m.RandomID == rid {
				return m.ID
			}
		}
	case *tg.UpdatesCombined:
		for _, up := range v.Updates {
			if m, ok := up.(*tg.UpdateMessageID); ok && m.RandomID == rid {
				return m.ID
			}
		}
	}
	return 0
}

// Send sends a text message. It shows as "sending" at once and is replaced
// by the confirmed message when Telegram answers.
func (t *Telegram) Send(ctx context.Context, convID, text string, files []string) error {
	s := t.live()
	if s == nil {
		return errNotConnected
	}
	text = strings.TrimSpace(text)
	if text == "" && len(files) == 0 {
		return errors.New("empty message")
	}
	in, err := s.inputPeer(ctx, convID)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		return t.sendWithFiles(ctx, s, convID, in, text, files)
	}
	rid := randomID()
	tmp := "tmp_" + strconv.FormatInt(rid, 36)
	now := time.Now().UnixMilli()
	pending := core.Message{ID: tmp, TmpID: tmp, Ts: now, FromMe: true, Sender: "Me", Text: text, Status: "sending"}
	if t.env.Store.HasMessages(convID) {
		t.env.Store.UpsertMessage(convID, pending)
	}
	if c := t.conversationCopy(convID); c != nil {
		c.LastMessage, c.LastFromMe, c.LastSender, c.LastTs = text, true, "", now
		t.env.Store.UpsertConversation(c)
		t.env.Store.Flush()
	}
	upd, err := s.api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: in, Message: text, RandomID: rid})
	if err != nil {
		err = friendly(err)
		if t.env.Store.HasMessages(convID) {
			pending.Status, pending.StatusText = "failed", err.Error()
			t.env.Store.UpsertMessage(convID, pending)
		}
		return err
	}
	if id := sentID(upd, rid); id > 0 && t.env.Store.HasMessages(convID) {
		t.env.Store.UpsertMessage(convID, core.Message{ID: strconv.Itoa(id), TmpID: tmp, Ts: now, FromMe: true, Sender: "Me", Text: text, Status: "sent"})
	}
	// Keep the updates manager's sequence in step with what we just did.
	if err := s.gaps.Handle(ctx, upd); err != nil {
		t.env.Log.Debug().Err(err).Msg("Updates manager rejected the send result")
	}
	return nil
}

// StartChat finds someone by "id:<chat id>" (from Contacts), "@username" or
// a t.me link, or a phone number, adds the chat to the list, and sends text
// into it if there is any.
func (t *Telegram) StartChat(ctx context.Context, to, text string) (string, error) {
	s := t.live()
	if s == nil {
		return "", errNotConnected
	}
	to = strings.TrimSpace(to)
	var (
		p   peers.Peer
		err error
	)
	switch {
	case strings.HasPrefix(to, "id:"):
		n, perr := strconv.ParseInt(strings.TrimPrefix(to, "id:"), 10, 64)
		if perr != nil {
			return "", errors.New("bad chat id")
		}
		p, err = s.peers.ResolveTDLibID(ctx, constant.TDLibPeerID(n))
	case strings.HasPrefix(to, "@") || strings.Contains(to, "t.me/"):
		p, err = s.peers.Resolve(ctx, to)
	default:
		phone, perr := normalizePhone(to)
		if perr != nil {
			return "", errors.New("enter a @username, a t.me link, or a phone number with its country code")
		}
		var u peers.User
		u, err = s.peers.ResolvePhone(ctx, strings.TrimPrefix(phone, "+"))
		p = u
		if err != nil {
			return "", errors.New("Telegram found nobody with that number who allows being found by it")
		}
	}
	if err != nil {
		return "", fmt.Errorf("could not find %s: %w", to, friendly(err))
	}
	convID := strconv.FormatInt(int64(p.TDLibPeerID()), 10)
	s.mu.Lock()
	s.inputs[convID] = p.InputPeer()
	s.mu.Unlock()
	if t.env.Store.Conversation(convID) == nil {
		t.env.Store.UpsertConversation(&core.Conversation{
			ID: convID, Name: p.VisibleName(), LastTs: time.Now().UnixMilli(),
			IsGroup: !p.TDLibPeerID().IsUser(), Extra: map[string]any{},
		})
		t.env.Store.Flush()
	}
	if strings.TrimSpace(text) != "" {
		t.env.Store.Messages(convID)
		if err := t.Send(ctx, convID, text, nil); err != nil {
			return convID, err
		}
	}
	return convID, nil
}

// Contacts lists the account's contacts, or searches people, groups and
// channels by name or username. IDs come back as "id:<chat id>" for
// StartChat.
func (t *Telegram) Contacts(ctx context.Context, query string) ([]core.Participant, error) {
	s := t.live()
	if s == nil {
		return nil, errNotConnected
	}
	query = strings.TrimSpace(query)
	var (
		users []tg.UserClass
		chats []tg.ChatClass
		order []tg.PeerClass
	)
	if query == "" {
		res, err := s.api.ContactsGetContacts(ctx, 0)
		if err != nil {
			return nil, friendly(err)
		}
		c, ok := res.(*tg.ContactsContacts)
		if !ok {
			return nil, nil
		}
		users = c.Users
		for _, u := range c.Users {
			if v, ok := u.(*tg.User); ok {
				order = append(order, &tg.PeerUser{UserID: v.ID})
			}
		}
	} else {
		res, err := s.api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: strings.TrimPrefix(query, "@"), Limit: 20})
		if err != nil {
			return nil, friendly(err)
		}
		users, chats = res.Users, res.Chats
		order = append(append(order, res.MyResults...), res.Results...)
	}
	_ = s.peers.Apply(ctx, users, chats)
	ents := entities(users, chats)
	var out []core.Participant
	seen := map[string]bool{}
	for _, p := range order {
		id := nativeID(p)
		if seen[id] {
			continue
		}
		seen[id] = true
		s.remember(ents, p)
		name, _ := peerInfo(p, ents)
		handle := ""
		if u, ok := p.(*tg.PeerUser); ok {
			if user, ok := ents.Users()[u.UserID]; ok {
				handle = userHandle(user)
			}
		} else if c, ok := p.(*tg.PeerChannel); ok {
			if ch, ok := ents.Channel(c.ChannelID); ok && ch.Username != "" {
				handle = "@" + ch.Username
			}
		}
		out = append(out, core.Participant{ID: "id:" + id, Name: name, Number: handle})
	}
	return out, nil
}

// SetTyping shows (or stops showing) the other side that the user is typing.
// Telegram shows it for about 6 s, so the panel repeats it while typing.
func (t *Telegram) SetTyping(ctx context.Context, convID string, typing bool) error {
	s := t.live()
	if s == nil {
		return nil
	}
	in, err := s.inputPeer(ctx, convID)
	if err != nil {
		return err
	}
	var action tg.SendMessageActionClass = &tg.SendMessageCancelAction{}
	if typing {
		action = &tg.SendMessageTypingAction{}
	}
	_, err = s.api.MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: in, Action: action})
	return err
}
