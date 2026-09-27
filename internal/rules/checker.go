package rules

import (
	"strings"
)

// Check kiểm tra cơ học chính văn chương theo quy tắc cấu trúc, trả danh sách sự thực vi phạm.
//
// Hợp đồng thiết kế:
//   - Chỉ trả sự thực, không ra chỉ lệnh (luật sắt một)
//   - Không chặn quy trình của bất kỳ nơi gọi nào
//   - severity ánh xạ cố định theo loại quy tắc (xem bảng chú thích trong types.go)
//
// Tham số:
//   - text: chính văn chương (chính văn bản cuối hay bản nháp đều được)
//   - s: quy tắc cấu trúc sau hợp nhất; IsEmpty thì trả nil ngay.
func Check(text string, s Structured) []Violation {
	if s.IsEmpty() {
		return nil
	}

	var violations []Violation
	violations = appendForbiddenChars(violations, text, s.ForbiddenChars)
	violations = appendForbiddenPhrases(violations, text, s.ForbiddenPhrases)
	violations = appendFatigueWords(violations, text, s.FatigueWords)
	return violations
}

// forbidden_chars: xuất hiện ≥1 lần là error.
// Cùng một quy tắc chỉ sinh một violation, actual là số lần xuất hiện.
func appendForbiddenChars(vs []Violation, text string, list []string) []Violation {
	for _, ch := range list {
		if ch == "" {
			continue
		}
		n := strings.Count(text, ch)
		if n == 0 {
			continue
		}
		vs = append(vs, Violation{
			Rule:     "forbidden_chars",
			Target:   ch,
			Actual:   n,
			Severity: SeverityError,
		})
	}
	return vs
}

// forbidden_phrases: xuất hiện ≥1 lần là error; hành vi giống forbidden_chars, chỉ khác tên rule.
func appendForbiddenPhrases(vs []Violation, text string, list []string) []Violation {
	for _, ph := range list {
		if ph == "" {
			continue
		}
		n := strings.Count(text, ph)
		if n == 0 {
			continue
		}
		vs = append(vs, Violation{
			Rule:     "forbidden_phrases",
			Target:   ph,
			Actual:   n,
			Severity: SeverityError,
		})
	}
	return vs
}

// fatigue_words: chỉ vi phạm khi số lần xuất hiện trong chương vượt ngưỡng, cấp warning.
// Không cộng dồn xuyên chương — vấn đề xuyên chương sau đó giao cho chẩn đoán.
func appendFatigueWords(vs []Violation, text string, m map[string]int) []Violation {
	for word, limit := range m {
		if word == "" || limit <= 0 {
			continue
		}
		n := strings.Count(text, word)
		if n <= limit {
			continue
		}
		vs = append(vs, Violation{
			Rule:     "fatigue_words",
			Target:   word,
			Limit:    limit,
			Actual:   n,
			Severity: SeverityWarning,
		})
	}
	return vs
}
