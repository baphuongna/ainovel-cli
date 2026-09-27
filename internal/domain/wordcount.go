package domain

import (
	"unicode"
	"unicode/utf8"
)

// WordCount đếm độ dài chính văn theo đơn vị tự nhiên của ngôn ngữ, tự nhận diện theo
// chữ viết của chính văn bản (không cần biết cấu hình language):
//   - Văn chủ yếu chữ Hán: số rune — "字数" truyền thống của tiểu thuyết mạng, giữ nguyên
//     hành vi cũ.
//   - Văn chữ Latin (tiếng Việt…): số từ (dãy chữ cái/chữ số liên tiếp). Đếm rune cho tiếng
//     Việt sẽ phóng đại ~5 lần so với "số từ" người dùng nói ("mỗi chương 3000 từ" trong rules
//     tương ứng khoảng 14-15 nghìn ký tự), làm lệch con số hiển thị trong TUI, /diag và
//     total_word_count đưa cho agent.
func WordCount(text string) int {
	han, letters, words := 0, 0, 0
	inWord := false
	for _, r := range text {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF:
			han++
			inWord = false
		case unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r):
			letters++
			if !inWord {
				words++
				inWord = true
			}
		case r == '-' || r == '\'' || r == '’':
			// Giữ nguyên từ ghép có gạch nối / nháy ("e-mail", "don't").
		default:
			inWord = false
		}
	}
	if han == 0 && letters == 0 {
		return 0
	}
	if han >= letters {
		return utf8.RuneCountInString(text)
	}
	return words
}
