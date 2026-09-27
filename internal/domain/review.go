package domain

// TimelineEvent sự kiện dòng thời gian.
type TimelineEvent struct {
	Chapter    int      `json:"chapter"`
	Time       string   `json:"time"`
	Event      string   `json:"event"`
	Characters []string `json:"characters,omitempty"`
}

// ForeshadowEntry một mục phục bút.
type ForeshadowEntry struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	PlantedAt   int    `json:"planted_at"`
	Status      string `json:"status"` // planted / advanced / resolved
	ResolvedAt  int    `json:"resolved_at,omitempty"`
}

// ForeshadowUpdate thao tác tăng lượng trên phục bút.
type ForeshadowUpdate struct {
	ID          string `json:"id"`
	Action      string `json:"action"` // plant / advance / resolve
	Description string `json:"description,omitempty"`
}

// RestoreOwnPlants đưa các plant phục bút mà bản cũ gieo trong chương này nhưng bản mới
// không khai báo lại lên đầu hàng đợi. Một chương đã gieo những phục bút nào là sự kiện
// lịch sử của chính nó, viết lại chính văn không thay đổi điều đó; đánh mất nó thì khi phát
// lại toàn bộ bản ghi chương, advance/resolve của chương này và các chương sau sẽ không tìm
// thấy plant tiền đề, cả chuỗi báo lỗi.
func RestoreOwnPlants(prev, next []ForeshadowUpdate) []ForeshadowUpdate {
	declared := make(map[string]struct{}, len(next))
	for _, u := range next {
		if u.Action == "plant" {
			declared[u.ID] = struct{}{}
		}
	}
	var restored []ForeshadowUpdate
	for _, u := range prev {
		if u.Action != "plant" {
			continue
		}
		if _, ok := declared[u.ID]; ok {
			continue
		}
		declared[u.ID] = struct{}{}
		restored = append(restored, u)
	}
	if len(restored) == 0 {
		return next
	}
	// plant phải xếp trước advance/resolve cùng chương, phát lại mới dựng được mục trước.
	return append(restored, next...)
}

// RelationshipEntry mục quan hệ nhân vật.
type RelationshipEntry struct {
	CharacterA string `json:"character_a"`
	CharacterB string `json:"character_b"`
	Relation   string `json:"relation"`
	Chapter    int    `json:"chapter"`
}

// ConsistencyIssue vấn đề nhất quán.
type ConsistencyIssue struct {
	Type           string `json:"type"`     // phương diện vấn đề cụ thể model đưa ra theo rubric
	Severity       string `json:"severity"` // critical / error / warning
	Description    string `json:"description"`
	Evidence       string `json:"evidence,omitempty"` // bằng chứng: đoạn văn gốc, tình tiết cụ thể hoặc dữ liệu trạng thái
	Suggestion     string `json:"suggestion,omitempty"`
	Chapters       []int  `json:"chapters,omitempty"` // bằng chứng thực sự rơi vào những chương nào
	RequiresChange bool   `json:"requires_change"`    // có nên vào ngay hàng đợi viết lại hay không, Editor phán theo ngữ nghĩa
}

// DimensionScore điểm xem xét theo từng phương diện.
type DimensionScore struct {
	Dimension string `json:"dimension"`         // do rubric xem xét định nghĩa, có thể mở rộng theo nhiệm vụ
	Score     int    `json:"score"`             // 0-100
	Verdict   string `json:"verdict,omitempty"` // tương thích bản xem cũ; runtime không dùng ngưỡng đè lên phán đoán của model nữa
	Comment   string `json:"comment,omitempty"` // kết luận ngắn của phương diện này
}

// ReviewEntry mục xem xét của Editor.
type ReviewEntry struct {
	Chapter          int                `json:"chapter"`
	Scope            string             `json:"scope"` // chapter / global / arc
	Issues           []ConsistencyIssue `json:"issues"`
	Dimensions       []DimensionScore   `json:"dimensions,omitempty"`      // điểm theo từng phương diện
	ContractStatus   string             `json:"contract_status,omitempty"` // met / partial / missed
	ContractMisses   []string           `json:"contract_misses,omitempty"` // các mục contract chưa đạt
	ContractNotes    string             `json:"contract_notes,omitempty"`  // mô tả ngắn về mức thực hiện contract
	Verdict          string             `json:"verdict"`                   // accept / polish / rewrite
	Summary          string             `json:"summary"`
	AffectedChapters []int              `json:"affected_chapters,omitempty"` // số các chương cần viết lại/đánh bóng
}

// CriticalCount trả về số vấn đề mức critical.
func (r *ReviewEntry) CriticalCount() int {
	n := 0
	for _, issue := range r.Issues {
		if issue.Severity == "critical" {
			n++
		}
	}
	return n
}

// ErrorCount trả về số vấn đề mức error.
func (r *ReviewEntry) ErrorCount() int {
	n := 0
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			n++
		}
	}
	return n
}

// Dimension trả về điểm của phương diện chỉ định; trả về nil nếu không tồn tại.
func (r *ReviewEntry) Dimension(name string) *DimensionScore {
	if r == nil {
		return nil
	}
	for i := range r.Dimensions {
		if r.Dimensions[i].Dimension == name {
			return &r.Dimensions[i]
		}
	}
	return nil
}
