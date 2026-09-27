package host

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// errorKind classifies a runtime error into a stable, short label for log
// filtering and alert routing. Returns "" when no special tag applies.
//
// err is the live error chain (may be nil after JSON serialization); msg is
// the rendered string fallback used when the chain has been flattened
// (e.g. inside sub-agent JSON results).
func errorKind(err error, msg string) string {
	if kind := agentcore.ErrorKind(err); kind != "" && kind != "unknown" {
		return kind
	}
	if msg == "" {
		return ""
	}
	if kind := agentcore.ErrorKind(errors.New(msg)); kind != "unknown" {
		return kind
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "tool argument validation failed"):
		return "tool_validation"
	case strings.Contains(lower, "too many concurrent requests"):
		return "overloaded"
	// providerError sẽ gắn loại có cấu trúc của litellm vào cuối văn bản.
	// HTTP/2 INTERNAL_ERROR vốn không có từ khóa phân loại được, giữ marker network tường minh này là đủ.
	case strings.Contains(lower, "[network,"):
		return "network"
	}
	return ""
}

// Bộ đếm ID sự kiện tăng đơn điệu; kết hợp timestamp sinh ID ổn định.
var eventIDCounter uint64

func nextEventID() string {
	return fmt.Sprintf("e%d", atomic.AddUint64(&eventIDCounter, 1))
}

// activeCall ghi lại ID, thời điểm bắt đầu và summary của một lần gọi đang chạy (TOOL / DISPATCH).
// summary được điền lại vào Event hoàn thành, đảm bảo replay (runtime queue) phục hồi được nội dung dòng.
type activeCall struct {
	id      string
	start   time.Time
	summary string
	depth   int
}

// observer chiếu việc phân phát của Engine và tiến độ Worker ra kênh xuất của Host.
// Nó là quan sát viên thuần, không tham gia bất kỳ quyết định điều khiển nào.
type observer struct {
	emitEv  func(Event)
	emitD   func(string)
	emitC   func()
	store   *storepkg.Store // dùng cho persist runtime queue (ReplayQueue tiêu thụ)
	agents  map[string]*agentState
	agentMu sync.Mutex

	// toolMu bảo vệ toàn bộ trạng thái dẫn xuất tool/stream bên dưới (lastThinkingByAgent,
	// dispatchStarts、toolStarts、streamExtractors、streamArg*、retryEvents、
	// các biến vô hướng stream*). Các trạng thái này được hai goroutine dẫn động đồng thời: callback
	// progress của Worker engine (engine.go dispatch/workerProgress) và luồng can thiệp Arbiter
	// phía Host (doIntervention tái dùng cùng observer.workerProgress), nên bắt buộc khóa tường
	// minh. Trong khóa được phép gọi emitEv/emitD/emitC (đều là thao tác kênh non-blocking)
	// và updateAgent (agentMu); thứ tự khóa luôn là toolMu → agentMu, cấm ngược.
	toolMu sync.Mutex

	// aborting do Host đặt tại lối Abort()/Close() và xóa tại Start/Resume/Continue.
	// Trong lúc đặt cờ, mọi sự kiện lỗi phát sinh từ context-cancel đều bị triệt tiêu (vừa là kỳ vọng
	// của người dùng, vừa tránh trùng với sự kiện "người dùng thủ công tạm dừng"). Lỗi thật (không phải
	aborting atomic.Bool

	streamThinking      bool
	lastThinkingByAgent map[string]string          // agent → văn bản thinking tích lũy gần nhất (để trích phần delta)
	dispatchStarts      map[string]*activeCall     // agent được phân phát → lần gọi DISPATCH đang thực hiện
	toolStarts          map[string]*activeCall     // agent → lần gọi TOOL đang thực hiện
	streamExtractors    map[string]*agentExtractor // agent → extractor nội dung tham số JSON của lần gọi tool hiện tại
	streamArgPrefixes   map[string]string          // agent/tool → tiền tố dòng tham số, dùng nhận sớm label gọn nhẹ
	streamArgLabels     map[string]string          // agent/tool → tên hiển thị đã nhận ra sớm từ dòng tham số
	retryEvents         map[string]string          // retry scope → ID sự kiện, cập nhật tại chỗ cùng một dòng (2/7)
	streamHasContent    bool                       // round stream hiện tại đã xuất nội dung chưa (xét có cần phân tách đoạn hay không)
	streamLastByte      byte                       // byte cuối của lần xuất streaming gần nhất (để bù xuống dòng chính xác)
}

// agentExtractor ghi lại tên tool đang được trích và thực thể extractor của một agent.
// Tên tool dùng phát hiện "một lần gọi tool mới bắt đầu", tránh cache bị tàn dư vòng trước làm bẩn.
type agentExtractor struct {
	tool       string
	ext        *jsonFieldExtractor
	emittedAny bool // extractor này đã xuất nội dung chưa; dùng bù phân tách đoạn trước lần xuất đầu
}

type agentState struct {
	name    string
	state   string
	tool    string
	summary string
	turn    int
	context AgentContextSnapshot
	updated time.Time
}

func newObserver(s *storepkg.Store, emitEv func(Event), emitD func(string), emitC func()) *observer {
	return &observer{
		emitEv:              emitEv,
		emitD:               emitD,
		emitC:               emitC,
		store:               s,
		agents:              make(map[string]*agentState),
		lastThinkingByAgent: make(map[string]string),
		dispatchStarts:      make(map[string]*activeCall),
		toolStarts:          make(map[string]*activeCall),
		streamExtractors:    make(map[string]*agentExtractor),
		streamArgPrefixes:   make(map[string]string),
		streamArgLabels:     make(map[string]string),
		retryEvents:         make(map[string]string),
	}
}

// ── Lối điều khiển trực tiếp từ Engine ──
//
// Engine chạy trực tiếp Worker, nguồn sự kiện chia hai luồng:
//  1. dispatchStart/dispatchFinish —— Engine gọi trực tiếp tại biên phân phát (dòng DISPATCH)
//  2. workerProgress —— chuyển tiếp tiến độ của Worker (ctx ToolProgress),
//     do handleToolUpdate xử lý thống nhất TOOL/thân văn bản streaming/thinking/retry/context
//     (dòng TOOL/thân văn streaming/thinking/retry/context).

// dispatchStart ghi lại bắt đầu một lần phân phát Worker và phát dòng DISPATCH.
func (o *observer) dispatchStart(agent, task, reason string) {
	o.toolMu.Lock()
	defer o.toolMu.Unlock()
	summary := dispatchSummary(agent, task)
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = ""
		a.summary = fmt.Sprintf("engine → %s", summary)
	})
	id := nextEventID()
	o.dispatchStarts[agent] = &activeCall{id: id, start: time.Now(), summary: summary}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "DISPATCH",
		Agent:    agent,
		Summary:  summary,
		Detail:   dispatchDetail(task, reason),
		Level:    "info",
	})
}

// dispatchFinish chốt dòng DISPATCH thành trạng thái hoàn thành và phục hồi trạng thái Worker;
// dọn các dòng TOOL mồ côi danh nghĩa Worker này (trên đường abort/lỗi ProgressToolEnd có thể vắng mặt).
func (o *observer) dispatchFinish(agent string, runErr error) {
	o.toolMu.Lock()
	defer o.toolMu.Unlock()
	o.updateAgent(agent, func(a *agentState) {
		a.state = "idle"
		a.tool = ""
	})
	delete(o.lastThinkingByAgent, agent)
	if call, ok := o.toolStarts[agent]; ok {
		delete(o.toolStarts, agent)
		delete(o.streamExtractors, agent)
		o.emitCallFinish(call, "TOOL", agent, runErr)
	}
	if call, ok := o.dispatchStarts[agent]; ok {
		delete(o.dispatchStarts, agent)
		o.emitCallFinish(call, "DISPATCH", agent, runErr)
	}
	o.streamClear()
}

// workerProgress thích ứng chuyển tiếp tiến độ Worker thành xử lý ToolExecUpdate sẵn có.
func (o *observer) workerProgress(p agentcore.ProgressPayload) {
	payload := p
	o.handleToolUpdate(agentcore.Event{Type: agentcore.EventToolExecUpdate, Progress: &payload})
}

func (o *observer) finalize() {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	for _, a := range o.agents {
		a.state = "idle"
		a.tool = ""
	}
}

// setAborting do Host gọi tại các điểm chuyển vòng đời Abort/Close/Start, điều khiển việc
// các sự kiện dẫn sinh kiểu "context canceled" có bị triệt tiêu hay không (tránh trùng "người dùng thủ công tạm dừng").
func (o *observer) setAborting(v bool) { o.aborting.Store(v) }

// retryEventID trả ID sự kiện cập nhật tại chỗ của retry scope (attempt<=1 thì tạo mới).
// Bên gọi đang giữ toolMu (hiện chỉ nhánh ProgressRetry của handleToolUpdate).
func (o *observer) retryEventID(scope string, attempt int) string {
	if strings.TrimSpace(scope) == "" {
		scope = "engine"
	}
	if o.retryEvents == nil {
		o.retryEvents = make(map[string]string)
	}
	if attempt <= 1 || o.retryEvents[scope] == "" {
		o.retryEvents[scope] = nextEventID()
	}
	return o.retryEvents[scope]
}

// emitAndLog dùng cho trạng thái "bắt đầu" của sự kiện kiểu gọi: gửi TUI nhưng không ghi runtime queue,
// tránh khi replay bị "một dòng bắt đầu, xong lại một dòng" trùng lặp. slog do host.emitEvent ghi thống nhất.
func (o *observer) emitAndLog(ev Event) {
	o.emitEv(ev)
}

// persistEvent ghi sự kiện vào runtime queue (slog do host.emitEvent ghi thống nhất).
func (o *observer) persistEvent(ev Event) {
	if o.store == nil || o.store.Runtime == nil {
		return
	}
	priority := domain.RuntimePriorityBackground
	switch {
	case ev.Level == "error":
		priority = domain.RuntimePriorityControl
	case ev.Category == "SYSTEM" || ev.Category == "ERROR":
		priority = domain.RuntimePriorityControl
	}
	if _, err := o.store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Time:     ev.Time,
		Priority: priority,
		Category: ev.Category,
		Summary:  ev.Summary,
		Payload:  ev,
	}); err != nil {
		slog.Warn("Persist sự kiện vận hành thất bại", "module", "observer", "category", ev.Category, "err", err)
	}
}

func (o *observer) updateAgent(name string, fn func(*agentState)) {
	if name == "" {
		return
	}
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	a, ok := o.agents[name]
	if !ok {
		a = &agentState{name: name, state: "idle"}
		o.agents[name] = a
	}
	fn(a)
	a.updated = time.Now()
}

func (o *observer) agentSnapshots() []AgentSnapshot {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	snaps := make([]AgentSnapshot, 0, len(o.agents))
	for _, a := range o.agents {
		snaps = append(snaps, AgentSnapshot{
			Name:      a.name,
			State:     a.state,
			Summary:   a.summary,
			Tool:      a.tool,
			Turn:      a.turn,
			Context:   a.context,
			UpdatedAt: a.updated,
		})
	}
	return snaps
}
