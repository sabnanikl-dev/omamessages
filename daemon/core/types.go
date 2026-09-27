package core

// The JSON the panel reads. Providers convert their library's types into
// these; nothing service-specific belongs here except through Extra.

type Participant struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number string `json:"number"`
	IsMe   bool   `json:"isMe"`
}

type Conversation struct {
	ID           string         `json:"id"` // native; the merged state namespaces it
	Name         string         `json:"name"`
	LastMessage  string         `json:"lastMessage"`
	LastFromMe   bool           `json:"lastFromMe"`
	LastSender   string         `json:"lastSender"`
	LastTs       int64          `json:"lastTs"` // unix ms
	Unread       bool           `json:"unread"`
	IsGroup      bool           `json:"isGroup"`
	Archived     bool           `json:"archived"`
	Pinned       bool           `json:"pinned"`
	Participants []Participant  `json:"participants"`
	OutgoingID   string         `json:"outgoingId,omitempty"`
	Extra        map[string]any `json:"extra,omitempty"` // gmessages: {"type":"rcs"}
}

type Attachment struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // image|video|audio|file
	Size      int64  `json:"size"`
	Mime      string `json:"mime,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	ThumbPath string `json:"thumbPath,omitempty"`
	Path      string `json:"path,omitempty"`
}

type Message struct {
	ID          string       `json:"id"`
	Ts          int64        `json:"ts"` // unix ms
	FromMe      bool         `json:"fromMe"`
	Sender      string       `json:"sender"`
	SenderID    string       `json:"senderId"`
	Text        string       `json:"text"`
	Status      string       `json:"status"` // sending|sent|delivered|read|failed|received
	StatusText  string       `json:"statusText,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Reactions   []string     `json:"reactions,omitempty"`
	TmpID       string       `json:"-"`
}

type Typing struct {
	ConversationID string `json:"conversationId"`
	Until          int64  `json:"until"`
}

type ConversationMessages struct {
	ConversationID string    `json:"conversationId"` // native
	HasMore        bool      `json:"hasMore"`
	Loading        bool      `json:"loading"`
	Error          string    `json:"error,omitempty"`
	UpdatedAt      int64     `json:"updatedAt"`
	Messages       []Message `json:"messages"`
}
