package rules

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// normalize chuẩn hóa NFC. Văn bản nhập qua /import hay sửa tay (/sync) có thể ở dạng NFD
// (dấu tiếng Việt tổ hợp tách rời): nhìn giống hệt nhưng khác byte, khiến mọi so khớp trượt.
func normalize(s string) string {
	if norm.NFC.IsNormalString(s) {
		return s
	}
	return norm.NFC.String(s)
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// countOccurrences đếm số lần needle xuất hiện trong text theo cách phù hợp với chữ viết:
//   - Có chữ Hán: đếm chuỗi con như cũ (tiếng Trung không có khoảng trắng giữa từ).
//   - Chữ Latin (tiếng Việt…): không phân biệt hoa thường — "Như thể" đầu câu vẫn là
//     "như thể" — và chỉ khớp trọn từ, để fatigue word "ta" không đếm cả "tay", "tai", "tan".
func countOccurrences(text, needle string) int {
	text, needle = normalize(text), normalize(needle)
	if needle == "" {
		return 0
	}
	if hasHan(needle) {
		return strings.Count(text, needle)
	}
	lt, ln := strings.ToLower(text), strings.ToLower(needle)
	// Chỉ kiểm tra ranh giới ở phía needle bắt đầu/kết thúc bằng chữ cái: needle là dấu câu
	// hay ký hiệu (forbidden_chars kiểu "——") thì đếm chuỗi con bình thường.
	first, _ := utf8.DecodeRuneInString(ln)
	last, _ := utf8.DecodeLastRuneInString(ln)
	checkStart, checkEnd := isWordRune(first), isWordRune(last)

	n := 0
	for i := 0; i < len(lt); {
		j := strings.Index(lt[i:], ln)
		if j < 0 {
			break
		}
		start, end := i+j, i+j+len(ln)
		ok := true
		if checkStart && start > 0 {
			if r, _ := utf8.DecodeLastRuneInString(lt[:start]); isWordRune(r) {
				ok = false
			}
		}
		if ok && checkEnd && end < len(lt) {
			if r, _ := utf8.DecodeRuneInString(lt[end:]); isWordRune(r) {
				ok = false
			}
		}
		if ok {
			n++
			i = end
		} else {
			_, size := utf8.DecodeRuneInString(lt[start:])
			i = start + size
		}
	}
	return n
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r)
}
