package host

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/store"
)

// buildStoryStateSummary lắp một đoạn tóm tắt tinh gọn về hiện trạng truyện, cho trợ lý đồng sáng tạo theo
// giai đoạn biết "đã viết tới đâu". Tái dùng điểm truy cập store, chỉ lấy dữ kiện cấp cao cần cho lập
// kế hoạch hướng đi (tiến độ / la bàn / quyển gần nhất / nhân vật chính / phục bút đang hoạt động);
// không kéo phần thân, không bơm JSON đầy đủ của novel_context — đồng sáng tạo là đối thoại, cần tổng quan dễ đọc, không phải ngữ cảnh viết.
func buildStoryStateSummary(s *store.Store) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	var warnings []string
	warn := func(scope string, err error) {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("đọc %s thất bại: %v", scope, err))
		}
	}

	if book, err := s.Book.Load(); book != nil {
		fmt.Fprintf(&b, "- Tên sách: %q\n", book.Title)
	} else {
		warn("book", err)
	}

	if progress, err := s.Progress.Load(); progress != nil {
		fmt.Fprintf(&b, "- Tiến độ: đã hoàn thành %d chương", len(progress.CompletedChapters))
		if progress.Layered {
			outline, outlineErr := s.Outline.LoadOutline()
			if outlineErr != nil {
				warn("outline", outlineErr)
			} else if len(outline) > 0 {
				fmt.Fprintf(&b, " / hiện đã chi tiết hóa %d chương (phần sau lập kế hoạch động theo cung)", len(outline))
			}
		} else if progress.TotalChapters > 0 {
			fmt.Fprintf(&b, " / quy hoạch %d chương", progress.TotalChapters)
		}
		fmt.Fprintf(&b, ", khoảng %d chữ, chương tiếp theo là chương %d\n", progress.TotalWordCount, progress.NextChapter())
		if progress.Layered && progress.CurrentVolume > 0 {
			fmt.Fprintf(&b, "- Vị trí hiện tại: quyển %d cung %d\n", progress.CurrentVolume, progress.CurrentArc)
		}
	} else {
		warn("progress", err)
	}

	if compass, err := s.Outline.LoadCompass(); compass != nil {
		if dir := strings.TrimSpace(compass.EndingDirection); dir != "" {
			fmt.Fprintf(&b, "- Hướng kết: %s\n", dir)
		}
		if compass.EstimatedScale != "" {
			fmt.Fprintf(&b, "- Quy mô dự kiến: %s\n", compass.EstimatedScale)
		}
		if len(compass.OpenThreads) > 0 {
			fmt.Fprintf(&b, "- Tuyến dài đang hoạt động: %s\n", strings.Join(compass.OpenThreads, "; "))
		}
	} else {
		warn("story_compass", err)
	}

	// Tóm tắt quyển gần nhất, để trợ lý biết truyện vừa đi tới đâu
	if vols, err := s.Summaries.LoadAllVolumeSummaries(); len(vols) > 0 {
		last := vols[len(vols)-1]
		fmt.Fprintf(&b, "- Quyển gần nhất %q: %s\n", last.Title, truncate(last.Summary, 200))
	} else {
		warn("volume_summaries", err)
	}

	// Nhân vật chính (core/important), tối đa 8
	if chars, err := s.Characters.Load(); len(chars) > 0 {
		var names []string
		for _, c := range chars {
			if c.Tier == "secondary" || c.Tier == "decorative" {
				continue
			}
			line := c.Name
			if role := strings.TrimSpace(c.Role); role != "" {
				line += "（" + role + "）"
			}
			names = append(names, line)
			if len(names) >= 8 {
				break
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "- Nhân vật chính: %s\n", strings.Join(names, ", "))
		}
	} else {
		warn("characters", err)
	}

	// Phục bút chưa thu, tối đa 6
	if fs, err := s.World.LoadActiveForeshadow(); len(fs) > 0 {
		var items []string
		for _, f := range fs {
			items = append(items, truncate(f.Description, 40))
			if len(items) >= 6 {
				break
			}
		}
		fmt.Fprintf(&b, "- Phục bút chưa thu: %s\n", strings.Join(items, "; "))
	} else {
		warn("foreshadow", err)
	}

	if len(warnings) > 0 {
		fmt.Fprintf(&b, "- Cảnh báo dữ liệu: %s\n", strings.Join(warnings, "; "))
	}

	return strings.TrimSpace(b.String())
}

// stageSystemPrompt lắp system prompt đầy đủ của đồng sáng tạo theo giai đoạn: prompt giai đoạn + tóm tắt
// trạng thái truyện hiện tại. Tóm tắt treo ở cuối như phụ lục dữ liệu (cách ly bằng đường kẻ với đặc tả định dạng),
func stageSystemPrompt(s *store.Store) string {
	prompt := stageCoCreateSystemPrompt
	if summary := buildStoryStateSummary(s); summary != "" {
		prompt += "\n\n---\n## Trạng thái truyện hiện tại\n(Sau đây là tóm tắt khách quan của nội dung đã viết, để bạn tham chiếu khi lập kế hoạch phần sau, không được chép nguyên văn vào <draft>)\n" + summary
	}
	return prompt
}
