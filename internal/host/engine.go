package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"

	"github.com/voocel/ainovel-cli/internal/arbiter"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/notify"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// engine là engine thực thi deterministic: đọc dữ kiện → Route → kiểm tra trước → chạy trực tiếp Worker →
// kiểm tra tiến trình → lặp; cảnh ngữ nghĩa tham vấn Arbiter khi cần. Nó thực thi quyết định, không tham gia phán đoán văn học
// (docs/engine-rfc.md). Đơn goroutine nối tiếp, trạng thái điều khiển chỉ đổi tại biên vòng lặp.
type engine struct {
	store   *storepkg.Store
	workers *subagent.Runner

	arbiterModel    agentcore.ChatModel
	failurePrompt   string
	planStartPrompt string // system prompt phán định khởi động: phán định chưa hoàn thành thì engine dựa StartPrompt phán định bù tại chỗ
	style           string // tên phong cách, truyền cho DecidePlanStart khi phán định bù
	// reconsult đưa can thiệp quá hạn về đường phán định đầy đủ của host (persist/audit/áp dụng toàn bộ hành động),
	// chạy async — engine chỉ vứt đơn phân công quá hạn, không tự làm phán định lại thiếu sót.
	reconsult func(text string)

	observer  *observer
	budget    *BudgetSentinel
	gate      *ChapterAdvanceGate
	refresh   func() // refresh RestorePack trước mỗi lần phân phát writer
	emitEvent func(Event)
	notify    func(kind, level, title, body string)
	onPause   func(summary string) // engine tự tạm dừng (ngắt mạch bế tắc/phán định thất bại abort): đi theo ngữ nghĩa tạm dừng hợp nhất của host (lifecycle=paused)
	onDone    func()               // run kết thúc (bất kể lý do); host định trạng thái cuối theo dữ kiện store

	mu      sync.Mutex
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	running bool
	pending []controlOp       // hành động trạng thái điều khiển của can thiệp, commit tại biên
	next    *flow.Instruction // lệnh ưu tiên thực thi vòng sau (plan_start / arbiter dispatch)
	// deferGateForNext chỉ sống chết cùng next: hold+dispatch phải chạy trước cặp
	// editor/writer tương ứng, để nó dựng hàng đợi viết lại, sau đó Gate mới xét được rewrites_drained.
	deferGateForNext bool

	// Theo dõi bế tắc: sau vòng trước Route vẫn ra cùng khóa lệnh thì tích lũy.
	// Lệnh Router là chiếu của hậu điều kiện tác vụ; hoàn thành thật sẽ làm lệnh tiếp theo đổi.
	lastKey string
	repeats int
	// Thử lại khi thất bại: cùng khóa lệnh chỉ thử lại một lần, thất bại nữa thì hỏi Arbiter.
	failedKey string
	// Giữ lỗi Worker gần nhất của cùng lệnh, để phán định bế tắc thấy nguyên nhân thất bại thật.
	lastWorkerErrorKey string
	lastWorkerError    error
}

// deadlockConsultAt / deadlockAbortAt: repeats đạt mức trước thì hỏi Arbiter, đạt mức sau thì ngắt mạch cứng.
// Engine deterministic phải có giới hạn trên rõ ràng cho vòng lặp không tiến triển (RFC §5).
const (
	deadlockConsultAt = 3
	deadlockAbortAt   = 5
)

// controlOp là hành động sửa trạng thái điều khiển trong phán định can thiệp (commit tại biên; RFC §3).
// text/facts giữ ngữ cảnh tham vấn gốc: khi dispatch đối chiếu thất bại thì re-consult theo dữ kiện mới.
type controlOp struct {
	hold     *arbiter.AdvanceHoldOp
	reopen   *arbiter.ReopenOp
	dispatch *arbiter.DispatchOp
	text     string
	facts    arbiter.InterventionFacts
}

// start khởi động vòng lặp engine; đang chạy thì no-op (trả false).
func (e *engine) start(initial *flow.Instruction) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = agentcore.WithToolProgress(ctx, e.observer.workerProgress)
	e.cancel = cancel
	e.running = true
	// initial rỗng thì không đè e.next — can thiệp lúc dừng máy có thể đã qua applyControlOp xếp vào
	// đơn phân công phán định (như editor viết lại), nếu start(nil) xóa nó thì Route sẽ giao writer viết tiếp,
	// ngược ý người dùng.
	if initial != nil {
		e.next = initial
		e.deferGateForNext = false
	}
	e.lastKey, e.repeats, e.failedKey = "", 0, ""
	e.lastWorkerErrorKey, e.lastWorkerError = "", nil
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.run(ctx)
	}()
	return true
}

// abort hủy vòng lặp hiện tại (ngữ nghĩa tạm dừng; checkpoint đảm bảo không mất dữ liệu).
func (e *engine) abort() {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// wait đợi goroutine Engine hiện tại thoát trọn vẹn. Host.Close sẽ cancel trước rồi mới gọi nó,
// đảm bảo tool ghi và runEnded đều kết thúc rồi mới đóng kênh sự kiện và thoát tiến trình.
func (e *engine) wait() {
	e.wg.Wait()
}

func (e *engine) isRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// enqueue xếp hành động trạng thái điều khiển của can thiệp vào hàng đợi biên (engine đang chạy); trả false nghĩa là không chạy,
// bên gọi nên tự thực thi ngay.
func (e *engine) enqueue(op controlOp) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return false
	}
	e.pending = append(e.pending, op)
	return true
}

func (e *engine) run(ctx context.Context) {
	defer func() {
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		leftover := e.pending
		e.pending = nil
		e.mu.Unlock()
		// Race thoát: khi enqueue và thoát chạy đồng thời, hành động can thiệp sót lại không được vứt đi âm thầm —
		// hold/reopen là ghi dữ kiện idempotent, bù thực thi bằng ctx riêng; dispatch không có engine để giao,
		// phục hồi PendingSteer bằng persist (host có thể đã xóa theo "enqueue thành công"), lần sau
		// Resume/Continue replay toàn bộ can thiệp.
		for _, op := range leftover {
			if op.dispatch != nil {
				if op.text != "" {
					if err := e.store.RunMeta.SetPendingSteer(op.text); err != nil {
						slog.Warn("Ghi lại can thiệp sót thất bại", "module", "engine", "err", err)
					}
				}
				e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "Engine đã dừng, đơn phân công phán định chưa thực thi; can thiệp đã giữ lại, khi tiếp tục sáng tác sẽ tự phán định lại"})
				op.dispatch = nil
			}
			if op.hold != nil || op.reopen != nil {
				if err := e.applyControlOp(context.Background(), op); err != nil {
					e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
						Summary: "Bù thực thi can thiệp lúc engine thoát thất bại: " + err.Error()})
				}
			}
		}
		e.onDone()
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		// hold+dispatch phải để cặp đơn phân công dựng dữ kiện viết lại trước; các trường hợp khác kiểm tra Gate
		// thống nhất trước khi phân phát, đảm bảo boundary hold và review chưa có phép không chạy thừa một Worker.
		deferGate := e.applyPendingOps(ctx) || e.nextDefersGate()
		if !deferGate {
			if e.gate.HandleBoundary() {
				return
			}
		}

		inst := e.takeNext()
		if inst == nil {
			state, err := flow.LoadState(e.store)
			if err != nil {
				e.pauseWithNotify(notify.KindWorkerFailure, "Đọc dữ kiện route thất bại, đã tạm dừng: "+err.Error())
				return
			}
			// Tóm tắt quyển có thể đã ghi đĩa mà tiến trình chưa kịp MarkComplete. Khi các artifact tổng hợp đầy đủ
			// thì bổ sung phán định hoàn thành theo dữ kiện rồi mới giao Router, tránh quyển kết bị giao nhầm đi viết tiếp quyển.
			if state.AggregateRefresh == nil && state.Progress != nil && state.Progress.Layered &&
				state.Progress.Phase == domain.PhaseWriting && state.ArcBoundary != nil &&
				state.ArcBoundary.IsVolumeEnd && state.HasArcReview && state.HasArcSummary && state.HasVolumeSummary {
				complete, reconcileErr := tools.ReconcileLayeredCompletion(e.store)
				if reconcileErr != nil {
					e.pauseWithNotify(notify.KindWorkerFailure, "Khôi phục trạng thái hoàn thành thất bại, đã tạm dừng: "+reconcileErr.Error())
					return
				}
				if complete {
					continue
				}
			}
			inst = flow.Route(state)
		}
		if inst == nil {
			var err error
			inst, err = e.planStartFallback(ctx)
			if err != nil {
				e.pauseWithNotify(notify.KindPlanStart, "Đọc dữ kiện khôi phục lập dàn ý thất bại, đã tạm dừng: "+err.Error())
				return
			}
		}
		if inst == nil {
			// Cảnh ngữ nghĩa hoặc trạng thái cuối: hoàn sách → kết thúc deterministic; còn lại (Steering sót lại v.v.)
			// → dừng máy tự nhiên, đợi người dùng Continue / can thiệp.
			return
		}
		replaced, err := e.precheck(inst)
		if err != nil {
			e.pauseWithNotify(notify.KindWorkerFailure, "Kiểm tra trước đơn phân công thất bại, đã tạm dừng: "+err.Error())
			return
		}
		if replaced != nil {
			inst = replaced
		}
		allowed, gateErr := e.gate.Allow(inst)
		if gateErr != nil {
			e.pauseWithNotify(notify.KindAdvanceGate, "Lỗi kiểm soát tiến trình chương, đã tạm dừng: "+gateErr.Error())
			return
		}
		if !allowed {
			return
		}
		if stop := e.trackDeadlock(ctx, &inst); stop {
			return
		}
		if inst == nil {
			continue // phán định bế tắc yêu cầu tính lại route
		}

		err = e.runWorker(ctx, inst)
		if ctx.Err() != nil {
			return
		}
		e.rememberWorkerError(inst, err)
		if err != nil {
			// trackDeadlock ghi trước lần thử này trước khi phân phát. Lỗi chưa vào được Worker
			// ngữ nghĩa thực thi hợp lệ không được tính là "cùng tác vụ không tiến triển".
			e.discardNonSemanticDeadlockAttempt(inst, err)
			if stop := e.handleWorkerError(ctx, inst, err); stop {
				return
			}
		}

		// Biên chính sách: cắt lỗ ngân sách ưu tiên trước tạm dừng nghiệm thu/tiến trình.
		if e.budget.HandleBoundary() {
			return
		}
		if e.gate.HandleBoundary() {
			return
		}
	}
}

func (e *engine) takeNext() *flow.Instruction {
	e.mu.Lock()
	defer e.mu.Unlock()
	inst := e.next
	e.next = nil
	e.deferGateForNext = false
	return inst
}

func (e *engine) nextDefersGate() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.next != nil && e.deferGateForNext
}

// planStartFallback phủ hai cửa sổ thiếu dữ kiện lập dàn ý mà Route không suy ra được planner:
//  1. Phán định đã ghi đĩa, save_foundation đầu chưa xảy ra → chạy tiếp theo PlanStartRecord đã chốt,
//     không phán định lại (RFC §6); foundation đầu ghi đĩa xong tier sẵn sàng, nhánh bù thiếu tiếp quản.
//  2. Phán định chưa từng hoàn thành (lỗi model lúc khởi động) nhưng dữ kiện đầu vào StartPrompt còn → phán định bù tại chỗ.
//     Đây là lần thử lại của phán định đầu, không vi phạm "khôi phục không dựa vào phán định lại" — kỷ luật đó nhắm vào phán định đã tồn tại.
//     Phán định bù thất bại thì đi tạm dừng rõ ràng: khởi động thất bại không cho phép dừng máy âm thầm.
func (e *engine) planStartFallback(ctx context.Context) (*flow.Instruction, error) {
	progress, err := e.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil {
		return nil, nil
	}
	if progress.Phase == domain.PhaseWriting || progress.Phase == domain.PhaseComplete {
		return nil, nil
	}
	meta, err := e.store.RunMeta.Load()
	if err != nil {
		return nil, fmt.Errorf("load run meta: %w", err)
	}
	if meta == nil || meta.PlanningTier != "" {
		return nil, nil
	}
	missing, err := e.store.FoundationMissing()
	if err != nil {
		return nil, fmt.Errorf("load foundation state: %w", err)
	}
	if len(missing) == 0 {
		return nil, nil
	}
	if meta.PlanStart != nil {
		return &flow.Instruction{
			Agent:  meta.PlanStart.Planner,
			Task:   meta.PlanStart.PlannerTask,
			Reason: "Bắt đầu lập dàn ý theo phán định khởi động đã chốt",
		}, nil
	}
	if meta.StartPrompt == "" {
		return nil, nil
	}
	return e.retryPlanStart(ctx, meta.StartPrompt), nil
}

// retryPlanStart phán định bù quyết định khởi động rồi chốt (phán định ghi dữ kiện trước rồi mới thực thi, cùng cấu trúc với StartPrepared).
func (e *engine) retryPlanStart(ctx context.Context, prompt string) *flow.Instruction {
	start := time.Now()
	decision, derr := runObservedDecision(e.observer, "Phán định bù khởi động", func() (arbiter.PlanStartDecision, error) {
		return arbiter.DecidePlanStart(ctx, e.arbiterModel, e.planStartPrompt, prompt, e.style)
	})
	rec := storepkg.DecisionRecord{Kind: "plan_start", Decider: "arbiter", Input: prompt,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	rec, recErr := e.store.Decisions.Append(rec)
	if recErr != nil {
		slog.Warn("Ghi đĩa audit phán định bù khởi động thất bại", "module", "engine", "err", recErr)
	}
	if derr != nil {
		e.pauseWithNotify(notify.KindPlanStart, "Phán định khởi động thất bại, đã tạm dừng (vui lòng kiểm tra cấu hình model/mạng rồi tiếp tục): "+derr.Error())
		return nil
	}
	if err := e.store.RunMeta.SetPlanStart(domain.PlanStartRecord{
		RawPrompt: prompt, Planner: decision.Planner, PlannerTask: decision.Task, DecisionID: rec.ID,
	}); err != nil {
		e.pauseWithNotify(notify.KindPlanStart, "Phán định khởi động không ghi đĩa được, đã tạm dừng: "+err.Error())
		return nil
	}
	e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info",
		Summary: fmt.Sprintf("Phán định khởi động đã bổ sung (planner: %s — %s)", decision.Planner, decision.Reason)})
	return &flow.Instruction{Agent: decision.Planner, Task: decision.Task, Reason: decision.Reason}
}

// precheck là hóa thân deterministic của ToolGate cũ: phân phát không hợp lệ thì viết lại trực tiếp, không cần văn bản hướng dẫn.
func (e *engine) precheck(inst *flow.Instruction) (*flow.Instruction, error) {
	progress, err := e.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	if progress != nil && progress.Phase == domain.PhaseComplete {
		// Giai đoạn hoàn sách lối ra hợp pháp duy nhất là reopen (hành động can thiệp), mọi phân phát đều vứt trực tiếp.
		slog.Warn("Phân phát bị vứt trong giai đoạn hoàn sách", "module", "engine", "agent", inst.Agent)
		return &flow.Instruction{}, nil // để rỗng: vòng sau Route về nil thì dừng máy tự nhiên
	}
	if inst.Agent == "writer" {
		if progress == nil || progress.Phase != domain.PhaseWriting {
			phase := "<nil>"
			if progress != nil {
				phase = string(progress.Phase)
			}
			return nil, fmt.Errorf("writer chỉ được phân phát trong giai đoạn writing (hiện phase=%s): %w", phase, errInvalidWriteTarget)
		}
		ch, err := writerTargetChapter(e.store)
		if err != nil {
			return nil, err
		}
		if ch > 0 {
			if err := tools.EnsureChapterExpanded(e.store, ch); err != nil {
				if !errors.Is(err, errs.ErrToolPrecondition) {
					return nil, err
				}
				// Chương mục tiêu chưa triển khai → đổi lệnh deterministic sang architect_long triển khai (văn bản hướng dẫn của gate cũ
				// là nói với LLM; Engine trực tiếp làm điều đúng).
				return &flow.Instruction{
					Agent:  "architect_long",
					Task:   fmt.Sprintf("Cung tiếp theo vẫn là khung (%s). Hãy gọi save_foundation(type=expand_arc) để triển khai cung tiếp theo; nếu quyển hiện tại đã viết xong, đổi sang type=append_volume để thêm và triển khai quyển tiếp theo.", err),
					Reason: "Chương mục tiêu viết chưa triển khai, triển khai trước rồi viết tiếp",
				}, nil
			}
		}
		e.refresh()
	}
	return nil, nil
}

// writerTargetChapter suy ra chương mà lần phân phát writer kế tiếp thực sự sẽ viết (đầu hàng đợi viết lại, nếu không thì chương tiếp theo).
func writerTargetChapter(st *storepkg.Store) (int, error) {
	progress, err := st.Progress.Load()
	if err != nil {
		return 0, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil {
		return 0, fmt.Errorf("progress chưa khởi tạo")
	}
	if len(progress.PendingRewrites) > 0 {
		return progress.PendingRewrites[0], nil
	}
	return progress.NextChapter(), nil
}

// trackDeadlock duy trì bộ đếm bế tắc: cùng Agent+Task xuất hiện liên tiếp nghĩa là vòng trước
// chưa thỏa hậu điều kiện route. Các checkpoint trung gian plan/draft/edit bên trong Worker
// chỉ dùng cho khôi phục và quan sát, không được reset bộ đếm cấp Engine (issue #84).
// repeats đạt ngưỡng thì tham vấn Arbiter, chạm giới hạn cứng thì ngắt mạch luôn.
// Trả stop=true nghĩa là vòng này nên kết thúc; inst có thể bị Arbiter viết lại (reroute) hoặc đặt nil (tính lại).
func (e *engine) trackDeadlock(ctx context.Context, inst **flow.Instruction) (stop bool) {
	in := *inst
	if in == nil || in.Agent == "" {
		*inst = nil
		return false
	}
	key := instructionKey(in)
	if key == e.lastKey {
		e.repeats++
	} else {
		e.lastKey, e.repeats = key, 1
	}
	if e.repeats < deadlockConsultAt {
		return false
	}
	if e.repeats >= deadlockAbortAt {
		e.pauseStuck(notify.KindDeadlock, in, fmt.Sprintf("Ngắt mạch bế tắc: lệnh liên tiếp %d lần không tiến triển (%s), đã tạm dừng chờ can thiệp thủ công", e.repeats, in.Agent))
		return true
	}
	// Tham vấn bế tắc Arbiter (repeats ∈ [consultAt, abortAt)). Phán định retry không reset bộ đếm.
	facts := e.failureFacts("deadlock", in, e.workerErrorFor(in))
	decision, err := runObservedDecision(e.observer, "Phán định bế tắc", func() (arbiter.FailureDecision, error) {
		return arbiter.DecideFailure(ctx, e.arbiterModel, e.failurePrompt, facts)
	})
	e.recordFailureDecision("deadlock", in, facts, decision, err)
	if err != nil {
		e.pauseWithNotify(notify.KindDeadlock, "Phán định bế tắc thất bại, đã tạm dừng chờ can thiệp thủ công: "+err.Error())
		return true
	}
	switch decision.Action {
	case "retry":
		return false
	case "reroute":
		*inst = &flow.Instruction{Agent: decision.Dispatch.Agent, Task: decision.Dispatch.Task, Reason: decision.Reason}
		return false
	default: // abort
		e.pauseStuck(notify.KindDeadlock, in, "Phán định bế tắc: "+decision.Reason)
		return true
	}
}

// runWorker chạy trực tiếp một subagent: sự kiện DISPATCH + chuyển tiếp tiến độ + phân tích kết quả.
func (e *engine) runWorker(ctx context.Context, inst *flow.Instruction) error {
	e.observer.dispatchStart(inst.Agent, inst.Task, inst.Reason)
	// Nhiệm vụ Writer đánh dấu trước là đang thực hiện (nhất quán với Dispatcher cũ: dàn ý UI lập tức phản ánh "▸ đang thực hiện").
	if inst.Agent == "writer" && inst.Chapter > 0 {
		if err := e.store.Progress.ValidateChapterWork(inst.Chapter); err != nil {
			runErr := fmt.Errorf("%w: %w", errInvalidWriteTarget, err)
			e.observer.dispatchFinish(inst.Agent, runErr)
			return runErr
		}
		if err := e.store.Progress.StartChapter(inst.Chapter); err != nil {
			runErr := fmt.Errorf("%w: đánh dấu trước chương %d đang thực hiện thất bại: %w", errInvalidWriteTarget, inst.Chapter, err)
			e.observer.dispatchFinish(inst.Agent, runErr)
			return runErr
		}
	}

	// Tiến độ Worker được chuyển tiếp tới observer qua ctx ToolProgress.
	runCtx := agentcore.WithToolProgress(ctx, func(p agentcore.ProgressPayload) {
		e.observer.workerProgress(p)
	})
	_, err := e.workers.Run(runCtx, inst.Agent, inst.Task)
	if err == nil {
		// Thành công thì xóa theo dõi thất bại: lần thất bại kế tiếp cùng khóa được hưởng lại hạn mức "thử lại một lần trước".
		e.failedKey = ""
	}
	e.observer.dispatchFinish(inst.Agent, err)
	return err
}

// handleWorkerError với cùng lệnh thì thử lại một lần trước, rồi giao loại lỗi và dữ kiện hiện tại cho Arbiter.
// Engine không hard-code lỗi thực thi nào "chắc chắn không thể khôi phục"; đổi lệnh ngữ nghĩa do model quyết định, biên Store tiếp tục
// chịu trách nhiệm chặn ghi không hợp lệ.
func (e *engine) handleWorkerError(ctx context.Context, inst *flow.Instruction, werr error) (stop bool) {
	msg := werr.Error()

	key := instructionKey(inst)
	if e.failedKey != key {
		// Thất bại đầu: thử lại nguyên lệnh một lần (vòng sau Route tính lại, dẫn động bằng dữ kiện nên idempotent tự nhiên).
		e.failedKey = key
		return false
	}
	e.failedKey = ""
	facts := e.failureFacts("worker_failure", inst, werr)
	decision, err := runObservedDecision(e.observer, "Phán định thất bại", func() (arbiter.FailureDecision, error) {
		return arbiter.DecideFailure(ctx, e.arbiterModel, e.failurePrompt, facts)
	})
	e.recordFailureDecision("worker_failure", inst, facts, decision, err)
	if err != nil {
		e.pauseWithNotify(notify.KindWorkerFailure, "Phán định thất bại không khả dụng, đã tạm dừng chờ can thiệp thủ công: "+msg+contentFilterAdvice(werr))
		return true
	}
	switch decision.Action {
	case "retry":
		return false
	case "reroute":
		e.mu.Lock()
		e.next = &flow.Instruction{Agent: decision.Dispatch.Agent, Task: decision.Dispatch.Task, Reason: decision.Reason}
		e.deferGateForNext = false
		e.mu.Unlock()
		return false
	default: // abort
		e.pauseStuck(notify.KindWorkerFailure, inst, "Phán định thất bại: "+decision.Reason+contentFilterAdvice(werr))
		return true
	}
}

// pauseStuck tạm dừng khi engine bỏ một lệnh: chương viết lại rời hàng đợi trước rồi mới dừng. Chỉ dùng khi engine đã phán định lệnh đó
// là lối đi không thông (ngắt mạch bế tắc, phán định bế tắc/thất bại abort), sự cố hạ tầng như phán định không khả dụng vẫn đi
// pauseWithNotify — đó là vấn đề bên ngoài, không nên đánh đổi thêm một chương viết lại.
func (e *engine) pauseStuck(kind string, inst *flow.Instruction, body string) {
	if e.dropStuckRewrite(inst) {
		body += fmt.Sprintf("; chương %d đã rời hàng đợi viết lại (giữ bản cuối trước đó), tiếp tục sáng tác sẽ tiến từ các chương sau", inst.Chapter)
	}
	e.pauseWithNotify(kind, body)
}

// dropStuckRewrite đưa chương viết lại kẹt chết ra khỏi hàng đợi. PendingRewrites là dữ kiện persist, khi engine bỏ
// lệnh này mà không xuất hàng thì khởi động lại sẽ lập tức replay đúng lệnh chết đó, khóa chết cả cuốn sách vĩnh viễn (issue #110).
// Trả true nghĩa là thực sự đã xuất hàng.
func (e *engine) dropStuckRewrite(inst *flow.Instruction) bool {
	if inst == nil || inst.Agent != "writer" || inst.Chapter <= 0 {
		return false
	}
	progress, err := e.store.Progress.Load()
	if err != nil || progress == nil || !slices.Contains(progress.PendingRewrites, inst.Chapter) {
		return false
	}
	if err := e.store.Progress.CompleteRewrite(inst.Chapter); err != nil {
		slog.Warn("Đưa chương viết lại kẹt chết ra khỏi hàng đợi thất bại", "module", "engine", "chapter", inst.Chapter, "err", err)
		return false
	}
	return true
}

// discardNonSemanticDeadlockAttempt hủy lần ghi trước của trackDeadlock cho lần phân phát này
// về ngữ nghĩa. Chỉ loại các loại lỗi ổn định mà gọi model chưa thực thi trọn vẹn; content_filter
// giữ trong đường tự sửa vốn có, các trường hợp thật sự không tiến triển như max_turns, stop_guard, hủy vẫn được đếm.
func (e *engine) discardNonSemanticDeadlockAttempt(inst *flow.Instruction, werr error) {
	if inst == nil || !isNonSemanticWorkerFailure(werr) {
		return
	}
	key := instructionKey(inst)
	if e.lastKey != key || e.repeats <= 0 {
		return
	}
	e.repeats--
	if e.repeats == 0 {
		e.lastKey = ""
	}
}

// isNonSemanticWorkerFailure chỉ nhận diện lỗi "lần thực thi model này không sinh ra ngữ nghĩa nào để phán đoán".
// Ưu tiên dựa vào hợp đồng chuỗi lỗi của agentcore; khi chuỗi lỗi bị provider san phẳng thì tái dùng phân loại log.
func isNonSemanticWorkerFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, agentcore.ErrContextOverflow) || errors.Is(err, agentcore.ErrStreamPartial) {
		return true
	}
	providerErr := agentcore.ClassifyProvider(err)
	classified := errors.Is(providerErr, agentcore.ErrProviderStreamIdle) ||
		errors.Is(providerErr, agentcore.ErrProviderQuota) ||
		errors.Is(providerErr, agentcore.ErrProviderRateLimit) ||
		errors.Is(providerErr, agentcore.ErrProviderTimeout) ||
		errors.Is(providerErr, agentcore.ErrProviderAuth) ||
		errors.Is(providerErr, agentcore.ErrProviderNetwork) ||
		errors.Is(providerErr, agentcore.ErrProviderOverloaded)
	return classified || errorKind(err, err.Error()) == "overloaded"
}

func instructionKey(inst *flow.Instruction) string {
	if inst == nil {
		return ""
	}
	return inst.Agent + "\x00" + inst.Task
}

func (e *engine) rememberWorkerError(inst *flow.Instruction, workerErr error) {
	if workerErr == nil || inst == nil {
		e.lastWorkerErrorKey, e.lastWorkerError = "", nil
		return
	}
	e.lastWorkerErrorKey, e.lastWorkerError = instructionKey(inst), workerErr
}

func (e *engine) workerErrorFor(inst *flow.Instruction) error {
	if e.lastWorkerErrorKey != instructionKey(inst) {
		return nil
	}
	return e.lastWorkerError
}

// contentFilterAdvice đính kèm lối đi khả thi cho người dùng vào các lần tạm dừng bị content filter chặn.
// Kiểm duyệt là hộp đen của provider, kiểm tra trước/né tránh đều không khả thi, việc làm được chỉ là đưa quyết định tới tay người dùng;
// bản thân việc bị chặn không ngắt mạch sớm — phân phát lại sau khi đổi ngữ cảnh có tỷ lệ tự sửa thật (đo thực tế ch21-24),
// đi hết "thử lại miễn phí → trọng tài" rồi mới tạm dừng.
func contentFilterAdvice(werr error) string {
	if !errors.Is(werr, agentcore.ErrProviderContentFilter) {
		return ""
	}
	return ". Đây là bị chặn bởi content filter của provider (không phải lỗi local), có thể chọn: /model chuyển sang provider không có lớp kiểm duyệt rồi nhập 'tiếp tục'; hoặc sửa cách diễn đạt bản nháp chương này (drafts/) rồi tiếp tục; thử lại nguyên văn nhiều khả năng vẫn bị chặn"
}

// errInvalidWriteTarget đánh dấu mục tiêu viết bất hợp pháp bị kiểm tra trước của runWorker chặn lại, cho chuỗi lỗi và
// dữ kiện Arbiter giữ ngữ nghĩa ổn định; có thử lại hay đổi lệnh vẫn do quy trình thất bại hợp nhất quyết định.
var errInvalidWriteTarget = errors.New("mục tiêu viết bất hợp pháp")

func (e *engine) failureFacts(kind string, inst *flow.Instruction, workerErr error) arbiter.FailureFacts {
	f := arbiter.FailureFacts{Kind: kind, Agent: inst.Agent, Task: inst.Task, Repeats: e.repeats}
	if workerErr != nil {
		f.Error = workerErr.Error()
		f.ErrorKind = errorKind(workerErr, f.Error)
		if f.ErrorKind == "" {
			f.ErrorKind = "unknown"
		}
	}
	missing, err := e.store.FoundationMissing()
	if err != nil {
		f.FactWarnings = append(f.FactWarnings, "Đọc trạng thái thiết lập nền thất bại: "+err.Error())
	} else {
		f.FoundationGap = missing
	}
	p, err := e.store.Progress.Load()
	if err != nil {
		f.FactWarnings = append(f.FactWarnings, "Đọc tiến độ sáng tác thất bại: "+err.Error())
	}
	if p != nil {
		f.Phase = string(p.Phase)
		f.NextChapter = p.NextChapter()
		f.PendingQueue = p.PendingRewrites
	}
	return f
}

func (e *engine) recordFailureDecision(kind string, inst *flow.Instruction, facts arbiter.FailureFacts, d arbiter.FailureDecision, derr error) {
	rec := storepkg.DecisionRecord{Kind: kind, Decider: "arbiter", Input: inst.Agent + ": " + inst.Task, Reason: d.Reason}
	if data, err := json.Marshal(facts); err == nil {
		rec.Facts = data
	}
	if derr == nil {
		if data, err := json.Marshal(d); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	if _, err := e.store.Decisions.Append(rec); err != nil {
		slog.Warn("Ghi đĩa audit phán định thất bại", "module", "engine", "kind", kind, "err", err)
	}
}

// applyPendingOps commit hành động trạng thái điều khiển của can thiệp tại biên vòng lặp; vòng lặp phải xả hết — re-consult đồng bộ
// (reconsult) sẽ thêm hành động mới trong lúc áp dụng, phải tiêu hóa hết trong biên này, nếu không giữa chừng sẽ
// phân phát thừa một worker (can thiệp phải hiệu lực trước sáng tác tiếp theo).
// Trả về có hold+dispatch phải chạy cặp đơn phân công trước hay không; trường hợp đó bên gọi hoãn kiểm tra Gate.
func (e *engine) applyPendingOps(ctx context.Context) (deferGate bool) {
	for {
		e.mu.Lock()
		ops := e.pending
		e.pending = nil
		e.mu.Unlock()
		if len(ops) == 0 {
			return deferGate
		}
		for _, op := range ops {
			pairedHoldDispatch := op.hold != nil && !op.hold.Cancel && op.dispatch != nil
			err := e.applyControlOp(ctx, op)
			if err != nil {
				// Hành động persist thất bại: host đã xóa PendingSteer theo "enqueue thành công",
				// ở đây ghi lại toàn bộ can thiệp, khi khôi phục/tiếp tục phán định lại và thử lại (hành động idempotent + re-consult theo dữ kiện mới).
				if op.text != "" {
					if serr := e.store.RunMeta.SetPendingSteer(op.text); serr != nil {
						slog.Warn("Ghi lại can thiệp thất bại", "module", "engine", "err", serr)
					}
				}
				e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "Thực thi hành động can thiệp thất bại, đã giữ lại; khi khôi phục/tiếp tục sẽ tự động thử lại"})
			} else if pairedHoldDispatch && e.nextDefersGate() {
				// Chỉ khi hold và cặp đơn phân công đều áp dụng thành công mới được bỏ qua Gate lần này.
				// Nếu hold ghi thất bại hoặc đơn phân công bị vứt vì dữ kiện quá hạn mà cứ bỏ qua tiếp, đều sẽ khiến
				// Worker chưa được bảo hộ tiến lên.
				deferGate = true
			}
		}
	}
}

// applyControlOp thực thi một hành động trạng thái điều khiển (hold ghi thẳng RunMeta, reopen gọi nhân tool, dispatch đối chiếu trước).
// Khi engine chưa chạy thì host gọi trực tiếp trên đường can thiệp; trả lần persist thất bại đầu (bên gọi dựa đó quyết định có
// giữ PendingSteer cho khôi phục replay hay không).
func (e *engine) applyControlOp(ctx context.Context, op controlOp) error {
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	if op.dispatch != nil {
		// Expect phải đối chiếu trước khi hold và các hành động cặp ghi đĩa. Nếu không, sau khi đơn phân công quá hạn thì hold cũ
		// sẽ sót lại, xung đột với hold do phán định lại theo dữ kiện mới, cuối cùng chỉ tạm dừng mà sót không sửa.
		fresh, err := arbiter.CollectInterventionFacts(e.store)
		if err != nil {
			return fmt.Errorf("Làm mới dữ kiện can thiệp: %w", err)
		}
		if fresh.Phase != op.facts.Phase || fresh.Flow != op.facts.Flow ||
			fresh.QueueHead() != op.facts.QueueHead() {
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
				Summary: "Đơn phân công phán định đã quá hạn (dữ kiện tiến triển), phán định lại theo dữ kiện mới nhất"})
			e.recordStale(op)
			if op.text != "" && e.reconsult != nil {
				// Re-consult đồng bộ: can thiệp phải hiệu lực trước sáng tác tiếp theo — async sẽ khiến engine trước khi phán định mới
				// áp dụng lại phân phát thêm một worker. Hành động mới do applyPendingOps xả hết tại biên này.
				e.reconsult(op.text)
			}
			return nil
		}
	}
	if op.hold != nil {
		if op.hold.Cancel {
			meta, err := e.store.RunMeta.Load()
			if err != nil {
				e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "Đọc tạm dừng một lần thất bại: " + err.Error(), Level: "error"})
				return err
			}
			if meta != nil && meta.AdvanceHold != nil {
				if err := e.store.RunMeta.ClearAdvanceHold(*meta.AdvanceHold); err != nil {
					e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "Hủy tạm dừng một lần thất bại: " + err.Error(), Level: "error"})
					return err
				}
			}
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Đã hủy tạm dừng một lần", Level: "info"})
		} else {
			hold := domain.AdvanceHold{After: op.hold.After, TargetChapter: op.hold.TargetChapter, Reason: op.hold.Reason}
			if err := e.store.RunMeta.SetAdvanceHold(hold); err != nil {
				e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "Đặt tạm dừng một lần thất bại: " + err.Error(), Level: "error"})
				return err // khi hold chưa ghi đĩa thì dispatch liên quan không được thực thi
			}
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Đã đặt tạm dừng một lần: " + op.hold.Reason, Level: "info"})
		}
	}
	if op.reopen != nil {
		args, _ := json.Marshal(map[string]any{"chapters": op.reopen.Chapters, "reason": op.reopen.Reason})
		if _, err := tools.NewReopenBookTool(e.store).Execute(ctx, args); err != nil {
			e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "Mở lại viết lại thất bại: " + err.Error(), Level: "error"})
			fail(err)
		} else {
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
				Summary: fmt.Sprintf("Đã mở lại viết lại toàn sách: chương %v vào hàng đợi", op.reopen.Chapters), Level: "info"})
		}
	}
	if op.dispatch != nil {
		// Expect đã đối chiếu trước mọi ghi trạng thái cặp. CheckpointSeq chỉ để audit không tham gia
		// đối chiếu: khi can thiệp tới thì worker phần lớn đang chạy, seq ắt tiến.
		e.mu.Lock()
		// Cửa sổ đã biết (biên best-effort, xem engine-arbiter.md làm rõ ③): đơn phân công từ đây nằm trong bộ nhớ,
		// worker bị giết cứng trước khi khởi động (kill -9, defer không chạy) sẽ mất ý định phân công lần này —
		// thoát bình thường/Abort do defer của run ghi lại PendingSteer làm fallback.
		e.next = &flow.Instruction{Agent: op.dispatch.Agent, Task: interventionDispatchTask(op.dispatch.Task, op.text), Reason: "Phán định can thiệp người dùng"}
		e.deferGateForNext = op.hold != nil && !op.hold.Cancel
		e.mu.Unlock()
	}
	return firstErr
}

// interventionDispatchTask giữ nguyên can thiệp gốc của người dùng, tránh Arbiter vô tình mở rộng
// mục tiêu sửa đổi khi thuật lại nhiệm vụ. Hạ nguồn có thể đọc ngữ cảnh rộng hơn để phán đoán, nhưng chỉ được coi nguyên văn là nguồn ủy quyền hành động.
func interventionDispatchTask(task, original string) string {
	task = strings.TrimSpace(task)
	if strings.TrimSpace(original) == "" {
		return task
	}
	return task + "\n\nCan thiệp gốc của người dùng (nguồn ủy quyền duy nhất cho lần sửa này; ngữ cảnh chỉ dùng để thấu hiểu, không được mở rộng mục tiêu hay phạm vi):\n" + original
}

func (e *engine) recordStale(op controlOp) {
	rec := storepkg.DecisionRecord{Kind: "decision_stale", Decider: "engine", Input: op.text}
	if data, err := json.Marshal(op.facts); err == nil {
		rec.Facts = data
	}
	if _, err := e.store.Decisions.Append(rec); err != nil {
		slog.Warn("Ghi stale thất bại", "module", "engine", "err", err)
	}
}

// pauseWithNotify engine tự tạm dừng (ngắt mạch bế tắc/phán định thất bại abort): thông báo ra ngoài màn hình + đi theo lối tạm dừng hợp nhất của host
// (onPause → abortWithEvent: lifecycle=paused + sự kiện trên màn hình + cancel ctx).
func (e *engine) pauseWithNotify(kind, body string) {
	e.notify(kind, "warn", "ainovel: engine tạm dừng", body)
	if e.onPause != nil {
		e.onPause(body)
		return
	}
	e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: body, Level: "warn"})
	e.abort()
}

// completionSummary báo cáo kết thúc deterministic của hoàn sách, không tốn gọi LLM.
func completionSummary(progress domain.Progress, book domain.BookMetadata) string {
	var b strings.Builder
	fmt.Fprintf(&b, "《%s》 hoàn tất sáng tác: tổng cộng %d chương %d chữ", book.Title, len(progress.CompletedChapters), progress.TotalWordCount)
	return b.String()
}
