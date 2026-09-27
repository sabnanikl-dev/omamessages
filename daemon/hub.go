package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"omarchy-omamessages/core"
)

// The hub owns the providers. It creates and starts the enabled ones, routes
// commands to them by namespaced conversation ID, and merges their stores
// into the one state.json the panel watches.

type MergedProvider struct {
	ID           core.ProviderID   `json:"id"`
	Name         string            `json:"name"`
	Enabled      bool              `json:"enabled"`
	Unread       int               `json:"unread"`
	Caps         core.Caps         `json:"caps"`
	Status       core.Status       `json:"status"`
	Error        string            `json:"error,omitempty"`
	Account      string            `json:"account,omitempty"`
	Connect      *core.ConnectStep `json:"connect,omitempty"`
	LastActivity int64             `json:"lastActivity,omitempty"`
	Extra        map[string]any    `json:"extra,omitempty"`
}

type MergedConversation struct {
	core.Conversation
	ID       string          `json:"id"` // namespaced
	Provider core.ProviderID `json:"provider"`
}

type MergedState struct {
	Providers     []MergedProvider     `json:"providers"`
	Conversations []MergedConversation `json:"conversations"` // pinned first, then lastTs desc
	Typing        []core.Typing        `json:"typing"`        // namespaced conversation IDs
	UnreadCount   int                  `json:"unreadCount"`
	UpdatedAt     int64                `json:"updatedAt"`
}

type slot struct {
	p     core.Provider
	store *core.Store
}

type Hub struct {
	dir, cacheDir string
	factories     map[core.ProviderID]core.Factory
	order         []core.ProviderID
	enabled       map[core.ProviderID]bool
	notify        bool

	mu    sync.Mutex
	slots map[core.ProviderID]*slot
	flush chan struct{}
	stop  context.CancelFunc
	done  chan struct{}
}

// serviceNames covers providers that are registered but not enabled, which
// have no instance to ask.
var serviceNames = map[core.ProviderID]string{
	core.GMessages: "Messages",
	core.Telegram:  "Telegram",
	core.WhatsApp:  "WhatsApp",
}

// serviceOrder is the order of the panel's tabs; anything else sorts after.
var serviceOrder = []core.ProviderID{core.GMessages, core.Telegram, core.WhatsApp}

func NewHub(dir, cacheDir string, factories map[core.ProviderID]core.Factory, enabled []core.ProviderID, notify bool) *Hub {
	h := &Hub{
		dir:       dir,
		cacheDir:  cacheDir,
		factories: factories,
		enabled:   map[core.ProviderID]bool{},
		notify:    notify,
		slots:     map[core.ProviderID]*slot{},
		flush:     make(chan struct{}, 1),
	}
	for _, id := range enabled {
		if _, ok := factories[id]; ok {
			h.enabled[id] = true
		}
	}
	for _, id := range serviceOrder {
		if _, ok := factories[id]; ok {
			h.order = append(h.order, id)
		}
	}
	var rest []core.ProviderID
	for id := range factories {
		if _, known := serviceNames[id]; !known {
			rest = append(rest, id)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
	h.order = append(h.order, rest...)
	// Create the enabled providers up front, so Merge and Route work before
	// Start (and in tests that never call it).
	for _, id := range h.order {
		if h.enabled[id] {
			h.slots[id] = h.newSlot(id)
		}
	}
	return h
}

func (h *Hub) newSlot(id core.ProviderID) *slot {
	store := core.NewStore(filepath.Join(h.dir, string(id)), h.requestFlush)
	env := core.Env{
		Store:    store,
		Dir:      filepath.Join(h.dir, string(id)),
		CacheDir: filepath.Join(h.cacheDir, string(id)),
		Notify:   h.notifier(id),
		Log:      logger.With().Str("provider", string(id)).Logger(),
		Guard: func(fn func()) {
			defer h.recoverInto(id, store, nil)
			fn()
		},
	}
	p := h.factories[id](env)
	store.Load()
	return &slot{p: p, store: store}
}

func (h *Hub) notifier(id core.ProviderID) func(core.Notification) {
	return func(n core.Notification) {
		if !h.notify {
			return
		}
		n.Provider = id
		SendNotification(n, h.displayName(id))
	}
}

func (h *Hub) displayName(id core.ProviderID) string {
	h.mu.Lock()
	s := h.slots[id]
	h.mu.Unlock()
	if s != nil {
		return s.p.Name()
	}
	if n, ok := serviceNames[id]; ok {
		return n
	}
	return string(id)
}

// Start starts every enabled provider and the merge loop.
func (h *Hub) Start(ctx context.Context) {
	ctx, h.stop = context.WithCancel(ctx)
	h.done = make(chan struct{})
	go h.flushLoop(ctx)
	h.mu.Lock()
	slots := make([]*slot, 0, len(h.slots))
	for _, id := range h.order {
		if s := h.slots[id]; s != nil {
			slots = append(slots, s)
		}
	}
	h.mu.Unlock()
	for _, s := range slots {
		go func() {
			if err := h.Do(s.p.ID(), func(p core.Provider) error { return p.Start(ctx) }); err != nil {
				logger.Err(err).Str("provider", string(s.p.ID())).Msg("Provider failed to start")
				s.store.SetStatus(core.StatusError, err.Error())
			}
		}()
	}
	h.requestFlush()
}

// Stop stops every provider and writes the merged state one last time.
func (h *Hub) Stop() {
	h.mu.Lock()
	slots := make([]*slot, 0, len(h.slots))
	for _, s := range h.slots {
		slots = append(slots, s)
	}
	h.mu.Unlock()
	for _, s := range slots {
		s.p.Stop()
	}
	if h.stop != nil {
		h.stop()
		<-h.done
	}
	h.writeMerged()
}

// Provider returns an enabled provider.
func (h *Hub) Provider(id core.ProviderID) (core.Provider, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.slots[id]; s != nil {
		return s.p, nil
	}
	if _, ok := h.factories[id]; ok {
		return nil, fmt.Errorf("%s is not enabled", h.nameLocked(id))
	}
	return nil, fmt.Errorf("unknown service %q", string(id))
}

func (h *Hub) nameLocked(id core.ProviderID) string {
	if n, ok := serviceNames[id]; ok {
		return n
	}
	return string(id)
}

// Route resolves a namespaced conversation ID to its provider and native ID.
func (h *Hub) Route(id string) (core.Provider, string, error) {
	pid, native, err := core.SplitID(id)
	if err != nil {
		return nil, "", err
	}
	p, err := h.Provider(pid)
	if err != nil {
		return nil, "", err
	}
	return p, native, nil
}

// Do runs fn against one provider. A panic in fn marks that provider as
// "error" and comes back as an error; the other providers never notice.
func (h *Hub) Do(id core.ProviderID, fn func(core.Provider) error) (err error) {
	p, err := h.Provider(id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	store := h.slots[id].store
	h.mu.Unlock()
	defer h.recoverInto(id, store, &err)
	return fn(p)
}

// DoConv runs fn against the provider that owns a namespaced conversation ID,
// passing the provider's native ID.
func (h *Hub) DoConv(id string, fn func(p core.Provider, native string) error) error {
	pid, native, err := core.SplitID(id)
	if err != nil {
		return err
	}
	return h.Do(pid, func(p core.Provider) error { return fn(p, native) })
}

// Send sends text (and files, where the service takes them) to a
// conversation. Files for a service without attachments are refused here,
// before the provider is involved.
func (h *Hub) Send(ctx context.Context, id, text string, files []string) error {
	return h.DoConv(id, func(p core.Provider, native string) error {
		if len(files) > 0 && !p.Caps().Attachments {
			return fmt.Errorf("%s: sending files is %w", p.Name(), core.ErrUnsupported)
		}
		if len(files) == 0 {
			files = nil
		}
		return p.Send(ctx, native, text, files)
	})
}

// SetTyping tells a conversation's service whether the user is typing, if
// the service can show that; otherwise it does nothing.
func (h *Hub) SetTyping(ctx context.Context, id string, typing bool) error {
	return h.DoConv(id, func(p core.Provider, native string) error {
		if t, ok := p.(core.Typer); ok {
			return t.SetTyping(ctx, native, typing)
		}
		return nil
	})
}

// recoverInto is deferred around provider code. On a panic it logs the stack,
// sets the provider's status to "error", and (when errp is set) returns the
// panic as the call's error.
func (h *Hub) recoverInto(id core.ProviderID, store *core.Store, errp *error) {
	r := recover()
	if r == nil {
		return
	}
	msg := fmt.Sprintf("%s crashed: %v", h.displayName(id), r)
	logger.Error().Str("provider", string(id)).Str("stack", string(debug.Stack())).Msg(msg)
	store.SetStatus(core.StatusError, msg)
	if errp != nil {
		*errp = errors.New(msg)
	}
}

// Merge builds the panel's view of every provider.
func (h *Hub) Merge() MergedState {
	h.mu.Lock()
	type snap struct {
		id      core.ProviderID
		name    string
		caps    core.Caps
		enabled bool
		state   core.ProviderState
	}
	snaps := make([]snap, 0, len(h.order))
	for _, id := range h.order {
		if s := h.slots[id]; s != nil {
			snaps = append(snaps, snap{id: id, name: s.p.Name(), caps: s.p.Caps(), enabled: true, state: s.store.Snapshot()})
		} else {
			snaps = append(snaps, snap{id: id, name: h.nameLocked(id), state: core.ProviderState{Status: core.StatusDisconnected}})
		}
	}
	h.mu.Unlock()

	out := MergedState{
		Providers:     make([]MergedProvider, 0, len(snaps)),
		Conversations: []MergedConversation{},
		Typing:        []core.Typing{},
		UpdatedAt:     time.Now().UnixMilli(),
	}
	for _, sn := range snaps {
		unread := 0
		for _, c := range sn.state.Conversations {
			if c.Unread {
				unread++
			}
			out.Conversations = append(out.Conversations, MergedConversation{
				Conversation: c,
				ID:           core.JoinID(sn.id, c.ID),
				Provider:     sn.id,
			})
		}
		for _, t := range sn.state.Typing {
			out.Typing = append(out.Typing, core.Typing{ConversationID: core.JoinID(sn.id, t.ConversationID), Until: t.Until})
		}
		out.UnreadCount += unread
		out.Providers = append(out.Providers, MergedProvider{
			ID:           sn.id,
			Name:         sn.name,
			Enabled:      sn.enabled,
			Unread:       unread,
			Caps:         sn.caps,
			Status:       sn.state.Status,
			Error:        sn.state.Error,
			Account:      sn.state.Account,
			Connect:      sn.state.Connect,
			LastActivity: sn.state.LastActivity,
			Extra:        sn.state.Extra,
		})
	}
	sort.SliceStable(out.Conversations, func(i, j int) bool {
		a, b := out.Conversations[i], out.Conversations[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		return a.LastTs > b.LastTs
	})
	return out
}

// Status is the short summary `omamessagesd status` prints.
func (h *Hub) Status() map[string]any {
	m := h.Merge()
	providers := map[string]string{}
	for _, p := range m.Providers {
		if p.Enabled {
			providers[string(p.ID)] = string(p.Status)
		} else {
			providers[string(p.ID)] = "off"
		}
	}
	return map[string]any{"providers": providers, "unread": m.UnreadCount, "conversations": len(m.Conversations)}
}

func (h *Hub) requestFlush() {
	select {
	case h.flush <- struct{}{}:
	default:
	}
}

// flushLoop coalesces bursts of store changes into one state.json write.
func (h *Hub) flushLoop(ctx context.Context) {
	defer close(h.done)
	const debounce = 150 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.flush:
		}
		timer := time.NewTimer(debounce)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		h.writeMerged()
	}
}

func (h *Hub) writeMerged() {
	if err := core.WriteJSONAtomic(filepath.Join(h.dir, "state.json"), h.Merge(), 0o600); err != nil {
		logger.Err(err).Msg("Failed to write merged state.json")
	}
}
