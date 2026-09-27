package gmessages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"omarchy-omamessages/core"
)

// Preview shows what the older gmessages plugin has cached, without
// touching Google or the pairing. Until cutover the old gmessagesd owns the
// pairing, and two clients on one pairing would fight, so the new daemon
// only reads the old plugin's files (state.json and messages/<id>.json) and
// never writes to its directory.

// PreviewMarker is written into the provider's own directory so the cutover
// migration knows that directory holds preview copies, not a real session.
const PreviewMarker = "PREVIEW"

const previewNote = "Read-only preview until cutover"

var errPreview = errors.New("Google Messages is read-only here until cutover")

type Preview struct {
	env core.Env
	src string // the old plugin's state dir; read, never written

	mu      sync.Mutex
	cancel  context.CancelFunc
	lastMod time.Time
}

// NewPreview returns a factory for a preview of the old plugin's state in src.
func NewPreview(src string) core.Factory {
	return func(env core.Env) core.Provider {
		return &Preview{env: env, src: src}
	}
}

// OldStateDir is where the older gmessages plugin keeps its state, honoring the same
// overrides as the old daemon.
func OldStateDir() string {
	if d := os.Getenv("OMARCHY_GMESSAGES_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(base, "omarchy", "gmessages")
}

func (p *Preview) ID() core.ProviderID { return ID }
func (p *Preview) Name() string        { return "Messages" }
func (p *Preview) Caps() core.Caps     { return core.Caps{} }

func (p *Preview) Start(ctx context.Context) error {
	if err := os.MkdirAll(p.env.Dir, 0o700); err == nil {
		_ = os.WriteFile(filepath.Join(p.env.Dir, PreviewMarker), []byte("Copies of the older gmessages plugin's state for a read-only preview. Safe to delete.\n"), 0o600)
	}
	p.env.Store.SetExtra("preview", true)
	err := p.reload(true)
	p.env.Store.SetStatus(core.StatusDisconnected, previewNote)
	ctx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	p.env.Spawn(func() { p.follow(ctx) })
	return err
}

func (p *Preview) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
}

// follow picks up the old daemon's changes to its state.json.
func (p *Preview) follow(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.reload(false); err != nil {
				p.env.Log.Debug().Err(err).Msg("Preview reload failed")
			}
		}
	}
}

// oldState is the part of the old plugin's state.json the preview uses.
type oldState struct {
	Status        string            `json:"status"`
	Account       string            `json:"account"`
	Phone         string            `json:"phone"`
	SMSDefault    *bool             `json:"smsDefault"`
	Conversations []oldConversation `json:"conversations"`
}

// oldConversation is the old Conversation JSON: the shared fields plus a
// top-level "type" that now lives in Extra.
type oldConversation struct {
	core.Conversation
	Type string `json:"type"`
}

// reload copies the old state into the store when state.json has changed
// since the last look, or always when force is set.
func (p *Preview) reload(force bool) error {
	path := filepath.Join(p.src, "state.json")
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	unchanged := !force && info.ModTime().Equal(p.lastMod)
	p.mu.Unlock()
	if unchanged {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var st oldState
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	for _, oc := range st.Conversations {
		c := oc.Conversation
		if oc.Type != "" {
			c.Extra = map[string]any{"type": oc.Type}
		}
		p.env.Store.UpsertConversation(&c)
	}
	p.env.Store.SetAccount(st.Account)
	if st.Phone != "" {
		p.env.Store.SetExtra("phone", st.Phone)
	}
	if st.SMSDefault != nil {
		p.env.Store.SetExtra("smsDefault", *st.SMSDefault)
	}
	// Whether the old daemon is itself connected, for the Accounts screen.
	p.env.Store.SetExtra("sourceStatus", st.Status)
	p.env.Store.Flush()
	p.mu.Lock()
	p.lastMod = info.ModTime()
	p.mu.Unlock()
	return nil
}

func (p *Preview) Refresh(ctx context.Context) error { return p.reload(true) }

// Open shows the old plugin's cached copy of a thread. Only threads opened in
// the old panel are cached there; nothing is fetched and nothing marked read.
func (p *Preview) Open(ctx context.Context, convID string, markRead bool) error {
	data, err := os.ReadFile(filepath.Join(p.src, "messages", core.SafeName(convID)+".json"))
	var old core.ConversationMessages
	if err == nil {
		err = json.Unmarshal(data, &old)
	}
	p.env.Store.UpdateMessages(convID, func(cm *core.ConversationMessages) {
		cm.Loading = false
		cm.HasMore = false
		cm.Error = ""
		switch {
		case err == nil:
			cm.Messages = old.Messages
			if cm.Messages == nil {
				cm.Messages = []core.Message{}
			}
		case errors.Is(err, os.ErrNotExist):
			cm.Messages = []core.Message{}
			cm.Error = "The old Google Messages panel hasn't cached this thread. Open it there once to see it here."
		default:
			cm.Error = "Could not read the cached thread: " + err.Error()
		}
	})
	return nil
}

func (p *Preview) More(ctx context.Context, convID string) error { return nil }
func (p *Preview) Send(ctx context.Context, convID, text string, files []string) error {
	return errPreview
}
func (p *Preview) Connect(ctx context.Context, method string) error     { return errPreview }
func (p *Preview) ConnectInput(ctx context.Context, value string) error { return errPreview }
func (p *Preview) CancelConnect()                                       {}
func (p *Preview) Disconnect(ctx context.Context) error                 { return errPreview }
func (p *Preview) StartChat(ctx context.Context, to, text string) (string, error) {
	return "", errPreview
}
func (p *Preview) Contacts(ctx context.Context, query string) ([]core.Participant, error) {
	return nil, errPreview
}
func (p *Preview) FetchMedia(ctx context.Context, convID, msgID string, idx int) (string, error) {
	return "", errPreview
}

// ConvertOldState turns the old plugin's state.json into this provider's,
// for the one-time migration at cutover: SMS/RCS moves into extra.type, the
// phone details into extra. Status isn't carried over (the new daemon finds
// out for itself).
func ConvertOldState(data []byte) (core.ProviderState, error) {
	var st oldState
	if err := json.Unmarshal(data, &st); err != nil {
		return core.ProviderState{}, err
	}
	out := core.ProviderState{
		Status:        core.StatusDisconnected,
		Account:       st.Account,
		Extra:         map[string]any{},
		Conversations: []core.Conversation{},
	}
	for _, oc := range st.Conversations {
		c := oc.Conversation
		if oc.Type != "" {
			c.Extra = map[string]any{"type": oc.Type}
		}
		out.Conversations = append(out.Conversations, c)
	}
	if st.Phone != "" {
		out.Extra["phone"] = st.Phone
	}
	if st.SMSDefault != nil {
		out.Extra["smsDefault"] = *st.SMSDefault
	}
	return out, nil
}
