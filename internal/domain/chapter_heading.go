package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Cách viết số chương chọn một trong ba ngôn ngữ: tiếng Việt "Chương 12", tiếng Trung "第 12 章", tiếng Anh "Chapter 12".
	chapterNumberRe = regexp.MustCompile(`^\s*(?i:(chương|chapter)\s*(\d+)|第\s*(\d+)\s*章)`)

	// Trang trí tiêu đề dòng đầu do model đưa ra: dòng đề mục từ # đến ######, hoặc cả dòng **in đậm**.
	headingDecorRe = regexp.MustCompile(`^\s*(?:#{1,6}\s*|\*\*)\s*|\s*\*\*\s*$`)

	// Tiền tố số chương đứng đầu TÊN chương (khác với dòng đề mục trong chính văn): dùng bóc phần chữ
	// thuần túy của tên chương trước khi engine tự đóng số chương theo ngôn ngữ. Rộng hơn chapterNumberRe
	// một bậc: nuốt luôn dấu phân cách theo sau (":"/"："/gạch/điểm), và chấp nhận cả số Hán (第一章)
	// để không nhân đôi thành "第 1 章: 第一章" khi tên chương vốn chỉ là số chương.
	chapterTitlePrefixRe = regexp.MustCompile(`^\s*(?i:(?:chương|chapter)\s*\d+|第\s*[0-9一二三四五六七八九十百千零两]+\s*章)\s*[:：.、\-—–]?\s*`)
)

// FixChapterNumber đổi số chương trong tiêu đề thành số chương thật.
//
// Sự cố thực đo: dàn ý sinh trong kỳ quy hoạch đặt tên chương là "Chương 19..23", khi lưu
// xuống đĩa thực tế là chương 1..5, thế là ChapterRecord có chapter=2 mà title="Chương 20"
// — cùng một bản ghi tự mâu thuẫn, mục lục độc giả thấy bị nhảy số. Số chương là sự kiện
// engine đã biết, không nên do trí nhớ của model quyết định.
//
// Tiêu đề không chứa số chương nhận diện được thì trả về nguyên dạng: không đoán, không nhét cứng.
func FixChapterNumber(title string, chapter int) string {
	m := chapterNumberRe.FindStringSubmatchIndex(title)
	if m == nil || chapter <= 0 {
		return title
	}
	head := title[m[0]:m[1]]
	for _, g := range [][2]int{{m[4], m[5]}, {m[6], m[7]}} {
		if g[0] < 0 {
			continue
		}
		if n, err := strconv.Atoi(title[g[0]:g[1]]); err == nil && n != chapter {
			// Chỉ thay con số, giữ nguyên cách viết và hoa thường gốc (Chương / 第…章 / Chapter).
			head = head[:g[0]-m[0]] + strconv.Itoa(chapter) + head[g[1]-m[0]:]
		}
	}
	return head + title[m[1]:]
}

// chapterNumberLabel đóng số chương theo ngôn ngữ tác phẩm: vi "Chương N", zh "第 N 章",
// ngôn ngữ khác (en) về "Chapter N". Giữ đồng bộ định dạng với chapterFmt trong internal/store/labels.go
// ("Chương %d" / "第 %d 章") — đề mục engine và nhãn store phải gọi chương theo cùng một cách.
func chapterNumberLabel(lang string, chapter int) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "vi":
		return fmt.Sprintf("Chương %d", chapter)
	case "zh":
		return fmt.Sprintf("第 %d 章", chapter)
	default:
		return fmt.Sprintf("Chapter %d", chapter)
	}
}

// chapterHeadingText render tên chương hoàn chỉnh: số chương do engine đóng lên đầu, tách khỏi
// phần chữ thuần túy bằng ": ". Tiêu đề đã kèm tiền tố số ("Chương 4: Bí Mật", "第 20 章 灵草秘密")
// thì bóc tiền tố ra rồi đóng lại theo đúng ngôn ngữ, tránh lặp "Chương 4: Chương 4:". Tiêu đề
// chỉ toàn số chương ("Chương 4", "第一章") giữ nguyên — số đã có, phần chữ rỗng.
func chapterHeadingText(title string, chapter int, lang string) string {
	if chapter <= 0 {
		return title
	}
	rest := strings.TrimSpace(chapterTitlePrefixRe.ReplaceAllString(title, ""))
	if rest == "" {
		return title
	}
	return fmt.Sprintf("%s: %s", chapterNumberLabel(lang, chapter), rest)
}

// ApplyChapterHeading bảo đảm dòng đầu chính văn là đề mục cấp một chuẩn mực, luôn mang số chương
// theo ngôn ngữ tác phẩm: vi "# Chương N: ...", zh "# 第 N 章: ...", ngôn ngữ khác "# Chapter N: ...".
//
// Engine đã giữ đúng tiêu đề trong ChapterFacts.Title, nhưng lại lưu chính văn model xuất ra
// nguyên dạng xuống đĩa: thực đo 15 chương thì 9 chương hoàn toàn không có dòng tiêu đề, 1 chương
// viết thành ## cấp hai, 1 chương viết thành **in đậm**. Tiêu đề là cấu trúc, không phải sáng
// tác — ở đây thống nhất do engine kết xuất, model viết hay không đều không ảnh hưởng thành phẩm.
//
// Chỉ thay dòng đó khi dòng đầu thực sự là đề mục (trùng với title, hoặc mang tiền tố số
// chương), nếu không thì nhất luật nối vào trước, tránh ăn nhầm một câu chính văn mở đầu bằng in đậm
// thành tiêu đề.
func ApplyChapterHeading(content, title string, chapter int, lang string) string {
	title = strings.TrimSpace(FixChapterNumber(strings.TrimSpace(title), chapter))
	if title == "" {
		return content
	}
	bareTitle := strings.TrimSpace(chapterTitlePrefixRe.ReplaceAllString(title, ""))
	heading := chapterHeadingText(title, chapter, lang)

	lines := strings.Split(content, "\n")
	first := 0
	for first < len(lines) && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	// So khớp cả tên đầy đủ lẫn phần chữ thuần túy: đề mục do engine đóng mang số chương, còn model
	// hay viết tên chương không số ("# Bát canh rong") — cả hai dạng đều phải bóc ra, không lặp lại.
	if first < len(lines) && isChapterHeadingLine(lines[first], title, bareTitle) {
		lines = lines[first+1:]
	} else {
		lines = lines[first:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return fmt.Sprintf("# %s\n\n%s", heading, strings.Join(lines, "\n"))
}

func isChapterHeadingLine(line string, titles ...string) bool {
	bare := strings.TrimSpace(headingDecorRe.ReplaceAllString(strings.TrimSpace(line), ""))
	if bare == "" {
		return false
	}
	for _, t := range titles {
		if t = strings.TrimSpace(t); t != "" && strings.EqualFold(bare, t) {
			return true
		}
	}
	// Mở đầu bằng số chương và đủ ngắn: là dòng đề mục, không phải đoạn chính văn.
	return chapterNumberRe.MatchString(bare) && len([]rune(bare)) <= 80
}
