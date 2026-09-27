package host

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/voocel/agentcore"
)

// TestObserverConcurrentProgress kiểm chứng trạng thái tool/stream của observer dưới hai
// luồng progress đồng thời (Worker Engine + can thiệp Arbiter phía Host) không có data race.
// Phải chạy `go test -race` mới bắt được race; chế độ thường chỉ kiểm chứng không deadlock, không panic.
func TestObserverConcurrentProgress(t *testing.T) {
	var mu sync.Mutex // bảo vệ slice events (callback emit đến từ nhiều goroutine)
	var events []Event
	o := testObserver(nil)
	o.emitEv = func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	// Luồng A: engine Worker — dispatch + tool start/delta/end + thinking + retry.
	worker := func(wg *sync.WaitGroup) {
		defer wg.Done()
		o.dispatchStart("writer", "写第 3 章", "plan")
		for i := 0; i < 200; i++ {
			o.workerProgress(agentcore.ProgressPayload{
				Kind:  agentcore.ProgressToolStart,
				Agent: "writer",
				Tool:  "draft_chapter",
				Args:  json.RawMessage(`{"chapter":3}`),
			})
			o.workerProgress(agentcore.ProgressPayload{
				Kind:      agentcore.ProgressToolDelta,
				Agent:     "writer",
				Tool:      "draft_chapter",
				Delta:     `{"chapter":3,"content":"段落`,
				DeltaKind: agentcore.DeltaToolCall,
			})
			o.workerProgress(agentcore.ProgressPayload{
				Kind:     agentcore.ProgressThinking,
				Agent:    "writer",
				Thinking: "思考片段",
			})
			o.workerProgress(agentcore.ProgressPayload{
				Kind:       agentcore.ProgressRetry,
				Agent:      "writer",
				Attempt:    2,
				MaxRetries: 7,
				Message:    "stream read error [network, openai]",
			})
			o.workerProgress(agentcore.ProgressPayload{
				Kind:  agentcore.ProgressToolEnd,
				Agent: "writer",
				Tool:  "draft_chapter",
			})
		}
		o.dispatchFinish("writer", nil)
	}

	// Luồng B: can thiệp Arbiter phía Host — luồng progress độc lập trên cùng observer.
	arbiter := func(wg *sync.WaitGroup) {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			o.workerProgress(agentcore.ProgressPayload{
				Kind:  agentcore.ProgressToolStart,
				Agent: "arbiter",
				Tool:  "save_review",
				Args:  json.RawMessage(`{"chapter":3,"scope":"chapter"}`),
			})
			o.workerProgress(agentcore.ProgressPayload{
				Kind:      agentcore.ProgressToolDelta,
				Agent:     "arbiter",
				Tool:      "save_review",
				Delta:     `{"chapter":3,"verdict":"pass"}`,
				DeltaKind: agentcore.DeltaToolCall,
			})
			o.workerProgress(agentcore.ProgressPayload{
				Kind:  agentcore.ProgressToolEnd,
				Agent: "arbiter",
				Tool:  "save_review",
			})
			o.workerProgress(agentcore.ProgressPayload{
				Kind:       agentcore.ProgressRetry,
				Agent:      "arbiter",
				Attempt:    1,
				MaxRetries: 5,
				Message:    "rate limited",
			})
		}
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go worker(&wg)
	go arbiter(&wg)
	// Luồng C: đọc snapshot agent đồng thời với progress (đường sidebar TUI).
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = o.agentSnapshots()
		}
	}()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("no events emitted; concurrent progress streams lost")
	}
}
