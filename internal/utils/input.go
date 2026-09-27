package utils

import (
	"strings"
	"unicode"
)

// CleanInputText xóa ký tự điều khiển không có ý nghĩa nghiệp vụ trong input terminal, giữ lại văn bản người dùng thấy.
// Trong trường hợp input một dòng, dấu xuống dòng và tab trong văn bản dán sẽ được chuẩn hóa thành dấu cách.
func CleanInputText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// CleanInputLine làm sạch input người một dòng, rồi bỏ khoảng trắng đầu và cuối.
func CleanInputLine(s string) string {
	return strings.TrimSpace(CleanInputText(s))
}

func CleanInputRunes(runes []rune) string {
	var b strings.Builder
	for _, r := range runes {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func ContainsControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
