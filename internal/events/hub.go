// Package events provides bounded, nonblocking live event fanout. Durable state
// and logs remain the authority when clients reconnect after a delivery gap.
package events

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"sync"
)

// Event and Log share the official domain contract used by feature providers.
type Event = domain.Event
type Log = domain.EventLog
type Subscription struct {
	Events <-chan Event
	hub    *Hub
	ch     chan Event
	jobID  string
}

func (s *Subscription) Close() { s.hub.remove(s) }

type Hub struct {
	mu     sync.Mutex
	size   int
	closed bool
	subs   map[*Subscription]struct{}
}

func NewHub(size int) *Hub {
	if size < 1 {
		size = 32
	}
	return &Hub{size: size, subs: make(map[*Subscription]struct{})}
}
func (h *Hub) Subscribe(jobID string) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Event, h.size)
	s := &Subscription{Events: ch, hub: h, ch: ch, jobID: jobID}
	if h.closed {
		close(ch)
	} else {
		h.subs[s] = struct{}{}
	}
	return s
}
func (h *Hub) remove(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.ch)
	}
}
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.jobID != "" && s.jobID != e.JobID {
			continue
		}
		select {
		case s.ch <- e:
		default:
			delete(h.subs, s)
			close(s.ch)
		}
	}
}
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		delete(h.subs, s)
		close(s.ch)
	}
}
