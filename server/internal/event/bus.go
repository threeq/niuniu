package event

import "sync"

type Bus struct {
	mu   sync.RWMutex
	subs map[chan OutputEvent]struct{}
}

func NewBus() *Bus {
	return &Bus{subs: make(map[chan OutputEvent]struct{})}
}

func (b *Bus) Subscribe() chan OutputEvent {
	ch := make(chan OutputEvent, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch chan OutputEvent) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		// Don't close here to avoid panic; let Close() handle all channels
	}
	b.mu.Unlock()
}

func (b *Bus) Publish(e OutputEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (b *Bus) Close() {
	b.mu.Lock()
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
	b.mu.Unlock()
}
