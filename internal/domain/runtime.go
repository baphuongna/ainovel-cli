package domain

import (
	"fmt"
	"strings"
)

// Phase biểu thị giai đoạn sáng tác tiểu thuyết.
type Phase string

const (
	PhaseInit     Phase = "init"
	PhasePremise  Phase = "premise"
	PhaseOutline  Phase = "outline"
	PhaseWriting  Phase = "writing"
	PhaseComplete Phase = "complete"
)

// FlowState loại luồng đang hoạt động, dùng cho khôi phục checkpoint.
type FlowState string

const (
	FlowWriting   FlowState = "writing"
	FlowReviewing FlowState = "reviewing"
	FlowRewriting FlowState = "rewriting"
	FlowPolishing FlowState = "polishing"
	FlowSteering  FlowState = "steering"
)

// PlanningTier biểu thị cấp độ độ dài quy hoạch tác phẩm.
type PlanningTier string

const (
	PlanningTierShort PlanningTier = "short"
	PlanningTierMid   PlanningTier = "mid"
	PlanningTierLong  PlanningTier = "long"
)

// Progress theo dõi tiến độ, lưu bền xuống meta/progress.json.
type Progress struct {
	Phase          Phase `json:"phase"`
	CurrentChapter int   `json:"current_chapter"`
	// TotalChapters ở chế độ không phân tầng là số chương của dàn ý chi tiết; ở chế độ phân tầng
	// chỉ là giá trị dung lượng nội bộ gồm cả ước tính khung xương, dùng cho chiến lược ngữ cảnh,
	// không đại diện tổng số chương cố định của toàn sách.
	TotalChapters     int         `json:"total_chapters"`
	CompletedChapters []int       `json:"completed_chapters"`
	TotalWordCount    int         `json:"total_word_count"`
	ChapterWordCounts map[int]int `json:"chapter_word_counts,omitempty"` // số ký tự từng chương, hỗ trợ sửa tổng số ký tự khi viết lại
	InProgressChapter int         `json:"in_progress_chapter,omitempty"` // chương đang được viết (khôi phục cấp cảnh)
	CompletedScenes   []int       `json:"completed_scenes,omitempty"`    // số thứ tự cảnh đã hoàn thành của chương hiện tại
	Flow              FlowState   `json:"flow,omitempty"`                // luồng hiện tại
	PendingRewrites   []int       `json:"pending_rewrites,omitempty"`    // hàng đợi chương chờ viết lại
	RewriteReason     string      `json:"rewrite_reason,omitempty"`      // lý do viết lại
	StrandHistory     []string    `json:"strand_history,omitempty"`      // ghi lại dominant_strand theo thứ tự chương
	HookHistory       []string    `json:"hook_history,omitempty"`        // ghi lại hook_type theo thứ tự chương
	// Theo dõi phân tầng truyện dài (chỉ chế độ truyện dài dùng, truyện ngắn/vừa là giá trị rỗng)
	CurrentVolume int  `json:"current_volume,omitempty"`
	CurrentArc    int  `json:"current_arc,omitempty"`
	Layered       bool `json:"layered,omitempty"`
	// ReopenedFromComplete đánh dấu sách này được mở lại từ trạng thái hoàn tất qua reopen để
	// vào viết lại. Viết lại chỉ sửa các chương đã có, không thêm bớt cấu trúc, nên sau khi cạn
	// hàng đợi phải buông theo "cấu trúc đầy đủ tức hoàn tất lại" (tránh phục bút cuối tập bị
	// nhiễu động bởi viết lại rồi kẹt ở writing → vòng lặp chết viết tiếp vượt giới); viết xuôi
	// không đặt cờ này, phán xét hoàn tất giữ nguyên ngữ nghĩa thận trọng là thu dây xong hết.
	ReopenedFromComplete bool `json:"reopened_from_complete,omitempty"`
	// ReopenCount ghi số lần tích lũy sách này được mở lại từ trạng thái hoàn tất (sự kiện kiểm
	// toán /reopen). Nó đồng thời bảo đảm lần hoàn tất sau khi mở lại khác nội dung progress.json
	// với lần hoàn tất trước: checkpoint khử trùng lặp theo digest, hoàn tất lặp lại có byte
	// giống nhau sẽ không sinh checkpoint mới, StopGuard sẽ phán nhầm complete_book thành công
	// là quay vòng chỗ cũ và leo thang chấm dứt.
	ReopenCount int `json:"reopen_count,omitempty"`
}

// IsResumable xác định có thể khôi phục từ checkpoint hay không.
func (p *Progress) IsResumable() bool {
	return p.Phase == PhaseWriting && p.CurrentChapter > 0
}

// NextChapter trả về số chương kế tiếp cần viết.
func (p *Progress) NextChapter() int {
	return p.LatestCompleted() + 1
}

// LatestCompleted trả về số chương đã hoàn thành lớn nhất; trả về 0 khi chưa có chương nào.
func (p *Progress) LatestCompleted() int {
	max := 0
	for _, ch := range p.CompletedChapters {
		if ch > max {
			max = ch
		}
	}
	return max
}

// ContextProfile chiến lược nạp ngữ cảnh, tự thích ứng theo tổng số chương.
type ContextProfile struct {
	SummaryWindow  int  // nạp tóm tắt N chương gần nhất
	TimelineWindow int  // nạp dòng thời gian N chương gần nhất
	Layered        bool // true = bật nạp tóm tắt phân tầng (tóm tắt tập + tóm tắt cung + tóm tắt chương)
}

// MemoryPolicy biểu thị chiến lược dùng bộ nhớ dùng chung lúc chạy.
// Nó vừa dùng cho đầu ra ngữ cảnh, vừa dùng cho quyết định handoff / reminder của tầng host.
type MemoryPolicy struct {
	Mode                string `json:"mode,omitempty"`
	SummaryWindow       int    `json:"summary_window,omitempty"`
	TimelineWindow      int    `json:"timeline_window,omitempty"`
	LayeredSummaries    bool   `json:"layered_summaries,omitempty"`
	SummaryStrategy     string `json:"summary_strategy,omitempty"`
	WorkingRefresh      string `json:"working_refresh,omitempty"`
	EpisodicRefresh     string `json:"episodic_refresh,omitempty"`
	PlanningRefresh     string `json:"planning_refresh,omitempty"`
	FoundationRefresh   string `json:"foundation_refresh,omitempty"`
	PlanningFocus       string `json:"planning_focus,omitempty"`
	FoundationFocus     string `json:"foundation_focus,omitempty"`
	PreviousTailChars   int    `json:"previous_tail_chars,omitempty"`
	ChapterPlanEnabled  bool   `json:"chapter_plan_enabled,omitempty"`
	RelatedLookup       bool   `json:"related_chapter_lookup,omitempty"`
	CurrentOutlineBound bool   `json:"current_outline_bound,omitempty"`
	HandoffPreferred    bool   `json:"handoff_preferred,omitempty"`
	ReadOnlyThreshold   int    `json:"read_only_threshold,omitempty"`
}

// NewContextProfile tính chiến lược ngữ cảnh theo tổng số chương.
func NewContextProfile(totalChapters int) ContextProfile {
	switch {
	case totalChapters <= 15:
		return ContextProfile{SummaryWindow: 10, TimelineWindow: 10}
	case totalChapters <= 50:
		return ContextProfile{SummaryWindow: 5, TimelineWindow: 8}
	default:
		return ContextProfile{SummaryWindow: 3, TimelineWindow: 5, Layered: true}
	}
}

// NewChapterMemoryPolicy sinh chiến lược bộ nhớ runtime cấp chương theo tiến độ và chiến lược ngữ cảnh.
func NewChapterMemoryPolicy(progress *Progress, profile ContextProfile, currentOutlineBound bool) MemoryPolicy {
	policy := MemoryPolicy{
		Mode:                "chapter",
		SummaryWindow:       profile.SummaryWindow,
		TimelineWindow:      profile.TimelineWindow,
		LayeredSummaries:    profile.Layered,
		WorkingRefresh:      "làm mới mỗi lần nạp theo chương",
		EpisodicRefresh:     "làm mới khi nộp chương, xem xét và thay đổi trạng thái truyện dài",
		PreviousTailChars:   800,
		ChapterPlanEnabled:  true,
		CurrentOutlineBound: currentOutlineBound,
		ReadOnlyThreshold:   5,
	}
	if profile.Layered {
		policy.SummaryStrategy = "tóm tắt tập + tóm tắt cung + tóm tắt chương gần nhất"
	} else {
		policy.SummaryStrategy = "tóm tắt chương gần nhất"
	}
	if progress != nil {
		if progress.TotalChapters > 30 {
			policy.RelatedLookup = true
		}
		if progress.Flow == FlowReviewing || progress.Flow == FlowRewriting || progress.Flow == FlowPolishing {
			policy.HandoffPreferred = true
		}
		if progress.Layered && len(progress.CompletedChapters) >= 6 {
			policy.HandoffPreferred = true
		}
		if len(progress.CompletedChapters) >= 12 {
			policy.HandoffPreferred = true
		}
		if progress.Layered && len(progress.CompletedChapters) >= 6 {
			policy.ReadOnlyThreshold = 4
		}
		if len(progress.CompletedChapters) >= 12 {
			policy.ReadOnlyThreshold = 4
		}
	}
	return policy
}

// NewArchitectMemoryPolicy trả về chiến lược bộ nhớ dùng ở giai đoạn quy hoạch.
func NewArchitectMemoryPolicy() MemoryPolicy {
	return MemoryPolicy{
		Mode:               "architect",
		PlanningRefresh:    "làm mới khi cấu trúc tập/cung, la bàn hoặc tóm tắt cập nhật",
		FoundationRefresh:  "làm mới khi nhân vật, phục bút, thiết lập thay đổi",
		PlanningFocus:      "dàn ý phân tầng, la bàn, tóm tắt tập",
		FoundationFocus:    "thiết lập nhân vật, snapshot nhân vật, sổ theo dõi phục bút",
		HandoffPreferred:   true,
		ChapterPlanEnabled: false,
		ReadOnlyThreshold:  4,
	}
}

// RunMeta thông tin runtime, lưu bền xuống meta/run.json.
type RunMeta struct {
	StartedAt            string             `json:"started_at"`
	Provider             string             `json:"provider,omitempty"`
	Style                string             `json:"style"`
	Model                string             `json:"model"`
	PlanningTier         PlanningTier       `json:"planning_tier,omitempty"`
	StartPrompt          string             `json:"start_prompt,omitempty"`           // nhu cầu sáng tác gốc của người dùng (sự kiện đầu vào, lưu trước phán định khởi động; nếu phán định thất bại thì phán bổ sung theo đây)
	PlanStart            *PlanStartRecord   `json:"plan_start,omitempty"`             // sự kiện phán định khởi động, căn cứ duy nhất để khôi phục crash trong kỳ quy hoạch
	PendingSteer         string             `json:"pending_steer,omitempty"`          // lệnh Steer chưa hoàn thành, tiêm lại khi khôi phục gián đoạn
	AdvanceMode          ChapterAdvanceMode `json:"advance_mode"`                     // chế độ đẩy chương: auto / review
	AdvancePermitChapter int                `json:"advance_permit_chapter,omitempty"` // chương tiến xuôi được cấp phép một lần ở chế độ review
	AdvanceHold          *AdvanceHold       `json:"advance_hold,omitempty"`           // ý định tạm dừng một lần do can thiệp hiện tại ký
}

// ChapterAdvanceMode quyết định chương mới có cần cấp phép từng chương hay không.
type ChapterAdvanceMode string

const (
	ChapterAdvanceAuto   ChapterAdvanceMode = "auto"
	ChapterAdvanceReview ChapterAdvanceMode = "review"
)

// Valid báo cáo chế độ đẩy chương có được phiên bản hiện tại hỗ trợ không.
func (m ChapterAdvanceMode) Valid() bool {
	return m == ChapterAdvanceAuto || m == ChapterAdvanceReview
}

// UnsupportedAdvanceModeError biểu thị chế độ điều khiển của sách không được binary hiện tại hỗ trợ.
// Bên gọi phải dừng dựng Host ghi được, và nhắc người dùng dùng phiên bản khớp; cấm đoán mù hạ cấp.
type UnsupportedAdvanceModeError struct {
	Mode ChapterAdvanceMode
}

func (e *UnsupportedAdvanceModeError) Error() string {
	return fmt.Sprintf("chế độ đẩy chương không được hỗ trợ %q, hãy dùng phiên bản ainovel mới đã tạo dự án này", e.Mode)
}

// AdvanceHoldAfter là điều kiện kích hoạt tất định cho tạm dừng một lần.
type AdvanceHoldAfter string

const (
	AdvanceHoldAtBoundary           AdvanceHoldAfter = "boundary"
	AdvanceHoldAfterRewritesDrained AdvanceHoldAfter = "rewrites_drained"
	AdvanceHoldAtChapter            AdvanceHoldAfter = "chapter"
)

// Valid báo cáo điều kiện tạm dừng có được phiên bản hiện tại hỗ trợ không.
func (a AdvanceHoldAfter) Valid() bool {
	return a == AdvanceHoldAtBoundary || a == AdvanceHoldAfterRewritesDrained || a == AdvanceHoldAtChapter
}

// AdvanceHold là ý định tạm dừng một lần do can thiệp hiện tại ký, được Host tiêu thụ tại ranh giới.
type AdvanceHold struct {
	After         AdvanceHoldAfter `json:"after"`
	TargetChapter int              `json:"target_chapter,omitempty"`
	Reason        string           `json:"reason"`
}

// Validate kiểm tra ràng buộc cấu trúc tự thân của ý định tạm dừng một lần.
func (h AdvanceHold) Validate() error {
	if !h.After.Valid() {
		return fmt.Errorf("điều kiện tạm dừng một lần không được hỗ trợ %q", h.After)
	}
	if h.After == AdvanceHoldAtChapter {
		if h.TargetChapter <= 0 {
			return fmt.Errorf("chương mục tiêu phải lớn hơn 0")
		}
	} else if h.TargetChapter != 0 {
		return fmt.Errorf("điều kiện tạm dừng %q không thể đặt chương mục tiêu", h.After)
	}
	if strings.TrimSpace(h.Reason) == "" {
		return fmt.Errorf("lý do tạm dừng một lần không được để trống")
	}
	return nil
}

// PlanStartRecord sự kiện lưu bền của phán định khởi động (phán định lưu sự kiện trước, rồi mới
// chạy; khôi phục không phán định lại). Sau lần save_foundation đầu tiên lưu scale xuống đĩa,
// khôi phục kỳ quy hoạch đổi thành suy ra từ PlanningTier, bản ghi này chỉ phủ cửa sổ "từ khi
// phán định xong đến lần lưu đầu tiên". DecisionID liên kết kiểm toán decisions.jsonl.
type PlanStartRecord struct {
	RawPrompt   string `json:"raw_prompt"`
	Planner     string `json:"planner"`
	PlannerTask string `json:"planner_task"`
	DecisionID  string `json:"decision_id,omitempty"`
}
