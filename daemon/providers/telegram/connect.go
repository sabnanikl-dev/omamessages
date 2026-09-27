package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tgerr"

	"omarchy-omamessages/core"
)

// Signing in to Telegram takes the app's API credentials (from
// my.telegram.org, once), then either phone → code → cloud password (only
// with 2FA on), or a QR code scanned from a logged-in phone. connectFlow walks
// those steps; it talks to Telegram only through authAPI, so tests can drive
// it without a network.

const (
	envAPIID   = "OMAMESSAGES_TELEGRAM_API_ID"
	envAPIHash = "OMAMESSAGES_TELEGRAM_API_HASH"
)

// authAPI is what the sign-in flow needs from a running Telegram client.
type authAPI interface {
	// SendCode asks Telegram to send a login code; via says where it went.
	SendCode(ctx context.Context, phone string) (hash, via string, err error)
	// SignIn returns auth.ErrPasswordAuthNeeded when 2FA is on.
	SignIn(ctx context.Context, phone, code, hash string) error
	Password(ctx context.Context, password string) error
	PasswordHint(ctx context.Context) string
	// QR shows login tokens until one is scanned. It returns
	// auth.ErrPasswordAuthNeeded when 2FA is on.
	QR(ctx context.Context, show func(url string, expires time.Time) error) error
	// Self describes the signed-in account, e.g. "+15550102030 · @ada".
	Self(ctx context.Context) (string, error)
}

// appCreds are the API credentials of the user's own Telegram app.
type appCreds struct {
	ID   int    `json:"id"`
	Hash string `json:"hash"`
}

var hashRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

func (c appCreds) valid() error {
	if c.ID <= 0 {
		return errors.New("the api_id is a number, like 1234567")
	}
	if !hashRe.MatchString(c.Hash) {
		return errors.New("the api_hash is 32 letters and digits (0-9, a-f)")
	}
	return nil
}

// loadCreds prefers the environment, then <dir>/app.json. ok is false when
// neither has any; err explains credentials that are present but wrong.
func loadCreds(getenv func(string) string, dir string) (c appCreds, ok bool, err error) {
	id, hash := strings.TrimSpace(getenv(envAPIID)), strings.TrimSpace(getenv(envAPIHash))
	if id != "" || hash != "" {
		if id == "" || hash == "" {
			return c, false, fmt.Errorf("set both %s and %s", envAPIID, envAPIHash)
		}
		n, perr := strconv.Atoi(id)
		if perr != nil {
			return c, false, fmt.Errorf("%s must be a number, like 1234567", envAPIID)
		}
		c = appCreds{ID: n, Hash: hash}
		if err := c.valid(); err != nil {
			return c, false, fmt.Errorf("%s/%s: %w", envAPIID, envAPIHash, err)
		}
		return c, true, nil
	}
	data, rerr := os.ReadFile(filepath.Join(dir, "app.json"))
	if errors.Is(rerr, os.ErrNotExist) {
		return c, false, nil
	}
	if rerr != nil {
		return c, false, rerr
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, false, fmt.Errorf("app.json is unreadable: %w", err)
	}
	if err := c.valid(); err != nil {
		return c, false, fmt.Errorf("app.json: %w", err)
	}
	return c, true, nil
}

// parseCredsInput reads "api_id api_hash" as typed or pasted into the panel,
// separated by spaces, a newline, a colon or a comma.
func parseCredsInput(s string) (appCreds, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == ':' || r == ',' })
	if len(fields) != 2 {
		return appCreds{}, errors.New("enter the api_id and the api_hash, separated by a space")
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return appCreds{}, errors.New("the api_id comes first and is a number, like 1234567")
	}
	c := appCreds{ID: n, Hash: fields[1]}
	return c, c.valid()
}

func saveCreds(dir string, c appCreds) error {
	return core.WriteJSONAtomic(filepath.Join(dir, "app.json"), c, 0o600)
}

// normalizePhone keeps a leading + and the digits.
func normalizePhone(s string) (string, error) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", errors.New("a phone number has only digits, like +1 555 010 2030")
		}
	}
	out := b.String()
	if len(strings.TrimPrefix(out, "+")) < 6 {
		return "", errors.New("that phone number is too short; include the country code, like +1 555 010 2030")
	}
	return out, nil
}

// friendly turns Telegram's error codes into sentences for the panel.
func friendly(err error) error {
	switch {
	case err == nil:
		return nil
	case tgerr.Is(err, "PHONE_NUMBER_INVALID"):
		return errors.New("Telegram doesn't recognize that phone number; include the country code")
	case tgerr.Is(err, "PHONE_NUMBER_BANNED"):
		return errors.New("Telegram has banned that phone number")
	case tgerr.Is(err, "PHONE_CODE_INVALID"):
		return errors.New("wrong code, try again")
	case tgerr.Is(err, "PHONE_CODE_EXPIRED"):
		return errors.New("that code has expired; cancel and start again for a new one")
	case tgerr.Is(err, "PASSWORD_HASH_INVALID"):
		return errors.New("wrong password, try again")
	case tgerr.Is(err, "API_ID_INVALID", "API_ID_PUBLISHED_FLOOD"):
		return errors.New("Telegram rejected the api_id/api_hash; check them at my.telegram.org")
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return fmt.Errorf("Telegram asks to wait %s before trying again", d.Round(time.Second))
	}
	return err
}

// connectFlow is one sign-in attempt.
type connectFlow struct {
	store *core.Store
	dir   string
	// start makes sure a client is running with these credentials and
	// returns its auth API.
	start func(appCreds) (authAPI, error)
	// done runs once signed in, with the account description.
	done func(account string)
	// spawn starts background work under the hub's panic isolation.
	spawn func(func())

	mu     sync.Mutex
	api    authAPI
	field  string // the input step waiting for an answer
	phone  string
	hash   string
	cancel context.CancelFunc // stops a running QR loop
	ended  bool
}

var (
	altQR    = core.ConnectAlt{Method: "qr", Label: "QR code"}
	altPhone = core.ConnectAlt{Method: "phone", Label: "phone number"}
)

// begin shows the first step. api is nil when there are no usable
// credentials yet; credsErr says why, if they exist but are wrong.
func (f *connectFlow) begin(ctx context.Context, method string, api authAPI, credsErr error) {
	f.mu.Lock()
	f.api = api
	f.mu.Unlock()
	if api == nil {
		reason := ""
		if credsErr != nil {
			reason = credsErr.Error() + ". "
		}
		f.ask("api_credentials", &core.ConnectStep{
			Kind: "input", Field: "api_credentials",
			Prompt: reason + "Enter your Telegram app's api_id and api_hash, separated by a space",
			Hint:   "Once only. Create an app at my.telegram.org → API development tools. Stored in telegram/app.json (0600); the " + envAPIID + "/" + envAPIHash + " environment variables win if set.",
		})
		return
	}
	if method == "qr" {
		f.startQR(ctx)
		return
	}
	f.askPhone()
}

func (f *connectFlow) ask(field string, step *core.ConnectStep) {
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return
	}
	f.field = field
	f.mu.Unlock()
	f.store.SetConnect(step)
}

func (f *connectFlow) askPhone() {
	f.ask("phone", &core.ConnectStep{
		Kind: "input", Field: "phone",
		Prompt: "Your phone number",
		Hint:   "In international form, e.g. +1 555 010 2030",
		Alts:   []core.ConnectAlt{altQR},
	})
}

func (f *connectFlow) askCode(via string) {
	hint := "If you use a cloud password, you'll be asked for it next."
	if via != "" {
		hint = "Sent to " + via + ". " + hint
	}
	f.ask("code", &core.ConnectStep{Kind: "input", Field: "code", Prompt: "Enter the code Telegram just sent", Hint: hint})
}

func (f *connectFlow) askPassword(hint string) {
	h := "Telegram → Settings → Privacy and Security → Two-Step Verification"
	if hint != "" {
		h = "Your hint: " + hint
	}
	f.ask("password", &core.ConnectStep{Kind: "input", Field: "password", Secret: true, Prompt: "Your Telegram cloud password", Hint: h})
}

// input answers the current input step.
func (f *connectFlow) input(ctx context.Context, value string) error {
	f.mu.Lock()
	field, api, phone, hash := f.field, f.api, f.phone, f.hash
	f.mu.Unlock()
	value = strings.TrimSpace(value)
	switch field {
	case "api_credentials":
		c, err := parseCredsInput(value)
		if err != nil {
			return err
		}
		api, err := f.start(c)
		if err != nil {
			return friendly(err)
		}
		if err := saveCreds(f.dir, c); err != nil {
			return fmt.Errorf("could not save the credentials: %w", err)
		}
		f.mu.Lock()
		f.api = api
		f.mu.Unlock()
		f.askPhone()
	case "phone":
		p, err := normalizePhone(value)
		if err != nil {
			return err
		}
		h, via, err := api.SendCode(ctx, p)
		if err != nil {
			return friendly(err)
		}
		f.mu.Lock()
		f.phone, f.hash = p, h
		f.mu.Unlock()
		f.askCode(via)
	case "code":
		err := api.SignIn(ctx, phone, strings.ReplaceAll(value, " ", ""), hash)
		if errors.Is(err, auth.ErrPasswordAuthNeeded) {
			f.askPassword(api.PasswordHint(ctx))
			return nil
		}
		if err != nil {
			return friendly(err)
		}
		f.finish(ctx)
	case "password":
		if value == "" {
			return errors.New("enter the password")
		}
		if err := api.Password(ctx, value); err != nil {
			return friendly(err)
		}
		f.finish(ctx)
	default:
		return errors.New("nothing is waiting for input")
	}
	return nil
}

// startQR shows login QR codes until one is scanned, in the background.
func (f *connectFlow) startQR(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	f.mu.Lock()
	if f.cancel != nil {
		f.cancel()
	}
	f.cancel = cancel
	f.field = ""
	api := f.api
	f.mu.Unlock()
	f.store.SetConnect(&core.ConnectStep{Kind: "waiting", Prompt: "Getting a QR code…", Alts: []core.ConnectAlt{altPhone}})
	f.spawn(func() {
		err := api.QR(ctx, func(url string, expires time.Time) error {
			path, err := writeQR(f.dir, url)
			if err != nil {
				return err
			}
			f.mu.Lock()
			ended := f.ended
			f.mu.Unlock()
			if !ended {
				f.store.SetConnect(&core.ConnectStep{
					Kind: "qr", QRPath: path, QRExpires: expires.UnixMilli(),
					Prompt: "On your phone: Telegram → Settings → Devices → Link Desktop Device",
					Alts:   []core.ConnectAlt{altPhone},
				})
			}
			return nil
		})
		if ctx.Err() != nil {
			return // cancelled or switched to the phone number
		}
		switch {
		case errors.Is(err, auth.ErrPasswordAuthNeeded):
			f.askPassword(api.PasswordHint(ctx))
		case err != nil:
			f.fail("QR sign-in failed: " + friendly(err).Error())
		default:
			f.finish(ctx)
		}
	})
}

// switchTo changes method mid-flow (the alternatives on a step).
func (f *connectFlow) switchTo(ctx context.Context, method string) {
	f.stopQR()
	if method == "qr" {
		f.startQR(ctx)
		return
	}
	f.askPhone()
}

func (f *connectFlow) stopQR() {
	f.mu.Lock()
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	f.mu.Unlock()
	_ = os.Remove(filepath.Join(f.dir, "qr.png"))
}

func (f *connectFlow) finish(ctx context.Context) {
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return
	}
	f.ended = true
	api := f.api
	f.mu.Unlock()
	f.stopQR()
	account, err := api.Self(ctx)
	if err != nil {
		account = ""
	}
	f.store.SetAccount(account)
	f.store.SetStatus(core.StatusConnected, "")
	if f.done != nil {
		f.done(account)
	}
}

func (f *connectFlow) fail(reason string) {
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return
	}
	f.ended = true
	f.mu.Unlock()
	f.stopQR()
	f.store.SetStatus(core.StatusDisconnected, reason)
}

// abort ends the flow without signing in; the store goes back to
// disconnected with no step.
func (f *connectFlow) abort() { f.fail("") }
