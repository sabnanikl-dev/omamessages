package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"rsc.io/qr"

	"omarchy-omamessages/core"
)

// Linking WhatsApp makes this computer a linked device of the phone. The
// default is a QR code the phone scans (WhatsApp → Settings → Linked devices
// → Link a device), refreshed about every 20 s; the alternative is a phone
// number, for which WhatsApp shows an 8-character code to type on the phone.
// linkFlow walks those steps; it only talks to WhatsApp through linker, so
// tests can drive it.

// linker is what the flow needs from a whatsmeow client that isn't linked yet.
type linker interface {
	// Start connects and returns the channel of QR codes and pairing events.
	Start(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error)
	// PairPhone asks for a pairing code for this number (digits only).
	PairPhone(ctx context.Context, phone string) (string, error)
	// Stop disconnects an unfinished link.
	Stop()
}

var (
	altPhone = core.ConnectAlt{Method: "phone", Label: "phone number"}
	altQR    = core.ConnectAlt{Method: "qr", Label: "QR code"}
)

const linkPrompt = "On your phone: WhatsApp → Settings → Linked devices → Link a device"

type linkFlow struct {
	store *core.Store
	dir   string
	link  linker
	spawn func(func())

	mu     sync.Mutex
	cancel context.CancelFunc
	mode   string // "qr" or "phone": which step owns the screen
	field  string // "phone" while waiting for the number
	ended  bool
}

// begin connects and shows the first step for method ("" or "qr", "phone").
func (f *linkFlow) begin(parent context.Context, method string) error {
	ctx, cancel := context.WithCancel(parent)
	f.mu.Lock()
	f.cancel = cancel
	f.mode = "qr"
	if method == "phone" {
		f.mode = "phone"
	}
	f.mu.Unlock()
	f.store.SetConnect(&core.ConnectStep{Kind: "waiting", Prompt: "Connecting to WhatsApp…"})
	items, err := f.link.Start(ctx)
	if err != nil {
		f.fail("Could not reach WhatsApp: " + err.Error())
		return err
	}
	if method == "phone" {
		f.askPhone()
	}
	f.spawn(func() { f.watch(ctx, items) })
	return nil
}

// watch follows the QR channel until the link succeeds, fails or is cancelled.
func (f *linkFlow) watch(ctx context.Context, items <-chan whatsmeow.QRChannelItem) {
	for {
		select {
		case <-ctx.Done():
			return
		case it, ok := <-items:
			if !ok {
				return
			}
			switch it.Event {
			case whatsmeow.QRChannelEventCode:
				f.showQR(it.Code, it.Timeout)
			case "success":
				// The client's Connected event finishes the job; say so meanwhile.
				f.show(&core.ConnectStep{Kind: "waiting", Prompt: "Linked. Loading your chats…"}, "")
				return
			case "timeout":
				f.fail("The code expired before it was used. Try again.")
				return
			case whatsmeow.QRChannelEventPasskeyRequest:
				f.fail("WhatsApp asked this device for a passkey, which Omarchy Messages can't do yet. Try linking with the phone number instead.")
				return
			case "err-client-outdated":
				f.fail("WhatsApp says this client is outdated; whatsmeow needs an update (rebuild the plugin).")
				return
			case "err-scanned-without-multidevice":
				f.fail("Your phone's WhatsApp doesn't support linked devices; update WhatsApp on the phone.")
				return
			case whatsmeow.QRChannelEventError:
				f.fail("Linking failed: " + errString(it.Error))
				return
			case whatsmeow.QRChannelEventPasskeyResponse:
				// Confirmed by whatsmeow itself; keep waiting.
			default:
				if strings.HasPrefix(it.Event, "err") {
					f.fail("Linking failed (" + it.Event + ")")
					return
				}
			}
		}
	}
}

func errString(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// show publishes a step unless the flow has ended. When only is set, the
// step only shows while that mode owns the screen (a fresh QR code must not
// replace a pairing code the user is typing).
func (f *linkFlow) show(step *core.ConnectStep, only string) {
	f.mu.Lock()
	if f.ended || (only != "" && f.mode != only) {
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()
	f.store.SetConnect(step)
}

func (f *linkFlow) showQR(code string, timeout time.Duration) {
	path, err := writeQR(f.dir, code)
	if err != nil {
		f.fail("Could not draw the QR code: " + err.Error())
		return
	}
	f.show(&core.ConnectStep{
		Kind: "qr", QRPath: path, QRExpires: time.Now().Add(timeout).UnixMilli(),
		Prompt: linkPrompt,
		Alts:   []core.ConnectAlt{altPhone},
	}, "qr")
}

func (f *linkFlow) askPhone() {
	f.mu.Lock()
	f.mode, f.field = "phone", "phone"
	f.mu.Unlock()
	f.show(&core.ConnectStep{
		Kind: "input", Field: "phone",
		Prompt: "Your WhatsApp phone number",
		Hint:   "With the country code, e.g. +1 555 010 2030",
		Alts:   []core.ConnectAlt{altQR},
	}, "")
}

// switchTo changes method mid-flow, reusing the connection.
func (f *linkFlow) switchTo(method string) {
	if method == "phone" {
		f.askPhone()
		return
	}
	f.mu.Lock()
	f.mode, f.field = "qr", ""
	f.mu.Unlock()
	// The next QR code from the channel shows up within ~20 s; say so now.
	f.show(&core.ConnectStep{Kind: "waiting", Prompt: "Getting a QR code…", Alts: []core.ConnectAlt{altPhone}}, "qr")
}

// pairDigits keeps the digits of a phone number: WhatsApp wants no "+".
func pairDigits(s string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", errors.New("a phone number has only digits, like +1 555 010 2030")
		}
	}
	if b.Len() < 7 {
		return "", errors.New("include the country code, like +1 555 010 2030")
	}
	return b.String(), nil
}

// input takes the phone number and shows the pairing code for it.
func (f *linkFlow) input(ctx context.Context, value string) error {
	f.mu.Lock()
	field := f.field
	f.mu.Unlock()
	if field != "phone" {
		return errors.New("nothing is waiting for input")
	}
	phone, err := pairDigits(value)
	if err != nil {
		return err
	}
	code, err := f.link.PairPhone(ctx, phone)
	if err != nil {
		return fmt.Errorf("WhatsApp didn't give a code for that number: %w", err)
	}
	f.mu.Lock()
	f.field = ""
	f.mu.Unlock()
	f.show(&core.ConnectStep{
		Kind: "code", Value: code,
		Prompt: "On your phone: WhatsApp → Settings → Linked devices → Link a device → Link with phone number instead, then enter this code",
		Hint:   "Sent a notification to +" + phone,
		Alts:   []core.ConnectAlt{altQR},
	}, "phone")
	return nil
}

// done ends the flow after the device is linked.
func (f *linkFlow) done() {
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return
	}
	f.ended = true
	cancel := f.cancel
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = os.Remove(filepath.Join(f.dir, "qr.png"))
}

func (f *linkFlow) fail(reason string) {
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return
	}
	f.ended = true
	cancel := f.cancel
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	f.link.Stop()
	_ = os.Remove(filepath.Join(f.dir, "qr.png"))
	f.store.SetStatus(core.StatusDisconnected, reason)
}

func (f *linkFlow) abort() { f.fail("") }

// writeQR renders a QR payload as <dir>/qr.png.
func writeQR(dir, payload string) (string, error) {
	code, err := qr.Encode(payload, qr.M)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "qr.png")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, code.PNG(), 0o600); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}
