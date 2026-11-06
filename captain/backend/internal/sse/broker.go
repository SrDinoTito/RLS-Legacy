// Package sse implements the Server-Sent Events broker (REQ-018, DSG-004):
// backend-side fan-out of state transitions to dashboard clients. The
// broker is deliberately dumb — it holds only subscriber channels and never
// blocks its callers: Broadcast is fire-and-forget (slow or dead
// subscribers drop messages, the channel is buffered so a momentary lag does
// not lose short bursts).
package sse

import (
	"encoding/json"
	"fmt"
	"sync"
)

const (
	// subscriberBuffer is the per-subscriber queue depth. Events beyond it
	// are dropped on the floor (SSE clients resync by reloading agent data
	// on reconnect anyway).
	subscriberBuffer = 64
	// upstreamFormat is the wire format of one SSE frame (event typed per
	// the design: heartbeat, agent_offline, agent_online, alert_new,
	// alert_resolved; data is a single-line compact JSON payload).
	upstreamFormat = "event: %s\ndata: %s\n\n"
)

// Broker fans out server events to SSE subscribers. Safe for concurrent
// use: subscriber registration happens under a mutex and reads from the
// close-free channels are lock free.
type Broker struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// New creates an empty broker.
func New() *Broker {
	return &Broker{subs: make(map[chan []byte]struct{})}
}

// Subscribe registers a new subscriber. The returned cancel func unregisters
// it (idempotent); the channel is deliberately never closed so a slow
// consumer hitting cancel first cannot race a send into a closed channel.
func (b *Broker) Subscribe() (<-chan []byte, func()) {
	// Buffered: Broadcast never blocks even if a subscriber stops reading.
	ch := make(chan []byte, subscriberBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Broadcast formats event+payload once per event and pushes it to every
// current subscriber without ever blocking: a full or unread channel drops
// the copy. Zero subscribers is a no-op, so it is safe (and cheap) to call
// from every write-path transition.
func (b *Broker) Broadcast(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return // nothing sane to send; state is still committed in the DB
	}
	b.broadcastRaw([]byte(fmt.Sprintf(upstreamFormat, event, data)))
}

// broadcastRaw pushes an already-encoded frame to all subscribers.
func (b *Broker) broadcastRaw(frame []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- frame:
		default: // subscriber lagging or paused: drop, never block ingest
		}
	}
}
