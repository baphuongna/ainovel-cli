package tui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/host"
)

// renderStatusBar render thanh trạng thái dùng lượng dưới cùng màn hình, chiếm dòng trống
// cuối vốn có của vùng nhập (không tốn chiều cao thêm):
//
//	◆ provider model(cửa sổ,suy nghĩ) │ ↑đầu vào ↓đầu ra ⚡tỉ lệ cache gần đây │ chi phí(/ngân sách) tiết kiệmX    ./thư-mục-sách
//
// Định vị là "nhìn một cái biết chi phí": danh tính model mình trả tiền, token tích lũy
// phiên, chi phí và cảnh báo ngân sách tiệm cận.
// Dữ liệu đến từ UISnapshot thăm dò 3s (mỗi lần gọi model xong usage được cộng vào);
// chi tiết per-role/per-model và chẩn đoán cache vẫn do cột trái gánh, ở đây không lặp.
func renderStatusBar(snap host.UISnapshot, outputDir string, width int) string {
	dim := lipgloss.NewStyle().Foreground(colorDim)
	val := lipgloss.NewStyle().Foreground(colorMuted)

	var segs []string
	if snap.ModelName != "" {
		s := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("◆") + " "
		if snap.Provider != "" {
			s += dim.Render(snap.Provider) + " "
		}
		s += val.Render(snap.ModelName)
		if suffix := modelInfoSuffix(snap); suffix != "" {
			s += dim.Render("(" + suffix + ")")
		}
		segs = append(segs, s)
	}
	if snap.TotalInputTokens > 0 || snap.TotalOutputTokens > 0 {
		s := dim.Render("↑") + val.Render(formatTokensCompact(snap.TotalInputTokens)) +
			" " + dim.Render("↓") + val.Render(formatTokensCompact(snap.TotalOutputTokens))
		// Tỉ lệ trúng gần đây chỉ hiện khi model thật sự hỗ trợ prompt cache và có mẫu,
		// tránh hiểu nhầm "0% cần kiểm tra".
		if snap.OverallCacheCapable && snap.OverallRecentSamples > 0 && snap.OverallRecentInput > 0 {
			rate := cacheHitRate(snap.OverallRecentCacheRead, snap.OverallRecentInput)
			s += " " + lipgloss.NewStyle().Foreground(cacheHitColor(rate)).Render("⚡"+formatPercent(rate))
		}
		segs = append(segs, s)
	}
	if snap.TotalCostUSD > 0 || snap.BudgetLimitUSD > 0 {
		cost := formatCostUSD(snap.TotalCostUSD)
		if cost == "" {
			cost = "$0"
		}
		style := val
		if snap.BudgetLimitUSD > 0 {
			// Ngân sách tiệm cận/vượt hạn dùng màu cảnh báo — thanh trạng thái thường trực
			// dễ thấy, là vị trí ngân sách đáng được nhìn nhất.
			switch ratio := snap.TotalCostUSD / snap.BudgetLimitUSD; {
			case ratio >= 1:
				style = lipgloss.NewStyle().Foreground(colorError).Bold(true)
			case ratio >= 0.8:
				style = lipgloss.NewStyle().Foreground(colorReview)
			}
		}
		s := style.Render(cost)
		if snap.BudgetLimitUSD > 0 {
			s += dim.Render("/" + formatCostUSD(snap.BudgetLimitUSD))
		}
		if saved := formatCostUSD(snap.TotalSavedUSD); saved != "" {
			s += dim.Render(" (tiết kiệm " + saved + ")")
		}
		segs = append(segs, s)
	}

	left := strings.Join(segs, dim.Render(" │ "))
	var right string
	if outputDir != "" {
		right = dim.Render("./" + filepath.Base(outputDir))
	}
	if left == "" && right == "" {
		return dim.Render("SẴN SÀNG")
	}
	return joinInlineSides(left, right, width)
}

// modelInfoSuffix lắp chú thích trong ngoặc cho model: cửa sổ ngữ cảnh + cấp suy nghĩ,
// ví dụ "200K,med".
func modelInfoSuffix(snap host.UISnapshot) string {
	var parts []string
	if w := formatContextWindow(snap.ModelContextWindow); w != "" {
		parts = append(parts, w)
	}
	if t := formatThinkingLevel(snap.ThinkingLevel); t != "" {
		parts = append(parts, t)
	}
	return strings.Join(parts, ",")
}

func formatThinkingLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "":
		return "auto"
	case "medium":
		return "med"
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}
