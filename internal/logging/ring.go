package logging

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Entry is a log message kept in memory.
type Entry struct {
	Time    time.Time
	Level   slog.Level
	Message string
	// Attrs are the attributes, with groups flattened ("group.key").
	Attrs []Attr
}

// Attr is one attribute of a message.
type Attr struct {
	Key, Value string
}

// Ring keeps the latest log messages in memory for the admin API: no need to open the files to see
// what just happened.
type Ring struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	full bool
}

// NewRing creates a buffer of size messages. Past that the oldest are overwritten.
func NewRing(size int) *Ring { return &Ring{buf: make([]Entry, max(1, size))} }

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Entries returns the messages at or above minLevel that contain text (in the message or an
// attribute, ignoring case; empty matches all), newest first, limit at most.
func (r *Ring) Entries(minLevel slog.Level, text string, limit int) []Entry {
	r.mu.Lock()
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	all := make([]Entry, 0, n)
	for i := range n {
		// Newest first.
		all = append(all, r.buf[(r.next-1-i+len(r.buf))%len(r.buf)])
	}
	r.mu.Unlock()
	text = strings.ToLower(text)
	out := make([]Entry, 0, min(limit, len(all)))
	for _, e := range all {
		if len(out) >= limit {
			break
		}
		if e.Level >= minLevel && (text == "" || matches(e, text)) {
			out = append(out, e)
		}
	}
	return out
}

func matches(e Entry, text string) bool {
	if strings.Contains(strings.ToLower(e.Message), text) {
		return true
	}
	return slices.ContainsFunc(e.Attrs, func(a Attr) bool {
		return strings.Contains(strings.ToLower(a.Key+"="+a.Value), text)
	})
}

// Handler returns a slog handler that stores messages at or above level in the ring.
func (r *Ring) Handler(level slog.Leveler) slog.Handler { return &ringHandler{ring: r, level: level} }

type ringHandler struct {
	ring   *Ring
	level  slog.Leveler
	attrs  []Attr
	prefix string
}

func (h *ringHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *ringHandler) Handle(_ context.Context, rec slog.Record) error {
	e := Entry{Time: rec.Time, Level: rec.Level, Message: rec.Message, Attrs: slices.Clone(h.attrs)}
	rec.Attrs(func(a slog.Attr) bool {
		e.Attrs = flatten(e.Attrs, h.prefix, a)
		return true
	})
	h.ring.add(e)
	return nil
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = slices.Clone(h.attrs)
	for _, a := range attrs {
		c.attrs = flatten(c.attrs, h.prefix, a)
	}
	return &c
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.prefix = h.prefix + name + "."
	return &c
}

// flatten appends an attribute with groups flattened. An empty attribute is skipped, as slog does.
func flatten(out []Attr, prefix string, a slog.Attr) []Attr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return out
	}
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range a.Value.Group() {
			out = flatten(out, p, g)
		}
		return out
	}
	return append(out, Attr{Key: prefix + a.Key, Value: a.Value.String()})
}
