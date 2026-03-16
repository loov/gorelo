package example

import "fmt"

// Channel type aliases
type Event string
type EventChan chan Event
type EventReceiver <-chan Event

// Producer sends events on a channel.
func Producer(ch chan<- Event, events ...Event) {
	for _, e := range events {
		ch <- e
	}
	close(ch)
}

// Consumer reads events from a channel.
func Consumer(ch <-chan Event) []Event {
	var result []Event
	for e := range ch {
		result = append(result, e)
	}
	return result
}

// Fanout distributes events to multiple channels.
func Fanout(in EventReceiver, out ...chan<- Event) {
	for e := range in {
		for _, ch := range out {
			ch <- e
		}
	}
}

// Merge combines multiple channels into one.
func Merge(channels ...<-chan Event) <-chan Event {
	out := make(chan Event)
	go func() {
		for _, ch := range channels {
			for e := range ch {
				out <- e
			}
		}
		close(out)
	}()
	return out
}

// Select-based processing.
func SelectEvents(a, b <-chan Event, done <-chan struct{}) Event {
	select {
	case e := <-a:
		return e
	case e := <-b:
		return e
	case <-done:
		return ""
	}
}

// Buffered channel constructor.
func NewEventChan(size int) EventChan {
	return make(EventChan, size)
}

// SendAll sends all events and returns the channel for chaining.
func SendAll(ch EventChan, events ...Event) EventChan {
	for _, e := range events {
		ch <- e
	}
	return ch
}

// FormatEvent uses Event in a type assertion context.
func FormatEvent(v any) string {
	if e, ok := v.(Event); ok {
		return fmt.Sprintf("event: %s", e)
	}
	return "unknown"
}
