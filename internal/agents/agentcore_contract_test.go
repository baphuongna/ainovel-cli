package agents

// agentcore contract test: đóng các hành vi framework mà project này phụ thuộc thành assertion có thể thực thi.
// Mỗi test đánh dấu bên phụ thuộc; phải xanh hết trước khi bump agentcore — comment sẽ lỗi thời, test thì không.
// Toàn bộ chạy qua subagent.Runner.Run — đây là kênh dispatch thực của Engine.
//
// Các contract đã đóng cứng:
//  1. StopAfterTools/StopAfterToolResult exit đã đóng băng sẽ qua StopGuard (StopTriggerAfterTool),
//     guard bác (InjectMessage) có thể kéo run quay lại tiếp tục —— guard/subagent_guards.go nhận biết nhiệm vụ
//     EditorStopGuard dựa vào hành vi này để bắt exit sớm "được phái tạo tóm tắt nhưng chỉ làm kiểm duyệt".
//  2. StopReasonError / StopReasonAborted trực tiếp kết thúc run, không chạm StopGuard ——
//     hardStopReasons trong guard/subagent_guards.go vì vậy chỉ cần liệt kê safety/content_filter.
//  3. provider từ chối (safety etc. non-error stop) sẽ qua đường end_turn chạm StopGuard,
//     và info.Message.StopReason giữ nguyên giá trị gốc —— nâng cấp ngay của hardStopReasons dựa vào đường này.
//  4. StopGuard trả InjectMessage thì model nhận vòng mới; trả Escalate thì kết thúc ngay,
//     và chuỗi lỗi có thể khớp errors.Is(err, agentcore.ErrStopGuard) ——
//     "vật lý không thể dừng" và nâng cấp vượt giới hạn trong guard/stop_guard.go dựa vào ngữ nghĩa này.
//  5. Lỗi của Runner.Run giữ chuỗi có kiểu: agent chưa đăng ký khớp subagent.ErrUnknownAgent ——
//     isDeterministicWorkerError trong host/engine.go dựa vào phân loại này chứ không phải matching lỗi.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
)

// contractModel là mock model trả phản hồi preset theo thứ tự gọi.
type contractModel struct {
	fn  func(i int, msgs []agentcore.Message) (*agentcore.LLMResponse, error)
	idx int64
}

func (m *contractModel) take(msgs []agentcore.Message) (*agentcore.LLMResponse, error) {
	i := int(atomic.AddInt64(&m.idx, 1) - 1)
	return m.fn(i, msgs)
}

func (m *contractModel) calls() int { return int(atomic.LoadInt64(&m.idx)) }

func (m *contractModel) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.take(msgs)
}

func (m *contractModel) GenerateStream(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.take(msgs)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message, StopReason: resp.Message.StopReason}
	close(ch)
	return ch, nil
}

func (m *contractModel) SupportsTools() bool { return true }

func assistantText(text string, stop agentcore.StopReason) agentcore.Message {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: stop,
	}
}

func assistantToolCall(name string, args string) agentcore.Message {
	return agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
			ID: "tc-" + name, Name: name, Args: json.RawMessage(args),
		})},
		StopReason: agentcore.StopReasonToolUse,
	}
}

func okTool(name string) agentcore.Tool {
	return agentcore.NewFuncTool(name, "contract test tool", map[string]any{"type": "object"},
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"ok"`), nil
		})
}

// runSubagent chạy một dispatch đơn với config cho trước qua Runner.Run (kênh dispatch của Engine).
// Trả về lỗi thực thi —— StopGuard nâng cấp kết thúc sẽ nổi dưới dạng error (đây cũng là contract),
// các test case mong kết thúc bình thường tự assert nil.
func runSubagent(t *testing.T, cfg subagent.Config) error {
	t.Helper()
	_, err := subagent.NewRunner(cfg).Run(context.Background(), cfg.Name, "contract")
	return err
}

// Contract 1: exit tool đã đóng băng qua StopGuard; guard bác (InjectMessage) thì run tiếp tục.
// Bên phụ thuộc: EditorStopGuard —— khi stop tool đã đóng băng như save_review trúng, guard nhận biết nhiệm vụ
// phải có cơ hội kéo exit sớm "sản phẩm chưa lưu" trở về.
func TestContract_TerminalToolExitConsultsStopGuard(t *testing.T) {
	var guardCalls atomic.Int32
	var trigger atomic.Value

	model := &contractModel{fn: func(i int, _ []agentcore.Message) (*agentcore.LLMResponse, error) {
		switch i {
		case 0:
			return &agentcore.LLMResponse{Message: assistantToolCall("finish", `{}`)}, nil
		default:
			// guard bác exit đã đóng băng thì model phải nhận vòng mới; vòng này kết thúc bình thường.
			return &agentcore.LLMResponse{Message: assistantText("done", agentcore.StopReasonStop)}, nil
		}
	}}

	if err := runSubagent(t, subagent.Config{
		Name:           "editorish",
		Description:    "contract",
		Model:          model,
		SystemPrompt:   "test",
		Tools:          []agentcore.Tool{okTool("finish")},
		MaxTurns:       5,
		StopAfterTools: []string{"finish"},
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return func(_ context.Context, info agentcore.StopInfo) agentcore.StopDecision {
				n := guardCalls.Add(1)
				if n == 1 {
					trigger.Store(info.Trigger)
					return agentcore.StopDecision{Allow: false, InjectMessage: "chưa lưu, tiếp tục"}
				}
				return agentcore.StopDecision{Allow: true}
			}
		},
	}); err != nil {
		t.Fatalf("subagent execute: %v", err)
	}

	if guardCalls.Load() < 2 {
		t.Fatalf("exit tool đã đóng băng phải chạm StopGuard và sau khi bác phải tiếp tục (mong ≥2 lần tư vấn), got %d", guardCalls.Load())
	}
	if got := trigger.Load(); got != agentcore.StopTriggerAfterTool {
		t.Fatalf("Trigger của exit đã đóng băng phải là StopTriggerAfterTool, got %v", got)
	}
	if model.calls() < 2 {
		t.Fatalf("sau guard bác model phải nhận vòng mới, got %d calls", model.calls())
	}
}

// Contract 2: StopReasonError / StopReasonAborted kết thúc trực tiếp, không chạm StopGuard.
// Bên phụ thuộc: comment hardStopReasons — chỉ cần xử lý ngữ nghĩa từ chối thực sự đi qua guard.
func TestContract_ErrorAndAbortedStopSkipStopGuard(t *testing.T) {
	for _, stop := range []agentcore.StopReason{agentcore.StopReasonError, agentcore.StopReasonAborted} {
		t.Run(string(stop), func(t *testing.T) {
			var guardCalls atomic.Int32
			model := &contractModel{fn: func(int, []agentcore.Message) (*agentcore.LLMResponse, error) {
				return &agentcore.LLMResponse{Message: assistantText("dead", stop)}, nil
			}}
			_ = runSubagent(t, subagent.Config{
				Name: "dying", Description: "contract", Model: model,
				SystemPrompt: "test", MaxTurns: 5,
				StopGuardFactory: func(_, _ string) agentcore.StopGuard {
					return func(context.Context, agentcore.StopInfo) agentcore.StopDecision {
						guardCalls.Add(1)
						return agentcore.StopDecision{Allow: true}
					}
				},
			}) // ngữ nghĩa error của error/aborted do subagent layer định nghĩa, ở đây chỉ quan tâm guard có được chạm không
			if guardCalls.Load() != 0 {
				t.Fatalf("%s kết thúc không nên chạm StopGuard, got %d lần tư vấn", stop, guardCalls.Load())
			}
		})
	}
}

// Contract 3: provider từ chối (safety etc.) đi đường end_turn chạm StopGuard,
// và info.Message.StopReason giữ nguyên giá trị. Bên phụ thuộc: nâng cấp ngay của hardStopReasons.
func TestContract_SafetyStopReachesStopGuardWithReason(t *testing.T) {
	var seen atomic.Value
	model := &contractModel{fn: func(int, []agentcore.Message) (*agentcore.LLMResponse, error) {
		return &agentcore.LLMResponse{Message: assistantText("refused", agentcore.StopReason("safety"))}, nil
	}}
	err := runSubagent(t, subagent.Config{
		Name: "refused", Description: "contract", Model: model,
		SystemPrompt: "test", MaxTurns: 5,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return func(_ context.Context, info agentcore.StopInfo) agentcore.StopDecision {
				seen.Store(info.Message.StopReason)
				return agentcore.StopDecision{Allow: false, Escalate: true}
			}
		},
	})
	if got := seen.Load(); got != agentcore.StopReason("safety") {
		t.Fatalf("StopGuard phải thấy stop reason safety gốc, got %v", got)
	}
	if !errors.Is(err, agentcore.ErrStopGuard) {
		t.Fatalf("Escalate phải nổi bằng lỗi có thể errors.Is(agentcore.ErrStopGuard), got %v", err)
	}
}

// Contract 4: lúc end_turn, InjectMessage khiến model nhận vòng mới và nội dung tiêm ở đó;
// Escalate kết thúc ngay, model không còn được gọi. Bên phụ thuộc: Worker StopGuard
// "vật lý không thể dừng + nâng cấp vượt giới hạn liên tiếp".
func TestContract_StopGuardInjectContinuesEscalateTerminates(t *testing.T) {
	var sawInject atomic.Bool
	model := &contractModel{fn: func(i int, msgs []agentcore.Message) (*agentcore.LLMResponse, error) {
		if i > 0 {
			for _, m := range msgs {
				if strings.Contains(m.TextContent(), "Cấm kết thúc-hợp đồng") {
					sawInject.Store(true)
				}
			}
		}
		return &agentcore.LLMResponse{Message: assistantText("try stop", agentcore.StopReasonStop)}, nil
	}}

	var guardCalls atomic.Int32
	err := runSubagent(t, subagent.Config{
		Name: "stubborn", Description: "contract", Model: model,
		SystemPrompt: "test", MaxTurns: 10,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return func(context.Context, agentcore.StopInfo) agentcore.StopDecision {
				switch guardCalls.Add(1) {
				case 1:
					return agentcore.StopDecision{Allow: false, InjectMessage: "Cấm kết thúc-hợp đồng"}
				default:
					return agentcore.StopDecision{Allow: false, Escalate: true}
				}
			}
		},
	})
	if !errors.Is(err, agentcore.ErrStopGuard) {
		t.Fatalf("Escalate phải nổi bằng lỗi có thể errors.Is(agentcore.ErrStopGuard), got %v", err)
	}

	if !sawInject.Load() {
		t.Fatal("sau InjectMessage vòng tiếp theo của model phải chứa message tiêm")
	}
	if guardCalls.Load() != 2 {
		t.Fatalf("mong guard được tư vấn đúng 2 lần (1 tiêm + 1 nâng cấp), got %d", guardCalls.Load())
	}
	if model.calls() != 2 {
		t.Fatalf("sau Escalate model không nên được gọi nữa, mong đúng 2 lần, got %d", model.calls())
	}
}

// Contract 5: lỗi của Runner.Run giữ chuỗi có kiểu — agent chưa đăng ký nổi dưới subagent.ErrUnknownAgent.
// Bên phụ thuộc: isDeterministicWorkerError trong host/engine.go (phân loại "retry chắc chắn cùng lỗi→
// tạm dừng ngay" dựa vào errors.Is, không phải matching lỗi).
func TestContract_RunUnknownAgentIsTyped(t *testing.T) {
	runner := subagent.NewRunner(subagent.Config{
		Name: "writer", Description: "contract",
		Model: &contractModel{fn: func(int, []agentcore.Message) (*agentcore.LLMResponse, error) {
			return &agentcore.LLMResponse{Message: assistantText("ok", agentcore.StopReasonStop)}, nil
		}},
		SystemPrompt: "test", MaxTurns: 3,
	})
	_, err := runner.Run(context.Background(), "ghost", "contract")
	if !errors.Is(err, subagent.ErrUnknownAgent) {
		t.Fatalf("agent chưa đăng ký phải khớp subagent.ErrUnknownAgent, got %v", err)
	}
}
