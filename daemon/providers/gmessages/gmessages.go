// Package gmessages is Google Messages (SMS/RCS through the phone), spoken
// with the Messages for Web protocol from mautrix-gmessages' libgm. Pairing
// goes through the user's Google account; see cookies.go for where the Google
// sign-in comes from.
package gmessages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"omarchy-omamessages/core"
)

const ID = core.GMessages

const (
	pageSize     = 40
	convListSize = 60
)

// logger is package-level because cookies.go logs from plain functions; New
// points it at the provider's logger.
var logger = zerolog.Nop()

// GMessages owns the libgm client and translates its events into Store updates.
type GMessages struct {
	env   core.Env
	store *core.Store

	mu       sync.Mutex
	life     context.Context // from Start; outlives single commands
	client   *libgm.Client
	auth     *libgm.AuthData
	sims     map[string]*gmproto.SIMCard
	pairing  bool
	pairStop context.CancelFunc
	// fingerprint of the cookies last written to session.json
	cookieFingerprint string
	cancel            context.CancelFunc
	// paging cursors per conversation; not mirrored to disk
	cursors map[string]*gmproto.Cursor
	// last answer to "is Messages the default SMS app", nil until probed
	smsDefault *bool
}

func New(env core.Env) core.Provider {
	logger = env.Log
	return &GMessages{
		env:     env,
		store:   env.Store,
		sims:    map[string]*gmproto.SIMCard{},
		cursors: map[string]*gmproto.Cursor{},
	}
}

func (g *GMessages) ID() core.ProviderID { return ID }
func (g *GMessages) Name() string        { return "Messages" }
func (g *GMessages) Caps() core.Caps {
	return core.Caps{StartByNumber: true, ContactSearch: true, Typing: true, Reactions: true, LoadHistory: true, PhoneTethered: true}
}

func (g *GMessages) sessionPath() string { return filepath.Join(g.env.Dir, "session.json") }

func (g *GMessages) lifeCtx() context.Context {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.life != nil {
		return g.life
	}
	return context.Background()
}

func (g *GMessages) loadSession() (*libgm.AuthData, error) {
	data, err := os.ReadFile(g.sessionPath())
	if err != nil {
		return nil, err
	}
	var auth libgm.AuthData
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

func (g *GMessages) saveSession() {
	g.mu.Lock()
	auth := g.auth
	g.mu.Unlock()
	if auth == nil {
		return
	}
	if err := core.WriteJSONAtomic(g.sessionPath(), auth, 0o600); err != nil {
		logger.Err(err).Msg("Failed to save session")
		return
	}
	g.mu.Lock()
	g.cookieFingerprint = cookieFingerprint(auth)
	g.mu.Unlock()
}

// saveRotatedCookies writes the session out when Google has replaced any of
// the cookies. It rotates some of them (__Secure-1PSIDTS most often) on
// ordinary responses, with no event to hook, so a daemon that only saved on
// token refresh would come back up with a stale session.
func (g *GMessages) saveRotatedCookies() {
	g.mu.Lock()
	auth := g.auth
	stale := auth != nil && cookieFingerprint(auth) != g.cookieFingerprint
	g.mu.Unlock()
	if stale {
		g.saveSession()
	}
}

// syncBrowserCookies copies the browser's current cookies into the session
// when they belong to the same Google sign-in (same SID). Google rotates the
// short-lived ones (__Secure-1PSIDTS, SIDCC) through accounts.google.com,
// which only the browser talks to, so the daemon's copy stops being accepted
// within hours unless it is refreshed from there. It reports whether anything
// changed; a browser signed in to a different session is left alone.
func (g *GMessages) syncBrowserCookies() bool {
	g.mu.Lock()
	auth := g.auth
	g.mu.Unlock()
	if auth == nil || !auth.IsGoogleAccount() {
		return false
	}
	fresh, label, err := browserCookies()
	if err != nil {
		logger.Debug().Err(err).Msg("Could not read browser cookies to refresh the session")
		return false
	}
	auth.CookiesLock.RLock()
	sameSession := auth.Cookies["SID"] != "" && auth.Cookies["SID"] == fresh["SID"]
	auth.CookiesLock.RUnlock()
	if !sameSession {
		logger.Debug().Str("source", label).Msg("Browser is signed in to a different Google session; not refreshing cookies")
		return false
	}
	before := cookieFingerprint(auth)
	auth.SetCookies(fresh)
	if cookieFingerprint(auth) == before {
		return false
	}
	logger.Info().Str("source", label).Msg("Refreshed Google cookies from the browser")
	g.saveSession()
	return true
}

const expiredCookiesHint = "Google rejected the saved sign-in. Open https://messages.google.com/web in your browser; the daemon picks the session up from there."

// isAuthError reports whether Google rejected the session cookies.
func isAuthError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "invalid authentication") ||
		strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "http 401"))
}

func cookieFingerprint(auth *libgm.AuthData) string {
	auth.CookiesLock.RLock()
	defer auth.CookiesLock.RUnlock()
	if auth.Cookies == nil {
		return ""
	}
	names := make([]string, 0, len(auth.Cookies))
	for name := range auth.Cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(auth.Cookies[name]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (g *GMessages) newClient(auth *libgm.AuthData) *libgm.Client {
	cli := libgm.NewClient(auth, nil, logger.With().Str("component", "libgm").Logger(), exhttp.SensibleClientSettings)
	// libgm calls this on its own goroutines, out of the hub's reach.
	cli.SetEventHandler(func(evt any) { g.env.Protect(func() { g.handleEvent(evt) }) })
	return cli
}

// Start connects with the saved session, or leaves the provider disconnected
// waiting for a Connect.
func (g *GMessages) Start(startCtx context.Context) error {
	g.mu.Lock()
	g.life = startCtx
	g.mu.Unlock()
	auth, err := g.loadSession()
	if err != nil || auth.Browser == nil {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warn().Err(err).Msg("Ignoring unreadable session file")
		}
		g.store.SetStatus(core.StatusDisconnected, "")
		return nil
	}
	g.mu.Lock()
	g.auth = auth
	g.client = g.newClient(auth)
	ctx, cancel := context.WithCancel(startCtx)
	g.cancel = cancel
	cli := g.client
	g.mu.Unlock()
	g.store.SetStatus(core.StatusConnecting, "")
	g.env.Spawn(func() {
		g.syncBrowserCookies()
		for {
			err := cli.Connect(ctx)
			if err == nil {
				break
			}
			logger.Err(err).Msg("Failed to connect")
			switch {
			case isAuthError(err):
				// The pairing survives an expired cookie; only the Google
				// sign-in needs renewing, so keep session.json and wait for
				// the browser to have a fresh one.
				g.store.SetStatus(core.StatusError, expiredCookiesHint)
				for !g.syncBrowserCookies() {
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Minute):
					}
				}
				g.store.SetStatus(core.StatusConnecting, "")
				continue
			case strings.Contains(err.Error(), "not found"):
				g.dropSession("The phone has unpaired this device. Pair again.")
			default:
				g.store.SetStatus(core.StatusError, "Connect failed: "+err.Error())
			}
			return
		}
		g.verifyLoop(ctx, cli)
	})
	return nil
}

// verifyLoop proves the phone is really answering. Google's relay accepts
// the long-poll connection even when the phone is off, so "connected" is only
// claimed after a request round-trips through the phone, and re-checked
// whenever nothing has been heard from it for a while.
func (g *GMessages) verifyLoop(ctx context.Context, cli *libgm.Client) {
	const idleProbeAfter = 3 * time.Minute
	const probeInterval = 30 * time.Second
	const cookieSyncInterval = 10 * time.Minute
	lastSync := time.Now()
	first := true
	for {
		if first {
			// Give the long-poll a moment to settle, then do the real check.
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			first = false
		} else {
			select {
			case <-ctx.Done():
				return
			case <-time.After(probeInterval):
			}
		}
		g.mu.Lock()
		current := g.client == cli
		g.mu.Unlock()
		if !current {
			return
		}
		status := g.store.Status()
		if status == core.StatusDisconnected || status == core.StatusPairing {
			return
		}
		// Keep the short-lived cookies in step with the browser, and pick up a
		// fresh sign-in straight away once Google has started rejecting ours.
		if status == core.StatusError || time.Since(lastSync) > cookieSyncInterval {
			lastSync = time.Now()
			if g.syncBrowserCookies() && status == core.StatusError {
				g.store.SetStatus(core.StatusConnecting, "")
				if err := cli.Reconnect(ctx); err != nil {
					logger.Err(err).Msg("Reconnect with refreshed cookies failed")
				}
				continue
			}
		}
		idle := time.Since(time.UnixMilli(g.store.LastActivity()))
		if status == core.StatusConnected && idle < idleProbeAfter {
			continue
		}
		if g.probe(ctx, cli) {
			g.saveRotatedCookies()
			if g.markProbed() {
				g.store.SetStatus(core.StatusConnected, "")
			}
			if status != core.StatusConnected {
				g.env.Spawn(func() { g.Refresh(ctx) })
			}
		} else if status == core.StatusConnected && idle > idleProbeAfter+probeInterval {
			g.store.SetStatus(core.StatusPhoneOffline, "Phone is not responding. Check it is online.")
		}
	}
}

// probe asks the phone a trivial question and reports whether it answered.
func (g *GMessages) probe(ctx context.Context, cli *libgm.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := cli.IsBugleDefault(ctx)
	if err != nil {
		logger.Debug().Err(err).Msg("Liveness probe failed")
		return false
	}
	g.setSMSDefault(resp.GetSuccess())
	return true
}

// setSMSDefault records the answer to the liveness probe, which doubles as
// the "is Messages the default SMS app" question. When it is not, the phone
// answers every request with an empty list.
func (g *GMessages) setSMSDefault(v bool) {
	g.mu.Lock()
	changed := g.smsDefault == nil || *g.smsDefault != v
	g.smsDefault = &v
	g.mu.Unlock()
	if changed {
		g.store.SetExtra("smsDefault", v)
		g.store.Flush()
	}
}

// markProbed records a successful explicit liveness probe. It returns true
// when that moved the status back to connected.
func (g *GMessages) markProbed() bool {
	g.store.SetExtra("lastCheck", time.Now().UnixMilli())
	return g.store.MarkAlive()
}

func (g *GMessages) alive() {
	if g.store.MarkAlive() {
		g.store.SetStatus(core.StatusConnected, "")
	}
}

func (g *GMessages) Stop() {
	g.mu.Lock()
	cli := g.client
	cancel := g.cancel
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cli != nil {
		cli.Disconnect()
	}
}

func (g *GMessages) dropSession(reason string) {
	g.mu.Lock()
	cli := g.client
	g.client = nil
	g.auth = nil
	g.sims = map[string]*gmproto.SIMCard{}
	g.cursors = map[string]*gmproto.Cursor{}
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
	g.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
	_ = os.Remove(g.sessionPath())
	g.store.Reset()
	g.store.SetStatus(core.StatusDisconnected, reason)
}

// --- connecting -------------------------------------------------------------

const pasteInstructions = "On messages.google.com, open devtools (F12) → Network, reload, right-click the first request → Copy → Copy as cURL, then paste it here."

// Connect pairs through the user's Google account, the only method Google
// still supports. method "" lifts the Google sign-in from a local browser
// profile; "cookies" goes straight to pasting it.
func (g *GMessages) Connect(ctx context.Context, method string) error {
	switch method {
	case "":
		return g.pairGoogle(nil)
	case "cookies":
		g.askForCookies("")
		return nil
	}
	return fmt.Errorf("unknown connect method %q", method)
}

// ConnectInput takes pasted cookies (devtools' "Copy as cURL" output, a
// cookie header, or a JSON object) and pairs with them.
func (g *GMessages) ConnectInput(ctx context.Context, value string) error {
	cookies, err := parseCookieBlob(value)
	if err != nil {
		return err
	}
	return g.pairGoogle(cookies)
}

// askForCookies shows the paste step, with why it's needed when there's a
// reason (e.g. no signed-in browser profile was found).
func (g *GMessages) askForCookies(reason string) {
	prompt := "Paste the cookies of a signed-in messages.google.com session."
	if reason != "" {
		prompt = reason + " " + prompt
	}
	g.store.SetConnect(&core.ConnectStep{Kind: "input", Field: "cookies", Secret: true, Prompt: prompt, Hint: pasteInstructions})
}

func (g *GMessages) setSigningIn(account, source string) {
	prompt := "Signing in to Google…"
	if account != "" {
		prompt = "Signing in as " + account + "…"
	}
	g.store.SetConnect(&core.ConnectStep{Kind: "waiting", Prompt: prompt, Hint: "from " + source})
}

func (g *GMessages) setEmoji(emoji, account, source string) {
	g.store.SetConnect(&core.ConnectStep{
		Kind:   "emoji",
		Value:  emoji,
		Prompt: "Tap this emoji in Google Messages on your phone to finish pairing.",
		Hint:   account + " · from " + source,
	})
}

// pairGoogle needs the cookies of a signed-in messages.google.com session:
// pass them in to use pasted ones, or nil to lift them from a local browser
// profile. When none can be found it asks for pasted ones instead of failing.
//
// It returns as soon as the phone has been reached and the confirmation emoji
// is known -- tapping it on the phone can take a while, so the wait happens in
// the background and lands in state.json.
func (g *GMessages) pairGoogle(cookies map[string]string) error {
	g.mu.Lock()
	if g.pairing {
		g.mu.Unlock()
		return nil
	}
	if g.client != nil && g.client.IsLoggedIn() {
		g.mu.Unlock()
		return errors.New("already paired; disconnect first")
	}
	// Claim the pairing slot before looking for cookies, which takes long
	// enough for a second click to slip past an unclaimed check.
	g.pairing = true
	g.mu.Unlock()
	release := func() {
		g.mu.Lock()
		g.pairing = false
		g.mu.Unlock()
	}

	source := "pasted cookies"
	if cookies == nil {
		var err error
		var label string
		cookies, label, err = browserCookies()
		if err != nil {
			release()
			g.askForCookies(err.Error() + ".")
			return nil
		}
		source = label
	}
	if missing := missingCookies(cookies); len(missing) > 0 {
		release()
		g.askForCookies(fmt.Sprintf("That Google session is incomplete (no %s).", strings.Join(missing, ", ")))
		return nil
	}
	logger.Info().Str("source", source).Strs("cookies", cookieNames(cookies)).
		Msg("Starting Google account pairing")

	auth := libgm.NewAuthData()
	auth.SetCookies(cookies)
	cli := g.newClient(auth)
	ctx, cancel := context.WithCancel(g.lifeCtx())
	g.mu.Lock()
	g.auth = auth
	g.client = cli
	g.pairing = true
	g.cancel = cancel
	g.pairStop = cancel
	g.mu.Unlock()
	g.setSigningIn("", source)

	reqCtx, reqCancel := context.WithTimeout(ctx, 60*time.Second)
	defer reqCancel()
	if err := cli.FetchConfig(reqCtx); err != nil {
		g.pairingFailed("Could not reach Google Messages: " + err.Error())
		return err
	}
	account := cli.Config.GetDeviceInfo().GetEmail()
	if account == "" {
		g.pairingFailed("")
		g.askForCookies("Google did not accept that session.")
		return errors.New("google did not accept the cookies")
	}
	g.setSigningIn(account, source)

	emoji, sess, err := cli.StartGaiaPairing(reqCtx, ctx)
	if err != nil {
		reason := gaiaPairError(err)
		logger.Err(err).Msg("Could not start Google account pairing")
		g.pairingFailed(reason)
		return errors.New(reason)
	}
	logger.Info().Str("account", account).Msg("Waiting for pairing confirmation on the phone")
	g.setEmoji(emoji, account, source)
	g.env.Spawn(func() { g.finishGoogle(ctx, cli, sess, account) })
	return nil
}

// finishGoogle waits for the user to tap the emoji on their phone.
func (g *GMessages) finishGoogle(ctx context.Context, cli *libgm.Client, sess *libgm.PairingSession, account string) {
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	phoneID, err := cli.FinishGaiaPairing(waitCtx, sess)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return // cancelled from the panel; pairingFailed already ran
		}
		logger.Err(err).Msg("Google account pairing failed")
		g.pairingFailed(gaiaPairError(err))
		return
	}
	logger.Info().Str("phone", phoneID).Str("account", account).Msg("Paired")
	g.mu.Lock()
	current := g.client == cli
	if current {
		g.pairing = false
		g.pairStop = nil
	}
	g.mu.Unlock()
	if !current {
		return
	}
	g.saveSession()
	g.store.SetAccount(account)
	g.store.SetStatus(core.StatusConnecting, "")
	if err := cli.Reconnect(ctx); err != nil {
		logger.Err(err).Msg("Failed to connect after pairing")
		g.store.SetStatus(core.StatusError, "Paired, but connecting failed: "+err.Error())
		return
	}
	g.env.Spawn(func() { g.verifyLoop(ctx, cli) })
}

// gaiaPairError turns libgm's pairing errors into something worth showing in
// the panel. Most of them mean the phone has to be touched, not the computer.
func gaiaPairError(err error) string {
	switch {
	case errors.Is(err, libgm.ErrNoDevicesFound):
		return "No phone found on this Google account. In Google Messages on your phone: profile picture → Device pairing → turn on pairing with your Google account."
	case errors.Is(err, libgm.ErrPairingInitTimeout):
		return "The phone did not answer. Open Google Messages on it, keep it on screen, and try again."
	case errors.Is(err, libgm.ErrIncorrectEmoji):
		return "The wrong emoji was tapped on the phone. Try again."
	case errors.Is(err, libgm.ErrPairingCancelled):
		return "Pairing was cancelled on the phone."
	case errors.Is(err, libgm.ErrPairingTimeout):
		return "Pairing timed out. Try again."
	case errors.Is(err, libgm.ErrNoCookies):
		return "That Google session has no cookies. Paste them instead."
	case errors.Is(err, events.ErrCallerNoPermission):
		return "That Google account is not allowed to use Google Messages for web."
	default:
		return "Pairing failed: " + err.Error()
	}
}

func (g *GMessages) pairingFailed(reason string) {
	g.mu.Lock()
	cli := g.client
	g.pairing = false
	g.client = nil
	g.auth = nil
	if g.pairStop != nil {
		g.pairStop()
		g.pairStop = nil
	}
	g.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
	g.store.SetStatus(core.StatusDisconnected, reason)
}

// CancelConnect abandons pairing, or the paste step if that's what is showing.
func (g *GMessages) CancelConnect() {
	g.mu.Lock()
	pairing := g.pairing
	g.mu.Unlock()
	if pairing {
		g.pairingFailed("")
	} else if g.store.Snapshot().Connect != nil {
		g.store.SetStatus(core.StatusDisconnected, "")
	}
}

// Disconnect unpairs this computer from the phone and forgets the session.
func (g *GMessages) Disconnect(ctx context.Context) error {
	g.mu.Lock()
	cli := g.client
	g.mu.Unlock()
	if cli != nil && cli.IsLoggedIn() {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := cli.Unpair(ctx); err != nil {
			logger.Warn().Err(err).Msg("Unpair request failed; dropping session anyway")
		}
	}
	g.dropSession("")
	return nil
}

func (g *GMessages) connectedClient() (*libgm.Client, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.client == nil || !g.client.IsLoggedIn() {
		return nil, errors.New("not paired")
	}
	return g.client, nil
}

// --- event handling ---------------------------------------------------------

func (g *GMessages) handleEvent(rawEvt any) {
	switch evt := rawEvt.(type) {
	case *events.PairSuccessful:
		// Only DoGaiaPairing and the retired QR flow emit this; finishGoogle
		// handles our pairing.
		logger.Debug().Str("phone", evt.PhoneID).Msg("PairSuccessful event")
	case *events.AuthTokenRefreshed:
		g.saveSession()
	case *events.ClientReady:
		for _, c := range evt.Conversations {
			g.store.UpsertConversation(convertConversation(c))
		}
		g.alive()
		g.store.Flush()
	case *gmproto.Settings:
		g.mu.Lock()
		phone := ""
		for _, sim := range evt.GetSIMCards() {
			g.sims[sim.GetSIMParticipant().GetID()] = sim
			if phone == "" {
				phone = sim.GetSIMData().GetFormattedPhoneNumber()
			}
		}
		g.mu.Unlock()
		if phone != "" {
			g.store.SetExtra("phone", phone)
		}
		g.alive()
		g.store.Flush()
	case *gmproto.Conversation:
		g.store.UpsertConversation(convertConversation(evt))
		g.alive()
		g.store.Flush()
	case *libgm.WrappedMessage:
		if !evt.IsOld {
			g.alive()
		}
		g.handleMessage(evt)
	case *gmproto.TypingData:
		g.alive()
		g.store.SetTyping(evt.GetConversationID(), time.Now().Add(6*time.Second))
	case *events.BrowserActive:
		g.alive()
	case *gmproto.RevokePairData:
		logger.Warn().Msg("Pairing revoked by phone")
		g.dropSession("The phone unpaired this device. Pair again.")
	case *events.GaiaLoggedOut:
		g.dropSession("The Google sign-in expired. Pair again.")
	case *events.PhoneNotResponding:
		g.store.SetStatus(core.StatusPhoneOffline, "Phone is not responding. Check it is online.")
	case *events.PhoneRespondingAgain:
		g.store.MarkAlive()
		g.store.SetStatus(core.StatusConnected, "")
	case *events.ListenFatalError:
		logger.Err(evt.Error).Msg("Fatal listen error")
		if isAuthError(evt.Error) {
			// verifyLoop retries once the browser has a fresh sign-in.
			g.store.SetStatus(core.StatusError, expiredCookiesHint)
		} else {
			g.store.SetStatus(core.StatusError, evt.Error.Error())
		}
	case *events.ListenTemporaryError:
		logger.Warn().Err(evt.Error).Msg("Temporary listen error")
	case *events.ListenRecovered:
		if g.store.Status() == core.StatusError {
			g.store.SetStatus(core.StatusConnecting, "")
		}
	case *events.PingFailed:
		if evt.ErrorCount > 1 {
			g.store.SetStatus(core.StatusError, "Connection to the phone keeps failing.")
		}
	case *events.NoDataReceived:
		g.env.Spawn(func() { g.Refresh(g.lifeCtx()) })
	case *events.HackySetActiveMayFail, *events.AccountChange:
	default:
		logger.Debug().Type("type", rawEvt).Msg("Unhandled event")
	}
}

func (g *GMessages) handleMessage(evt *libgm.WrappedMessage) {
	convID := evt.GetConversationID()
	conv := g.store.Conversation(convID)
	msg := convertMessage(conv, evt.Message)
	if g.store.HasMessages(convID) {
		g.store.UpsertMessage(convID, msg)
	}
	if conv != nil && msg.Ts >= conv.LastTs {
		c := *conv
		preview := msg.Text
		if preview == "" && len(msg.Attachments) > 0 {
			preview = "[" + msg.Attachments[0].Kind + "]"
		}
		c.LastMessage = preview
		c.LastFromMe = msg.FromMe
		c.LastSender = msg.Sender
		c.LastTs = msg.Ts
		if !msg.FromMe && !evt.IsOld && msg.Status == "received" {
			c.Unread = true
		}
		g.store.UpsertConversation(&c)
		g.store.Flush()
	}
	if !evt.IsOld && !msg.FromMe && msg.Status == "received" {
		title := msg.Sender
		if conv != nil && (conv.IsGroup || title == "") {
			if title == "" {
				title = conv.Name
			} else {
				title = conv.Name + " · " + title
			}
		}
		if title == "" {
			title = "Google Messages"
		}
		body := msg.Text
		if body == "" && len(msg.Attachments) > 0 {
			body = "Sent a " + msg.Attachments[0].Kind
		}
		go g.env.Notify(core.Notification{ConvID: convID, Title: title, Body: body})
	}
}

// --- commands ---------------------------------------------------------------

func (g *GMessages) Refresh(ctx context.Context) error {
	cli, err := g.connectedClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := cli.ListConversations(ctx, convListSize, gmproto.ListConversationsRequest_INBOX)
	if err != nil {
		logger.Err(err).Msg("ListConversations failed")
		return err
	}
	convs := resp.GetConversations()
	logger.Info().Int("count", len(convs)).Msg("ListConversations returned")
	for _, c := range convs {
		logger.Debug().Str("id", c.GetConversationID()).Str("name", c.GetName()).
			Str("status", c.GetStatus().String()).Int64("last_ts", c.GetLastMessageTimestamp()).
			Msg("Conversation from phone")
		g.store.UpsertConversation(convertConversation(c))
	}
	g.markProbed()
	g.store.Flush()
	return nil
}

// Open fetches the newest page of a conversation and marks it read.
func (g *GMessages) Open(ctx context.Context, convID string, markRead bool) error {
	cli, err := g.connectedClient()
	if err != nil {
		return err
	}
	g.store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		cm.Loading = true
		cm.Error = ""
	})

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := cli.FetchMessages(ctx, convID, pageSize, nil)
	if err != nil {
		g.store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
			cm.Loading = false
			cm.Error = err.Error()
		})
		return err
	}
	g.setCursor(convID, resp.GetCursor())
	conv := g.store.Conversation(convID)
	var newest *core.Message
	g.store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		cm.Loading = false
		cm.HasMore = resp.GetCursor() != nil && int64(len(resp.GetMessages())) < resp.GetTotalMessages()
		existing := map[string]core.Message{}
		for _, m := range cm.Messages {
			existing[m.ID] = m
		}
		for _, raw := range resp.GetMessages() {
			m := convertMessage(conv, raw)
			existing[m.ID] = m
		}
		cm.Messages = cm.Messages[:0]
		for _, m := range existing {
			cm.Messages = append(cm.Messages, m)
		}
		cm.Messages = core.DropStalePlaceholders(cm.Messages)
		sortMessages(cm.Messages)
		if n := len(cm.Messages); n > 0 {
			last := cm.Messages[n-1]
			newest = &last
		}
	})

	if markRead && conv != nil && conv.Unread && newest != nil {
		if err := cli.MarkRead(ctx, convID, newest.ID); err != nil {
			logger.Warn().Err(err).Msg("MarkRead failed")
		} else {
			c := *conv
			c.Unread = false
			g.store.UpsertConversation(&c)
			g.store.Flush()
		}
	}
	return nil
}

func (g *GMessages) setCursor(convID string, c *gmproto.Cursor) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cursors[convID] = c
}

func (g *GMessages) cursor(convID string) *gmproto.Cursor {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cursors[convID]
}

// More fetches the next older page.
func (g *GMessages) More(ctx context.Context, convID string) error {
	cli, err := g.connectedClient()
	if err != nil {
		return err
	}
	cursor := g.cursor(convID)
	if cursor == nil {
		g.store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
			cm.Loading = false
			cm.HasMore = false
		})
		return nil
	}
	g.store.UpdateMessages(convID, func(cm *core.ConversationMessages) { cm.Loading = true })
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := cli.FetchMessages(ctx, convID, pageSize, cursor)
	conv := g.store.Conversation(convID)
	if err == nil {
		g.setCursor(convID, resp.GetCursor())
	}
	g.store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		cm.Loading = false
		if err != nil {
			cm.Error = err.Error()
			return
		}
		cm.HasMore = resp.GetCursor() != nil && len(resp.GetMessages()) > 0
		seen := map[string]bool{}
		for _, m := range cm.Messages {
			seen[m.ID] = true
		}
		for _, raw := range resp.GetMessages() {
			m := convertMessage(conv, raw)
			if !seen[m.ID] {
				cm.Messages = append(cm.Messages, m)
			}
		}
		sortMessages(cm.Messages)
	})
	return err
}

func sortMessages(ms []core.Message) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].Ts < ms[j-1].Ts; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

func (g *GMessages) simFor(outgoingID string) *gmproto.SIMPayload {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sim, ok := g.sims[outgoingID]; ok {
		return sim.GetSIMData().GetSIMPayload()
	}
	for _, sim := range g.sims {
		return sim.GetSIMData().GetSIMPayload()
	}
	return nil
}

// Send posts a text message to an existing conversation. The echo of the
// sent message arrives back through the event stream and replaces the
// optimistic placeholder written here.
func (g *GMessages) Send(ctx context.Context, convID, text string, files []string) error {
	if files != nil {
		return core.ErrUnsupported
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty message")
	}
	cli, err := g.connectedClient()
	if err != nil {
		return err
	}
	conv := g.store.Conversation(convID)
	if conv == nil {
		return fmt.Errorf("unknown conversation %s", convID)
	}
	tmpID := "tmp_" + uuid.NewString()
	req := &gmproto.SendMessageRequest{
		ConversationID: convID,
		MessagePayload: &gmproto.MessagePayload{
			TmpID:          tmpID,
			ConversationID: convID,
			ParticipantID:  conv.OutgoingID,
			TmpID2:         tmpID,
			MessageInfo: []*gmproto.MessageInfo{{
				Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}},
			}},
		},
		SIMPayload: g.simFor(conv.OutgoingID),
		TmpID:      tmpID,
	}
	if g.store.HasMessages(convID) {
		g.store.UpsertMessage(convID, core.Message{
			ID: tmpID, TmpID: tmpID, Ts: time.Now().UnixMilli(), FromMe: true, Sender: "Me",
			SenderID: conv.OutgoingID, Text: text, Status: "sending",
		})
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := cli.SendMessage(ctx, req)
	if err == nil && resp.GetStatus() != gmproto.SendMessageResponse_SUCCESS {
		err = fmt.Errorf("phone rejected the message (%s)", resp.GetStatus().String())
	}
	if err != nil {
		if g.store.HasMessages(convID) {
			g.store.UpsertMessage(convID, core.Message{
				ID: tmpID, TmpID: tmpID, Ts: time.Now().UnixMilli(), FromMe: true, Sender: "Me",
				SenderID: conv.OutgoingID, Text: text, Status: "failed", StatusText: err.Error(),
			})
		}
		return err
	}
	return nil
}

// StartChat resolves (or creates) the conversation for a phone number, sends
// text into it if there is any, and returns its ID so the panel can open it.
func (g *GMessages) StartChat(ctx context.Context, to, text string) (string, error) {
	cli, err := g.connectedClient()
	if err != nil {
		return "", err
	}
	number := strings.TrimSpace(to)
	if number == "" {
		return "", errors.New("empty number")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := cli.GetOrCreateConversation(reqCtx, &gmproto.GetOrCreateConversationRequest{
		Numbers: []*gmproto.ContactNumber{{MysteriousInt: 2, Number: number, Number2: number}},
	})
	if err != nil {
		return "", err
	}
	if resp.GetConversation().GetConversationID() == "" {
		return "", fmt.Errorf("phone did not return a conversation (%s)", resp.GetStatus().String())
	}
	conv := convertConversation(resp.GetConversation())
	g.store.UpsertConversation(conv)
	g.store.Flush()
	if text = strings.TrimSpace(text); text != "" {
		if err := g.Send(ctx, conv.ID, text, nil); err != nil {
			return conv.ID, err
		}
	}
	return conv.ID, nil
}

// Contacts lists the phone's contacts, filtered by name or number when query
// is not empty.
func (g *GMessages) Contacts(ctx context.Context, query string) ([]core.Participant, error) {
	cli, err := g.connectedClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := cli.ListContacts(ctx)
	if err != nil {
		return nil, err
	}
	return matchContacts(resp.GetContacts(), query), nil
}

// matchContacts turns the phone's contacts into compose results. The ID is
// the phone number: it goes straight back into StartChat, which starts a
// chat with a number. (It used to be Google's participant ID, a short
// internal number like "238", which StartChat then texted as if it were a
// phone number.)
func matchContacts(contacts []*gmproto.Contact, query string) []core.Participant {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []core.Participant
	for _, c := range contacts {
		name := strings.TrimSpace(c.GetName())
		num := c.GetNumber().GetNumber()
		if num == "" {
			continue
		}
		if name == "" {
			name = c.GetNumber().GetFormattedNumber()
		}
		if q != "" && !strings.Contains(strings.ToLower(name), q) && !strings.Contains(num, q) {
			continue
		}
		out = append(out, core.Participant{ID: num, Name: name, Number: num})
	}
	return out
}

func (g *GMessages) FetchMedia(ctx context.Context, convID, msgID string, idx int) (string, error) {
	return "", core.ErrUnsupported
}
