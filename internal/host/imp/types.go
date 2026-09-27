// Package imp hiện thực pipeline nhập khẩu ngữ nghĩa theo giai đoạn cho tiểu thuyết ngoài
// (docs/import-pipeline.md).
//
// Mô hình chịu trách nhiệm hiểu ngữ nghĩa mở, code chịu trách nhiệm tọa độ, độ phủ, kiểu, hash,
// thứ tự và tính idempotent; toàn bộ sản phẩm ngữ nghĩa được xác minh xong trong workspace
// độc lập (meta/import/) rồi mới công bố vào trạng thái sách chính thức. Hành động kế tiếp chỉ
// suy ra từ artifact (NextAction), không lưu enum giai đoạn dễ trôi, khôi phục không phụ thuộc from=N.
package imp

import "time"

// Options điều khiển một lượt nhập. Khi khôi phục các trường có thể rỗng, suy trực tiếp từ
// workspace đang hoạt động và Intent đã lưu.
type Options struct {
	SourcePath      string // bắt buộc với nhập mới; có thể rỗng khi khôi phục
	AutoConfirm     bool   // --yes: tự động chấp nhận phân tách sau khi kiểm tra hợp lệ
	StoryResolution string // --story=open|closed: chỉ tiền chọn khi synthesis trả về uncertain
	ContinueAfter   bool   // --continue: không tạo Hold hoàn tất nhập
	Guidance        string // --guide: hướng dẫn phân tách bằng ngôn ngữ tự nhiên, ghi xuống workspace
	//                        thì tự nhiên làm phân tách cũ mất khớp và nhận diện lại
	// AcceptSegmentation: xác nhận thủ công rõ ràng (y) sau khi TUI xem trước. Chỉ phê duyệt phân
	// tách hiện tại một lần, không ghi intent; khác --yes ở chỗ: --yes là ủy quyền mù khi chưa xem
	// trước, không phê duyệt phân tách kèm ghi chú dung sai (Notes), còn y là phán định sau khi đã xem trước.
	AcceptSegmentation bool
}

// intent trích xuất từ Options phần cấp phép của người dùng cần lưu bền.
func (o Options) intent() Intent {
	return Intent{
		Version:             workspaceSchemaVersion,
		AutoConfirm:         o.AutoConfirm,
		StoryResolution:     o.StoryResolution,
		ContinueAfterImport: o.ContinueAfter,
	}
}

// Stage biểu thị giai đoạn hiện tại của luồng nhập, chỉ dùng để UI hiển thị, không phải
// nguồn sự thật cho khôi phục (RFC §14.1).
type Stage string

const (
	StageIngesting            Stage = "ingesting"
	StageSegmenting           Stage = "segmenting"
	StageAwaitingConfirmation Stage = "awaiting_confirmation"
	StageAnalyzing            Stage = "analyzing"
	StageSynthesizing         Stage = "synthesizing"
	StageAwaitingStoryStatus  Stage = "awaiting_story_status"
	StageValidating           Stage = "validating"
	StagePublishing           Stage = "publishing"
	StageDone                 Stage = "done"
	StageError                Stage = "error"
)

// Event là tiến trình phát ra phía ngoài của luồng nhập. Event là projection, không tham gia khôi phục.
type Event struct {
	Time    time.Time
	Stage   Stage
	Current int    // tiến độ chương/khoảng
	Total   int    // tổng số
	Message string // mô tả cho người đọc
	Level   string // ""=tiến độ thường; "warn"=trạng thái cảnh báo như backoff thử lại / hỏi lại sau kiểm tra
	Key     string // khác rỗng thì UI cập nhật tại chỗ các sự kiện liên tiếp cùng Key (ví dụ 7 lần backoff
	//                     nhấp nháy trên một dòng), đồng bộ với cơ chế ID của bảng sự kiện
	RetryAt time.Time // khác 0 = thời điểm hết hạn của lần thử lại kế; UI vẽ đếm ngược từng giây theo đó,
	//                     đến giờ thì xoá (yêu cầu đang trên đường)
	Err       error // mang theo khi StageError
	Continued bool  // khi StageDone do Host gán: đã tự động khởi động lại Engine hay chưa (--continue × auto)
}
