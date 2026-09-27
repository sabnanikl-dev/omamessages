// Package telegram is Telegram, spoken with gotd/td (pure Go MTProto). It
// signs in as a regular user with the user's own API app credentials; see
// connect.go for the sign-in flow.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"rsc.io/qr"

	"omarchy-omamessages/core"
)

const ID = core.Telegram

type Telegram struct {
	env core.Env

	mu       sync.Mutex
	life     context.Context
	client   *telegram.Client
	creds    appCreds
	loggedIn qrlogin.LoggedIn
	router   *router
	// dispatcher routes updates to our handlers; runCtx lives as long as
	// the client does.
	dispatcher tg.UpdateDispatcher
	runCtx     context.Context
	sess       *liveSession
	stopRun    context.CancelFunc
	runDone    chan struct{}
	flow       *connectFlow
	authorize  bool // signed in, as far as we know
}

func New(env core.Env) core.Provider { return &Telegram{env: env} }

func (t *Telegram) ID() core.ProviderID { return ID }
func (t *Telegram) Name() string        { return "Telegram" }
func (t *Telegram) Caps() core.Caps {
	return core.Caps{StartByNumber: true, ContactSearch: true, Typing: true, LoadHistory: true, Reactions: true, FetchMedia: true, Attachments: true}
}

func (t *Telegram) sessionPath() string { return filepath.Join(t.env.Dir, "session.json") }

func (t *Telegram) lifeCtx() context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.life != nil {
		return t.life
	}
	return context.Background()
}

// Start resumes a saved session. Without one (or without API credentials)
// it stays disconnected and starts nothing until Connect.
func (t *Telegram) Start(ctx context.Context) error {
	t.mu.Lock()
	t.life = ctx
	t.mu.Unlock()
	if _, err := os.Stat(t.sessionPath()); err != nil {
		t.env.Store.SetStatus(core.StatusDisconnected, "")
		return nil
	}
	creds, ok, err := loadCreds(os.Getenv, t.env.Dir)
	if !ok {
		msg := "API credentials missing; connect again to enter them"
		if err != nil {
			msg = err.Error()
		}
		t.env.Store.SetStatus(core.StatusError, msg)
		return nil
	}
	t.env.Store.SetStatus(core.StatusConnecting, "")
	api, err := t.ensureClient(creds)
	if err != nil {
		t.env.Store.SetStatus(core.StatusError, "Could not reach Telegram: "+friendly(err).Error())
		return nil
	}
	status, err := t.clientAuthStatus(ctx)
	if err != nil {
		t.env.Store.SetStatus(core.StatusError, "Could not reach Telegram: "+friendly(err).Error())
		return nil
	}
	if !status.Authorized {
		// The session was revoked elsewhere (e.g. from another device).
		t.stopClient()
		_ = os.Remove(t.sessionPath())
		t.env.Store.SetStatus(core.StatusDisconnected, "This computer was signed out of Telegram. Connect again.")
		return nil
	}
	account, _ := api.Self(ctx)
	t.signedIn(account)
	return nil
}

func (t *Telegram) clientAuthStatus(ctx context.Context) (*auth.Status, error) {
	t.mu.Lock()
	cli := t.client
	t.mu.Unlock()
	if cli == nil {
		return nil, errors.New("no client")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return cli.Auth().Status(ctx)
}

func (t *Telegram) signedIn(account string) {
	t.mu.Lock()
	t.authorize = true
	t.flow = nil
	t.mu.Unlock()
	if account != "" {
		t.env.Store.SetAccount(account)
	}
	t.env.Store.MarkAlive()
	t.env.Store.SetStatus(core.StatusConnected, "")
	t.startSession()
}

// ensureClient starts a gotd client with these credentials, unless one is
// already running with them, and waits until it has a connection.
func (t *Telegram) ensureClient(creds appCreds) (authAPI, error) {
	t.mu.Lock()
	if t.client != nil && t.creds == creds {
		api := &realAuth{client: t.client, loggedIn: t.loggedIn}
		t.mu.Unlock()
		return api, nil
	}
	t.mu.Unlock()
	t.stopClient()

	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(dispatcher)
	t.handleUpdates(dispatcher)
	r := &router{h: dispatcher}
	cli := telegram.NewClient(creds.ID, creds.Hash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: t.sessionPath()},
		UpdateHandler:  r,
		Device: telegram.DeviceConfig{
			DeviceModel:   "Omarchy",
			SystemVersion: "Linux",
			AppVersion:    "Omarchy Messages",
		},
	})

	runCtx, stop := context.WithCancel(t.lifeCtx())
	ready := make(chan struct{})
	failed := make(chan error, 1)
	done := make(chan struct{})
	t.env.Spawn(func() {
		defer close(done)
		err := cli.Run(runCtx, func(ctx context.Context) error {
			close(ready)
			<-ctx.Done()
			return ctx.Err()
		})
		if runCtx.Err() != nil {
			return // stopped on purpose
		}
		t.env.Log.Warn().Err(err).Msg("Telegram client stopped")
		failed <- err
		t.mu.Lock()
		current := t.client == cli
		if current {
			t.client = nil
		}
		wasIn := t.authorize
		t.mu.Unlock()
		if current && wasIn {
			t.env.Store.SetStatus(core.StatusError, "Lost the connection to Telegram: "+friendly(err).Error())
			t.env.Spawn(func() { t.reconnectLater(creds) })
		}
	})

	select {
	case <-ready:
	case err := <-failed:
		stop()
		return nil, err
	case <-time.After(45 * time.Second):
		stop()
		return nil, errors.New("timed out connecting to Telegram")
	}
	t.mu.Lock()
	t.client, t.creds, t.loggedIn, t.stopRun, t.runDone = cli, creds, loggedIn, stop, done
	t.router, t.dispatcher, t.runCtx = r, dispatcher, runCtx
	t.mu.Unlock()
	return &realAuth{client: cli, loggedIn: loggedIn}, nil
}

// reconnectLater retries a dropped signed-in client, backing off to 5 min.
func (t *Telegram) reconnectLater(creds appCreds) {
	ctx := t.lifeCtx()
	for wait := 15 * time.Second; ; wait = min(wait*2, 5*time.Minute) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		t.mu.Lock()
		stillIn, running := t.authorize, t.client != nil
		t.mu.Unlock()
		if !stillIn || running {
			return
		}
		if _, err := t.ensureClient(creds); err == nil {
			t.signedIn("")
			return
		}
	}
}

func (t *Telegram) stopClient() {
	t.mu.Lock()
	stop, done := t.stopRun, t.runDone
	t.client, t.stopRun, t.runDone, t.sess = nil, nil, nil, nil
	t.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

func (t *Telegram) Stop() {
	t.mu.Lock()
	fl := t.flow
	t.mu.Unlock()
	if fl != nil {
		fl.stopQR()
	}
	t.stopClient()
}

// --- connecting -------------------------------------------------------------

// Connect starts signing in. method "qr" goes straight to the QR code;
// anything else starts with the phone number. Picking an alternative while a
// flow is showing switches that flow's method.
func (t *Telegram) Connect(ctx context.Context, method string) error {
	t.mu.Lock()
	if t.authorize {
		t.mu.Unlock()
		return errors.New("already connected; disconnect first")
	}
	fl := t.flow
	t.mu.Unlock()
	if fl != nil && t.env.Store.Snapshot().Connect != nil && fl.hasCreds() {
		fl.switchTo(t.lifeCtx(), method)
		return nil
	}
	if fl != nil {
		fl.abort()
	}

	fl = &connectFlow{
		store: t.env.Store,
		dir:   t.env.Dir,
		start: t.ensureClient,
		done:  t.signedIn,
		spawn: t.env.Spawn,
	}
	t.mu.Lock()
	t.flow = fl
	t.mu.Unlock()

	creds, ok, credsErr := loadCreds(os.Getenv, t.env.Dir)
	if !ok {
		fl.begin(t.lifeCtx(), method, nil, credsErr)
		return nil
	}
	t.env.Store.SetConnect(&core.ConnectStep{Kind: "waiting", Prompt: "Connecting to Telegram…"})
	api, err := t.ensureClient(creds)
	if err != nil {
		fl.fail("Could not reach Telegram: " + friendly(err).Error())
		return nil
	}
	fl.begin(t.lifeCtx(), method, api, nil)
	return nil
}

func (f *connectFlow) hasCreds() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.api != nil
}

func (t *Telegram) ConnectInput(ctx context.Context, value string) error {
	t.mu.Lock()
	fl := t.flow
	t.mu.Unlock()
	if fl == nil {
		return errors.New("nothing is waiting for input")
	}
	return fl.input(ctx, value)
}

func (t *Telegram) CancelConnect() {
	t.mu.Lock()
	fl := t.flow
	t.flow = nil
	in := t.authorize
	t.mu.Unlock()
	if fl != nil {
		fl.abort()
	}
	if !in {
		// Nothing to keep a connection open for.
		t.stopClient()
		t.env.Store.SetStatus(core.StatusDisconnected, "")
	}
}

// Disconnect logs this computer out of Telegram and forgets its chats. The
// API credentials stay: they belong to the user's app, not the account.
func (t *Telegram) Disconnect(ctx context.Context) error {
	t.CancelConnect()
	t.mu.Lock()
	cli := t.client
	in := t.authorize
	t.authorize = false
	t.mu.Unlock()
	if cli != nil && in {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if _, err := cli.API().AuthLogOut(lctx); err != nil {
			t.env.Log.Warn().Err(err).Msg("Log out request failed; forgetting the session anyway")
		}
		cancel()
	}
	t.stopClient()
	_ = os.Remove(t.sessionPath())
	_ = os.RemoveAll(t.env.CacheDir)
	t.env.Store.Reset()
	t.env.Store.SetStatus(core.StatusDisconnected, "")
	return nil
}

// --- the real authAPI -----------------------------------------------------------

type realAuth struct {
	client   *telegram.Client
	loggedIn qrlogin.LoggedIn
}

func (a *realAuth) SendCode(ctx context.Context, phone string) (string, string, error) {
	sent, err := a.client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		return "", "", err
	}
	code, ok := sent.(*tg.AuthSentCode)
	if !ok {
		return "", "", fmt.Errorf("unexpected answer from Telegram (%T)", sent)
	}
	via := ""
	switch code.Type.(type) {
	case *tg.AuthSentCodeTypeApp:
		via = "your Telegram app"
	case *tg.AuthSentCodeTypeSMS, *tg.AuthSentCodeTypeSMSWord, *tg.AuthSentCodeTypeSMSPhrase, *tg.AuthSentCodeTypeFirebaseSMS, *tg.AuthSentCodeTypeFragmentSMS:
		via = "your phone by SMS"
	case *tg.AuthSentCodeTypeCall, *tg.AuthSentCodeTypeFlashCall, *tg.AuthSentCodeTypeMissedCall:
		via = "your phone as a call"
	case *tg.AuthSentCodeTypeEmailCode:
		via = "your email"
	}
	return code.PhoneCodeHash, via, nil
}

func (a *realAuth) SignIn(ctx context.Context, phone, code, hash string) error {
	_, err := a.client.Auth().SignIn(ctx, phone, code, hash)
	return err
}

func (a *realAuth) Password(ctx context.Context, password string) error {
	_, err := a.client.Auth().Password(ctx, password)
	return err
}

func (a *realAuth) PasswordHint(ctx context.Context) string {
	p, err := a.client.API().AccountGetPassword(ctx)
	if err != nil {
		return ""
	}
	return p.Hint
}

func (a *realAuth) QR(ctx context.Context, show func(url string, expires time.Time) error) error {
	_, err := a.client.QR().Auth(ctx, a.loggedIn, func(ctx context.Context, token qrlogin.Token) error {
		return show(token.URL(), token.Expires())
	})
	if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
		return auth.ErrPasswordAuthNeeded
	}
	return err
}

func (a *realAuth) Self(ctx context.Context) (string, error) {
	u, err := a.client.Self(ctx)
	if err != nil {
		return "", err
	}
	var parts []string
	if u.Phone != "" {
		parts = append(parts, "+"+strings.TrimPrefix(u.Phone, "+"))
	}
	if u.Username != "" {
		parts = append(parts, "@"+u.Username)
	}
	return strings.Join(parts, " · "), nil
}

// writeQR renders a login URL as <dir>/qr.png.
func writeQR(dir, url string) (string, error) {
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "qr.png")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, code.PNG(), 0o600); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}
