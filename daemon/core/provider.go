// Package core holds what every messaging service shares: the Provider
// interface the hub drives, the JSON types the panel reads, and the
// per-provider store that mirrors them to disk.
package core

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
)

type ProviderID string

const (
	GMessages ProviderID = "gmessages"
	Telegram  ProviderID = "telegram"
	WhatsApp  ProviderID = "whatsapp"
)

// JoinID namespaces a provider's own conversation ID for the merged state,
// e.g. "telegram:-100123".
func JoinID(p ProviderID, native string) string {
	return string(p) + ":" + native
}

// SplitID undoes JoinID. Only the first colon separates, so native IDs that
// contain colons of their own survive the round trip.
func SplitID(id string) (ProviderID, string, error) {
	p, native, ok := strings.Cut(id, ":")
	if !ok || p == "" || native == "" {
		return "", "", errors.New("conversation id must look like <service>:<id>, got " + strconv.Quote(id))
	}
	return ProviderID(p), native, nil
}

// Caps tells the panel what a provider supports, so it can hide what isn't
// there instead of failing when it is used.
type Caps struct {
	Attachments   bool `json:"attachments"`
	StartByNumber bool `json:"startByNumber"`
	ContactSearch bool `json:"contactSearch"`
	Typing        bool `json:"typing"`
	Reactions     bool `json:"reactions"`
	LoadHistory   bool `json:"loadHistory"`
	PhoneTethered bool `json:"phoneTethered"`
	// FetchMedia: received attachments can be downloaded and opened.
	FetchMedia bool `json:"fetchMedia"`
}

// Status is one provider's connection state.
type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusConnecting   Status = "connecting"
	StatusPairing      Status = "pairing"
	StatusConnected    Status = "connected"
	StatusPhoneOffline Status = "phone_offline"
	StatusError        Status = "error"
)

type ConnectAlt struct {
	Method string `json:"method"`
	Label  string `json:"label"`
}

// ConnectStep is the one thing the panel should show while a provider is
// connecting. The provider moves it along; the panel only draws it.
type ConnectStep struct {
	Kind      string       `json:"kind"`            // waiting | emoji | qr | code | input
	Field     string       `json:"field,omitempty"` // input only: phone | code | password | cookies | api_credentials
	Prompt    string       `json:"prompt"`
	Hint      string       `json:"hint,omitempty"`
	Value     string       `json:"value,omitempty"` // emoji or pairing code to display
	Secret    bool         `json:"secret,omitempty"`
	QRPath    string       `json:"qrPath,omitempty"`
	QRExpires int64        `json:"qrExpires,omitempty"`
	Alts      []ConnectAlt `json:"alternatives,omitempty"`
}

type Notification struct {
	Provider ProviderID
	ConvID   string // native
	Title    string
	Body     string
}

// Env is everything the hub hands a provider when it creates one.
type Env struct {
	Store    *Store
	Dir      string // ~/.local/state/omarchy/omamessages/<id>
	CacheDir string // ~/.cache/omarchy/omamessages/<id>
	Notify   func(Notification)
	Log      zerolog.Logger
	// Guard runs fn and turns a panic in it into this provider's "error"
	// status instead of a crashed daemon. Set by the hub; use Protect/Spawn.
	Guard func(fn func())
}

// Protect runs fn now, under the hub's panic isolation when there is a hub
// (tests that build an Env by hand just run fn).
func (e Env) Protect(fn func()) {
	if e.Guard != nil {
		e.Guard(fn)
		return
	}
	fn()
}

// Spawn is `go fn()` with the same panic isolation as Protect.
func (e Env) Spawn(fn func()) {
	go e.Protect(fn)
}

// Provider is one messaging service. Every conversation ID it takes or
// returns is its own native ID; the hub adds and strips the namespace.
type Provider interface {
	ID() ProviderID
	Name() string
	Caps() Caps
	Start(ctx context.Context) error // resume the saved session if there is one; nil and "disconnected" if not
	Stop()
	Connect(ctx context.Context, method string) error // "" = default method
	ConnectInput(ctx context.Context, value string) error
	CancelConnect()
	Disconnect(ctx context.Context) error // logs out and removes session + cache
	Refresh(ctx context.Context) error
	Open(ctx context.Context, convID string, markRead bool) error
	More(ctx context.Context, convID string) error
	Send(ctx context.Context, convID, text string, files []string) error // files != nil && !Caps().Attachments → ErrUnsupported
	FetchMedia(ctx context.Context, convID, msgID string, idx int) (path string, err error)
	StartChat(ctx context.Context, to, text string) (convID string, err error)
	// Contacts searches for people to start a chat with. Each result's ID
	// must be something StartChat accepts as `to`: the compose screen passes
	// it straight back.
	Contacts(ctx context.Context, query string) ([]Participant, error)
}

// Typer is implemented by providers that can show the other side that the
// user is typing. It's optional: the hub skips providers without it.
type Typer interface {
	SetTyping(ctx context.Context, convID string, typing bool) error
}

type Factory func(Env) Provider

var ErrUnsupported = errors.New("not supported by this service")
