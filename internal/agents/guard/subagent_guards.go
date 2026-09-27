package guard

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

// subagentMaxConsecutiveBlocks sau N lần chặn liên tiếp thì nâng cấp thành dừng, tránh model yếu loop vô hạn.
const subagentMaxConsecutiveBlocks = 3

// BlockHook là callback audit của StopGuard: gọi đồng bộ mỗi khi chặn/nâng cấp. Host dùng nó
// đưa sự kiện chặn lên TUI event stream và thông báo ngoài màn hình — nếu không
// chặn chỉ vào log, người dùng chỉ thấy "đơ + token tăng nhanh", không biết hệ thống đang tự chữa hay chạy không (issue #75).
// Callback không tham gia quyết định guard. reason nhận giá trị:
//   - "blocked"    đã tiêm message thúc, model sẽ tiếp tục đẩy
//   - "escalated"  chạy không vượt giới hạn, run vòng này kết thúc giao về layer trên
//   - "hard_stop"  provider từ chối (safety/content_filter), dừng ngay
type BlockHook func(agent, reason string, consecutive int32)

// hardStopReasons là các nguyên nhân từ chối phía provider không thể khôi phục bằng message thúc.
// Tiêm "phải commit" với chúng vô ích, ngược lại mỗi lần tạo một lần gọi LLM đầy đủ
// rồi cuối cùng escalate khiến Engine chạy lại toàn bộ task Worker, chồng chất lãng phí nhiều lần
// (thực nghiệm ch02 gặp safety một chương tạo 3 lần phái lại 17 lần gọi LLM, tỷ lệ trúng
// từ 50% xuống 2.8%).
//
// Lưu ý StopReasonError / StopReasonAborted không cần liệt kê: agentcore trong
// loop.go khi nhận hai stop reason này trực tiếp kết thúc run, không gọi StopGuard.
// Ở đây chỉ liệt kê ngữ nghĩa từ chối provider thực sự đi qua StopGuard.
var hardStopReasons = map[agentcore.StopReason]struct{}{
	"safety":         {},
	"content_filter": {},
}

// newCheckpointDeltaGuard tạo một StopGuard:
// sau baseline nếu không xuất hiện checkpoint của step chỉ định, thì từ chối end_turn.
// baseline do phía gọi bắt ở thời điểm factory, đảm bảo ngữ nghĩa per-run đúng.
//
// blockMsg nhận tập step checkpoint đã quan sát được sau baseline, lắp ráp
// message thúc theo tiến độ thực tế — message tĩnh trong cảnh "tool bắt buộc cứ liên tục lỗi" là gây hiểu nhầm
// (thúc model gọi một tool đang thất bại, xem #75).
//
// Ngữ nghĩa đếm là "có tiến bộ thì reset": giữa hai lần chặn nếu xuất hiện
// checkpoint mới (draft lại / check lại...) coi như model đang đẩy, consecutive về 0;
// chỉ chạy không liên tục không sản phẩm mới tích lũy rồi nâng cấp dừng.
func newCheckpointDeltaGuard(st *store.Store, agentName string, requiredSteps []string, blockMsg func(seen map[string]struct{}) string, onBlock BlockHook) agentcore.StopGuard {
	var baseline int64
	if cp := st.Checkpoints.LatestGlobal(); cp != nil {
		baseline = cp.Seq
	}
	need := make(map[string]struct{}, len(requiredSteps))
	for _, s := range requiredSteps {
		need[s] = struct{}{}
	}
	var consecutive atomic.Int32
	var lastBlockSeq atomic.Int64 // Seq checkpoint mới nhất quan sát được khi chặn lần trước; -1 nghĩa là chưa chặn lần nào
	lastBlockSeq.Store(-1)
	return func(_ context.Context, info agentcore.StopInfo) agentcore.StopDecision {
		// Lỗi không thể khôi phục: nâng cấp ngay, không lãng phí một lần thúc.
		if _, hard := hardStopReasons[info.Message.StopReason]; hard {
			slog.Error("subagent stop_guard phát hiện dừng không thể khôi phục, nâng cấp ngay",
				"module", "agent.guard", "agent", agentName,
				"turn", info.TurnIndex, "stop_reason", info.Message.StopReason)
			if onBlock != nil {
				onBlock(agentName, "hard_stop", consecutive.Load())
			}
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		// Quét ngược checkpoint sau baseline, thu thập step đã xuất hiện (dùng chung cho quyết định cho qua + message tiến độ).
		// checkpoint mới ở đuôi, gặp <= baseline là break.
		all := st.Checkpoints.All()
		latestSeq := baseline
		seen := make(map[string]struct{})
		for i := len(all) - 1; i >= 0; i-- {
			cp := all[i]
			if cp.Seq <= baseline {
				break
			}
			if cp.Seq > latestSeq {
				latestSeq = cp.Seq
			}
			seen[cp.Step] = struct{}{}
		}
		for s := range need {
			if _, ok := seen[s]; ok {
				consecutive.Store(0)
				return agentcore.StopDecision{Allow: true}
			}
		}
		// Từ lần chặn trước đến giờ có artifact mới lưu = model đang đẩy (như draft lại sau khi thúc rồi thử lại kết thúc),
		// reset đếm; nâng cấp chỉ phạt chạy không không tiến bộ, chứ không phải gom chặn cả run rồi bỏ.
		if prev := lastBlockSeq.Load(); prev >= 0 && latestSeq > prev {
			consecutive.Store(0)
		}
		lastBlockSeq.Store(latestSeq)
		n := consecutive.Add(1)
		if n > subagentMaxConsecutiveBlocks {
			slog.Error("subagent stop_guard chặn liên tiếp vượt giới hạn, nâng cấp thành dừng",
				"module", "agent.guard", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
			if onBlock != nil {
				onBlock(agentName, "escalated", n)
			}
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		slog.Warn("subagent stop_guard chặn end_turn",
			"module", "agent.guard", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
		if onBlock != nil {
			onBlock(agentName, "blocked", n)
		}
		return agentcore.StopDecision{Allow: false, InjectMessage: blockMsg(seen)}
	}
}

// staticBlockMsg chuyển văn bản cố định thành signature blockMsg (sản phẩm của architect/editor là lưu đơn tool,
// không có tiến độ nhiều bước, message tĩnh là đủ).
func staticBlockMsg(msg string) func(map[string]struct{}) string {
	return func(map[string]struct{}) string { return msg }
}

// NewWriterStopGuard yêu cầu writer vòng này tạo ít nhất một commit_chapter thành công.
// Message thúc lắp ráp theo tiến độ step đã lưu: writer là subagent duy nhất có chuỗi tool nhiều bước,
// message tĩnh "phải gọi commit_chapter" khi bước trước thiếu hoặc commit lỗi sẽ gây hiểu nhầm.
func NewWriterStopGuard(st *store.Store, onBlock BlockHook) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "writer", []string{"commit"}, writerBlockMsg, onBlock)
}

// writerBlockMsg theo checkpoint step đã xuất hiện trong vòng này để xác định writer bị kẹt ở bước nào.
// Tên step tương ứng với giá trị lưu của từng tool: plan / draft / edit / consistency_check / commit.
func writerBlockMsg(seen map[string]struct{}) string {
	_, hasDraft := seen["draft"]
	_, hasEdit := seen["edit"]
	_, hasCheck := seen["consistency_check"]
	switch {
	case !hasDraft && !hasEdit:
		return "Cấm kết thúc: vòng này chưa lưu văn bản chính. Hoàn thành chương theo thứ tự plan_chapter → draft_chapter → check_consistency → commit_chapter; văn bản chỉ xuất trong chat là mất, phải lưu qua tool rồi nộp."
	case !hasCheck:
		return "Cấm kết thúc: văn bản đã lưu nhưng chưa kết thúc. Gọi check_consistency trước kiểm tra nhất quán, rồi gọi commit_chapter nộp chương. draft_chapter / edit_chapter chỉ lưu bản nháp, không tính hoàn thành."
	default:
		return "Cấm kết thúc: chương này chỉ thiếu commit_chapter nộp. Gọi commit_chapter ngay; nếu nó trả lỗi, xử lý theo thông báo lỗi (kiểm tra số chương, bổ sung hành động trước theo gợi ý) rồi thử nộp lại, không kết thúc khi chưa nộp."
	}
}

// NewArchitectStopGuard yêu cầu architect vòng này lưu ít nhất một sản phẩm quy hoạch.
func NewArchitectStopGuard(st *store.Store, onBlock BlockHook) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "architect",
		[]string{
			"book", "premise", "outline", "layered_outline", "characters", "world_rules",
			"foundation_audit", "expand_arc", "append_volume", "update_compass", "complete_book", "revise_outline", "resolve_outline_feedback", "plan",
		},
		staticBlockMsg("Bạn phải gọi save_book, save_foundation, revise_outline, resolve_outline_feedback, audit_foundation hoặc plan_chapter (viết chỉ thị viết lại cho chương trong hàng đợi viết lại) để lưu sản phẩm rồi mới được kết thúc. Chỉ xuất văn bản Markdown/JSON trong chat là mất."),
		onBlock,
	)
}

// NewEditorStopGuard yêu cầu editor vòng này lưu sản phẩm khớp "nhiệm vụ" mới được kết thúc.
//
// Nhận biết nhiệm vụ: khi được phái tạo tóm tắt, chỉ save_review (kiểm duyệt) không tính hoàn thành — phải tạo tóm tắt tương ứng.
// Nếu không, editor "được phái tạo tóm tắt cung nhưng kiểm duyệt trước" sẽ thỏa tiêu chí cũ lỏng rồi kết thúc sớm, tóm tắt cung không bao giờ lưu
// (phối hợp với dispatcher bỏ qua trùng từng khiến arc xương sống trong tập chết loop, xem outline-exhaustion-livelock).
// exit tool đã đóng băng cũng hỏi StopGuard (test hợp đồng TestContract_TerminalToolExitConsultsStopGuard),
// nên save_review hard-stop trong build.go là an toàn: trong nhiệm vụ tóm tắt, khi editor kiểm duyệt trước, guard này sẽ
// bác exit đó và thúc, cho đến khi tóm tắt tương ứng được lưu.
func NewEditorStopGuard(st *store.Store, task string, onBlock BlockHook) agentcore.StopGuard {
	switch {
	case strings.Contains(task, "save_volume_summary") || strings.Contains(task, "tóm tắt tập"):
		return newCheckpointDeltaGuard(st, "editor", []string{"volume_summary"},
			staticBlockMsg("Nhiệm vụ lần này là tạo tóm tắt tập: bạn phải gọi save_volume_summary lưu rồi mới được kết thúc, save_review không tính hoàn thành. "), onBlock)
	case strings.Contains(task, "save_arc_summary") || strings.Contains(task, "tóm tắt cung"):
		return newCheckpointDeltaGuard(st, "editor", []string{"arc_summary"},
			staticBlockMsg("Nhiệm vụ lần này là tạo tóm tắt cung: bạn phải gọi save_arc_summary lưu rồi mới được kết thúc, save_review không tính hoàn thành. "), onBlock)
	default:
		// Nhiệm vụ xem xét hoặc tạm thời: lưu bất kỳ xem xét/tóm tắt nào là được (giữ hành vi lỏng hiện có).
		return newCheckpointDeltaGuard(st, "editor",
			[]string{"review", "arc_summary", "volume_summary"},
			staticBlockMsg("Bạn phải gọi save_review / save_arc_summary / save_volume_summary để lưu kết quả rồi mới được kết thúc."), onBlock)
	}
}
