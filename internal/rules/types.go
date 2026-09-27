// Package rules hiện thực tầng đầu vào của sở thích người dùng (Policy): chuẩn hóa quy tắc
// viết từ các nguồn, hợp nhất thành snapshot của cuốn sách (xem snapshot.go), lúc runtime do
// novel_context inject và commit_chapter kiểm tra cơ học.
//
// Rule là lớp sự thực thứ tư, đứng cạnh Progress / Checkpoint / Artifact nhưng tính chất ngược
// lại: ba lớp trước là đầu ra hệ thống, Rule là đầu vào bền vững của ý định người dùng.
//
// Ràng buộc thiết kế (không thỏa hiệp):
//   - Tool chỉ trả sự thực, không trả chỉ lệnh (Violation là sự thực, editor quyết định có kích hoạt viết lại không)
//   - Không đưa vào đường verdict mới (tái dùng PendingRewrites)
//   - Không đưa vào trường mức độ nghiêm ngặt (severity ánh xạ cố định theo loại quy tắc, editor tự phán định ngữ nghĩa)
//   - Không đụng Flow Router (rule không tham gia định tuyến)
package rules

// SourceKind đánh dấu nguồn file quy tắc, chỉ dùng để sinh nhãn nguồn (như global:my-style.md).
type SourceKind int

const (
	// SourceGlobal — sở thích toàn cục của người dùng (mọi .md dưới ~/.ainovel/rules/, hợp nhất theo thứ tự từ điển tên file), tái dùng xuyên sách.
	SourceGlobal SourceKind = iota
	// SourceProject — quy tắc của cuốn sách (mọi .md dưới ./.ainovel/rules/, hợp nhất theo thứ tự từ điển tên file), mức ưu tiên cao nhất.
	SourceProject
)

// String trả về tên dễ đọc của nguồn, dùng làm tiền tố nhãn nguồn.
func (k SourceKind) String() string {
	switch k {
	case SourceGlobal:
		return "global"
	case SourceProject:
		return "project"
	default:
		return "unknown"
	}
}

// Structured chứa các trường quy tắc cấu trúc kiểm tra cơ học được (kết quả ứng viên/hợp nhất
// sau chuẩn hóa các nguồn). Số chữ chương cố ý không nằm trong đây: một chương dài bao nhiêu là
// vấn đề tính trọn vẹn tự sự, thuộc phán định ngữ nghĩa của (writer/editor); số hóa thành đường cứng
// cơ học sẽ dụ model chèn nước để vượt mốc — mong muốn về số chữ đi kênh ngôn ngữ tự nhiên preferences.
type Structured struct {
	Genre            string         `json:"genre,omitempty"`
	ForbiddenChars   []string       `json:"forbidden_chars,omitempty"`
	ForbiddenPhrases []string       `json:"forbidden_phrases,omitempty"`
	FatigueWords     map[string]int `json:"fatigue_words,omitempty"`
}

// IsEmpty dùng nhận định hoàn toàn không có quy tắc cấu trúc; checker có thể dựa đó để bỏ qua.
func (s Structured) IsEmpty() bool {
	return s.Genre == "" &&
		len(s.ForbiddenChars) == 0 &&
		len(s.ForbiddenPhrases) == 0 &&
		len(s.FatigueWords) == 0
}

// Severity đánh dấu mức nghiêm trọng của Violation.
// Ánh xạ cố định (người dùng không cấu hình được):
//
//	forbidden_chars xuất hiện        -> Error
//	forbidden_phrases xuất hiện      -> Error
//	fatigue_words vượt ngưỡng        -> Warning
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Violation là đầu ra của checker: phát biểu sự thực rằng chương này vi phạm một quy tắc cơ học.
//
// Lưu ý: commit_chapter truyền violations nguyên sang JSON trả về, không chặn commit;
// editor lúc xem xét ánh xạ các sự thực này vào bảy chiều hiện có (aesthetic/pacing/character/consistency),
// LLM tự quyết định có nâng verdict kích hoạt polish/rewrite hay không.
type Violation struct {
	Rule     string   `json:"rule"`             // forbidden_chars / forbidden_phrases / fatigue_words
	Target   string   `json:"target,omitempty"` // đối tượng vi phạm cụ thể (từ/ký tự nào)
	Limit    any      `json:"limit,omitempty"`  // ngưỡng; fatigue_words=int / forbidden_*=rỗng
	Actual   any      `json:"actual"`           // giá trị thực tế: số lần xuất hiện
	Severity Severity `json:"severity"`         // error / warning
}
