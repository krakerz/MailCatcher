// Package events fans out "new message" notifications to SSE clients and long-polls.
package events

import "sync"

type Event struct {
	MessageID int64 `json:"id"`
	InboxID   int64 `json:"inbox"`
}

type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

// Subscribe returns a buffered channel and a cancel func that must be called.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish never blocks: a subscriber with a full buffer misses the event, which is
// fine because every consumer re-queries the store rather than trusting the event.
func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
