package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"omarchy-omamessages/core"
)

// Send sends a text message. It shows as "sending" at once, under the ID it
// will be sent with, and turns "sent" when WhatsApp's server takes it.
func (w *WhatsApp) Send(ctx context.Context, convID, text string, files []string) error {
	if files != nil {
		return core.ErrUnsupported
	}
	cli := w.connected()
	if cli == nil {
		return errNotConnected
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty message")
	}
	chat, err := types.ParseJID(convID)
	if err != nil {
		return fmt.Errorf("not a WhatsApp chat: %q", convID)
	}
	id := cli.GenerateMessageID()
	now := time.Now().UnixMilli()
	pending := core.Message{ID: "tmp_" + id, TmpID: "tmp_" + id, Ts: now, FromMe: true, Sender: "Me", Text: text, Status: "sending"}
	w.env.Store.Messages(convID)
	w.env.Store.UpsertMessage(convID, pending)
	if c := w.conversationCopy(convID); c != nil {
		c.LastMessage, c.LastFromMe, c.LastSender, c.LastTs = text, true, "", now
		w.env.Store.UpsertConversation(c)
		w.env.Store.Flush()
	}

	resp, err := cli.SendMessage(ctx, chat, &waE2E.Message{Conversation: proto.String(text)}, whatsmeow.SendRequestExtra{ID: id})
	if err != nil {
		pending.Status, pending.StatusText = "failed", sendError(err)
		w.env.Store.UpsertMessage(convID, pending)
		return errors.New(pending.StatusText)
	}
	ts := now
	if !resp.Timestamp.IsZero() {
		ts = resp.Timestamp.UnixMilli()
	}
	sent := core.Message{ID: id, TmpID: pending.TmpID, Ts: ts, FromMe: true, Sender: "Me", Text: text, Status: "sent",
		SenderID: cli.Store.ID.ToNonAD().String()}
	w.env.Store.UpsertMessage(convID, sent)
	w.env.Store.MarkAlive()
	return nil
}

func sendError(err error) string {
	switch {
	case errors.Is(err, whatsmeow.ErrNotLoggedIn), errors.Is(err, whatsmeow.ErrNotConnected):
		return "WhatsApp is not connected"
	case errors.Is(err, context.DeadlineExceeded):
		return "WhatsApp didn't answer in time"
	}
	return "Not sent: " + err.Error()
}

// target is who a new chat is with: a JID (from Contacts or a known chat) or
// a phone number to look up.
type target struct {
	jid   types.JID
	phone string // "+15550102030", when jid is empty
}

// parseTarget reads the recipient field of a new message.
func parseTarget(to string) (target, error) {
	to = strings.TrimSpace(to)
	if strings.Contains(to, "@") {
		jid, err := types.ParseJID(to)
		if err != nil || jid.User == "" {
			return target{}, fmt.Errorf("not a WhatsApp address: %q", to)
		}
		return target{jid: jid.ToNonAD()}, nil
	}
	digits, err := pairDigits(to)
	if err != nil {
		return target{}, errors.New("enter a name from your contacts, or a phone number with its country code")
	}
	return target{phone: "+" + digits}, nil
}

// StartChat opens (or finds) a chat with a contact, a group, or a phone
// number, and sends text into it if there is any.
func (w *WhatsApp) StartChat(ctx context.Context, to, text string) (string, error) {
	cli := w.connected()
	if cli == nil {
		return "", errNotConnected
	}
	t, err := parseTarget(to)
	if err != nil {
		return "", err
	}
	chat := t.jid
	if chat.IsEmpty() {
		res, err := cli.IsOnWhatsApp(ctx, []string{t.phone})
		if err != nil {
			return "", fmt.Errorf("couldn't check that number: %w", err)
		}
		if len(res) == 0 || !res[0].IsIn {
			return "", fmt.Errorf("%s isn't on WhatsApp", t.phone)
		}
		chat = res[0].JID.ToNonAD()
	}
	chat = w.chatID(cli, chat)
	convID := chat.String()
	if w.env.Store.Conversation(convID) == nil {
		names := contactNames{w: w, cli: cli}
		c := &core.Conversation{ID: convID, Name: names.Name(chat), IsGroup: chat.Server == types.GroupServer, LastTs: time.Now().UnixMilli(), Extra: map[string]any{}}
		if !c.IsGroup {
			c.Participants = []core.Participant{{ID: convID, Name: c.Name, Number: fallbackName(chat)}}
		}
		w.env.Store.UpsertConversation(c)
		w.env.Store.Flush()
		if c.IsGroup && c.Name == fallbackName(chat) {
			w.env.Spawn(func() { w.learnGroupName(cli, chat) })
		}
	}
	if strings.TrimSpace(text) != "" {
		if err := w.Send(ctx, convID, text, nil); err != nil {
			return convID, err
		}
	}
	return convID, nil
}

// Contacts searches the people whatsmeow knows (saved names and profile
// names) and the groups in the chat list. It works offline.
func (w *WhatsApp) Contacts(ctx context.Context, query string) ([]core.Participant, error) {
	cli := w.connected()
	if cli == nil {
		return nil, errNotConnected
	}
	all, err := cli.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return nil, err
	}
	var people []core.Participant
	for jid, ci := range all {
		if jid.Server != types.DefaultUserServer {
			continue // hidden @lid twins of the same people
		}
		name := ""
		for _, s := range []string{ci.FullName, ci.FirstName, ci.BusinessName, ci.PushName} {
			if s != "" {
				name = s
				break
			}
		}
		if name == "" {
			continue
		}
		people = append(people, core.Participant{ID: jid.String(), Name: name, Number: "+" + jid.User})
	}
	for _, c := range w.env.Store.Snapshot().Conversations {
		if c.IsGroup {
			people = append(people, core.Participant{ID: c.ID, Name: c.Name, Number: "group"})
		}
	}
	return matchContacts(people, query, 20), nil
}

// matchContacts filters by name or number and ranks: names that start with
// the query, then names containing it, then numbers; alphabetical within.
func matchContacts(people []core.Participant, query string, limit int) []core.Participant {
	q := strings.ToLower(strings.TrimSpace(query))
	qDigits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, q)
	type ranked struct {
		p    core.Participant
		rank int
	}
	var out []ranked
	for _, p := range people {
		name := strings.ToLower(p.Name)
		rank := -1
		switch {
		case q == "":
			rank = 0
		case strings.HasPrefix(name, q):
			rank = 0
		case strings.Contains(name, " "+q) || strings.Contains(name, q):
			rank = 1
		case len(qDigits) >= 3 && strings.Contains(p.Number, qDigits):
			rank = 2
		}
		if rank >= 0 {
			out = append(out, ranked{p, rank})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		return strings.ToLower(out[i].p.Name) < strings.ToLower(out[j].p.Name)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	res := make([]core.Participant, len(out))
	for i, r := range out {
		res[i] = r.p
	}
	return res
}
