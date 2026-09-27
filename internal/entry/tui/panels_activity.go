package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// renderEventContent render danh sách sự kiện thành luồng sự kiện có tầng bậc.
// DISPATCH làm tiêu đề cấp cao, công cụ subagent hiển thị thụt lề, hình thành cây
// điều phối rõ ràng.
// spinnerFrame dùng render icon động cho dòng "đang chạy" (đồng bộ với topbar spinner).
func renderEventContent(events []host.Event, width, spinnerFrame int) string {
	var b strings.Builder
	for i, ev := range events {
		b.WriteString(renderEventLine(ev, width, spinnerFrame))
		if i < len(events)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// Khung spinner cho sự kiện dạng gọi đang chạy (bubbles.Spinner.Dot, độc lập với
// MiniDot của thanh trên).
var eventRunningFrames = toolSpinnerFrames

func runningSpinner(frame int) string {
	return eventRunningFrames[frame%len(eventRunningFrames)]
}

func renderEventLine(ev host.Event, width, spinnerFrame int) string {
	tsStr := lipgloss.NewStyle().Foreground(colorDim).Render(ev.Time.Format("15:04:05"))
	indent := ""
	if ev.Depth > 0 {
		indent = "  "
	}
	maxSumW := max(20, width-12-ev.Depth*2)

	running := ev.Running()
	durStr := renderEventDuration(ev.Duration)

	switch {
	case ev.Category == "DECISION":
		var icon string
		switch {
		case running:
			icon = lipgloss.NewStyle().Foreground(colorContext).Bold(true).Render(runningSpinner(spinnerFrame))
		case ev.Failed:
			icon = lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("✕")
		default:
			icon = lipgloss.NewStyle().Foreground(colorSuccess).Render("✓")
		}
		name := lipgloss.NewStyle().Foreground(colorContext).Bold(true).Render("ARBITER")
		label := lipgloss.NewStyle().Foreground(colorMuted).Render("（" + truncate(ev.Summary, maxSumW-9) + "）")
		line := tsStr + " " + icon + " " + name + label
		if !running {
			line += durStr
		}
		return line

	case ev.Category == "DISPATCH":
		// Ba trạng thái: đang chạy (spinner accent + đậm) / thất bại (✕ đỏ) / xong (✓ xanh)
		var icon string
		switch {
		case running:
			icon = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(runningSpinner(spinnerFrame))
		case ev.Failed:
			icon = lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("✕")
		default:
			icon = lipgloss.NewStyle().Foreground(colorSuccess).Render("✓")
		}
		sum := renderDispatchSummary(ev.Summary, maxSumW)
		if running {
			// Đang chạy giữ nguyên nhưng in đậm
			sum = lipgloss.NewStyle().Bold(true).Render(sum)
		}
		line := tsStr + " " + icon + " " + sum
		if !running {
			line += durStr
		}
		return line

	case ev.Category == "TOOL":
		// Công cụ bên trong Worker (Depth=1)
		var icon, sum string
		switch {
		case running:
			icon = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(runningSpinner(spinnerFrame))
			sum = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(truncate(ev.Summary, maxSumW))
		case ev.Failed:
			icon = lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("✕")
			sum = lipgloss.NewStyle().Foreground(colorError).Render(truncate(ev.Summary, maxSumW))
		default:
			icon = lipgloss.NewStyle().Foreground(colorDim).Render("├")
			sum = lipgloss.NewStyle().Foreground(colorMuted).Render(truncate(ev.Summary, maxSumW))
		}
		line := tsStr + " " + indent + icon + " " + sum
		if !running {
			line += durStr
		}
		return line

	case ev.Category == "ERROR":
		icon := lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("✕")
		errStyle := lipgloss.NewStyle().Foreground(colorError)
		line := tsStr + " " + indent + icon + " " + errStyle.Render(truncate(ev.Summary, maxSumW))
		if durStr != "" {
			line += durStr
		}
		return line

	case ev.Category == "SYSTEM":
		icon := lipgloss.NewStyle().Foreground(colorAccent).Render("⚙")
		sumColor := colorMuted
		if ev.Level == "warn" {
			sumColor = colorAccent
		}
		text := truncate(ev.Summary, maxSumW)
		if cd := retryCountdown(ev.RetryAt, time.Now()); cd != "" {
			cd = " · " + cd
			text = truncate(ev.Summary, max(20, maxSumW-lipgloss.Width(cd))) + cd
		}
		sum := lipgloss.NewStyle().Foreground(sumColor).Render(text)
		return tsStr + " " + indent + icon + " " + sum

	case ev.Category == "USER":
		// Hiển thị lại văn bản Steer / Continue người dùng gửi từ ô nhập; tách hình thái với
		// ⚙ của SYSTEM, dùng ✎ ngụ ý "đầu vào". Màu dùng colorAccent2 (xanh non) tách khỏi
		// vàng của SYSTEM, tránh đọc nhầm thành thông điệp hệ thống.
		icon := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true).Render("✎")
		sum := lipgloss.NewStyle().Foreground(colorAccent2).Render(truncate(ev.Summary, maxSumW))
		return tsStr + " " + indent + icon + " " + sum

	case ev.Category == "CONTEXT" || ev.Category == "COMPACT":
		icon := lipgloss.NewStyle().Foreground(colorContext).Render("⚙")
		sumColor := colorContext
		if ev.Level == "debug" {
			sumColor = colorMuted
		}
		sum := lipgloss.NewStyle().Foreground(sumColor).Render(truncate(ev.Summary, maxSumW))
		return tsStr + " " + indent + icon + " " + sum

	default:
		// Category đã biết theo màu ánh xạ; category lạ theo màu tiền cảnh mặc định của
		// terminal, tránh nhét cứng colorText.
		if color, ok := categoryColors[ev.Category]; ok {
			icon := lipgloss.NewStyle().Foreground(color).Render("·")
			sum := lipgloss.NewStyle().Foreground(color).Render(truncate(ev.Summary, maxSumW))
			return tsStr + " " + indent + icon + " " + sum
		}
		icon := lipgloss.NewStyle().Foreground(colorDim).Render("·")
		return tsStr + " " + indent + icon + " " + truncate(ev.Summary, maxSumW)
	}
}

// retryCountdown trả về văn bản đếm ngược retry ("thử lại sau 7s"); chưa đặt hạn hoặc
// đã đến hạn (request đang trên đường) trả về rỗng. Sự kiện chỉ mang thời điểm hết hạn,
// số giây còn lại tính lúc render — spinner tick chạy vẽ lại tức tạo thành đếm ngược từng
// giây, bảng sự kiện và bảng import dùng chung (khớp cơ chế cập nhật tại chỗ "cùng
// ID/Key nhảy trên một dòng").
func retryCountdown(retryAt, now time.Time) string {
	if retryAt.IsZero() {
		return ""
	}
	remain := retryAt.Sub(now)
	if remain <= 0 {
		return ""
	}
	secs := int((remain + time.Second - 1) / time.Second)
	return fmt.Sprintf("Thử lại sau %ds", secs)
}

// renderDispatchSummary render tóm tắt DISPATCH: tên Agent dùng màu vai, nhiệm vụ dùng
// màu nhạt.
func renderDispatchSummary(summary string, maxW int) string {
	agentName := summary
	taskPart := ""
	if idx := strings.Index(summary, "（"); idx > 0 {
		agentName = summary[:idx]
		taskPart = summary[idx:]
	}
	displayName := agentDisplayName(agentName)
	color := eventAgentColor(agentName)
	nameW := lipgloss.Width(displayName)
	if nameW >= maxW {
		return lipgloss.NewStyle().Foreground(color).Bold(true).Render(truncate(displayName, maxW))
	}
	result := lipgloss.NewStyle().Foreground(color).Bold(true).Render(displayName)
	if taskPart != "" {
		remaining := maxW - nameW
		if remaining > 2 {
			result += lipgloss.NewStyle().Foreground(colorMuted).Render(truncate(taskPart, remaining))
		}
	}
	return result
}

// eventAgentColor trả về màu chủ đề tương ứng với vai của Agent.
func eventAgentColor(agent string) lipgloss.AdaptiveColor {
	switch {
	case strings.HasPrefix(agent, "architect"):
		return colorAccent2
	case agent == "writer":
		return colorTool
	case agent == "editor":
		return colorReview
	default:
		return colorAccent
	}
}

// renderEventDuration render Duration thành chú thích trong ngoặc màu nhạt, giá trị 0
// trả về rỗng.
func renderEventDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return " " + lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render("("+formatDuration(d)+")")
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%ds", m, s)
}

func renderEventActivity(snap host.UISnapshot, frame, width int) string {
	if !snap.IsRunning {
		return ""
	}
	return renderEventSparkle(frame, width)
}

var sparkleFrames = []string{
	"✦  ·   ✧   ·  ✦",
	"·  ✧   ·  ✦   ·",
	"  ✧   ·  ✦   · ",
	"   ·  ✦   ·  ✧ ",
	"✧   ·  ✦  ·   ✧",
	" ·  ✧   ·  ✦  ·",
	"✦   ·  ✧   ·  ✦",
	" ·  ✦   ·  ✧   ",
}

func renderEventSparkle(frame, width int) string {
	pattern := sparkleFrames[frame%len(sparkleFrames)]

	var b strings.Builder
	for _, ch := range pattern {
		switch ch {
		case '✦':
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#d4a21a")).Bold(true).Render("✦"))
		case '✧':
			b.WriteString(lipgloss.NewStyle().Foreground(colorAccent2).Render("✧"))
		case '·':
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render("·"))
		default:
			b.WriteRune(ch)
		}
	}
	_ = width
	return " " + b.String()
}

// renderEventFlowViewport bọc viewport render bảng luồng sự kiện.
func renderEventFlowViewport(vp viewport.Model, width, height int, focused bool) string {
	// Thanh tiêu đề
	titleColor := colorDim
	if focused {
		titleColor = colorAccent
	}
	title := lipgloss.NewStyle().Foreground(titleColor).Render(":: Nhật Ký Tiến Độ (Activity Stream)")
	lineW := width - lipgloss.Width(title) - 4
	if lineW < 0 {
		lineW = 0
	}
	separator := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))
	header := " " + title + " " + separator

	vpH := height - 1
	if vpH < 1 {
		vpH = 1
	}
	style := lipgloss.NewStyle().
		Width(width).
		Height(vpH).
		Padding(0, 1)

	return header + "\n" + style.Render(vp.View())
}

// renderStreamPanel render bảng stream (nửa dưới cột giữa).
func renderStreamPanel(vp viewport.Model, width, height int, focused, running bool, frame int) string {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Underline(focused)
	title := titleStyle.Render("▍Luồng Viết Trực Tiếp (Live Stream)")
	if running {
		status := renderStreamActivity(frame)
		title += " " + status
	}
	lineW := width - lipgloss.Width(title) - 4
	if lineW < 0 {
		lineW = 0
	}
	separator := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))
	header := " " + title + " " + separator

	// Nội dung viewport (height gồm dòng header, chiều cao thật của viewport phải trừ 1).
	// vpStyle tầng ngoài không đặt Foreground — màu chính văn chương do contentStyle bên
	// trong renderChapterBlock quản (nền sáng nâu đậm / nền tối mặc định terminal). Nếu tầng
	// ngoài thêm Foreground, dưới chủ đề nền sáng khối điều phối agent (✻ vàng + label xanh
	// non) sẽ bị nâu đậm "đè" thành màu chính văn thường.
	vpH := height - 1
	if vpH < 1 {
		vpH = 1
	}
	vpStyle := lipgloss.NewStyle().
		Width(width).
		Height(vpH).
		Padding(0, 1)

	return header + "\n" + vpStyle.Render(vp.View())
}

var streamCursorFrames = []string{"·", "✢", "✳", "✶", "✻", "✽"}

func renderStreamCursor(frame int) string {
	f := frame % len(streamCursorFrames)
	var dots [3]string
	for i := range 3 {
		dots[i] = streamCursorFrames[(f+i)%len(streamCursorFrames)]
	}
	trail := dots[0] + " " + dots[1] + " " + dots[2]
	return "\n" + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(trail)
}

var streamActivityFrames = [][2]string{
	{"✦", "✧"},
	{"✦", "✧"},
	{"✧", "✦"},
	{"✧", "✦"},
	{"✦", "✧"},
	{"✦", "✧"},
	{"✧", "✦"},
	{"✧", "✦"},
}

func renderStreamActivity(frame int) string {
	pair := streamActivityFrames[frame%len(streamActivityFrames)]
	major := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(pair[0])
	minor := lipgloss.NewStyle().Foreground(colorAccent2).Render(pair[1])
	return major + " " + minor
}

// renderStreamContent render stream theo vòng thành khối ngữ nghĩa.
// Khối điều phối Agent (bắt đầu bằng ▸ hoặc ✻) dùng tiêu đề accent + lệnh dim; khối chính
// văn theo màu mặc định terminal. cursor khác rỗng thì nối cuối, biểu thị AI đang xuất.
func renderStreamContent(rounds []string, width int, cursor string) string {
	if width < 24 {
		width = 24
	}

	var blocks []string
	for _, round := range rounds {
		text := strings.TrimSpace(round)
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "▸") || strings.HasPrefix(text, "✻") {
			blocks = append(blocks, renderAgentBlock(text, width))
		} else {
			blocks = append(blocks, renderChapterBlock(text, width))
		}
	}
	result := strings.Join(blocks, "\n\n")
	if cursor != "" {
		result += cursor
	}
	return result
}

// renderAgentBlock render khối điều phối Agent: icon + tiêu đề + đường phân cách + lệnh
// nhiệm vụ.
//
// label dùng colorAccent2 xanh non + Bold + Underline ba tầng nhấn — trước đây colorAccent
// vàng + Bold trên nền tối quá gần hình ảnh với dòng suy nghĩ màu xám colorDim, không phân
// được chính phụ. Xanh non là màu lạnh, tách hẳn về sắc độ với xám ấm của dòng suy nghĩ;
// Underline ổn định có tác dụng ở mọi terminal, là neo trực quan đáng tin hơn Bold. Icon ✻
// ngược lại dùng vàng làm neo, với label tạo tương phản hai màu.
func renderAgentBlock(text string, width int) string {
	headerLine, body, _ := strings.Cut(text, "\n")

	iconStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true).Underline(true)

	// Tách icon tiền tố (✻ hoặc ▸) và label chính văn, tô màu riêng; định dạng cũ không
	// icon giữ đơn sắc.
	var headerStyled string
	if first, rest, ok := strings.Cut(headerLine, " "); ok && (first == "✻" || first == "▸") {
		headerStyled = iconStyle.Render(first) + " " + labelStyle.Render(rest)
	} else {
		headerStyled = labelStyle.Render(headerLine)
	}

	// Dòng tiêu đề + đường phân cách (lineW dùng chiều rộng trực quan của headerLine chứ
	// không phải độ rộng byte sau render)
	titleW := lipgloss.Width(headerLine)
	lineW := max(0, width-titleW-1)
	header := headerStyled +
		" " + lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))

	var b strings.Builder
	b.WriteString(header)

	// Lệnh nhiệm vụ: màu dim, thụt 2 ô; giữa header chừa một dòng trống, tránh dính hình
	// ảnh vào nhau.
	body = strings.TrimSpace(body)
	if body != "" {
		taskStyle := lipgloss.NewStyle().Foreground(colorMuted)
		lines := wrapStreamText(body, max(16, width-6))
		b.WriteString("\n\n")
		for i, line := range lines {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(taskStyle.Render("  " + line))
		}
	}
	return b.String()
}

// renderChapterBlock render khối chính văn, tự phân biệt nội dung suy nghĩ và chính văn
// chương. Nội dung suy nghĩ (đoạn đánh dấu ThinkingSep) dùng colorDim nghiêng; chính văn
// chương theo bodyTextColor: nền tối kế thừa tiền cảnh mặc định terminal, nền sáng dùng
// nâu đậm giữ sắc ấm.
func renderChapterBlock(text string, width int) string {
	contentStyle := lipgloss.NewStyle().Foreground(bodyTextColor)
	thinkStyle := lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	wrapW := max(16, width-4)

	// Chia theo ThinkingSep: đoạn lẻ là suy nghĩ, đoạn chẵn là chính văn
	// Định dạng: [chính văn] \x02 [suy nghĩ] [chính văn] \x02 [suy nghĩ] ...
	parts := strings.Split(text, utils.ThinkingSep)

	var b strings.Builder
	for i, part := range parts {
		part = strings.TrimRight(part, " \n")
		if part == "" {
			continue
		}
		isThinking := i > 0 && i%2 != 0 // đoạn lẻ sau ThinkingSep là suy nghĩ

		style := contentStyle
		if isThinking {
			style = thinkStyle
		}

		lines := wrapStreamText(part, wrapW)
		for j, line := range lines {
			if b.Len() > 0 && j == 0 {
				b.WriteString("\n\n") // dòng trống giữa các đoạn: chừa khoảng cách trực quan giữa suy nghĩ và chính văn
			} else if j > 0 {
				b.WriteString("\n")
			}
			b.WriteString(style.Render(line))
		}
	}
	return b.String()
}

func wrapStreamText(text string, width int) []string {
	if width < 8 {
		return []string{text}
	}

	var out []string
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(raw) == "" {
			out = append(out, "")
			continue
		}
		prefix, rest, nextPrefix := parseWrapPrefix(raw)
		wrapped := wrapRunes(rest, max(4, width-lipgloss.Width(prefix)))
		for i, line := range wrapped {
			if i == 0 {
				out = append(out, prefix+line)
				continue
			}
			out = append(out, nextPrefix+line)
		}
	}
	return out
}

func parseWrapPrefix(line string) (prefix, content, nextPrefix string) {
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	trimmed := strings.TrimSpace(line)

	switch {
	case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "), strings.HasPrefix(trimmed, "• "):
		prefix = indent + trimmed[:2]
		content = strings.TrimSpace(trimmed[2:])
		nextPrefix = indent + "  "
		return prefix, content, nextPrefix
	case orderedListPrefix(trimmed) != "":
		marker := orderedListPrefix(trimmed)
		prefix = indent + marker
		content = strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
		nextPrefix = indent + strings.Repeat(" ", lipgloss.Width(marker))
		return prefix, content, nextPrefix
	case strings.HasPrefix(trimmed, "```"):
		return indent, trimmed, indent
	default:
		return indent, trimmed, indent
	}
}

func orderedListPrefix(line string) string {
	end := strings.Index(line, ". ")
	if end <= 0 {
		return ""
	}
	for _, r := range line[:end] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return line[:end+2]
}

func wrapRunes(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	if width < 2 {
		return []string{text}
	}

	var lines []string
	var current strings.Builder
	currentWidth := 0

	for _, r := range text {
		rw := lipgloss.Width(string(r))
		if currentWidth > 0 && currentWidth+rw > width {
			lines = append(lines, strings.TrimRight(current.String(), " "))
			current.Reset()
			currentWidth = 0
			if r == ' ' {
				continue
			}
		}
		current.WriteRune(r)
		currentWidth += rw
	}
	if current.Len() > 0 {
		lines = append(lines, strings.TrimRight(current.String(), " "))
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
