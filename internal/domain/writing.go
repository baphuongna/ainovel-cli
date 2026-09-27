package domain

import "strings"

// ChapterPlan ý tưởng viết chương, Writer tự sinh.
// Không còn ép tách cảnh, Agent tự quyết định cách tổ chức nội dung.
type ChapterPlan struct {
	Chapter    int             `json:"chapter"`
	Title      string          `json:"title"`
	Goal       string          `json:"goal"`
	Conflict   string          `json:"conflict"`
	Hook       string          `json:"hook"`
	EmotionArc string          `json:"emotion_arc,omitempty"`
	Notes      string          `json:"notes,omitempty"` // ghi chú tự do của Agent
	Contract   ChapterContract `json:"contract,omitempty"`
}

// ChapterContract là hợp đồng nghiệm thu chương mà Writer và Editor dùng chung.
// Nó định nghĩa các mục đẩy chuyện bắt buộc phải hoàn thành của chương, các mục cấm vượt
// ranh giới và các điểm cần lưu ý khi xem xét.
type ChapterContract struct {
	RequiredBeats    []string `json:"required_beats,omitempty"`    // các mục đẩy chuyện bắt buộc phải có trong chương này
	ForbiddenMoves   []string `json:"forbidden_moves,omitempty"`   // các nước đi rõ ràng không được xảy ra trong chương này
	ContinuityChecks []string `json:"continuity_checks,omitempty"` // các điểm liên tục cần đối chiếu đặc biệt trong chương này
	EvaluationFocus  []string `json:"evaluation_focus,omitempty"`  // các điểm Editor cần kiểm tra trọng tâm
	EmotionTarget    string   `json:"emotion_target,omitempty"`    // tùy chọn: cảm xúc chính muốn độc giả cảm nhận ở chương này
	PayoffPoints     []string `json:"payoff_points,omitempty"`     // tùy chọn: các điểm sự kiện/điểm chi trả muốn hồi ứng ở chương trọng yếu
	HookGoal         string   `json:"hook_goal,omitempty"`         // tùy chọn: ham muốn đọc tiếp mà móc cuối chương muốn dẫn dắt
}

// ChapterSummary tóm tắt chương, dùng cho cửa sổ ngữ cảnh của các chương sau.
type ChapterSummary struct {
	Chapter    int      `json:"chapter"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Characters []string `json:"characters"`
	KeyEvents  []string `json:"key_events"`
}

// ArcSummary tóm tắt cấp cung, Editor sinh khi kết thúc cung.
type ArcSummary struct {
	Volume    int      `json:"volume"`
	Arc       int      `json:"arc"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	KeyEvents []string `json:"key_events"`
}

// VolumeSummary tóm tắt cấp tập, sinh khi kết thúc tập.
type VolumeSummary struct {
	Volume    int      `json:"volume"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	KeyEvents []string `json:"key_events"`
}

// CharacterSnapshot snapshot trạng thái nhân vật, ghi tại ranh giới cung.
type CharacterSnapshot struct {
	Volume     int    `json:"volume"`
	Arc        int    `json:"arc"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Power      string `json:"power,omitempty"`
	Motivation string `json:"motivation"`
	Relations  string `json:"relations,omitempty"`
}

// OutlineFeedback phản hồi của Writer cho dàn ý, tùy chọn khi nộp chương.
type OutlineFeedback struct {
	Deviation  string `json:"deviation"`  // mô tả sự lệch khỏi dàn ý
	Suggestion string `json:"suggestion"` // góp ý điều chỉnh
}

// WritingStyleRules quy tắc viết chưng cất từ các chương đã viết, Editor sinh tại ranh giới cung.
// Thay thế các đoạn văn gốc (style_anchors / voice_samples), dùng quy tắc thay cho việc chép nguyên văn.
type WritingStyleRules struct {
	Volume    int              `json:"volume"`
	Arc       int              `json:"arc"`
	Prose     []string         `json:"prose"`      // 3-5 quy tắc phong cách tự sự, mỗi quy tắc ≤50 ký tự
	Dialogue  []CharacterVoice `json:"dialogue"`   // quy tắc phong cách thoại của nhân vật
	Taboos    []string         `json:"taboos"`     // danh sách điều cấm
	UpdatedAt string           `json:"updated_at"` // dấu thời gian ISO8601
}

// CharacterVoice quy tắc phong cách thoại của một nhân vật.
type CharacterVoice struct {
	Name  string   `json:"name"`
	Rules []string `json:"rules"` // 2-3 quy tắc đặc trưng ngôn ngữ, mỗi quy tắc ≤30 ký tự
}

// RelatedChapter chương liên quan được khuyến nghị đọc lại.
type RelatedChapter struct {
	Chapter int    `json:"chapter"`
	Reason  string `json:"reason"`
}

// RecallItem là thông tin dài hạn được triệu hồi chọn lọc theo nhiệm vụ hiện tại.
// Nó không thay thế công cụ chính thức, chỉ chịu trách nhiệm đưa lại cho model một lượng nhỏ
// thông tin lịch sử thực sự liên quan đến lượt hiện tại.
type RecallItem struct {
	Kind    string `json:"kind"`
	Key     string `json:"key,omitempty"`
	Chapter int    `json:"chapter,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// CommitResult là giá trị trả về có cấu trúc của công cụ commit_chapter.
// Chỉ gồm các trường sự kiện; "bước kế tiếp làm gì" do kênh Reminder tự sinh dựa trên Progress hiện tại.
type CommitResult struct {
	Chapter        int              `json:"chapter"`
	Committed      bool             `json:"committed"`
	WordCount      int              `json:"word_count"`
	NextChapter    int              `json:"next_chapter"`
	ReviewRequired bool             `json:"review_required"`
	ReviewReason   string           `json:"review_reason,omitempty"`
	HookType       string           `json:"hook_type,omitempty"`
	DominantStrand string           `json:"dominant_strand,omitempty"`
	Feedback       *OutlineFeedback `json:"feedback,omitempty"`
	// Tín hiệu phân tầng truyện dài
	ArcEnd         bool `json:"arc_end,omitempty"`
	VolumeEnd      bool `json:"volume_end,omitempty"`
	Volume         int  `json:"volume,omitempty"`
	Arc            int  `json:"arc,omitempty"`
	NeedsExpansion bool `json:"needs_expansion,omitempty"`  // cung kế tiếp là khung xương, cần mở rộng chương
	NeedsNewVolume bool `json:"needs_new_volume,omitempty"` // cần Architect dựng tập kế tiếp
	NextVolume     int  `json:"next_volume,omitempty"`      // số thứ tự tập/cung kế tiếp
	NextArc        int  `json:"next_arc,omitempty"`         // số thứ tự cung kế tiếp
	// Sự kiện trạng thái hoàn tất: sau lần commit này toàn sách đã hoàn thành hay chưa
	BookComplete bool `json:"book_complete,omitempty"`
	// Snapshot Progress.Flow hiện tại (writing / reviewing / rewriting / polishing)
	Flow string `json:"flow,omitempty"`
}

// HasDirective xác định bản quy hoạch chương này có thực sự nói rõ "nên viết ra đằng nào" không.
//
// Quy hoạch rỗng tương đương không có quy hoạch: khi viết lại chỉ liệt kê khuyết điểm mà không
// cho hướng, Writer nhận được vẫn chỉ là câu "viết lại chương N" — thực đo Editor phán đây là
// vấn đề nghiêm trọng nhất trong hàng đợi.
func (p ChapterPlan) HasDirective() bool {
	return strings.TrimSpace(p.Goal) != "" ||
		len(p.Contract.RequiredBeats) > 0 ||
		len(p.Contract.PayoffPoints) > 0 ||
		len(p.Contract.ContinuityChecks) > 0
}
