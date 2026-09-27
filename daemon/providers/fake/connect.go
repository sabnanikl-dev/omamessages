package fake

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"omarchy-omamessages/core"
)

// The fake's connect flow scripts every kind of step the panel has to draw,
// so the Accounts and Connect screens can be finished before any real
// service uses them. Methods:
//
//	""/"emoji"  sign-in wait, then an emoji to "tap"; connects by itself after a few seconds
//	"qr"        a QR image refreshed every 20 s; never completes (pick another method or cancel)
//	"code"      a pairing code to type on the "phone"; connects by itself after a few seconds
//	"input"     phone → code → password text steps; the code 000000 is rejected
//
// Every step offers the other methods as alternatives.

const signedOutFile = "signed-out"

// How long the scripted steps take; tests shorten them.
var (
	signInDelay   = 1500 * time.Millisecond
	autoConnect   = 6 * time.Second
	qrRefreshEach = 20 * time.Second
)

type flow struct {
	cancel context.CancelFunc
	field  string // the input step waiting for ConnectInput, if any
}

var fakeAlts = []core.ConnectAlt{
	{Method: "emoji", Label: "emoji"},
	{Method: "qr", Label: "QR code"},
	{Method: "code", Label: "pairing code"},
	{Method: "input", Label: "phone number"},
}

func altsExcept(method string) []core.ConnectAlt {
	var out []core.ConnectAlt
	for _, a := range fakeAlts {
		if a.Method != method {
			out = append(out, a)
		}
	}
	return out
}

func (f *Fake) signedOut() bool {
	_, err := os.Stat(filepath.Join(f.env.Dir, signedOutFile))
	return err == nil
}

func (f *Fake) Connect(ctx context.Context, method string) error {
	if method == "" {
		method = "emoji"
	}
	if f.env.Store.Status() == core.StatusConnected {
		return errors.New("already connected; disconnect first")
	}
	f.mu.Lock()
	if f.flow != nil {
		f.flow.cancel()
	}
	life := f.ctx
	if life == nil {
		life = context.Background()
	}
	fctx, cancel := context.WithCancel(life)
	fl := &flow{cancel: cancel}
	f.flow = fl
	f.mu.Unlock()

	switch method {
	case "emoji":
		f.env.Store.SetConnect(&core.ConnectStep{Kind: "waiting", Prompt: "Signing in as demo@fake…", Hint: "from your browser", Alts: altsExcept("emoji")})
		f.env.Spawn(func() {
			if !sleep(fctx, signInDelay) {
				return
			}
			f.env.Store.SetConnect(&core.ConnectStep{Kind: "emoji", Value: "🦊", Prompt: "Tap this emoji on your phone", Hint: "Signed in as demo@fake (from your browser)", Alts: altsExcept("emoji")})
			if sleep(fctx, autoConnect) {
				f.finish(fl)
			}
		})
	case "qr":
		f.env.Spawn(func() {
			for i := 0; ; i++ {
				path := filepath.Join(f.env.Dir, "qr.png")
				if err := writeFakeQR(path, uint64(i)); err != nil {
					f.fail(fl, "Could not draw the QR code: "+err.Error())
					return
				}
				f.env.Store.SetConnect(&core.ConnectStep{
					Kind: "qr", QRPath: path, QRExpires: time.Now().Add(qrRefreshEach).UnixMilli(),
					Prompt: "On your phone: Fake → Settings → Linked devices → Link a device",
					Alts:   altsExcept("qr"),
				})
				if !sleep(fctx, qrRefreshEach) {
					return
				}
			}
		})
	case "code":
		f.env.Store.SetConnect(&core.ConnectStep{Kind: "code", Value: "FAKE-1234", Prompt: "Enter this code on your phone", Hint: "Fake → Linked devices → Link with phone number instead", Alts: altsExcept("code")})
		f.env.Spawn(func() {
			if sleep(fctx, autoConnect) {
				f.finish(fl)
			}
		})
	case "input":
		f.ask(fl, "phone")
	default:
		cancel()
		return errors.New("unknown connect method " + method)
	}
	return nil
}

// ask shows one of the input steps.
func (f *Fake) ask(fl *flow, field string) {
	step := &core.ConnectStep{Kind: "input", Field: field, Alts: altsExcept("input")}
	switch field {
	case "phone":
		step.Prompt = "Your phone number"
		step.Hint = "In international form, e.g. +1 555 010 2030"
	case "code":
		step.Prompt = "Enter the code Fake just sent you"
		step.Hint = "Any 6 digits except 000000. If you use a cloud password, you'll be asked for it next."
	case "password":
		step.Prompt = "Your cloud password"
		step.Secret = true
		step.Hint = "Anything works here"
	}
	f.mu.Lock()
	fl.field = field
	f.mu.Unlock()
	f.env.Store.SetConnect(step)
}

func (f *Fake) ConnectInput(ctx context.Context, value string) error {
	f.mu.Lock()
	fl := f.flow
	field := ""
	if fl != nil {
		field = fl.field
	}
	f.mu.Unlock()
	value = strings.TrimSpace(value)
	switch field {
	case "phone":
		if len(value) < 4 {
			return errors.New("that doesn't look like a phone number")
		}
		f.ask(fl, "code")
	case "code":
		if value == "000000" || len(value) != 6 {
			return errors.New("wrong code, try again")
		}
		f.ask(fl, "password")
	case "password":
		if value == "" {
			return errors.New("enter the password")
		}
		f.finish(fl)
	default:
		return errors.New("nothing is waiting for input")
	}
	return nil
}

// finish completes fl if it is still the current flow.
func (f *Fake) finish(fl *flow) {
	f.mu.Lock()
	current := f.flow == fl
	if current {
		f.flow = nil
	}
	f.mu.Unlock()
	if !current {
		return
	}
	fl.cancel()
	_ = os.Remove(filepath.Join(f.env.Dir, signedOutFile))
	_ = os.Remove(filepath.Join(f.env.Dir, "qr.png"))
	f.goOnline()
}

func (f *Fake) fail(fl *flow, reason string) {
	f.mu.Lock()
	current := f.flow == fl
	if current {
		f.flow = nil
	}
	f.mu.Unlock()
	if current {
		fl.cancel()
		f.env.Store.SetStatus(core.StatusDisconnected, reason)
	}
}

func (f *Fake) CancelConnect() {
	f.mu.Lock()
	fl := f.flow
	f.flow = nil
	f.mu.Unlock()
	if fl != nil {
		fl.cancel()
	}
	_ = os.Remove(filepath.Join(f.env.Dir, "qr.png"))
	if f.env.Store.Status() != core.StatusConnected {
		f.env.Store.SetStatus(core.StatusDisconnected, "")
	}
}

// Disconnect forgets the fake's chats and stays signed out across restarts.
func (f *Fake) Disconnect(ctx context.Context) error {
	f.CancelConnect()
	if err := os.MkdirAll(f.env.Dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(f.env.Dir, signedOutFile), nil, 0o600); err != nil {
		return err
	}
	f.env.Store.Reset()
	return nil
}

// writeFakeQR draws a QR-looking pattern (not a real code) as a PNG.
func writeFakeQR(path string, seed uint64) error {
	const modules, scale = 29, 8
	r := rand.New(rand.NewPCG(seed, 42))
	img := image.NewGray(image.Rect(0, 0, modules*scale, modules*scale))
	finder := func(x, y int) (bool, bool) {
		for _, o := range [][2]int{{0, 0}, {modules - 7, 0}, {0, modules - 7}} {
			dx, dy := x-o[0], y-o[1]
			if dx >= 0 && dx < 7 && dy >= 0 && dy < 7 {
				ring := dx == 0 || dx == 6 || dy == 0 || dy == 6
				center := dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4
				return true, ring || center
			}
		}
		return false, false
	}
	for y := 0; y < modules; y++ {
		for x := 0; x < modules; x++ {
			in, dark := finder(x, y)
			if !in {
				dark = r.IntN(2) == 0
			}
			c := color.Gray{Y: 255}
			if dark {
				c = color.Gray{Y: 0}
			}
			for py := 0; py < scale; py++ {
				for px := 0; px < scale; px++ {
					img.SetGray(x*scale+px, y*scale+py, c)
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	fh, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := png.Encode(fh, img); err != nil {
		fh.Close()
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
