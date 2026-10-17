// Package events provides bounded, nonblocking live event fanout. Durable state
// and logs remain the authority when clients reconnect after a delivery gap.
package events

import (
	"sync"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
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

// MaxSubscribers bounds total live buffers across all jobs. Each subscriber
// additionally has a bounded buffer and slow readers are disconnected.
const MaxSubscribers = 128

type Hub struct {
	mu     sync.Mutex
	size   int
	limit  int
	closed bool
	subs   map[*Subscription]struct{}
}

func NewHub(size int) *Hub { return NewHubWithLimit(size, MaxSubscribers) }

// NewHubWithLimit permits smaller bounded hubs for tests and embedded uses.
func NewHubWithLimit(size, limit int) *Hub {
	if size < 1 {
		size = 32
	}
	if limit < 1 {
		limit = MaxSubscribers
	}
	return &Hub{size: size, limit: limit, subs: make(map[*Subscription]struct{})}
}
func (h *Hub) Subscribe(jobID string) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, domain.E(domain.CodeUnavailable, "Live job connections are unavailable. Try again shortly.")
	}
	if len(h.subs) >= h.limit {
		return nil, domain.E(domain.CodeUnavailable, "Live job connection limit reached. Close another job view and try again.")
	}
	ch := make(chan Event, h.size)
	s := &Subscription{Events: ch, hub: h, ch: ch, jobID: jobID}
	h.subs[s] = struct{}{}
	return s, nil
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
