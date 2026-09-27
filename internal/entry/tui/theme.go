package tui

import "github.com/charmbracelet/lipgloss"

// Bảng màu chủ đề — chất sách ấm
// AdaptiveColor: Light = giá trị cho nền sáng, Dark = giá trị cho nền tối
//
// Nguyên tắc thiết kế: mức Light giữ ổn định không động (nền sáng đã chỉnh ưng ý);
// mức Dark thống nhất sáng hơn Light ~25% lightness, tăng nhẹ độ bão hòa, bảo đảm
// nền tối đủ tương phản (colorDim trước đây #6b6355 trên nền đen #1c1c1c gần như
// không thấy, đường phân cách/chữ phụ biến mất hết).
//
// colorAccent2 trên nền tối đổi từ #7a9e7e sang xanh non #5fb8a3, tách khỏi "xanh
// khoẻ" của colorSuccess — trước đây hai màu hoàn toàn trùng, làm màu nhận dạng
// architect agent và cảm giác vui "tỉ lệ trúng cao" lẫn vào nhau.
// bodyTextColor là chiến lược tiền cảnh cho "chính văn trung tính":
//   - terminal tối → NoColor, kế thừa tiền cảnh mặc định của terminal, tránh chúng
//     ta nhét cứng trắng ngà #e8e0d0 đụng chủ đề nền ấm/lạnh người dùng tự cấu hình
//     (người dùng thực đo màu mặc định nền tối đọc dịu hơn).
//   - terminal sáng → dùng mức Light của colorText (nâu đậm #3d3529), giữ sắc ấm
//     thương hiệu; nền sáng màu đen mặc định tương phản quá gắt, nâu đậm đã chỉnh
//     nhìn dịu hơn trên nền sáng.
//
// AdaptiveColor bắt buộc cả hai đầu đều phải có giá trị màu, không có mức "không
// màu", nên ở đây phán nền một lần lúc khởi động, sau đó mọi giá trị tổng quan/chính
// văn chương/mô tả lệnh v.v. là "chính văn trung tính" quy chiếu thống nhất bodyTextColor.
var bodyTextColor lipgloss.TerminalColor = func() lipgloss.TerminalColor {
	if lipgloss.HasDarkBackground() {
		return lipgloss.NoColor{}
	}
	return lipgloss.Color("#3d3529")
}()

var (
	colorText    = lipgloss.AdaptiveColor{Light: "#3d3529", Dark: "#e8e0d0"}
	colorDim     = lipgloss.AdaptiveColor{Light: "#8a7e6b", Dark: "#8a8175"}
	colorMuted   = lipgloss.AdaptiveColor{Light: "#7a7060", Dark: "#b8b09c"}
	colorAccent  = lipgloss.AdaptiveColor{Light: "#b8860b", Dark: "#e5b449"}
	colorAccent2 = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#5fb8a3"}
	colorRunning = lipgloss.AdaptiveColor{Light: "#6f8641", Dark: "#b5d075"}
	colorSuccess = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#7ec488"}
	colorError   = lipgloss.AdaptiveColor{Light: "#b5433a", Dark: "#e07060"}
	colorReview  = lipgloss.AdaptiveColor{Light: "#b07530", Dark: "#e09b5a"}
	colorContext = lipgloss.AdaptiveColor{Light: "#6b5a9e", Dark: "#a890d8"}
	colorTool    = lipgloss.AdaptiveColor{Light: "#3a7a8a", Dark: "#7ec5d8"}
)

// Ánh xạ màu nhãn trạng thái
var statusColors = map[string]lipgloss.AdaptiveColor{
	"READY":    colorDim,
	"PAUSING":  colorAccent,
	"PAUSED":   colorAccent,
	"RUNNING":  colorRunning,
	"REVIEW":   colorReview,
	"REWRITE":  colorReview,
	"COMPLETE": colorSuccess,
	"ERROR":    colorError,
}

// Hiển thị trạng thái: icon + nhãn tiếng Việt. Phù hợp chủ đề ấm tổng thể, tránh khối
// màu đặc gây gợn. Icon của RUNNING để trống, do khung spinner điền động, hòa cảm giác
// động vào chính chỉ báo trạng thái.
var statusDisplay = map[string]struct {
	icon  string
	label string
}{
	"READY":    {"○", "Sẵn sàng"},
	"RUNNING":  {"", "Đang chạy"},
	"REVIEW":   {"◆", "Xem xét"},
	"REWRITE":  {"◆", "Viết lại"},
	"COMPLETE": {"●", "Hoàn thành"},
	"PAUSED":   {"⏸", "Tạm dừng"},
	"PAUSING":  {"⏸", "Đang tạm dừng"},
	"ERROR":    {"✕", "Lỗi"},
}

// Ánh xạ màu phân loại sự kiện
var categoryColors = map[string]lipgloss.AdaptiveColor{
	"DISPATCH": colorAccent,
	"DECISION": colorContext,
	"TOOL":     colorTool,
	"SYSTEM":   colorAccent,
	"USER":     colorAccent2,
	"REVIEW":   colorReview,
	"CHECK":    colorSuccess,
	"ERROR":    colorError,
	"AGENT":    colorMuted,
	"CONTEXT":  colorContext,
	"COMPACT":  colorContext,
}

// Kiểu cơ bản
var (
	baseBorder = lipgloss.RoundedBorder()

	topBarStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Padding(0, 1)

	statusIconStyle = lipgloss.NewStyle().
			Bold(true)

	statusLabelStyle = lipgloss.NewStyle().
				Foreground(colorText)

	panelTitleStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	fieldLabelStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Width(10)

	// fieldValueStyle / cardContentStyle dùng bodyTextColor — các giá trị vùng tổng quan
	// (trạng thái chạy, số chương đã hoàn thành, số ký tự v.v.), mục dàn ý, danh sách nhân
	// vật, tóm tắt chương v.v. là "nội dung chính văn trung tính", trên nền tối theo màu
	// tiền cảnh mặc định của terminal (tránh nhét trắng ngà đụng chủ đề), trên nền sáng
	// dùng nâu đậm giữ sắc ấm. Các phần tử mang tính ngữ nghĩa mạnh (tiêu đề, giá trị nổi
	// bật, trạng thái, lỗi, tô màu tỉ lệ trúng v.v.) vẫn theo màu chủ đề colorAccent /
	// colorError v.v.
	fieldValueStyle = lipgloss.NewStyle().Foreground(bodyTextColor)

	highlightValueStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	contextUsageMetaStyle = lipgloss.NewStyle().
				Foreground(colorDim)

	cardTitleStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	cardContentStyle = lipgloss.NewStyle().Foreground(bodyTextColor)
)
