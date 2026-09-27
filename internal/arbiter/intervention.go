package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// InterventionFacts là gói sự kiện phân tuyến can thiệp (snapshot tại thời điểm Collect).
// Engine dùng Phase/QueueHead đối chiếu trước khi thực thi Dispatch ở biên (giữa tham vấn
// và thực thi là lúc worker chạy, sự kiện có thể đã tiến triển; không khớp → hủy và hỏi
// lại bằng sự kiện mới).
type InterventionFacts struct {
	Phase                    string           `json:"phase,omitempty"`
	Flow                     string           `json:"flow,omitempty"`
	Title                    string           `json:"title,omitempty"`
	CompletedChapters        int              `json:"completed_chapters"`
	OutlinedChapters         int              `json:"outlined_chapters,omitempty"`
	DynamicPlanning          bool             `json:"dynamic_planning"`
	NextChapter              int              `json:"next_chapter,omitempty"`
	PendingRewrites          []int            `json:"pending_rewrites,omitempty"`
	ReopenCount              int              `json:"reopen_count,omitempty"` // tổng số lần người dùng tường minh /reopen mở lại sách đã hoàn thành
	FoundationMissing        []string         `json:"foundation_missing,omitempty"`
	PlanningTier             string           `json:"planning_tier,omitempty"`
	AdvanceMode              string           `json:"advance_mode,omitempty"`
	HasAdvanceHold           bool             `json:"has_advance_hold"`
	AdvanceHoldAfter         string           `json:"advance_hold_after,omitempty"`
	AdvanceHoldTargetChapter int              `json:"advance_hold_target_chapter,omitempty"`
	AdvanceHoldReason        string           `json:"advance_hold_reason,omitempty"`
	Running                  bool             `json:"running"`                  // có run nào đang chạy hay không khi can thiệp đến
	CheckpointSeq            int64            `json:"checkpoint_seq,omitempty"` // checkpoint mới nhất tại thời điểm Collect; Engine dùng để đối chiếu
	RecentDecisions          []RecentDecision `json:"recent_decisions,omitempty"`
}

// RecentDecision là ký ức can thiệp: tóm tắt vài phán định gần nhất, phủ các tham chiếu
// xuyên can thiệp kiểu "lần trước sửa tới đâu rồi".
type RecentDecision struct {
	At     string `json:"at"`
	Input  string `json:"input"`
	Reason string `json:"reason,omitempty"`
}

// QueueHead trả về đầu hàng đợi viết lại (0 nếu không có), Engine dùng để đối chiếu.
func (f InterventionFacts) QueueHead() int {
	if len(f.PendingRewrites) > 0 {
		return f.PendingRewrites[0]
	}
	return 0
}

// CollectInterventionFacts đọc đủ sự kiện phân tuyến từ store. Mọi thất bại khi đọc sự
// kiện điều khiển đều trả lỗi tường minh, cấm Arbiter ra quyết định ngữ nghĩa trên
// snapshot rời rạc ghép từ giá trị zero.
func CollectInterventionFacts(st *storepkg.Store) (InterventionFacts, error) {
	var f InterventionFacts
	if st == nil {
		return f, fmt.Errorf("store không được để trống")
	}
	missing, err := st.FoundationMissing()
	if err != nil {
		return f, fmt.Errorf("đọc trạng thái thiết lập cơ bản: %w", err)
	}
	f.FoundationMissing = missing
	book, err := st.Book.Load()
	if err != nil {
		return f, fmt.Errorf("đọc thông tin tác phẩm: %w", err)
	}
	if book != nil {
		f.Title = book.Title
	}
	p, err := st.Progress.Load()
	if err != nil {
		return f, fmt.Errorf("đọc tiến độ: %w", err)
	}
	if p != nil {
		f.Phase = string(p.Phase)
		f.Flow = string(p.Flow)
		f.CompletedChapters = len(p.CompletedChapters)
		f.DynamicPlanning = p.Layered
		if p.Layered {
			outline, outlineErr := st.Outline.LoadOutline()
			if outlineErr != nil {
				return f, fmt.Errorf("đọc dàn ý chi tiết hiện tại: %w", outlineErr)
			}
			f.OutlinedChapters = len(outline)
		} else {
			f.OutlinedChapters = p.TotalChapters
		}
		f.NextChapter = p.NextChapter()
		f.PendingRewrites = append([]int(nil), p.PendingRewrites...)
		f.ReopenCount = p.ReopenCount
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		return f, fmt.Errorf("đọc metadata chạy: %w", err)
	}
	if meta != nil {
		f.PlanningTier = string(meta.PlanningTier)
		f.AdvanceMode = string(meta.AdvanceMode)
		if meta.AdvanceHold != nil {
			f.HasAdvanceHold = true
			f.AdvanceHoldAfter = string(meta.AdvanceHold.After)
			f.AdvanceHoldTargetChapter = meta.AdvanceHold.TargetChapter
			f.AdvanceHoldReason = meta.AdvanceHold.Reason
		}
	}
	if cp := st.Checkpoints.LatestGlobal(); cp != nil {
		f.CheckpointSeq = cp.Seq
	}
	recent, err := st.Decisions.Recent(5)
	if err != nil {
		return f, fmt.Errorf("đọc các phán định gần đây: %w", err)
	}
	for _, r := range recent {
		if r.Kind != "intervention" {
			continue
		}
		f.RecentDecisions = append(f.RecentDecisions, RecentDecision{
			At: r.At, Input: truncateRunes(r.Input, 80), Reason: r.Reason,
		})
	}
	return f, nil
}

// AdvanceHoldOp là hành động tạm dừng một lần: tạm dừng tại biên công việc, khi hàng viết
// lại đã cạn hoặc sau khi hoàn thành chương mục tiêu, cũng có thể hủy.
type AdvanceHoldOp struct {
	Cancel        bool                    `json:"cancel,omitempty"`
	After         domain.AdvanceHoldAfter `json:"after,omitempty"`
	TargetChapter int                     `json:"target_chapter,omitempty"`
	Reason        string                  `json:"reason,omitempty"`
}

// ReopenOp là viết lại sách đã hoàn thành: mở lại toàn bộ sách vào trạng thái viết lại và
// xếp các chương mục tiêu vào hàng (chỉ hợp lệ khi phase=complete).
type ReopenOp struct {
	Chapters []int  `json:"chapters"`
	Reason   string `json:"reason,omitempty"`
}

// InterventionDecision là phán định can thiệp. Tổ hợp hành động tự do, thứ tự thực thi do
// Engine cố định: answer → rules → hold → reopen → dispatch; nhiều nhất một dispatch
// (bảo đảm ở mức kiểu).
type InterventionDecision struct {
	Answer   string         `json:"answer,omitempty"`
	Rules    string         `json:"rules,omitempty"`
	Hold     *AdvanceHoldOp `json:"hold,omitempty"`
	Reopen   *ReopenOp      `json:"reopen,omitempty"`
	Dispatch *DispatchOp    `json:"dispatch,omitempty"`
	Reason   string         `json:"reason"`
}

var interventionContract = llmcontract.Contract{
	Name:        "arbiter_intervention",
	Description: "Phán định can thiệp của người dùng: trả lời, quy tắc, tạm dừng, mở lại và giao việc",
	Schema: schema.Object(
		schema.Property("answer", llmcontract.Nullable(schema.String("văn bản hiển thị lại cho người dùng; nếu không có thì là null"))).Required(),
		schema.Property("rules", llmcontract.Nullable(schema.String("nguyên văn quy tắc viết dài hạn cần ghi xuống; nếu không có thì là null"))).Required(),
		schema.Property("hold", llmcontract.Nullable(schema.Object(
			schema.Property("cancel", schema.Bool("có hủy tạm dừng một lần hiện có hay không")).Required(),
			schema.Property("after", llmcontract.Nullable(schema.Enum("điểm kích hoạt tạm dừng; là null khi hủy", string(domain.AdvanceHoldAtBoundary), string(domain.AdvanceHoldAfterRewritesDrained), string(domain.AdvanceHoldAtChapter)))).Required(),
			schema.Property("target_chapter", llmcontract.Nullable(schema.Int("chương mục tiêu khi after=chapter; trường hợp khác là null"))).Required(),
			schema.Property("reason", llmcontract.Nullable(schema.String("tóm tắt yêu cầu của người dùng; có thể là null khi hủy"))).Required(),
		))).Required(),
		schema.Property("reopen", llmcontract.Nullable(schema.Object(
			schema.Property("chapters", schema.Array("số chương cần mở lại", schema.Int("số chương"))).Required(),
			schema.Property("reason", llmcontract.Nullable(schema.String("lý do mở lại"))).Required(),
		))).Required(),
		schema.Property("dispatch", dispatchSchema("đích giao việc; là null khi không cần giao việc")).Required(),
		schema.Property("reason", schema.String("lý do phán định trong một câu")).Required(),
	),
}

// ValidateAgainst kiểm tra cơ học theo sự kiện (tính hợp lệ trong kịch bản; hệ thống kiểu
// đã loại hành động xuyên kịch bản).
func (d *InterventionDecision) ValidateAgainst(f InterventionFacts) error {
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason không được để trống")
	}
	if d.Answer == "" && d.Rules == "" && d.Hold == nil && d.Reopen == nil && d.Dispatch == nil {
		return fmt.Errorf("quyết định rỗng: cần ít nhất một hành động hoặc answer")
	}
	if err := d.Dispatch.validate(); err != nil {
		return err
	}
	if err := validateDispatchAgainst(d.Dispatch, f.Phase); err != nil {
		return err
	}
	complete := f.Phase == string(domain.PhaseComplete)
	if d.Reopen != nil {
		if !complete {
			return fmt.Errorf("reopen chỉ dành cho kỳ hoàn thành (phase hiện tại=%s)", f.Phase)
		}
		if len(d.Reopen.Chapters) == 0 {
			return fmt.Errorf("reopen.chapters không được để trống")
		}
		for _, ch := range d.Reopen.Chapters {
			if ch < 1 || ch > f.CompletedChapters {
				return fmt.Errorf("chương reopen %d vượt phạm vi (đã hoàn thành %d chương)", ch, f.CompletedChapters)
			}
		}
	}
	if complete && d.Dispatch != nil {
		return fmt.Errorf("cấm giao việc trực tiếp trong kỳ hoàn thành; viết lại dùng reopen (sau khi vào hàng sẽ do Router tự động giao)")
	}
	if d.Hold != nil && !d.Hold.Cancel {
		if f.Phase != string(domain.PhaseWriting) {
			return fmt.Errorf("tạm dừng một lần chỉ dành cho kỳ viết (phase hiện tại=%s)", f.Phase)
		}
		hold := domain.AdvanceHold{After: d.Hold.After, TargetChapter: d.Hold.TargetChapter, Reason: d.Hold.Reason}
		if err := hold.Validate(); err != nil {
			return fmt.Errorf("hold không hợp lệ: %w", err)
		}
		nextChapter := f.NextChapter
		if nextChapter == 0 {
			nextChapter = f.CompletedChapters + 1
		}
		if hold.After == domain.AdvanceHoldAtChapter && hold.TargetChapter < nextChapter {
			return fmt.Errorf("chương mục tiêu %d sớm hơn chương kế tiếp hiện tại %d", hold.TargetChapter, nextChapter)
		}
	}
	return nil
}

// validateDispatchAgainst hiện thực kỷ luật giai đoạn trong prompt thành phòng tuyến cơ học.
// Architect được bảo trì cấu trúc trong kỳ quy hoạch và kỳ viết; Writer/Editor chỉ được
// tiêu thụ sự kiện tác phẩm đã trọn vẹn và đã vào writing.
func validateDispatchAgainst(dispatch *DispatchOp, phase string) error {
	if dispatch == nil {
		return nil
	}
	if phase == "" {
		return fmt.Errorf("thiếu phase, cấm thực thi giao việc")
	}
	if phase == string(domain.PhaseComplete) {
		return fmt.Errorf("cấm giao việc trực tiếp trong kỳ hoàn thành")
	}
	switch dispatch.Agent {
	case "writer", "editor":
		if phase != string(domain.PhaseWriting) {
			return fmt.Errorf("%s chỉ được giao ở giai đoạn writing (phase hiện tại=%s)", dispatch.Agent, phase)
		}
	}
	return nil
}

// DecideIntervention phân tuyến can thiệp. Ngữ nghĩa thất bại: trả về error → bên gọi
// hiển thị lại tường minh nguyên nhân thất bại thật, và không tạo bất kỳ ghi nào (thà
// không động, chứ không được động nhầm).
func DecideIntervention(ctx context.Context, model agentcore.ChatModel, systemPrompt string, facts InterventionFacts, text string) (InterventionDecision, error) {
	payload, err := marshalPayload(struct {
		Intervention string            `json:"intervention"`
		Facts        InterventionFacts `json:"facts"`
	}{Intervention: text, Facts: facts})
	if err != nil {
		return InterventionDecision{}, err
	}
	return decide(ctx, model, interventionContract, systemPrompt, payload, func(d *InterventionDecision) error {
		return d.ValidateAgainst(facts)
	})
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
