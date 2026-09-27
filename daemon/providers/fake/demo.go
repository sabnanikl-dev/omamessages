//go:build demo

package fake

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"omarchy-omamessages/core"
)

// Demo is a stand-in for a real service with made-up people and chats, for
// screenshots. It runs under the real service IDs (gmessages, telegram,
// whatsapp) so the panel shows their tabs and marks. Only in `-tags demo`
// builds, started with `omamessagesd serve --demo`.

type demoLine struct {
	fromMe  bool
	sender  string
	text    string
	minsAgo int
	photo   bool   // a received photo (a drawn picture)
	file    string // a received file's name
}

type demoConv struct {
	id, name string
	group    bool
	unread   bool
	pinned   bool
	kind     string // gmessages: sms|rcs
	lines    []demoLine
}

type demoService struct {
	name, account string
	caps          core.Caps
	convs         []demoConv
}

var demoServices = map[core.ProviderID]demoService{
	core.GMessages: {
		name: "Messages", account: "demo@example.com",
		caps: core.Caps{StartByNumber: true, ContactSearch: true, Typing: true, LoadHistory: true, PhoneTethered: true},
		convs: []demoConv{
			{id: "101", name: "Priya Shah", kind: "rcs", unread: true, lines: []demoLine{
				{false, "Priya Shah", "Landing at 6:40, can you grab me?", 38, false, ""},
			}},
			{id: "102", name: "52886", kind: "sms", unread: true, lines: []demoLine{
				{false, "52886", "Your verification code is 481 223", 52, false, ""},
			}},
			{id: "103", name: "Jake Morrison", kind: "sms", lines: []demoLine{
				{false, "Jake Morrison", "Pickup game Thursday?", 2890, false, ""},
				{true, "", "👍", 2885, false, ""},
			}},
			{id: "104", name: "Riverside Dental", kind: "sms", lines: []demoLine{
				{false, "Riverside Dental", "Reminder: your appointment is Tue at 3:30pm. Reply C to confirm.", 4400, false, ""},
			}},
		},
	},
	core.Telegram: {
		name: "Telegram", account: "+1 555 010 2030 · @demo",
		caps: core.Caps{StartByNumber: true, ContactSearch: true, Typing: true, LoadHistory: true, Reactions: true, FetchMedia: true, Attachments: true},
		convs: []demoConv{
			{id: "-1001001", name: "Dev chat", group: true, unread: true, lines: []demoLine{
				{false, "Priya Shah", "PR for the panel tabs is up", 70, false, ""},
				{true, "", "looking now", 64, false, ""},
				{false, "Leo Park", "CI is green on my side", 30, false, ""},
				{false, "Priya Shah", "merged, thanks!", 18, false, ""},
			}},
			{id: "2002", name: "Alex Rivera", unread: true, lines: []demoLine{
				{true, "", "did the desk arrive?", 140, false, ""},
				{false, "Alex Rivera", "the new desk setup", 111, true, ""},
				{false, "Alex Rivera", "", 110, false, "invoice-sept.pdf"},
				{true, "", "looks clean! that monitor arm 👌", 105, false, ""},
				{false, "Alex Rivera", "right? cable management took an hour", 100, false, ""},
			}},
			{id: "-1003003", name: "Omarchy News", lines: []demoLine{
				{false, "Omarchy News", "3.2 is out: new shell plugins, faster bar", 2950, false, ""},
			}},
		},
	},
	core.WhatsApp: {
		name: "WhatsApp", account: "+1 555 010 2030 · Demo",
		caps: core.Caps{PhoneTethered: true, FetchMedia: true, StartByNumber: true, ContactSearch: true, Typing: true},
		convs: []demoConv{
			{id: "15550100001@s.whatsapp.net", name: "Mom", unread: true, pinned: false, lines: []demoLine{
				{false, "Mom", "Did you get the photos from the weekend?", 1510, false, ""},
				{true, "", "Yes! the lake one is great", 1502, false, ""},
				{false, "Mom", "Are you coming Sunday?", 12, false, ""},
			}},
			{id: "120363000000000101@g.us", name: "Family", group: true, lines: []demoLine{
				{false, "Dad", "Dinner at ours, 7pm", 1600, false, ""},
				{true, "", "see you all then", 1580, false, ""},
			}},
			{id: "15550100003@s.whatsapp.net", name: "Sara Lindqvist", lines: []demoLine{
				{false, "Sara Lindqvist", "you have to see this place", 400, false, ""},
				{true, "", "saving it for next trip", 395, false, ""},
				{false, "Sara Lindqvist", "lol yes", 390, false, ""},
			}},
		},
	},
}

type Demo struct {
	id  core.ProviderID
	svc demoService
	env core.Env
	now time.Time
}

// DemoFactories returns demo stand-ins for the three real services.
func DemoFactories() map[core.ProviderID]core.Factory {
	out := map[core.ProviderID]core.Factory{}
	for id, svc := range demoServices {
		id, svc := id, svc
		out[id] = func(env core.Env) core.Provider { return &Demo{id: id, svc: svc, env: env, now: time.Now()} }
	}
	return out
}

func (d *Demo) ID() core.ProviderID { return d.id }
func (d *Demo) Name() string        { return d.svc.name }
func (d *Demo) Caps() core.Caps     { return d.svc.caps }

func (d *Demo) ts(minsAgo int) int64 {
	return d.now.Add(-time.Duration(minsAgo) * time.Minute).UnixMilli()
}

func (d *Demo) Start(ctx context.Context) error {
	for _, c := range d.svc.convs {
		last := c.lines[len(c.lines)-1]
		conv := &core.Conversation{
			ID: c.id, Name: c.name, IsGroup: c.group, Unread: c.unread, Pinned: c.pinned,
			LastMessage: last.text, LastFromMe: last.fromMe, LastTs: d.ts(last.minsAgo),
			Participants: []core.Participant{{ID: c.id, Name: c.name}},
		}
		if last.text == "" && last.file != "" {
			conv.LastMessage = last.file
		}
		if c.group && !last.fromMe {
			conv.LastSender = firstWord(last.sender)
		}
		if c.kind != "" {
			conv.Extra = map[string]any{"type": c.kind}
		}
		d.env.Store.UpsertConversation(conv)
	}
	d.env.Store.SetAccount(d.svc.account)
	if d.id == core.GMessages {
		d.env.Store.SetExtra("phone", "+1 555 010 2030")
	}
	d.env.Store.MarkAlive()
	d.env.Store.SetStatus(core.StatusConnected, "")
	return nil
}

func firstWord(s string) string {
	for i, r := range s {
		if r == ' ' {
			return s[:i]
		}
	}
	return s
}

func (d *Demo) Open(ctx context.Context, convID string, markRead bool) error {
	for _, c := range d.svc.convs {
		if c.id != convID {
			continue
		}
		var msgs []core.Message
		for i, l := range c.lines {
			m := core.Message{ID: strconv.Itoa(i + 1), Ts: d.ts(l.minsAgo), FromMe: l.fromMe, Sender: l.sender, SenderID: l.sender, Text: l.text, Status: "received"}
			if l.fromMe {
				m.Sender, m.Status = "Me", "read"
			}
			if l.photo {
				p := filepath.Join(d.env.CacheDir, "media", core.SafeName(convID), m.ID+"-thumb.png")
				if err := drawPhoto(p); err == nil {
					m.Attachments = []core.Attachment{{Name: "Photo", Kind: "image", Mime: "image/png", Width: 1280, Height: 853, Size: 312000, ThumbPath: p}}
				}
			}
			if l.file != "" {
				m.Attachments = []core.Attachment{{Name: l.file, Kind: "file", Mime: "application/pdf", Size: 188416}}
			}
			msgs = append(msgs, m)
		}
		d.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
			cm.Messages, cm.Loading, cm.HasMore = msgs, false, false
		})
		if markRead {
			if cc := d.env.Store.Conversation(convID); cc != nil && cc.Unread {
				cp := *cc
				cp.Unread = false
				d.env.Store.UpsertConversation(&cp)
				d.env.Store.Flush()
			}
		}
		if convID == "2002" {
			// Alex is typing, for the thread screenshot.
			d.env.Store.SetTyping(convID, time.Now().Add(10*time.Minute))
		}
		return nil
	}
	return nil
}

// drawPhoto paints a lake at sunset: a sky gradient, a sun, hills and water.
func drawPhoto(path string) error {
	const w, h = 640, 427
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	lerp := func(a, b uint8, t float64) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(y) / float64(h)
			c := color.RGBA{lerp(59, 230, t*1.4), lerp(93, 142, t*1.2), lerp(122, 13, t), 255}
			if t > 1/1.4 {
				c = color.RGBA{230, 142, 13, 255}
			}
			// sun
			if dx, dy := float64(x-420), float64(y-205); dx*dx+dy*dy < 38*38 {
				c = color.RGBA{255, 214, 140, 255}
			}
			// hills
			hill := 250 + 30*math.Sin(float64(x)/70) + 18*math.Sin(float64(x)/23)
			if float64(y) > hill {
				c = color.RGBA{lerp(40, 22, t), lerp(52, 30, t), lerp(48, 36, t), 255}
			}
			// water
			if y > 300 {
				r := img.RGBAAt(x, 600-y)
				c = color.RGBA{uint8(float64(r.R) * 0.55), uint8(float64(r.G) * 0.6), uint8(float64(r.B)*0.7 + 20), 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func (d *Demo) Stop()                                         {}
func (d *Demo) Refresh(ctx context.Context) error             { return nil }
func (d *Demo) More(ctx context.Context, convID string) error { return nil }
func (d *Demo) Send(ctx context.Context, convID, text string, files []string) error {
	return nil
}
func (d *Demo) Connect(ctx context.Context, method string) error     { return nil }
func (d *Demo) ConnectInput(ctx context.Context, value string) error { return nil }
func (d *Demo) CancelConnect()                                       {}
func (d *Demo) Disconnect(ctx context.Context) error                 { return nil }
func (d *Demo) FetchMedia(ctx context.Context, convID, msgID string, idx int) (string, error) {
	return "", core.ErrUnsupported
}
func (d *Demo) StartChat(ctx context.Context, to, text string) (string, error) {
	return "", core.ErrUnsupported
}
func (d *Demo) Contacts(ctx context.Context, query string) ([]core.Participant, error) {
	var out []core.Participant
	for _, c := range d.svc.convs {
		if !c.group {
			out = append(out, core.Participant{ID: c.id, Name: c.name, Number: "+1 555 010 0000"})
		}
	}
	return out, nil
}
