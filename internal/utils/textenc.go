package utils

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// DecodeText giải mã byte của tệp văn bản người dùng cung cấp sang UTF-8:
// nếu không phải UTF-8 hợp lệ thì chuyển qua GB18030 (siêu tập GBK) -
// các truyện tiếng Trung dạng txt trên mạng thường mã hóa GBK, đọc trực tiếp
// là toàn rác. Byte không phải GBK sẽ được bộ giải mã thay bằng U+FFFD
// (vốn đã là rác, lỗi buộc phải từ phía gọi sẽ hướng dẫn người dùng).
// Cuối cùng loại bỏ UTF-8 BOM (nếu không thì khớp đầu dòng sẽ dính nó).
func DecodeText(data []byte) string {
	if !utf8.Valid(data) {
		if decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data); err == nil {
			data = decoded
		}
	}
	return strings.TrimPrefix(string(data), "\uFEFF")
}
