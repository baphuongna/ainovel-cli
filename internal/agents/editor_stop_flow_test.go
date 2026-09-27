package agents

// Kiểm tra end-to-end hành vi kết hợp save_review hard-stop + StopGuard nhận biết nhiệm vụ
// (đấu nối thực trong build.go editor: StopAfterToolResult trúng save_review/save_*_summary,
// StopGuardFactory dùng guard thật, tool lưu checkpoint thật).
//
// Cảnh 1 (nhiệm vụ tóm tắt kiểm duyệt trước): editor được phái tạo tóm tắt cung, nhưng gọi save_review trước——
// hard-stop trigger nhưng guard bác, sau khi tiêm thúc editor đi đến save_arc_summary mới thực sự thoát.
// Đây là tiền đề an toàn để restore save_review hard-stop, ngăn chết loop tóm tắt cung không bao giờ lưu.
//
// Cảnh 2 (nhiệm vụ xem xét kết thúc một bước): editor được phái xem xét, save_review lưu xong là hard-stop cho qua,
// không chạy thêm vòng LLM nào.

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/internal/agents/guard"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// editorStopAfterToolResult giữ cùng tiêu chí với cấu hình editor trong build.go.
func editorStopAfterToolResult(toolName string, _ json.RawMessage) bool {
	return toolName == "save_review" || toolName == "save_arc_summary" || toolName == "save_volume_summary"
}

func checkpointTool(t *testing.T, st *store.Store, name, step string) agentcore.Tool {
	t.Helper()
	return agentcore.NewFuncTool(name, "fake "+name, map[string]any{"type": "object"},
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if _, err := st.Checkpoints.Append(domain.ArcScope(1, 1), step, "artifact", "digest"); err != nil {
				t.Fatalf("append checkpoint %s: %v", step, err)
			}
			return json.RawMessage(`"saved"`), nil
		})
}

func runEditorLike(t *testing.T, st *store.Store, task string, model agentcore.ChatModel, tools []agentcore.Tool) {
	t.Helper()
	cfg := subagent.Config{
		Name:                "editor",
		Description:         "test editor",
		Model:               model,
		SystemPrompt:        "test",
		Tools:               tools,
		MaxTurns:            10,
		StopAfterToolResult: editorStopAfterToolResult,
		StopGuardFactory: func(_, task string) agentcore.StopGuard {
			return guard.NewEditorStopGuard(st, task, nil)
		},
	}
	tool := subagent.NewRunner(cfg).AsTool()
	args, _ := json.Marshal(map[string]string{"agent": "editor", "task": task})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("subagent execute: %v", err)
	}
}

func TestEditorFlow_SummaryTaskSurvivesEarlyReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}

	var calls atomic.Int32
	model := &contractModel{fn: func(i int, _ []agentcore.Message) (*agentcore.LLMResponse, error) {
		switch i {
		case 0:
			// chạy lệch: nhiệm vụ tóm tắt mà kiểm duyệt trước.
			return &agentcore.LLMResponse{Message: assistantToolCall("save_review", `{}`)}, nil
		default:
			// guard bác hard-stop và tiêm thúc, vòng này mới tạo tóm tắt.
			calls.Add(1)
			return &agentcore.LLMResponse{Message: assistantToolCall("save_arc_summary", `{}`)}, nil
		}
	}}

	runEditorLike(t, st, "tạo tóm tắt cung 1 tập 1 (save_arc_summary)", model, []agentcore.Tool{
		checkpointTool(t, st, "save_review", "review"),
		checkpointTool(t, st, "save_arc_summary", "arc_summary"),
	})

	if calls.Load() == 0 {
		t.Fatal("save_review hard-stop bị guard bác, editor phải tiếp tục đến save_arc_summary——nếu run kết thúc sau kiểm duyệt, nghĩa là exit đã đóng băng đi qua guard, chết loop tóm tắt cung sẽ quay lại")
	}
	all := st.Checkpoints.All()
	var hasSummary bool
	for _, cp := range all {
		if cp.Step == "arc_summary" {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Fatal("tóm tắt cung phải cuối cùng được lưu")
	}
}

func TestEditorFlow_ReviewTaskStopsAtSaveReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}

	model := &contractModel{fn: func(i int, _ []agentcore.Message) (*agentcore.LLMResponse, error) {
		if i == 0 {
			return &agentcore.LLMResponse{Message: assistantToolCall("save_review", `{}`)}, nil
		}
		t.Fatal("nhiệm vụ xem xét save_review lưu xong phải hard-stop, model không được nhận vòng thêm")
		return nil, nil
	}}

	runEditorLike(t, st, "xem xét cấp cung tập 1 cung 1 (scope=arc)", model, []agentcore.Tool{
		checkpointTool(t, st, "save_review", "review"),
	})

	if got := model.calls(); got != 1 {
		t.Fatalf("nhiệm vụ xem xét phải kết thúc sau đúng một lần gọi model, got %d", got)
	}
}
