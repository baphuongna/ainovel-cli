package host

// Engine tích hợp end-to-end (engine-rfc.md §7 nghiệm thu nguyên mẫu):
// Store thật + tool Worker thật + ChatModel kịch bản hóa, kiểm chứng
//  1. Chuỗi viết sách đầy đủ do Route dẫn dắt: viết chương 1 → viết chương 2 → hoàn sách → engine dừng tự nhiên
//  2. Đường Worker thất bại: thử lại một lần → Arbiter phán định worker_failure abort → tạm dừng + audit ghi đĩa
//  3. Đường bế tắc: cùng lệnh không tiến triển ×3 → Arbiter phán định deadlock → audit ghi đĩa → abort dừng máy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/internal/arbiter"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// scriptedChatModel là ChatModel tối tiểu sinh response theo callback.
type scriptedChatModel struct {
	fn func(msgs []agentcore.Message) agentcore.Message
}

func TestFailureFactsKeepPartialStateAndWarnings(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(3); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "premise.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	e := &engine{store: st}
	workerErr := fmt.Errorf("writer exhausted: %w", agentcore.ErrMaxTurns)
	facts := e.failureFacts("worker_failure", &flow.Instruction{Agent: "writer", Task: "续写"}, workerErr)
	if facts.ErrorKind != "max_turns" || facts.Phase != string(domain.PhaseInit) {
		t.Fatalf("Phải giữ loại lỗi và dữ kiện tiến độ đọc được: %+v", facts)
	}
	if len(facts.FactWarnings) == 0 {
		t.Fatalf("Dữ kiện nền không đọc được phảithành cảnh báo giao cho Arbiter: %+v", facts)
	}
}

func TestIsNonSemanticWorkerFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "context overflow", err: agentcore.ErrContextOverflow, want: true},
		{name: "partial stream", err: agentcore.ErrStreamPartial, want: true},
		{name: "stream idle", err: agentcore.ErrProviderStreamIdle, want: true},
		{name: "quota", err: agentcore.ErrProviderQuota, want: true},
		{name: "rate limit", err: agentcore.ErrProviderRateLimit, want: true},
		{name: "timeout", err: agentcore.ErrProviderTimeout, want: true},
		{name: "auth", err: agentcore.ErrProviderAuth, want: true},
		{name: "network wrapped", err: fmt.Errorf("provider: %w", agentcore.ErrProviderNetwork), want: true},
		{name: "raw EOF", err: fmt.Errorf("upstream closed: EOF"), want: true},
		{name: "overloaded", err: agentcore.ErrProviderOverloaded, want: true},
		{name: "flattened overloaded", err: fmt.Errorf("bad_response_status_code: Too many concurrent requests [provider, HTTP 500, openai]"), want: true},
		{name: "content filter", err: agentcore.ErrProviderContentFilter, want: false},
		{name: "max turns", err: agentcore.ErrMaxTurns, want: false},
		{name: "stop guard", err: agentcore.ErrStopGuard, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "tool validation", err: agentcore.ErrToolValidation, want: false},
		{name: "unknown", err: fmt.Errorf("unknown failure"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNonSemanticWorkerFailure(tt.err); got != tt.want {
				t.Fatalf("isNonSemanticWorkerFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestInterventionDispatchTaskPreservesOriginalAuthority(t *testing.T) {
	const task = "检查重复内容并安排必要返工"
	const original = "  后续不要重复解释能力来源；不要改动无关内容。\n"

	got := interventionDispatchTask(task, original)
	if !strings.Contains(got, task) {
		t.Fatalf("Mất nhiệm vụ phân công: %q", got)
	}
	if !strings.Contains(got, original) {
		t.Fatalf("Can thiệp gốc của người dùng không được giữ nguyên văn: %q", got)
	}
	if !strings.Contains(got, "nguồn ủy quyền duy nhất") {
		t.Fatalf("Thiếu phần giải thích ranh giới ủy quyền: %q", got)
	}
}

func (m *scriptedChatModel) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{Message: m.fn(msgs)}, nil
}

func (m *scriptedChatModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, _ := m.Generate(ctx, msgs, tools, opts...)
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message, StopReason: resp.Message.StopReason}
	close(ch)
	return ch, nil
}

func (m *scriptedChatModel) SupportsTools() bool { return true }

// editThenCancelModel tái hiện #84: mỗi lần Worker đều thành công sinh một edit
// checkpoint nội dung khác nhau, sau đó trong cùng run trả context canceled, luôn không commit.
type editThenCancelModel struct {
	edits atomic.Int32
}

func (m *editThenCancelModel) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == agentcore.RoleTool {
		return nil, context.Canceled
	}
	n := int(m.edits.Add(1))
	return &agentcore.LLMResponse{Message: testToolCallMsg("edit_chapter", map[string]any{
		"chapter":    1,
		"old_string": fmt.Sprintf("版本%d", n-1),
		"new_string": fmt.Sprintf("版本%d", n),
	})}, nil
}

func (m *editThenCancelModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message, StopReason: resp.Message.StopReason}
	close(ch)
	return ch, nil
}

func (m *editThenCancelModel) SupportsTools() bool { return true }

// providerNetworkModel mô phỏng Worker gặp lỗi mạng nhất thời trước mọi đầu ra của model.
// Khi MaxRetries=0 mỗi lần subagent.Run tương ứng một lần gọi, tiện kiểm chứng bộ đếm thử lại của Engine.
type providerNetworkModel struct {
	calls atomic.Int32
}

func (m *providerNetworkModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls.Add(1)
	return nil, fmt.Errorf("test provider EOF: %w", agentcore.ErrProviderNetwork)
}

func (m *providerNetworkModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.calls.Add(1)
	return nil, fmt.Errorf("test provider EOF: %w", agentcore.ErrProviderNetwork)
}

func (m *providerNetworkModel) SupportsTools() bool { return true }

func testToolCallMsg(name string, args any) agentcore.Message {
	data, _ := json.Marshal(args)
	return agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
			ID: "tc-" + name, Name: name, Args: data,
		})},
		StopReason: agentcore.StopReasonToolUse,
	}
}

func testTextMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: agentcore.StopReasonStop,
	}
}

var chapterRe = regexp.MustCompile(`(?:Viết lại|Trau chuốt|Viết) chương (\d+)`)

// scriptedWriterModel quyết định bước tiếp theo theo số kết quả tool đã có trong hội thoại,
// đi trọn chuỗi plan → draft → check → commit (tool thật, ghi đĩa thật).
func scriptedWriterModel() *scriptedChatModel {
	return &scriptedChatModel{fn: func(msgs []agentcore.Message) agentcore.Message {
		chapter := 0
		toolResults := 0
		for _, m := range msgs {
			if m.Role == agentcore.RoleUser {
				if match := chapterRe.FindStringSubmatch(m.TextContent()); match != nil {
					chapter, _ = strconv.Atoi(match[1])
				}
			}
			if m.Role == agentcore.RoleTool {
				toolResults++
			}
		}
		switch toolResults {
		case 0:
			return testToolCallMsg("plan_chapter", map[string]any{
				"chapter": chapter, "title": fmt.Sprintf("第%d章", chapter),
				"goal": "推进主线", "conflict": "主角遇阻", "hook": "悬念收尾",
			})
		case 1:
			return testToolCallMsg("draft_chapter", map[string]any{
				"chapter": chapter, "mode": "write",
				"content": strings.Repeat(fmt.Sprintf("第%d章的正文段落，主角在黑暗中摸索前行。", chapter), 20),
			})
		case 2:
			return testToolCallMsg("check_consistency", map[string]any{"chapter": chapter})
		default:
			return testToolCallMsg("commit_chapter", map[string]any{
				"chapter": chapter, "title": fmt.Sprintf("第%d章", chapter), "summary": fmt.Sprintf("第%d章摘要", chapter),
				"characters": []string{"主角"}, "key_events": []string{"推进"},
				"timeline_events": []any{}, "foreshadow_updates": []any{},
				"relationship_changes": []any{}, "state_changes": []any{}, "cast_intros": []any{},
				"hook_type": "crisis", "dominant_strand": "quest", "feedback": nil,
			})
		}
	}}
}

// newTestEngine lắp engine có store/observer thật; trả engine, bộ gom sự kiện và tín hiệu hoàn thành.
func newTestEngine(t *testing.T, st *storepkg.Store, workers *subagent.Runner, arbiterModel agentcore.ChatModel) (*engine, *[]Event, chan struct{}) {
	t.Helper()
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatalf("init run meta: %v", err)
	}
	var mu sync.Mutex
	events := &[]Event{}
	done := make(chan struct{}, 1)
	obs := newObserver(st, func(ev Event) {
		mu.Lock()
		*events = append(*events, ev)
		mu.Unlock()
	}, func(string) {}, func() {})
	e := &engine{
		store:           st,
		workers:         workers,
		arbiterModel:    arbiterModel,
		failurePrompt:   "sys",
		planStartPrompt: "sys",
		style:           "default",
		observer:        obs,
		refresh:         func() {},
		emitEvent: func(ev Event) {
			mu.Lock()
			*events = append(*events, ev)
			mu.Unlock()
		},
		notify: func(string, string, string, string) {},
		onDone: func() {
			select {
			case done <- struct{}{}:
			default:
			}
		},
	}
	e.gate = NewChapterAdvanceGate(st, func(string) { e.abort() }, func(string, string) {})
	return e, events, done
}

func waitEngineDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Engine không dừng trong thời hạn")
	}
}

func mustInterventionFacts(t *testing.T, st *storepkg.Store) arbiter.InterventionFacts {
	t.Helper()
	facts, err := arbiter.CollectInterventionFacts(st)
	if err != nil {
		t.Fatalf("CollectInterventionFacts: %v", err)
	}
	return facts
}

func TestEngine_ReviewPermitWritesExactlyOneNewChapter(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(3); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一", CoreEvent: "a"},
		{Chapter: 2, Title: "二", CoreEvent: "b"},
		{Chapter: 3, Title: "三", CoreEvent: "c"},
	}); err != nil {
		t.Fatal(err)
	}
	writer := subagent.Config{
		Name: "writer", Description: "test writer", Model: scriptedWriterModel(), SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st), tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st), tools.NewCommitChapterTool(st, tools.NewStyleStatsIndex(st)),
		},
		MaxTurns: 10, StopAfterTools: []string{"commit_chapter"},
	}
	e, _, done := newTestEngine(t, st, subagent.NewRunner(writer), nil)
	if err := st.RunMeta.SetAdvanceMode(domain.ChapterAdvanceReview); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.GrantAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	if len(progress.CompletedChapters) != 1 || progress.CompletedChapters[0] != 1 {
		t.Fatalf("Một giấy phép phải ổn định đúng một chương mới: %v", progress.CompletedChapters)
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvancePermitChapter != 0 {
		t.Fatalf("Sau nộp ổn định giấy phép phải được tiêu thụ: %+v", meta)
	}
}

func TestEngine_StalePairedDispatchDoesNotBypassHold(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(3); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	e, _, _ := newTestEngine(t, st, subagent.NewRunner(), nil)
	e.pending = []controlOp{{
		hold:     &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAtBoundary, Reason: "先停下"},
		dispatch: &arbiter.DispatchOp{Agent: "editor", Task: "过期任务"},
		facts:    arbiter.InterventionFacts{Phase: string(domain.PhaseOutline)},
	}}

	if e.applyPendingOps(context.Background()) {
		t.Fatal("Đơn phân công cặp có dữ kiện quá hạn chưa rơi vào next thì không được bỏ qua Gate")
	}
	if e.next != nil || e.deferGateForNext {
		t.Fatalf("Đơn phân công quá hạn không được để lại lệnh khả thi: next=%+v defer=%v", e.next, e.deferGateForNext)
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvanceHold != nil {
		t.Fatalf("Khi đơn phân công cặp quá hạn không được để lại hold mồ côi: %+v", meta.AdvanceHold)
	}
	if e.gate.HandleBoundary() {
		t.Fatal("Không có hold mồ côi thì Gate không được ngụy tạo tạm dừng")
	}
}

// TestEngine_WritesBookToCompletion chuỗi đầy đủ: sách hai chương không phân tầng viết từ writing tới complete.
func TestEngine_WritesBookToCompletion(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(2); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "第一章", CoreEvent: "开端"},
		{Chapter: 2, Title: "第二章", CoreEvent: "终局"},
	}); err != nil {
		t.Fatalf("outline: %v", err)
	}

	writer := subagent.Config{
		Name: "writer", Description: "test writer",
		Model:        scriptedWriterModel(),
		SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st),
			tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st),
			tools.NewCommitChapterTool(st, tools.NewStyleStatsIndex(st)),
		},
		MaxTurns:       10,
		StopAfterTools: []string{"commit_chapter"},
	}
	e, events, done := newTestEngine(t, st, subagent.NewRunner(writer), nil)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	if progress.Phase != domain.PhaseComplete {
		t.Fatalf("Viết đủ hai chương phải hoàn sách, got phase=%s completed=%v", progress.Phase, progress.CompletedChapters)
	}
	if len(progress.CompletedChapters) != 2 {
		t.Fatalf("Phải hoàn thành 2 chương, got %v", progress.CompletedChapters)
	}
	// Hình dạng sự kiện: mỗi chương một DISPATCH (engine phát động), dòng TOOL đến từ chuyển tiếp tiến độ
	var dispatches, toolRows int
	for _, ev := range *events {
		switch ev.Category {
		case "DISPATCH":
			dispatches++
		case "TOOL":
			toolRows++
		}
	}
	if dispatches < 2 {
		t.Fatalf("Phải có ít nhất 2 sự kiện DISPATCH, got %d", dispatches)
	}
	if toolRows == 0 {
		t.Fatal("Tiến độ tool của Worker không được chiếu qua chuyển tiếp (thiếu dòng TOOL)")
	}
}

// TestEngine_WorkerFailureConsultsArbiterAndAborts đường thất bại:
// writer quay trống bị StopGuard leo thang → thử lại một lần → Arbiter phán định abort → tạm dừng + audit.
func TestEngine_WorkerFailureConsultsArbiterAndAborts(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(2); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一", CoreEvent: "s"}}); err != nil {
		t.Fatalf("outline: %v", err)
	}

	var runs atomic.Int32
	// writer mỗi vòng chỉ trả chữ không ghi đĩa → guard.NewWriterStopGuard chặn liên tiếp rồi leo thang → Execute báo lỗi
	idle := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg("我写完了(其实什么都没做)")
	}}
	writer := subagent.Config{
		Name: "writer", Description: "idle writer",
		Model: idle, SystemPrompt: "test", MaxTurns: 20,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			runs.Add(1)
			return failNTimesGuard()
		},
	}
	// Arbiter phán định abort
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg(`{"action":"abort","dispatch":null,"reason":"writer 反复空转,建议人工检查模型配置"}`)
	}}
	e, _, done := newTestEngine(t, st, subagent.NewRunner(writer), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	if got := runs.Load(); got != 2 {
		t.Fatalf("Thất bại đầu phải thử lại một lần (tổng 2 lần spawn), got %d", got)
	}
	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var found bool
	for _, r := range recs {
		if r.Kind == "worker_failure" && r.Decider == "arbiter" {
			found = true
			if !strings.Contains(string(r.Decision), "abort") {
				t.Fatalf("Nội dung phán định phải chứa abort: %s", r.Decision)
			}
		}
	}
	if !found {
		t.Fatalf("Phán định worker_failure phải ghi đĩa: %+v", recs)
	}
}

// seedStuckRewrite dựng hiện trường "chương 2 đã hoàn thành và xếp vào hàng đợi viết lại".
func seedStuckRewrite(t *testing.T, st *storepkg.Store) {
	t.Helper()
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(5); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := st.Progress.SetPendingRewrites([]int{2}, "评审要求重写"); err != nil {
		t.Fatalf("pending: %v", err)
	}
	if err := st.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("flow: %v", err)
	}
}

// TestEngine_DeadlockAbortDropsStuckRewrite chốt chặt mặt deadlock của issue #110: khi ngắt mạch bế tắc
// chương viết lại kẹt chết phải xuất hàng. PendingRewrites là dữ kiện persist, chỉ tạm dừng mà không xuất hàng thì khởi động lại sẽ lập tức
// replay đúng lệnh chết đó, khóa chết cả cuốn sách vĩnh viễn.
func TestEngine_DeadlockAbortDropsStuckRewrite(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	seedStuckRewrite(t, st)
	e, events, _ := newTestEngine(t, st, subagent.NewRunner(), nil)

	inst := &flow.Instruction{Agent: "writer", Task: "重写第 2 章", Chapter: 2}
	e.lastKey, e.repeats = instructionKey(inst), deadlockAbortAt-1

	if stop := e.trackDeadlock(context.Background(), &inst); !stop {
		t.Fatal("Ngắt mạch bế tắc vẫn phải dừng máy chờ can thiệp thủ công")
	}
	p, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if len(p.PendingRewrites) != 0 {
		t.Fatalf("Khi ngắt mạch chương viết lại kẹt chết phải xuất hàng: %v", p.PendingRewrites)
	}
	if p.Flow != domain.FlowWriting {
		t.Fatalf("Sau khi hàng đợi xả hết flow phải về writing, thực tế %s", p.Flow)
	}
	var notified bool
	for _, ev := range *events {
		if strings.Contains(ev.Summary, "rời hàng đợi viết lại") {
			notified = true
		}
	}
	if !notified {
		t.Fatalf("Bỏ qua viết lại phải báo rõ cho người dùng: %+v", *events)
	}
}

// TestEngine_DropStuckRewriteOnlyTouchesQueuedChapter xuất hàng là thao tác phá hủy, diện vô tình sát thương phải chốt chặt:
// chỉ có "chương nằm trong hàng đợi viết lại" mới được đưa ra, các lệnh khác tuyệt đối không đụng hàng đợi.
func TestEngine_DropStuckRewriteOnlyTouchesQueuedChapter(t *testing.T) {
	cases := []struct {
		name string
		inst *flow.Instruction
	}{
		{"非 writer 指令", &flow.Instruction{Agent: "editor", Task: "弧级评审"}},
		{"不涉及章节的 writer 指令", &flow.Instruction{Agent: "writer", Task: "续写"}},
		{"不在队列里的续写章", &flow.Instruction{Agent: "writer", Task: "写第 3 章", Chapter: 3}},
		{"空指令", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := storepkg.NewStore(t.TempDir())
			seedStuckRewrite(t, st)
			e, _, _ := newTestEngine(t, st, subagent.NewRunner(), nil)
			if e.dropStuckRewrite(tc.inst) {
				t.Fatal("Không được xuất hàng")
			}
			p, err := st.Progress.Load()
			if err != nil {
				t.Fatalf("progress: %v", err)
			}
			if len(p.PendingRewrites) != 1 || p.PendingRewrites[0] != 2 {
				t.Fatalf("Hàng đợi viết lại không được bị sát thương nhầm: %v", p.PendingRewrites)
			}
		})
	}
}

// TestEngine_TransientProviderFailuresDoNotBecomeDeadlock hồi quy chuỗi lỗi chương 135:
// worker_failure=retry sau hai vòng lỗi mạng không được bị trackDeadlock ở vòng kế coi là
// "cùng tác vụ viết liên tiếp không tiến triển" rồi kích hoạt deadlock đổi lệnh.
func TestEngine_TransientProviderFailuresDoNotBecomeDeadlock(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "第一章", CoreEvent: "开端"}}); err != nil {
		t.Fatalf("outline: %v", err)
	}

	network := &providerNetworkModel{}
	writer := subagent.Config{
		Name: "writer", Description: "network failing writer",
		Model: network, SystemPrompt: "test", MaxTurns: 5, MaxRetries: 0,
	}
	var arbiterCalls atomic.Int32
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		if arbiterCalls.Add(1) == 1 {
			return testTextMsg(`{"action":"retry","dispatch":null,"reason":"瞬态网络故障，重试原任务"}`)
		}
		return testTextMsg(`{"action":"abort","dispatch":null,"reason":"网络持续不可用，暂停等待恢复"}`)
	}}
	e, events, done := newTestEngine(t, st, subagent.NewRunner(writer), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	if got := network.calls.Load(); got != 4 {
		t.Fatalf("Hai chu kỳ Engine thất bại mỗi chu kỳ chạy 2 lần Worker, got %d", got)
	}
	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var workerFailures, deadlocks int
	for _, rec := range recs {
		switch rec.Kind {
		case "worker_failure":
			workerFailures++
		case "deadlock":
			deadlocks++
		}
	}
	if workerFailures != 2 || deadlocks != 0 {
		t.Fatalf("Lỗi mạng chỉ được vào worker_failure, got worker_failure=%d deadlock=%d records=%+v", workerFailures, deadlocks, recs)
	}
	var failedDispatches, duplicateErrors int
	for _, ev := range *events {
		if ev.Category == "DISPATCH" && ev.Failed && ev.Kind == "network" && strings.Contains(ev.Detail, "test provider EOF") {
			failedDispatches++
		}
		if ev.Category == "ERROR" && strings.Contains(ev.Detail, "test provider EOF") {
			duplicateErrors++
		}
	}
	if failedDispatches != 4 || duplicateErrors != 0 {
		t.Fatalf("Mỗi lần Worker thất bại chỉ được cập nhật DISPATCH, got dispatch=%d duplicate_error=%d events=%+v", failedDispatches, duplicateErrors, *events)
	}
}

// failNTimesGuard StopGuard leo thang ngay lập tức (mô phỏng ngắt mạch quay trống).
func failNTimesGuard() agentcore.StopGuard {
	return func(context.Context, agentcore.StopInfo) agentcore.StopDecision {
		return agentcore.StopDecision{Allow: false, Escalate: true}
	}
}

// TestEngine_RetriesUnfinishedPlanStart đường tự sửa sau khi phán định khởi động thất bại: StartPrompt đã ghi đĩa,
// PlanStart缺 (lỗi model lúc khởi động) → lúc engine khởi động phán định bù tại chỗ → chốt PlanStartRecord → phân phát planner.
// Planner không ghi đĩa → đi đường bế tắc sẵn có để dừng máy, chứng minh sau phán định bù engine trở lại quỹ đạo bình thường.
func TestEngine_RetriesUnfinishedPlanStart(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(0); err != nil {
		t.Fatalf("progress: %v", err)
	}
	// Mô phỏng hiện trường StartPrepared thất bại: dữ kiện đầu vào còn, dữ kiện phán định thiếu.
	if err := st.RunMeta.SetStartPrompt("凡人修仙"); err != nil {
		t.Fatalf("start prompt: %v", err)
	}

	// Arbiter: lần gọi đầu là phán định bù (plan_start), sau đó là tham vấn bế tắc (abort kết thúc).
	var arbCalls atomic.Int32
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		if arbCalls.Add(1) == 1 {
			return testTextMsg(`{"planner":"architect_long","task":"围绕凡人修仙规划三卷框架","reason":"长篇修仙题材"}`)
		}
		return testTextMsg(`{"action":"abort","dispatch":null,"reason":"规划师空转,停机"}`)
	}}
	// Planner trả thành công nhưng không ghi đĩa gì → Route luôn trả cùng lệnh bù → bế tắc.
	architect := subagent.Config{
		Name: "architect_long", Description: "idle planner",
		Model: &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
			return testTextMsg("已规划(其实没有落盘)")
		}},
		SystemPrompt: "test", MaxTurns: 3,
	}
	e, events, done := newTestEngine(t, st, subagent.NewRunner(architect), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil || meta.PlanStart == nil {
		t.Fatalf("Sau phán định bù PlanStart phải được chốt, meta=%+v err=%v", meta, err)
	}
	if meta.PlanStart.Planner != "architect_long" || meta.PlanStart.RawPrompt != "凡人修仙" || meta.PlanStart.DecisionID == "" {
		t.Fatalf("Trường PlanStartRecord không đầy đủ: %+v", meta.PlanStart)
	}
	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var planStartRec bool
	for _, r := range recs {
		if r.Kind == "plan_start" && strings.Contains(string(r.Decision), "architect_long") {
			planStartRec = true
		}
	}
	if !planStartRec {
		t.Fatalf("Phán định bù phải để lại audit plan_start: %+v", recs)
	}
	var dispatched, healed bool
	for _, ev := range *events {
		if ev.Category == "DISPATCH" {
			dispatched = true
		}
		if strings.Contains(ev.Summary, "Phán định khởi động đã bổ sung") {
			healed = true
		}
	}
	if !dispatched || !healed {
		t.Fatalf("Sau phán định bù phải phân phát planner và hiển thị lại sự kiện bổ sung, dispatched=%v healed=%v", dispatched, healed)
	}
}

// TestEngine_PlanStartRetryFailurePauses phán định bù thất bại không cho phép dừng máy âm thầm:
// Arbiter liên tục không khả dụng → tạm dừng rõ ràng hiển thị lại + audit plan_start kèm error + không phân phát.
func TestEngine_PlanStartRetryFailurePauses(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(0); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.RunMeta.SetStartPrompt("凡人修仙"); err != nil {
		t.Fatalf("start prompt: %v", err)
	}

	var e *engine
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		e.abort() // mô phỏng host hủy cuộc gọi thất bại liên tục, đường thất bại kết thúc rõ ràng bởi context.
		return testTextMsg("这不是 JSON")
	}}
	e, events, done := newTestEngine(t, st, subagent.NewRunner(), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	for _, ev := range *events {
		if ev.Category == "DISPATCH" {
			t.Fatal("Phán định bù thất bại không được phân phát worker nào")
		}
	}
	var paused bool
	for _, ev := range *events {
		if strings.Contains(ev.Summary, "Phán định khởi động thất bại") {
			paused = true
		}
	}
	if !paused {
		t.Fatalf("Phán định bù thất bại phải hiển thị rõ lý do tạm dừng, events=%+v", *events)
	}
	recs, err := st.Decisions.Recent(5)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var errRec bool
	for _, r := range recs {
		if r.Kind == "plan_start" && r.Error != "" && len(r.Decision) == 0 {
			errRec = true
		}
	}
	if !errRec {
		t.Fatalf("Phán định thất bại phải ghi đĩa kèm error: %+v", recs)
	}
}

// TestEngine_DeadlockConsultsArbiter đường bế tắc: lệnh bù lập dàn ý xuất hiện liên tiếp
// → lần 3 tham vấn Arbiter → abort dừng máy + audit deadlock.
func TestEngine_DeadlockConsultsArbiter(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(3); err != nil {
		t.Fatalf("progress: %v", err)
	}
	// Giai đoạn lập dàn ý + tier đã biết + thiếu mục luôn còn → Route mỗi vòng ra cùng lệnh bù
	if err := st.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("tier: %v", err)
	}

	// architect không có guard, trả thành công nhưng không ghi đĩa gì → lệnh Route bất biến
	lazy := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg("知道了(什么也不做)")
	}}
	architect := subagent.Config{
		Name: "architect_long", Description: "lazy architect",
		Model: lazy, SystemPrompt: "test", MaxTurns: 5,
	}
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg(`{"action":"abort","dispatch":null,"reason":"规划师反复无产出"}`)
	}}
	e, _, done := newTestEngine(t, st, subagent.NewRunner(architect), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var found bool
	for _, r := range recs {
		if r.Kind == "deadlock" && r.Decider == "arbiter" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Phán định deadlock phải ghi đĩa: %+v", recs)
	}
}

// TestEngine_IntermediateCheckpointsDoNotMaskDeadlock chốt #84: Writer sửa đi sửa lại
// bản nháp sẽ sinh digest mới và edit checkpoint mới, nhưng chỉ cần Route vẫn là cùng một
// "đánh bóng chương 1", là nghĩa là hậu điều kiện cấp Engine (commit) chưa xong, phải tiếp tục tích lũy bế tắc.
func TestEngine_IntermediateCheckpointsDoNotMaskDeadlock(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "第一章", CoreEvent: "开端"}}); err != nil {
		t.Fatalf("outline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "版本0 正文初稿"); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := st.Progress.MarkChapterComplete(1, len([]rune("版本0 正文初稿")), "mystery", "quest"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := st.Progress.SetPendingRewrites([]int{1}, "测试打磨不提交"); err != nil {
		t.Fatalf("pending rewrite: %v", err)
	}
	if err := st.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("flow: %v", err)
	}

	writerModel := &editThenCancelModel{}
	writer := subagent.Config{
		Name: "writer", Description: "edit then cancel writer",
		Model: writerModel, SystemPrompt: "test",
		Tools:    []agentcore.Tool{tools.NewEditChapterTool(st)},
		MaxTurns: 5,
	}
	// Kể cả khi Arbiter luôn yêu cầu retry cho worker_failure / deadlock, lần thứ 5 hiện có
	// ngắt mạch cứng cũng phải chặn trước khi phân phát, không được reset bởi edit checkpoint.
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg(`{"action":"retry","dispatch":null,"reason":"继续重试"}`)
	}}
	e, _, done := newTestEngine(t, st, subagent.NewRunner(writer), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	if got := writerModel.edits.Load(); got != deadlockAbortAt-1 {
		t.Fatalf("deadlock phải ngắt mạch cứng trước lần phân phát thứ %d, thực tế edit %d lần", deadlockAbortAt, got)
	}
	var edits int
	for _, cp := range st.Checkpoints.All() {
		if cp.Scope.Matches(domain.ChapterScope(1)) && cp.Step == "edit" {
			edits++
		}
	}
	if edits != deadlockAbortAt-1 {
		t.Fatalf("Phải giữ %d edit checkpoint khác nhau, thực tế %d", deadlockAbortAt-1, edits)
	}
	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var hasWorkerFailure, hasDeadlockWithCause bool
	for _, rec := range recs {
		switch rec.Kind {
		case "worker_failure":
			hasWorkerFailure = true
		case "deadlock":
			var facts arbiter.FailureFacts
			if err := json.Unmarshal(rec.Facts, &facts); err != nil {
				t.Fatalf("decode deadlock facts: %v", err)
			}
			if facts.ErrorKind == "canceled" && strings.Contains(facts.Error, "context canceled") {
				hasDeadlockWithCause = true
			}
		}
	}
	if !hasWorkerFailure || !hasDeadlockWithCause {
		t.Fatalf("Phải ghi worker_failure trước, deadlock phải giữ lỗi cuối: %+v", recs)
	}
}

// TestEngine_PauseWithEditorDispatchWaitsForRewriteQueue kiểm chứng sửa lỗi (chặn xem xét 2):
// Phán định viết lại của Arbiter = điểm neo + giao editor xếp hàng. Điểm neo phải đợi editor dựng hàng đợi viết lại,
// writer viết lại xả hết rồi mới tiêu thụ — không được bị "hàng đợi đã xả" đọc nhầm tiêu thụ trước khi editor chạy.
func TestEngine_PauseWithEditorDispatchWaitsForRewriteQueue(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(3); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一", CoreEvent: "a"},
		{Chapter: 2, Title: "二", CoreEvent: "b"},
		{Chapter: 3, Title: "三", CoreEvent: "c"},
	}); err != nil {
		t.Fatalf("outline: %v", err)
	}
	// Chương 1 đã hoàn thành (sẽ bị viết lại); worker writer sẽ viết lại nó trước, rồi điểm neo tiêu thụ.
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatalf("start ch1: %v", err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1200, "crisis", "quest"); err != nil {
		t.Fatalf("complete ch1: %v", err)
	}

	// editor: một lần save_review (verdict=rewrite, affected=[1]) đưa chương 1 vào hàng đợi.
	editorModel := &scriptedChatModel{fn: func(msgs []agentcore.Message) agentcore.Message {
		toolResults := 0
		for _, m := range msgs {
			if m.Role == agentcore.RoleTool {
				toolResults++
			}
		}
		if toolResults == 0 {
			return testToolCallMsg("save_review", map[string]any{
				"chapter": 1, "scope": "chapter",
				"dimensions": []map[string]any{
					{"dimension": "consistency", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "character", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "pacing", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "continuity", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "foreshadow", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "hook", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "aesthetic", "score": 55, "comment": "语气不符(引用:原文第一段)"},
				},
				"issues": []map[string]any{{
					"type": "aesthetic", "severity": "error", "description": "语气", "evidence": "原文", "suggestion": "改冷",
					"chapters": []int{1}, "requires_change": true,
				}},
				"contract_status": nil, "contract_misses": []string{}, "contract_notes": nil,
				"verdict": "rewrite", "summary": "第1章语气需重写",
			})
		}
		return testTextMsg("done")
	}}
	editor := subagent.Config{
		Name: "editor", Description: "test editor", Model: editorModel,
		SystemPrompt: "test", MaxTurns: 6,
		Tools:          []agentcore.Tool{tools.NewSaveReviewTool(st)},
		StopAfterTools: []string{"save_review"},
	}
	writer := subagent.Config{
		Name: "writer", Description: "test writer", Model: scriptedWriterModel(),
		SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st),
			tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st),
			tools.NewCommitChapterTool(st, tools.NewStyleStatsIndex(st)),
		},
		MaxTurns: 10, StopAfterTools: []string{"commit_chapter"},
	}

	e, _, done := newTestEngine(t, st, subagent.NewRunner(editor, writer), nil)
	// Mô phỏng phán định viết lại của Arbiter: hold + dispatch editor (engine chưa chạy → áp dụng ngay).
	e.applyControlOp(context.Background(), controlOp{
		hold:     &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAfterRewritesDrained, Reason: "重写第1章语气,改完暂停验收"},
		dispatch: &arbiter.DispatchOp{Agent: "editor", Task: "复核第 1 章：语气改冷，用 issues[].chapters 与 requires_change 入队"},
		facts:    mustInterventionFacts(t, st),
	})
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	// Khẳng định cốt lõi ①: điểm neo không tiêu thụ trước khi editor xếp hàng — chương 1 thực sự trải qua viết lại
	// (commit viết lại sẽ drain nó khỏi hàng đợi).
	if len(progress.PendingRewrites) != 0 {
		t.Fatalf("Hàng đợi viết lại phải đã xả hết, got %v", progress.PendingRewrites)
	}
	if progress.ChapterWordCounts[1] == 1200 {
		t.Fatal("Chương 1 phải được viết lại thật (số chữ phải đổi)")
	}
	// Khẳng định cốt lõi ②: sau khi xả hết điểm neo tiêu thụ, engine tạm dừng — chương 2 không được viết tiếp.
	if len(progress.CompletedChapters) != 1 {
		t.Fatalf("Điểm neo phải tạm dừng trước khi viết tiếp chương 2, completed=%v", progress.CompletedChapters)
	}
	meta, _ := st.RunMeta.Load()
	if meta != nil && meta.AdvanceHold != nil {
		t.Fatalf("Tạm dừng một lần phải đã được tiêu thụ, got %+v", meta.AdvanceHold)
	}
}

// TestEngine_BoundaryHoldDoesNotDispatchAnotherWorker hồi quy:
// Khi can thiệp người dùng chỉ phán định ra boundary hold (không có đơn phân công), engine phải ngay tại biên hiện tại
// tiêu thụ hold và tạm dừng, không được viết thêm một chương nào.
func TestEngine_BoundaryHoldDoesNotDispatchAnotherWorker(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(3); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一", CoreEvent: "a"},
		{Chapter: 2, Title: "二", CoreEvent: "b"},
		{Chapter: 3, Title: "三", CoreEvent: "c"},
	}); err != nil {
		t.Fatalf("outline: %v", err)
	}

	writer := subagent.Config{
		Name: "writer", Description: "test writer", Model: scriptedWriterModel(),
		SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st),
			tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st),
			tools.NewCommitChapterTool(st, tools.NewStyleStatsIndex(st)),
		},
		MaxTurns: 10, StopAfterTools: []string{"commit_chapter"},
	}
	e, _, done := newTestEngine(t, st, subagent.NewRunner(writer), nil)
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	// Can thiệp hold-only tới trong lúc viết chương 1 (cùng thời điểm như Steer thật).
	e.enqueue(controlOp{
		hold:  &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAtBoundary, Reason: "先停一下我看看"},
		facts: mustInterventionFacts(t, st),
	})
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	// Can thiệp tới lúc chương 1 đang chạy → chương 1 viết xong; điểm neo tiêu thụ ngay tại biên → chương 2 không được mở viết.
	if n := len(progress.CompletedChapters); n > 1 {
		t.Fatalf("Sau boundary hold không được viết thêm chương nào, completed=%v", progress.CompletedChapters)
	}
	meta, _ := st.RunMeta.Load()
	if meta != nil && meta.AdvanceHold != nil {
		t.Fatalf("Tạm dừng một lần phải đã được tiêu thụ, got %+v", meta.AdvanceHold)
	}
}

func TestEngine_TargetChapterHoldStopsAtRequestedChapter(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(3); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一", CoreEvent: "a"},
		{Chapter: 2, Title: "二", CoreEvent: "b"},
		{Chapter: 3, Title: "三", CoreEvent: "c"},
	}); err != nil {
		t.Fatal(err)
	}

	writer := subagent.Config{
		Name: "writer", Description: "test writer", Model: scriptedWriterModel(), SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st), tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st), tools.NewCommitChapterTool(st, tools.NewStyleStatsIndex(st)),
		},
		MaxTurns: 10, StopAfterTools: []string{"commit_chapter"},
	}
	e, _, done := newTestEngine(t, st, subagent.NewRunner(writer), nil)
	if err := st.RunMeta.SetAdvanceHold(domain.AdvanceHold{
		After: domain.AdvanceHoldAtChapter, TargetChapter: 2, Reason: "写到第2章",
	}); err != nil {
		t.Fatal(err)
	}
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	if !slices.Equal(progress.CompletedChapters, []int{1, 2}) {
		t.Fatalf("应准确停在第2章, completed=%v", progress.CompletedChapters)
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvanceHold != nil {
		t.Fatalf("目标章节 hold 应已消费: %+v", meta.AdvanceHold)
	}
}

// TestEngine_ExitRaceRestoresPendingDispatch hồi quy (chặn xem xét 3):
// Khi can thiệp xếp hàng đua với engine thoát, đơn phân công phán định sót lại không được vứt âm thầm — PendingSteer phải được ghi lại,
// hành động dữ kiện kiểu pause phải được bù thực thi.
func TestEngine_ExitRaceRestoresPendingDispatch(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init(2); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}

	// worker treo cho tới khi ctx bị hủy: tạo cửa sổ "sau khi xếp hàng engine bị abort".
	blocked := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		time.Sleep(50 * time.Millisecond)
		return testTextMsg("...")
	}}
	writer := subagent.Config{Name: "writer", Description: "slow", Model: blocked, SystemPrompt: "t", MaxTurns: 100}
	// Cần outline để Route giao writer
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一", CoreEvent: "a"}, {Chapter: 2, Title: "二", CoreEvent: "b"}}); err != nil {
		t.Fatalf("outline: %v", err)
	}
	e, _, done := newTestEngine(t, st, subagent.NewRunner(writer), nil)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	// Worker đang chạy: xếp hàng pause+dispatch, ngay sau đó abort (hành động mãi mãi không chờ được biên kế).
	e.enqueue(controlOp{
		hold:     &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAfterRewritesDrained, Reason: "验收"},
		dispatch: &arbiter.DispatchOp{Agent: "writer", Task: "重写第 1 章"},
		text:     "重写第1章然后停下来",
		facts:    mustInterventionFacts(t, st),
	})
	e.abort()
	waitEngineDone(t, done)

	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta.PendingSteer != "重写第1章然后停下来" {
		t.Fatalf("Đơn phân công sót lại phải được ghi lại vào PendingSteer cho khôi phục replay, got %q", meta.PendingSteer)
	}
	if meta.AdvanceHold == nil {
		t.Fatal("Hành động dữ kiện hold phải được bù thực thi trong dọn dẹp lúc thoát")
	}
}
