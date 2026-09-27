package host

import (
	"sync"
	"testing"
)

// Emit sau khi đóng phải bị từ chối rõ ràng, không được dựa recover nuốt race.
func TestEmitAfterCloseDoesNotPanic(t *testing.T) {
	h := &Host{
		events:   make(chan Event, 1),
		streamCh: make(chan string, 1),
		done:     make(chan struct{}, 1),
	}
	h.closeOutputChannels()

	h.emitEvent(Event{Summary: "after close"})
	h.emitDelta("after close")
}

func TestConcurrentEmitAndCloseDoesNotRaceChannelLifecycle(t *testing.T) {
	h := &Host{
		events:   make(chan Event, 1),
		streamCh: make(chan string, 1),
		done:     make(chan struct{}, 1),
	}

	var emitters sync.WaitGroup
	for range 8 {
		emitters.Add(1)
		go func() {
			defer emitters.Done()
			for range 100 {
				h.emitEvent(Event{})
				h.emitDelta("delta")
			}
		}()
	}
	h.closeOutputChannels()
	emitters.Wait()

	if !h.outputClosed {
		t.Fatal("closeOutputChannels phải đánh dấu đầu ra đã đóng một cách nguyên tử")
	}
}
