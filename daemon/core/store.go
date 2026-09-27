package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Log is the daemon's logger; main replaces it before anything runs.
var Log = zerolog.Nop()

// ProviderState is what one provider mirrors to <dir>/state.json, and what the
// hub merges into the panel's state.json.
type ProviderState struct {
	Status        Status         `json:"status"`
	Error         string         `json:"error,omitempty"`
	Account       string         `json:"account,omitempty"`
	Connect       *ConnectStep   `json:"connect,omitempty"`
	LastActivity  int64          `json:"lastActivity,omitempty"` // unix ms of the last sign of life from the service
	Extra         map[string]any `json:"extra,omitempty"`        // gmessages: phone, smsDefault, lastCheck
	UpdatedAt     int64          `json:"updatedAt"`
	Conversations []Conversation `json:"conversations"`
	Typing        []Typing       `json:"typing,omitempty"`
}

// Store holds one provider's conversations and opened threads in memory and
// mirrors them to disk. Every ID in here is the provider's native one.
type Store struct {
	mu       sync.Mutex
	dir      string
	onChange func()
	state    ProviderState
	convs    map[string]*Conversation
	messages map[string]*ConversationMessages
	typing   map[string]int64
}

func NewStore(dir string, onChange func()) *Store {
	if onChange == nil {
		onChange = func() {}
	}
	return &Store{
		dir:      dir,
		onChange: onChange,
		state:    ProviderState{Status: StatusDisconnected, Conversations: []Conversation{}},
		convs:    map[string]*Conversation{},
		messages: map[string]*ConversationMessages{},
		typing:   map[string]int64{},
	}
}

func (s *Store) Dir() string         { return s.dir }
func (s *Store) StatePath() string   { return filepath.Join(s.dir, "state.json") }
func (s *Store) MessagesDir() string { return filepath.Join(s.dir, "messages") }
func (s *Store) MessagesPath(id string) string {
	return filepath.Join(s.MessagesDir(), SafeName(id)+".json")
}

// Load restores the last run's conversations, account and extras, so the
// panel isn't blank after a restart while the service reconnects. Status and
// any connect step are not restored: they describe a process that is gone.
func (s *Store) Load() {
	data, err := os.ReadFile(s.StatePath())
	if err != nil {
		if !os.IsNotExist(err) {
			Log.Warn().Err(err).Str("path", s.StatePath()).Msg("Ignoring unreadable state")
		}
		return
	}
	var prev ProviderState
	if err := json.Unmarshal(data, &prev); err != nil {
		Log.Warn().Err(err).Str("path", s.StatePath()).Msg("Ignoring unparseable state")
		return
	}
	s.mu.Lock()
	for i := range prev.Conversations {
		c := prev.Conversations[i]
		s.convs[c.ID] = &c
	}
	s.state.Account = prev.Account
	s.state.Extra = prev.Extra
	s.state.LastActivity = prev.LastActivity
	s.mu.Unlock()
}

// Snapshot returns a copy of the current state for the hub's merge.
func (s *Store) Snapshot() ProviderState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Store) snapshotLocked() ProviderState {
	list := make([]Conversation, 0, len(s.convs))
	for _, c := range s.convs {
		if !c.Archived {
			list = append(list, *c)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Pinned != list[j].Pinned {
			return list[i].Pinned
		}
		return list[i].LastTs > list[j].LastTs
	})
	now := time.Now().UnixMilli()
	typing := make([]Typing, 0)
	for id, until := range s.typing {
		if until > now {
			typing = append(typing, Typing{ConversationID: id, Until: until})
		} else {
			delete(s.typing, id)
		}
	}
	out := s.state
	out.Conversations = list
	out.Typing = typing
	if s.state.Extra != nil {
		out.Extra = make(map[string]any, len(s.state.Extra))
		for k, v := range s.state.Extra {
			out.Extra[k] = v
		}
	}
	if s.state.Connect != nil {
		step := *s.state.Connect
		out.Connect = &step
	}
	return out
}

// SetStatus updates the connection status and flushes. Any status other than
// pairing ends the connect flow, so its step is cleared.
func (s *Store) SetStatus(st Status, errText string) {
	s.mu.Lock()
	s.state.Status = st
	s.state.Error = errText
	if st != StatusPairing {
		s.state.Connect = nil
	}
	s.mu.Unlock()
	s.Flush()
}

func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Status
}

// SetConnect publishes the step the panel should draw; nil clears it.
func (s *Store) SetConnect(step *ConnectStep) {
	s.mu.Lock()
	s.state.Connect = step
	if step != nil {
		s.state.Status = StatusPairing
		s.state.Error = ""
	}
	s.mu.Unlock()
	s.Flush()
}

func (s *Store) SetAccount(a string) {
	s.mu.Lock()
	changed := s.state.Account != a
	s.state.Account = a
	s.mu.Unlock()
	if changed {
		s.Flush()
	}
}

// SetExtra records a provider-specific field. It does not flush.
func (s *Store) SetExtra(k string, v any) {
	s.mu.Lock()
	if s.state.Extra == nil {
		s.state.Extra = map[string]any{}
	}
	s.state.Extra[k] = v
	s.mu.Unlock()
}

// MarkAlive records proof that the service answered. It returns true when
// that moved the status back to connected, so callers flush only when needed.
func (s *Store) MarkAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastActivity = time.Now().UnixMilli()
	switch s.state.Status {
	case StatusConnecting, StatusPhoneOffline, StatusError:
		s.state.Status = StatusConnected
		s.state.Error = ""
		return true
	}
	return false
}

func (s *Store) LastActivity() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.LastActivity
}

// Reset forgets everything, on disk too, e.g. after a disconnect.
func (s *Store) Reset() {
	s.mu.Lock()
	s.convs = map[string]*Conversation{}
	s.messages = map[string]*ConversationMessages{}
	s.typing = map[string]int64{}
	s.state = ProviderState{Status: StatusDisconnected, Conversations: []Conversation{}}
	s.mu.Unlock()
	_ = os.RemoveAll(s.MessagesDir())
	s.Flush()
}

func (s *Store) UpsertConversation(c *Conversation) {
	s.mu.Lock()
	s.convs[c.ID] = c
	s.mu.Unlock()
}

func (s *Store) Conversation(id string) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.convs[id]
}

func (s *Store) SetTyping(convID string, until time.Time) {
	s.mu.Lock()
	s.typing[convID] = until.UnixMilli()
	s.mu.Unlock()
	s.Flush()
}

// Flush writes <dir>/state.json and tells the hub to re-merge.
func (s *Store) Flush() {
	s.mu.Lock()
	s.state.UpdatedAt = time.Now().UnixMilli()
	snap := s.snapshotLocked()
	s.mu.Unlock()
	if err := WriteJSONAtomic(s.StatePath(), snap, 0o600); err != nil {
		Log.Err(err).Str("path", s.StatePath()).Msg("Failed to write state")
	}
	s.onChange()
}

// Messages returns the cached thread for a conversation, loading it from disk
// or creating an empty one the first time it is asked for.
func (s *Store) Messages(convID string) *ConversationMessages {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm := s.messages[convID]
	if cm == nil {
		cm = loadMessages(s.MessagesPath(convID))
		if cm == nil {
			cm = &ConversationMessages{Messages: []Message{}}
		}
		cm.ConversationID = convID
		cm.Loading = false
		cm.Error = ""
		s.messages[convID] = cm
	}
	return cm
}

func loadMessages(path string) *ConversationMessages {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cm ConversationMessages
	if err := json.Unmarshal(data, &cm); err != nil {
		return nil
	}
	if cm.Messages == nil {
		cm.Messages = []Message{}
	}
	// Paging cursors live in the provider, not on disk, so older pages have to
	// be fetched afresh.
	cm.HasMore = false
	return &cm
}

func (s *Store) HasMessages(convID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.messages[convID]
	return ok
}

// UpsertMessage merges one message into a conversation's cache. Outgoing
// echoes arrive first with only a tmpID, then again with the real ID, so a
// tmpID match replaces the placeholder.
func (s *Store) UpsertMessage(convID string, m Message) {
	s.mu.Lock()
	cm := s.messages[convID]
	if cm == nil {
		s.mu.Unlock()
		return
	}
	replaced := false
	for i := range cm.Messages {
		if cm.Messages[i].ID == m.ID || (m.TmpID != "" && cm.Messages[i].TmpID == m.TmpID) || (m.TmpID != "" && cm.Messages[i].ID == m.TmpID) {
			cm.Messages[i] = m
			replaced = true
			break
		}
	}
	// Some services echo a sent message back with their own IDs, not the tmpID
	// we tagged it with. Match the optimistic placeholder by content instead.
	if !replaced && m.FromMe && !IsPlaceholder(m) {
		if i := placeholderIndex(cm.Messages, m); i >= 0 {
			cm.Messages[i] = m
			replaced = true
		}
	}
	if !replaced {
		cm.Messages = append(cm.Messages, m)
	}
	sort.SliceStable(cm.Messages, func(i, j int) bool { return cm.Messages[i].Ts < cm.Messages[j].Ts })
	s.mu.Unlock()
	s.FlushMessages(convID)
}

// IsPlaceholder reports whether m is an optimistic outgoing entry that the
// service hasn't confirmed yet.
func IsPlaceholder(m Message) bool {
	return strings.HasPrefix(m.ID, "tmp_")
}

// placeholderIndex finds a pending outgoing placeholder that the real message
// m corresponds to: same text, sent within the last few minutes.
func placeholderIndex(ms []Message, m Message) int {
	const window = 5 * 60 * 1000
	for i := range ms {
		p := ms[i]
		if !IsPlaceholder(p) || !p.FromMe || p.Text != m.Text {
			continue
		}
		if m.Ts-p.Ts > -window && m.Ts-p.Ts < window {
			return i
		}
	}
	return -1
}

// UpdateMessages runs fn on a conversation's cached thread under the store's
// lock, then writes the thread out. fn must not call back into the store.
func (s *Store) UpdateMessages(convID string, fn func(cm *ConversationMessages)) {
	cm := s.Messages(convID)
	s.mu.Lock()
	fn(cm)
	s.mu.Unlock()
	s.FlushMessages(convID)
}

// DropStalePlaceholders removes optimistic entries that a fresh fetch from the
// service has superseded, or that have been pending for too long.
func DropStalePlaceholders(ms []Message) []Message {
	now := time.Now().UnixMilli()
	out := ms[:0]
	for _, p := range ms {
		if IsPlaceholder(p) {
			if p.Status == "sending" && now-p.Ts > 5*60*1000 {
				continue
			}
			if placeholderSuperseded(ms, p) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func placeholderSuperseded(ms []Message, p Message) bool {
	for _, m := range ms {
		if !IsPlaceholder(m) && m.FromMe && m.Text == p.Text && m.Ts-p.Ts > -5*60*1000 && m.Ts-p.Ts < 5*60*1000 {
			return true
		}
	}
	return false
}

// SetAttachmentPath records where an attachment was downloaded to.
func (s *Store) SetAttachmentPath(convID, msgID string, idx int, path string) {
	s.mu.Lock()
	cm := s.messages[convID]
	found := false
	if cm != nil {
		for i := range cm.Messages {
			if cm.Messages[i].ID == msgID && idx >= 0 && idx < len(cm.Messages[i].Attachments) {
				cm.Messages[i].Attachments[idx].Path = path
				found = true
				break
			}
		}
	}
	s.mu.Unlock()
	if found {
		s.FlushMessages(convID)
	}
}

func (s *Store) FlushMessages(convID string) {
	s.mu.Lock()
	cm := s.messages[convID]
	if cm == nil {
		s.mu.Unlock()
		return
	}
	cm.UpdatedAt = time.Now().UnixMilli()
	snapshot := *cm
	snapshot.Messages = make([]Message, len(cm.Messages)) // never nil: an empty thread is [], not null
	copy(snapshot.Messages, cm.Messages)
	s.mu.Unlock()
	if err := WriteJSONAtomic(s.MessagesPath(convID), snapshot, 0o600); err != nil {
		Log.Err(err).Str("conv", convID).Msg("Failed to write messages file")
	}
}

// SafeName turns an ID into a file name. The panel does the same to find a
// thread's file, so the two must stay in step.
func SafeName(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func WriteJSONAtomic(path string, v any, mode os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
