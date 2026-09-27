// Package whatsapp is WhatsApp, spoken with whatsmeow as a linked device of
// the user's phone (like WhatsApp Web). whatsmeow keeps its keys and contacts
// in its own SQLite store, whatsapp/store.db.
package whatsapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"omarchy-omamessages/core"
)

const ID = core.WhatsApp

var errNotConnected = errors.New("WhatsApp is not connected")

func init() {
	// Shown on the phone under Linked devices.
	store.SetOSInfo("Omarchy", [3]uint32{0, 1, 0})
}

type WhatsApp struct {
	env core.Env

	mu        sync.Mutex
	life      context.Context
	container *sqlstore.Container
	client    *whatsmeow.Client
	flow      *linkFlow
	linked    bool // this computer is a linked device, as far as we know
	dls       *downloads
	// limits concurrent thumbnail downloads
	thumbSlots chan struct{}
	pres       *presence
	subscribed map[types.JID]bool // people whose typing we asked to hear
}

func New(env core.Env) core.Provider { return &WhatsApp{env: env} }

func (w *WhatsApp) ID() core.ProviderID { return ID }
func (w *WhatsApp) Name() string        { return "WhatsApp" }
func (w *WhatsApp) Caps() core.Caps {
	return core.Caps{PhoneTethered: true, FetchMedia: true, StartByNumber: true, ContactSearch: true, Typing: true}
}

func (w *WhatsApp) lifeCtx() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.life != nil {
		return w.life
	}
	return context.Background()
}

func (w *WhatsApp) storePath() string { return filepath.Join(w.env.Dir, "store.db") }

// openStore opens whatsmeow's device store, creating it the first time.
func (w *WhatsApp) openStore(ctx context.Context) (*sqlstore.Container, error) {
	w.mu.Lock()
	c := w.container
	w.mu.Unlock()
	if c != nil {
		return c, nil
	}
	if err := os.MkdirAll(w.env.Dir, 0o700); err != nil {
		return nil, err
	}
	c, err := sqlstore.New(ctx, "sqlite3", "file:"+w.storePath()+"?_foreign_keys=on&_busy_timeout=5000",
		waLog.Zerolog(w.env.Log.With().Str("component", "whatsmeow-store").Logger()))
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(w.storePath(), 0o600)
	w.mu.Lock()
	w.container = c
	w.mu.Unlock()
	return c, nil
}

func (w *WhatsApp) newClient(dev *store.Device) *whatsmeow.Client {
	cli := whatsmeow.NewClient(dev, waLog.Zerolog(w.env.Log.With().Str("component", "whatsmeow").Logger()))
	// whatsmeow calls this on its own goroutines.
	cli.AddEventHandler(func(evt any) { w.env.Protect(func() { w.handle(cli, evt) }) })
	return cli
}

// Start resumes a linked device. Without one it stays disconnected and
// touches the network only when Connect is pressed.
func (w *WhatsApp) Start(ctx context.Context) error {
	w.mu.Lock()
	w.life = ctx
	w.mu.Unlock()
	if _, err := os.Stat(w.storePath()); err != nil {
		w.env.Store.SetStatus(core.StatusDisconnected, "")
		return nil
	}
	c, err := w.openStore(ctx)
	if err != nil {
		w.env.Store.SetStatus(core.StatusError, "Could not open the WhatsApp store: "+err.Error())
		return nil
	}
	dev, err := c.GetFirstDevice(ctx)
	if err != nil {
		w.env.Store.SetStatus(core.StatusError, "Could not read the WhatsApp store: "+err.Error())
		return nil
	}
	if dev.ID == nil {
		w.env.Store.SetStatus(core.StatusDisconnected, "")
		return nil
	}
	cli := w.newClient(dev)
	w.mu.Lock()
	w.client, w.linked = cli, true
	w.mu.Unlock()
	w.env.Store.SetStatus(core.StatusConnecting, "")
	if err := cli.Connect(); err != nil {
		// whatsmeow keeps retrying on its own for network errors.
		w.env.Store.SetStatus(core.StatusError, "Could not reach WhatsApp: "+err.Error())
	}
	return nil
}

func (w *WhatsApp) Stop() {
	w.presenceFor().stop()
	w.mu.Lock()
	cli, c, fl := w.client, w.container, w.flow
	w.mu.Unlock()
	if fl != nil {
		fl.done()
	}
	if cli != nil {
		cli.Disconnect()
	}
	if c != nil {
		_ = c.Close()
	}
}

func (w *WhatsApp) account(cli *whatsmeow.Client) string {
	if cli.Store.ID == nil {
		return ""
	}
	a := "+" + cli.Store.ID.User
	if cli.Store.PushName != "" {
		a += " · " + cli.Store.PushName
	}
	return a
}

// handle routes whatsmeow's events: messages and history to chats.go,
// connection changes to status.
func (w *WhatsApp) handle(cli *whatsmeow.Client, evt any) {
	w.mu.Lock()
	current := w.client == cli
	fl := w.flow
	w.mu.Unlock()
	if !current {
		return
	}
	switch e := evt.(type) {
	case *events.Message:
		w.onMessage(cli, e)
	case *events.HistorySync:
		w.onHistory(cli, e)
	case *events.Receipt:
		w.onReceipt(cli, e)
	case *events.MarkChatAsRead:
		w.onMarkRead(cli, e)
	case *events.ChatPresence:
		w.onChatPresence(e)
	case *events.Contact:
		name := e.Action.GetFullName()
		if name == "" {
			name = e.Action.GetFirstName()
		}
		w.rename(w.chatID(cli, e.JID).String(), name, true)
	case *events.PushName:
		w.rename(w.chatID(cli, e.JID).String(), e.NewPushName, false)
	case *events.GroupInfo:
		if e.Name != nil {
			w.rename(e.JID.String(), e.Name.Name, true)
		}
	case *events.AppStateSyncComplete:
		w.env.Spawn(func() { w.renameSweep(cli) })
	case *events.PairSuccess:
		w.env.Log.Info().Str("jid", e.ID.String()).Str("platform", e.Platform).Msg("Linked to the phone")
		w.mu.Lock()
		w.linked = true
		w.mu.Unlock()
	case *events.Connected:
		if fl != nil {
			fl.done()
			w.mu.Lock()
			w.flow = nil
			w.mu.Unlock()
		}
		w.env.Store.SetAccount(w.account(cli))
		w.env.Store.MarkAlive()
		w.env.Store.SetStatus(core.StatusConnected, "")
		w.env.Spawn(func() { w.renameSweep(cli) })
	case *events.Disconnected:
		w.mu.Lock()
		linked := w.linked
		w.mu.Unlock()
		if linked {
			// whatsmeow reconnects by itself; presence subscriptions don't survive.
			w.mu.Lock()
			w.subscribed = nil
			w.mu.Unlock()
			w.env.Store.SetStatus(core.StatusConnecting, "")
		}
	case *events.LoggedOut:
		w.presenceFor().stop()
		w.mu.Lock()
		w.linked = false
		w.subscribed = nil
		w.mu.Unlock()
		reason := "This computer was unlinked from your phone. Connect again to relink."
		if e.OnConnect {
			reason = "WhatsApp no longer accepts this link (" + e.Reason.String() + "). Connect again to relink."
		}
		w.env.Store.SetStatus(core.StatusDisconnected, reason)
	case *events.StreamReplaced:
		w.env.Store.SetStatus(core.StatusError, "Another WhatsApp session using this link took over.")
	case *events.TemporaryBan:
		w.env.Store.SetStatus(core.StatusError, "WhatsApp temporarily banned this account: "+e.String())
	case *events.ClientOutdated:
		w.env.Store.SetStatus(core.StatusError, "WhatsApp says this client is outdated; whatsmeow needs an update (rebuild the plugin).")
	case *events.KeepAliveTimeout:
		if e.ErrorCount > 2 {
			w.env.Store.SetStatus(core.StatusConnecting, "")
		}
	case *events.KeepAliveRestored:
		w.env.Store.MarkAlive()
		w.env.Store.SetStatus(core.StatusConnected, "")
	}
}

// --- connecting -------------------------------------------------------------

func (w *WhatsApp) Connect(ctx context.Context, method string) error {
	w.mu.Lock()
	linked, fl := w.linked, w.flow
	w.mu.Unlock()
	if linked && w.env.Store.Status() != core.StatusDisconnected {
		return errors.New("already linked; disconnect first")
	}
	if fl != nil && w.env.Store.Snapshot().Connect != nil {
		fl.switchTo(method)
		return nil
	}
	c, err := w.openStore(w.lifeCtx())
	if err != nil {
		return err
	}
	dev, err := c.GetFirstDevice(w.lifeCtx())
	if err != nil {
		return err
	}
	if dev.ID != nil {
		// A stale link the phone dropped: start over with a fresh device.
		_ = dev.Delete(w.lifeCtx())
		dev = c.NewDevice()
	}
	cli := w.newClient(dev)
	fl = &linkFlow{store: w.env.Store, dir: w.env.Dir, link: &realLinker{cli: cli}, spawn: w.env.Spawn}
	w.mu.Lock()
	w.client, w.flow, w.linked = cli, fl, false
	w.mu.Unlock()
	if err := fl.begin(w.lifeCtx(), method); err != nil {
		return nil // the step shows the reason
	}
	return nil
}

func (w *WhatsApp) ConnectInput(ctx context.Context, value string) error {
	w.mu.Lock()
	fl := w.flow
	w.mu.Unlock()
	if fl == nil {
		return errors.New("nothing is waiting for input")
	}
	return fl.input(ctx, value)
}

func (w *WhatsApp) CancelConnect() {
	w.mu.Lock()
	fl := w.flow
	w.flow = nil
	linked := w.linked
	w.mu.Unlock()
	if fl != nil {
		fl.abort()
	}
	if !linked {
		w.env.Store.SetStatus(core.StatusDisconnected, "")
	}
}

// Disconnect unlinks this computer from the phone and forgets everything:
// chats, media, and whatsmeow's store (keys, contacts).
func (w *WhatsApp) Disconnect(ctx context.Context) error {
	w.CancelConnect()
	w.presenceFor().stop()
	w.mu.Lock()
	cli, c := w.client, w.container
	w.client, w.linked, w.container, w.subscribed = nil, false, nil, nil
	w.mu.Unlock()
	if cli != nil {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if cli.IsLoggedIn() {
			if err := cli.Logout(lctx); err != nil {
				w.env.Log.Warn().Err(err).Msg("Unlink request failed; forgetting the link anyway")
				_ = cli.Store.Delete(lctx)
			}
		} else if cli.Store.ID != nil {
			_ = cli.Store.Delete(lctx)
		}
		cancel()
		cli.Disconnect()
	}
	if c != nil {
		_ = c.Close()
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(w.storePath() + suffix)
	}
	_ = os.Remove(filepath.Join(w.env.Dir, "qr.png"))
	_ = os.RemoveAll(w.env.CacheDir)
	w.env.Store.Reset()
	w.env.Store.SetStatus(core.StatusDisconnected, "")
	return nil
}

// --- the real linker ----------------------------------------------------------------

type realLinker struct{ cli *whatsmeow.Client }

func (l *realLinker) Start(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	ch, err := l.cli.GetQRChannel(ctx)
	if err != nil {
		return nil, err
	}
	if err := l.cli.Connect(); err != nil {
		return nil, err
	}
	return ch, nil
}

func (l *realLinker) PairPhone(ctx context.Context, phone string) (string, error) {
	return l.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
}

func (l *realLinker) Stop() { l.cli.Disconnect() }
