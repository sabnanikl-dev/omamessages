package whatsapp

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// While a linked device is "online", WhatsApp stops pushing notifications to
// the phone, like with WhatsApp Web. Typing indicators need it online,
// though. So the daemon goes online only while the user is active in a
// WhatsApp chat in the panel (opening it, typing in it), and back offline
// after idle with nothing happening.

const presenceIdle = 90 * time.Second

type presence struct {
	mu     sync.Mutex
	online bool
	timer  *time.Timer
	idle   time.Duration
	send   func(available bool) error
}

// touch goes online if needed and restarts the idle countdown.
func (p *presence) touch() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.online {
		if err := p.send(true); err != nil {
			return err
		}
		p.online = true
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(p.idle, p.stop)
	return nil
}

// stop goes offline now (idle timeout, disconnect, shutdown).
func (p *presence) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if p.online {
		_ = p.send(false)
		p.online = false
	}
}

func (w *WhatsApp) presenceFor() *presence {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pres == nil {
		w.pres = &presence{idle: presenceIdle, send: func(available bool) error {
			var err error
			w.env.Protect(func() {
				cli := w.connected()
				if cli == nil {
					return
				}
				ctx, cancel := context.WithTimeout(w.lifeCtx(), 10*time.Second)
				defer cancel()
				state := types.PresenceUnavailable
				if available {
					state = types.PresenceAvailable
				}
				err = cli.SendPresence(ctx, state)
			})
			return err
		}}
	}
	return w.pres
}

// active: the user is in this chat in the panel. Go online, and for a
// person, ask to hear their typing.
func (w *WhatsApp) active(ctx context.Context, chat types.JID) {
	cli := w.connected()
	if cli == nil {
		return
	}
	if err := w.presenceFor().touch(); err != nil {
		w.env.Log.Debug().Err(err).Msg("Could not go online")
		return
	}
	if chat.Server != types.DefaultUserServer {
		return // groups send typing to members that are online
	}
	w.mu.Lock()
	if w.subscribed == nil {
		w.subscribed = map[types.JID]bool{}
	}
	done := w.subscribed[chat]
	w.subscribed[chat] = true
	w.mu.Unlock()
	if !done {
		if err := cli.SubscribePresence(ctx, chat); err != nil {
			w.env.Log.Debug().Err(err).Msg("Presence subscription failed")
		}
	}
}

// SetTyping shows (or stops showing) the other side that the user is typing.
func (w *WhatsApp) SetTyping(ctx context.Context, convID string, typing bool) error {
	cli := w.connected()
	if cli == nil {
		return nil
	}
	chat, err := types.ParseJID(convID)
	if err != nil {
		return err
	}
	state := types.ChatPresencePaused
	if typing {
		w.active(ctx, chat)
		state = types.ChatPresenceComposing
	}
	return cli.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText)
}

// onChatPresence: someone is typing (or recording) in a chat.
func (w *WhatsApp) onChatPresence(e *events.ChatPresence) {
	w.mu.Lock()
	cli := w.client
	w.mu.Unlock()
	if cli == nil || e.IsFromMe {
		return
	}
	until := time.Now()
	if e.State == types.ChatPresenceComposing {
		until = until.Add(8 * time.Second)
	}
	w.env.Store.SetTyping(w.chatID(cli, e.Chat).String(), until)
}
