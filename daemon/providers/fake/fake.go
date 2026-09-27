// Package fake is a messaging service that talks to nobody. It seeds a few
// canned conversations, confirms whatever you send, and answers back after a
// moment, so the panel and the hub can be exercised end to end without any
// account. Start it with `omamessagesd serve --providers fake`.
package fake

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"omarchy-omamessages/core"
)

const ID core.ProviderID = "fake"

type Fake struct {
	id   core.ProviderID
	name string
	env  core.Env

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	flow   *flow // the connect flow in progress, if any
	seq    atomic.Int64
	// ReplyDelay is how long the other side "types" before answering.
	ReplyDelay time.Duration
}

// New is the factory for the stock fake service.
func New(env core.Env) core.Provider { return Named(ID, "Fake")(env) }

// Named returns a factory for a fake service under another ID, so tests can
// run several side by side.
func Named(id core.ProviderID, name string) core.Factory {
	return func(env core.Env) core.Provider {
		return &Fake{id: id, name: name, env: env, ReplyDelay: 1500 * time.Millisecond}
	}
}

func (f *Fake) ID() core.ProviderID { return f.id }
func (f *Fake) Name() string        { return f.name }
func (f *Fake) Caps() core.Caps     { return core.Caps{Typing: true} }

func (f *Fake) Start(ctx context.Context) error {
	f.mu.Lock()
	f.ctx, f.cancel = context.WithCancel(ctx)
	f.mu.Unlock()
	if f.signedOut() {
		f.env.Store.SetStatus(core.StatusDisconnected, "")
		return nil
	}
	f.goOnline()
	return nil
}

// goOnline is the fake's "session resumed": seed if empty, then connected.
func (f *Fake) goOnline() {
	if len(f.env.Store.Snapshot().Conversations) == 0 {
		f.Seed(time.Now())
	}
	f.env.Store.SetAccount("demo@" + string(f.id))
	f.env.Store.MarkAlive()
	f.env.Store.SetStatus(core.StatusConnected, "")
}

func (f *Fake) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil {
		f.cancel()
	}
}

type cannedConv struct {
	id, name string
	group    bool
	unread   bool
	ago      time.Duration
	lines    []cannedLine
}

type cannedLine struct {
	fromMe bool
	sender string
	text   string
}

var canned = []cannedConv{
	{id: "ada", name: "Ada Lovelace", unread: true, ago: 4 * time.Minute, lines: []cannedLine{
		{false, "Ada Lovelace", "Did the engine run overnight?"},
		{true, "", "It did, all 40 cards."},
		{false, "Ada Lovelace", "Send me the output when you can"},
	}},
	{id: "team", name: "Fake team", group: true, unread: true, ago: 35 * time.Minute, lines: []cannedLine{
		{false, "Grace", "standup moved to 10"},
		{false, "Linus", "fine by me"},
		{true, "", "👍"},
		{false, "Grace", "merged, thanks!"},
	}},
	{id: "grace", name: "Grace Hopper", ago: 26 * time.Hour, lines: []cannedLine{
		{false, "Grace Hopper", "Found the bug. It was a moth."},
		{true, "", "Tape it in the logbook"},
	}},
}

// Seed writes the canned conversations, with timestamps relative to now.
func (f *Fake) Seed(now time.Time) {
	for _, c := range canned {
		last := c.lines[len(c.lines)-1]
		f.env.Store.UpsertConversation(&core.Conversation{
			ID:           c.id,
			Name:         c.name,
			LastMessage:  last.text,
			LastFromMe:   last.fromMe,
			LastSender:   last.sender,
			LastTs:       now.Add(-c.ago).UnixMilli(),
			Unread:       c.unread,
			IsGroup:      c.group,
			Participants: []core.Participant{{ID: c.id, Name: c.name}},
		})
	}
	f.env.Store.Flush()
}

func (f *Fake) Refresh(ctx context.Context) error {
	if f.env.Store.MarkAlive() {
		f.env.Store.Flush()
	}
	return nil
}

// Open fills the thread from the canned lines the first time, and marks the
// conversation read.
func (f *Fake) Open(ctx context.Context, convID string, markRead bool) error {
	conv := f.env.Store.Conversation(convID)
	if conv == nil {
		return fmt.Errorf("no conversation %q", convID)
	}
	cm := f.env.Store.Messages(convID)
	if len(cm.Messages) == 0 {
		for _, c := range canned {
			if c.id != convID {
				continue
			}
			base := conv.LastTs - int64(len(c.lines)-1)*int64(3*time.Minute/time.Millisecond)
			for i, l := range c.lines {
				f.env.Store.UpsertMessage(convID, f.message(l.fromMe, l.sender, l.text, base+int64(i)*int64(3*time.Minute/time.Millisecond)))
			}
		}
	}
	f.env.Store.FlushMessages(convID)
	if markRead && conv.Unread {
		c := *conv
		c.Unread = false
		f.env.Store.UpsertConversation(&c)
		f.env.Store.Flush()
	}
	return nil
}

func (f *Fake) More(ctx context.Context, convID string) error { return nil }

// Send shows the message as sending, confirms it shortly after, then has the
// other side type and answer.
func (f *Fake) Send(ctx context.Context, convID, text string, files []string) error {
	if files != nil {
		return core.ErrUnsupported
	}
	conv := f.env.Store.Conversation(convID)
	if conv == nil {
		return fmt.Errorf("no conversation %q", convID)
	}
	f.env.Store.Messages(convID)
	now := time.Now().UnixMilli()
	tmp := "tmp_" + strconv.FormatInt(f.seq.Add(1), 10)
	f.env.Store.UpsertMessage(convID, core.Message{ID: tmp, TmpID: tmp, Ts: now, FromMe: true, Text: text, Status: "sending"})
	f.bumpConversation(convID, text, true, "", false)

	f.mu.Lock()
	life := f.ctx
	f.mu.Unlock()
	if life == nil {
		life = context.Background()
	}
	f.env.Spawn(func() {
		if !sleep(life, 300*time.Millisecond) {
			return
		}
		sent := f.message(true, "", text, now)
		sent.TmpID = tmp
		sent.Status = "sent"
		f.env.Store.UpsertMessage(convID, sent)

		f.env.Store.SetTyping(convID, time.Now().Add(f.ReplyDelay+time.Second))
		if !sleep(life, f.ReplyDelay) {
			return
		}
		f.env.Store.SetTyping(convID, time.Now())
		sender := conv.Name
		if conv.IsGroup {
			sender = "Grace"
		}
		reply := "You said: " + text
		f.env.Store.UpsertMessage(convID, f.message(false, sender, reply, time.Now().UnixMilli()))
		f.bumpConversation(convID, reply, false, sender, true)
		title := sender
		if conv.IsGroup {
			title = conv.Name + " · " + sender
		}
		f.env.Notify(core.Notification{ConvID: convID, Title: title, Body: reply})
	})
	return nil
}

func (f *Fake) bumpConversation(convID, text string, fromMe bool, sender string, unread bool) {
	conv := f.env.Store.Conversation(convID)
	if conv == nil {
		return
	}
	c := *conv
	c.LastMessage = text
	c.LastFromMe = fromMe
	c.LastSender = sender
	c.LastTs = time.Now().UnixMilli()
	if unread {
		c.Unread = true
	}
	f.env.Store.UpsertConversation(&c)
	f.env.Store.Flush()
}

func (f *Fake) message(fromMe bool, sender, text string, ts int64) core.Message {
	status := "received"
	if fromMe {
		status = "read"
	}
	return core.Message{
		ID:       "m" + strconv.FormatInt(f.seq.Add(1), 10),
		Ts:       ts,
		FromMe:   fromMe,
		Sender:   sender,
		SenderID: sender,
		Text:     text,
		Status:   status,
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (f *Fake) StartChat(ctx context.Context, to, text string) (string, error) {
	return "", core.ErrUnsupported
}
func (f *Fake) Contacts(ctx context.Context, query string) ([]core.Participant, error) {
	return nil, core.ErrUnsupported
}
func (f *Fake) FetchMedia(ctx context.Context, convID, msgID string, idx int) (string, error) {
	return "", core.ErrUnsupported
}
