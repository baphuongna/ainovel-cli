package rules

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Lint là kiểm tra đường đáy dựng sẵn của sản phẩm: quét dấu vết cơ chế còn sót trong chính văn,
// không liên quan quy tắc người dùng, luôn chạy lúc commit. Cùng hợp đồng với Check — chỉ trả
// sự thực (luật sắt một), không chặn quy trình, do việc xem xét/người dùng phán định.
//
// Hiện ba loại (đều từ khuyết điểm thực chứng của sản phẩm chạy dài thật):
//   - markdown_residue: chính văn còn sót ** đậm, dòng tiêu đề # ngoài dòng đầu (xuất txt sẽ lộ ký tự trần)
//   - non_cjk_fragments / cjk_leak: đoạn văn trộn lẫn chữ, hướng tự phán định theo chữ chính của chính văn
//     (chính văn tiếng Trung thì báo đoạn Latin; chính văn chữ Latin như tiếng Việt thì báo đoạn chữ Hán)
func Lint(text string) []Violation {
	var vs []Violation
	vs = appendMarkdownResidue(vs, text)
	vs = appendScriptMixing(vs, text)
	vs = appendBrokenWords(vs, text)
	vs = appendSelfDuplication(vs, text)
	vs = appendEnglishResidue(vs, text)
	return vs
}

func appendMarkdownResidue(vs []Violation, text string) []Violation {
	if n := strings.Count(text, "**"); n > 0 {
		vs = append(vs, Violation{
			Rule:     "markdown_residue",
			Target:   "**",
			Actual:   n,
			Severity: SeverityWarning,
		})
	}
	headings := 0
	seenContent := false
	for line := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		// Tiêu đề # của dòng khác rỗng đầu tiên là định dạng hợp pháp của file chương (không đóng cứng theo số dòng, chấp nhận dòng trống dẫn đầu)
		first := !seenContent
		seenContent = true
		if !first && strings.HasPrefix(t, "#") {
			headings++
		}
	}
	if headings > 0 {
		vs = append(vs, Violation{
			Rule:     "markdown_residue",
			Target:   "#",
			Actual:   headings,
			Severity: SeverityWarning,
		})
	}
	return vs
}

var (
	latinFragmentRe = regexp.MustCompile(`[A-Za-z]{2,}`)
	// Gồm cả dấu câu CJK: thứ lọt vào chính văn tiếng Việt không chỉ là chữ Hán, mà cả dấu
	// toàn rộng kiểu "。，、？！". Thực đo chương 4 có một "。" cô lập — quy tắc chỉ chữ Hán thuần hoàn toàn không nhìn thấy.
	cjkFragmentRe = regexp.MustCompile(`[\p{Han}\x{3000}-\x{303f}\x{ff01}-\x{ff5e}]+`)
)

// appendScriptMixing báo cáo đoạn "chữ khác loại" lẫn vào chính văn.
//
// Chữ nào tính là lẫn vào do chính văn tự quyết định, không đọc cấu hình: chính văn tiếng
// Trung lẫn trần "pattern" là khuyết điểm, còn chính văn tiếng Việt từ nào cũng là chữ Latin,
// theo cùng một quy tắc sẽ báo nhầm hàng nghìn lần mỗi chương, nhấn chìm ngữ cảnh xem xét —
// thực đo một chương trúng 1021 lần mà toàn chương không có một chữ Hán nào. Ngược lại, chính
// văn tiếng Việt lộ ra "根系之力" mới là khuyết điểm thật, quy tắc cũ hoàn toàn không nhìn thấy.
//
// Vì vậy trước tiên phán định chữ chính của chính văn theo tỷ trọng ký tự, rồi chỉ báo phe
// thiểu số. Hai loại đề tài đều đứng vững, và cấu hình viết sai cũng không mất hiệu lực. Từ
// mượn hợp pháp (tên thương hiệu/viết tắt) vẫn sẽ trúng — sự thực cấp warning, do việc xem xét phán định.
func appendScriptMixing(vs []Violation, text string) []Violation {
	latin := latinFragmentRe.FindAllString(text, -1)
	han := cjkFragmentRe.FindAllString(text, -1)

	// So sánh là "số chữ Hán" với "số từ Latin", không phải số ký tự hai bên: một chữ Hán xấp xỉ
	// một từ, còn một từ Latin có vài chữ cái. So theo số ký tự, chính văn tiếng Trung lẫn vài
	// "pattern"/"DNA" sẽ đẩy số ký tự Latin vượt số chữ Hán, dẫn tới phán đoán sai ngôn ngữ chính văn.
	rule, matches := "non_cjk_fragments", latin
	if runeCount(han) <= len(latin) {
		// Chính văn là chữ Latin (tiếng Việt v.v.): chữ Hán mới là thứ lẫn vào.
		rule, matches = "cjk_leak", han
	}
	if len(matches) == 0 {
		return vs
	}

	seen := make(map[string]struct{})
	var examples []string
	for _, m := range matches {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		if len(examples) < 3 {
			examples = append(examples, m)
		}
	}
	return append(vs, Violation{
		Rule:     rule,
		Target:   strings.Join(examples, "、"),
		Actual:   len(matches),
		Severity: SeverityWarning,
	})
}

func runeCount(ss []string) int {
	n := 0
	for _, s := range ss {
		n += utf8.RuneCountInString(s)
	}
	return n
}

// brokenWordRe khớp từ bị cắt đôi bởi phân cách đoạn: một dòng kết thúc bằng chữ cái, vượt qua
// dòng trống rồi lại bắt đầu bằng chữ thường. Thực đo chính văn tiếng Việt: "n Tông, Ng" +
// dòng trống + "ọc Lâm dừng bước" — tên riêng Ngọc bị bổ làm hai nửa. Trong chữ Latin một từ
// không thể vắt qua đoạn, vì vậy hình thù này không có cách viết chính đáng, có thể phán là khuyết điểm luôn.
var brokenWordRe = regexp.MustCompile(`(?m)[\p{L}]\n\s*\n[[:space:]]*[\p{Ll}]`)

func appendBrokenWords(vs []Violation, text string) []Violation {
	matches := brokenWordRe.FindAllString(text, -1)
	if len(matches) == 0 {
		return vs
	}
	return append(vs, Violation{
		Rule:     "broken_word",
		Target:   strings.Join(strings.Fields(matches[0]), "⏎"),
		Actual:   len(matches),
		Severity: SeverityWarning,
	})
}

// paraMinRunes là giới hạn dưới của đoạn văn tham gia so sánh lặp. Đoạn ngắn vốn dễ trùng
// ("Hắn gật đầu.", một tiếng "Bắt đầu!"), chỉ có văn thành đoạn tái hiện nguyên văn mới là sự cố sinh nội dung.
const paraMinRunes = 20

// paragraphs cắt ra các đoạn chính văn đủ dài, đáng so sánh, bỏ qua dòng tiêu đề.
func paragraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(text, "\n\n") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "#") {
			continue
		}
		if utf8.RuneCountInString(p) >= paraMinRunes {
			out = append(out, p)
		}
	}
	return out
}

// selfDupThreshold / crossDupThreshold là giới hạn trên tỷ lệ ký tự lặp trên chương này.
//
// Ngưỡng từ thực đo: trong một cuốn 22 chương, 19 chương cả hai mục đều 0.0%, ba chương xảy
// ra sự cố lần lượt là tự lặp 43.5% (chương 21, cùng một đoạn xuất hiện 5 lần), xuyên chương
// 32.2% (chương 14 chép chương 13) và 12.9% (chương 22 chép chương 21). Tín hiệu là nhị phân,
// ở giữa không có vùng xám, nên lấy 10% chừa đủ dư địa cho "điệp khúc/chú ngục cố ý lặp lại".
const (
	selfDupThreshold  = 0.10
	crossDupThreshold = 0.10
)

func appendSelfDuplication(vs []Violation, text string) []Violation {
	paras := paragraphs(text)
	if len(paras) == 0 {
		return vs
	}
	count := make(map[string]int, len(paras))
	total := 0
	for _, p := range paras {
		count[p]++
		total += utf8.RuneCountInString(p)
	}
	dup, worst, worstN := 0, "", 0
	for p, n := range count {
		if n > 1 {
			dup += utf8.RuneCountInString(p) * (n - 1)
			if n > worstN {
				worst, worstN = p, n
			}
		}
	}
	if total == 0 || float64(dup)/float64(total) < selfDupThreshold {
		return vs
	}
	return append(vs, Violation{
		Rule:     "self_duplication",
		Target:   fmt.Sprintf("×%d %s", worstN, truncateRunes(worst, 50)),
		Limit:    fmt.Sprintf("<%.0f%%", selfDupThreshold*100),
		Actual:   fmt.Sprintf("%.0f%%", 100*float64(dup)/float64(total)),
		Severity: SeverityError,
	})
}

// CheckAgainstPrevious phát hiện "chương mới thực ra là bản sao của chương trước".
//
// Writer có read_chapter, sẽ xem chương trước viết gì rồi chép lại coi như tiếp viết — thực
// đo chương 14 có 32.2% ký tự lấy nguyên văn từ chương 13, chương 22 có 12.9% từ chương 21.
//
// self_duplication không nhìn thấy sự cố loại này: nó chỉ thống kê trong phạm vi một chương,
// mà chương chép đó bên trong hoàn toàn sạch. Vì vậy phải so riêng theo "văn bản trước".
// previous rỗng thì bỏ qua.
func CheckAgainstPrevious(text string, previous []string) []Violation {
	cur := paragraphs(text)
	if len(cur) == 0 || len(previous) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	for _, prev := range previous {
		for _, p := range paragraphs(prev) {
			seen[p] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}

	dup, total, sample := 0, 0, ""
	for _, p := range unique(cur) {
		n := utf8.RuneCountInString(p)
		total += n
		if _, ok := seen[p]; ok {
			dup += n
			if sample == "" {
				sample = p
			}
		}
	}
	if total == 0 || float64(dup)/float64(total) < crossDupThreshold {
		return nil
	}
	return []Violation{{
		Rule:     "copied_previous_chapter",
		Target:   truncateRunes(sample, 60),
		Limit:    fmt.Sprintf("<%.0f%%", crossDupThreshold*100),
		Actual:   fmt.Sprintf("%.0f%%", 100*float64(dup)/float64(total)),
		Severity: SeverityError,
	}}
}

func unique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0:0]
	for _, x := range in {
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// englishFunctionWordRe chỉ thu hư từ tiếng Anh — chúng tuyệt đối không thể vào chính văn
// tiếng Việt như từ mượn. Từ nội dung (flow / lesson / footsteps v.v.) cố ý không thu: trong
// đề tài hiện đại có thể là ngoại ngữ hợp lý, mà cụm kiểu "the flow" đã bị the trong nó bắt,
// không cần mạo hiểm báo nhầm.
var englishFunctionWordRe = regexp.MustCompile(
	`(?i)\b(not|the|and|but|for|from|with|they|this|that|just|one|was|were|have|when|which|while|into|over)\b`)

// vietnameseMarkRe khớp chữ riêng của tiếng Việt: 7 chữ cơ sở (ăâđêôơư, kể cả in hoa) cộng
// vùng Latin Extended Additional U+1EA0-U+1EF9 — vùng này gần như chuyên cho chữ mang dấu
// thanh tiếng Việt. Chỉ liệt kê vài ký tự tổ hợp sẵn là chưa đủ: trong một câu tiếng Việt
// thường, hơn nửa chữ mang dấu đều nằm trong vùng đó.
var vietnameseMarkRe = regexp.MustCompile(`[ăâđêôơưĂÂĐÊÔƠƯ\x{1ea0}-\x{1ef9}]`)

// vietnameseMarkFloor là số chữ riêng cần có để phán định "chính văn là tiếng Việt".
// Một chương thật có hàng trăm hàng nghìn; đặt giới hạn dưới chỉ để quy tắc này im hẳn
// trong tác phẩm tiếng Anh/tiếng Trung.
const vietnameseMarkFloor = 12

// appendEnglishResidue báo cáo hư từ tiếng Anh xen vào chính văn tiếng Việt.
//
// cjk_leak hoàn toàn bất lực với việc này: tiếng Việt và tiếng Việt cùng thuộc chữ Latin,
// "not"/"the" lẫn vào không thể phân biệt với chính văn ở tầng ký tự. Mà đây không phải chuyện
// nhỏ — thực đo chương 18 xuất hiện 27 lần not, 9 lần the, 3 lần from, ví dụ
// "Lá cây bắt đầu chuyển động—not nhanh chóng mà nhẹ nhàng".
func appendEnglishResidue(vs []Violation, text string) []Violation {
	if len(vietnameseMarkRe.FindAllString(text, vietnameseMarkFloor)) < vietnameseMarkFloor {
		return vs
	}
	matches := englishFunctionWordRe.FindAllString(text, -1)
	if len(matches) == 0 {
		return vs
	}
	seen := make(map[string]struct{})
	var examples []string
	for _, m := range matches {
		low := strings.ToLower(m)
		if _, ok := seen[low]; ok {
			continue
		}
		seen[low] = struct{}{}
		if len(examples) < 4 {
			examples = append(examples, low)
		}
	}
	return append(vs, Violation{
		Rule:     "english_residue",
		Target:   strings.Join(examples, ", "),
		Actual:   len(matches),
		Severity: SeverityError,
	})
}
