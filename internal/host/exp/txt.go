package exp

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// chapterTitleIndex cho số chương tra tiêu đề, thiếu trả về chuỗi rỗng.
type chapterTitleIndex map[int]string

func buildTitleIndex(outline []domain.OutlineEntry) chapterTitleIndex {
	idx := make(chapterTitleIndex, len(outline))
	for _, e := range outline {
		if e.Title != "" {
			idx[e.Chapter] = e.Title
		}
	}
	return idx
}

// chapterLocation là vị trí của một chương trong dàn ý phân tầng. Chỉ giữ thông tin tập
// mà bố cục xuất cần — cung không vào bản xuất (ở góc nhìn độc giả, cung là cấu trúc
// nội bộ quá chi tiết).
type chapterLocation struct {
	VolumeIdx       int
	VolumeTitle     string
	IsFirstOfVolume bool
}

// buildLocations dựng {chapter -> location} theo thứ tự chương toàn cục của dàn ý phân tầng.
// Số chương được dựng lại theo cùng quy tắc với FlattenOutline (cộng dồn theo thứ tự trong tập
// trong cung) để khớp với số chương của Progress.CompletedChapters. Tầng cung vẫn phải duyệt
// (bắt buộc để tính số chương toàn cục), nhưng không rơi vào location — bản xuất chỉ chèn
// phân cách ở đầu mỗi tập.
func buildLocations(volumes []domain.VolumeOutline) map[int]chapterLocation {
	if len(volumes) == 0 {
		return nil
	}
	locs := make(map[int]chapterLocation)
	ch := 0
	for _, v := range volumes {
		firstOfVol := true
		for _, a := range v.Arcs {
			for range a.Chapters {
				ch++
				locs[ch] = chapterLocation{
					VolumeIdx:       v.Index,
					VolumeTitle:     v.Title,
					IsFirstOfVolume: firstOfVol,
				}
				firstOfVol = false
			}
		}
	}
	return locs
}

// chapterHeaderRe khớp dòng đầu là tiêu đề Markdown mang số chương (# 第N章 / ## 第 12 章 ...).
// Nhận cả dạng tiếng Việt/Anh do engine đóng vào chính văn ("# Chương 12: ..." / "# Chapter 12: ...",
// xem domain.ApplyChapterHeading) để bản xuất không lặp tiêu đề hai lần.
var chapterHeaderRe = regexp.MustCompile(`^#+\s+(?:第.+?章|(?i:chương|chapter)\s*\d+)`)

// atxTitleRe trích phần chữ của tiêu đề ATX (# tiêu đề).
var atxTitleRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)

// stripChapterTitleHeader bỏ dòng đầu nếu là tiêu đề chương bị trùng lặp với tiêu đề
// thống nhất do bộ xuất tạo. Hai trường hợp: ① "# 第N章 …" (mang số chương);
// ② tiêu đề markdown mà phần chữ đúng là tiêu đề chương này (writer hay viết tên chương
// thuần túy lên dòng đầu chính văn, ví dụ "# 边村浮生", trùng với "第 N 章 边村浮生"
// do bộ xuất tạo). Các h1 khác (như "# 序章") coi là một phần chính văn, giữ nguyên.
// Bên gọi chịu trách nhiệm TrimSpace trước, nên dòng trống ở đầu không nằm trong phạm vi cân nhắc.
func stripChapterTitleHeader(content, title string) string {
	first, rest, hasNewline := strings.Cut(content, "\n")
	if !isChapterTitleLine(first, title) {
		return content
	}
	if !hasNewline {
		return ""
	}
	return strings.TrimLeft(rest, "\n")
}

func isChapterTitleLine(line, title string) bool {
	if chapterHeaderRe.MatchString(line) {
		return true
	}
	if title = strings.TrimSpace(title); title == "" {
		return false
	}
	m := atxTitleRe.FindStringSubmatch(line)
	return len(m) == 2 && strings.TrimSpace(m[1]) == title
}

// renderTXT ghép văn bản cuối cùng.
//
// Thứ tự chương do chapters quyết định (bên gọi đã sắp tăng dần theo số chương và loại trùng).
// bodies/titleIdx/locations đều xử lý theo kiểu "thiếu thì hạ cấp": thiếu tiêu đề chỉ xuất
// "Chương N"; thiếu định vị phân tầng thì coi như dàn ý phẳng.
func renderTXT(
	novelName string,
	chapters []int,
	titleIdx chapterTitleIndex,
	locations map[int]chapterLocation,
	bodies map[int]string,
) string {
	var b strings.Builder

	if name := strings.TrimSpace(novelName); name != "" {
		b.WriteString("《")
		b.WriteString(name)
		b.WriteString("》\n\n")
	}

	useLayered := len(locations) > 0

	for i, ch := range chapters {
		if useLayered {
			if loc, ok := locations[ch]; ok && loc.IsFirstOfVolume {
				b.WriteString("\n═══════════════════════════════════════════\n")
				fmt.Fprintf(&b, "           Tập %d  %s\n", loc.VolumeIdx, strings.TrimSpace(loc.VolumeTitle))
				b.WriteString("═══════════════════════════════════════════\n\n")
			}
		}

		title := strings.TrimSpace(titleIdx[ch])
		if title != "" {
			fmt.Fprintf(&b, "Chương %d  %s\n\n", ch, title)
		} else {
			fmt.Fprintf(&b, "Chương %d\n\n", ch)
		}

		body := stripChapterTitleHeader(strings.TrimSpace(bodies[ch]), title)
		b.WriteString(body)
		b.WriteString("\n")
		if i < len(chapters)-1 {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}
