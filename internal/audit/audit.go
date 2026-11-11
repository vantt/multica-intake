// Package audit writes one JSON line per tool call to stderr (the container
// log). It records who did what and how it ended — never request text, reply
// text or any token.
package audit

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Entry is one audit line.
type Entry struct {
	Time      string `json:"ts"`
	Event     string `json:"event"` // "tool"
	Tool      string `json:"tool"`
	Account   string `json:"account,omitempty"` // caller's Multica user id
	Name      string `json:"account_name,omitempty"`
	Workspace string `json:"workspace,omitempty"` // slug
	Issue     string `json:"issue,omitempty"`     // identifier
	OK        bool   `json:"ok"`
	Code      string `json:"code,omitempty"` // error code when !OK
	Millis    int64  `json:"ms"`
}

// Logger serialises entries to w.
type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

// New returns a logger writing to w; nil w disables logging.
func New(w io.Writer) *Logger { return &Logger{w: w} }

// Log writes e, stamping the time.
func (l *Logger) Log(e Entry, started time.Time) {
	if l == nil || l.w == nil {
		return
	}
	e.Event = "tool"
	e.Time = time.Now().UTC().Format(time.RFC3339)
	e.Millis = time.Since(started).Milliseconds()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}
