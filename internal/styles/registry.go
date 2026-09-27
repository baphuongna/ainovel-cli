// Package styles là nguồn chân lý duy nhất của hệ thống style: key bền vững để lưu config,
// nhãn tiếng Việt để hiển thị, và alias thể loại để suy style từ genre tự do.
//
// Style KEY không bao giờ Việt hoá (giữ "wuxia", không đổi thành "tu tiên") — chỉ nhãn
// hiển thị và alias suy đoán là tiếng Việt, để config cũ/mới luôn tương thích.
package styles

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// StyleOption mô tả một preset style người dùng có thể chọn.
type StyleOption struct {
	// Key là định danh bền vững lưu trong config (ví dụ "wuxia"); không đổi khi Việt hoá nhãn.
	Key string
	// Label là nhãn tiếng Việt hiển thị trong setup wizard và /config.
	Label string
	// Description là mô tả ngắn tiếng Việt đi kèm nhãn.
	Description string
	// Aliases là các từ khóa thể loại map về style này; so khớp bỏ hoa thường và dấu.
	Aliases []string
}

// StyleOptions là toàn bộ registry, theo thứ tự hiển thị trong setup wizard và /config.
// Thứ tự đăng ký cũng là thứ tự ưu tiên khi hai alias cùng độ dài cùng khớp một chuỗi genre.
var StyleOptions = []StyleOption{
	{Key: "default", Label: "Chung (không đặc thù)", Description: "Không áp style đặc thù.", Aliases: []string{"default", "chung"}},
	{Key: "fantasy", Label: "Kỳ ảo / Fantasy", Description: "Thế giới phép thuật, sinh vật thần thoại.", Aliases: []string{"fantasy", "kỳ ảo"}},
	{Key: "romance", Label: "Ngôn tình", Description: "Tình yêu, mối quan hệ lãng mạn.", Aliases: []string{"romance", "ngôn tình"}},
	{Key: "suspense", Label: "Trinh thám / Kinh dị", Description: "Bí ẩn, kinh dị, gay cấn.", Aliases: []string{"suspense", "trinh thám", "kinh dị"}},
	{Key: "wuxia", Label: "Võ hiệp / Tu tiên", Description: "Võ công, tu tiên, kiếm hiệp.", Aliases: []string{"wuxia", "tu tiên", "tiên hiệp", "kiếm hiệp", "huyền huyễn", "xianxia", "võ hiệp"}},
}

// foldText chuẩn hoá về dạng so khớp: tách dấu (NFD) rồi bỏ dấu thanh/mác, đ→d,
// thường hoá, mọi ký tự không phải chữ/số thành khoảng trắng, cuối cùng gộp khoảng trắng thừa.
// Sau fold, "Tiên Hiệp" và "tien hiep" cho cùng kết quả.
func foldText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		switch {
		case r == 'đ' || r == 'Đ':
			// đ là chữ Latin duy nhất không tách dấu được bằng NFD, phải map tay.
			b.WriteByte('d')
		case unicode.Is(unicode.Mn, r):
			// Dấu thanh/mác đã tách rời sau NFD — bỏ hẳn.
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// containsWord báo alias có xuất hiện trong text theo ranh giới từ (không cắt giữa từ).
// Cả hai chuỗi phải đã qua foldText nên chỉ chứa chữ thường, chữ số và khoảng trắng đơn.
func containsWord(text, alias string) bool {
	for i := 0; i+len(alias) <= len(text); i++ {
		if text[i:i+len(alias)] != alias {
			continue
		}
		beforeOK := i == 0 || text[i-1] == ' '
		afterOK := i+len(alias) == len(text) || text[i+len(alias)] == ' '
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

// ResolveStyleFromGenre suy style key từ chuỗi thể loại tự do (user_rules.genre, yêu cầu mở đầu…).
//
// So khớp bỏ hoa thường và dấu: "Tiên Hiệp" hay "tien hiep" đều ra "wuxia".
// Ưu tiên khớp nguyên chuỗi; nếu trượt, chấp nhận alias dài nhất xuất hiện theo ranh giới từ,
// để các chuỗi ghép như "truyện tiên hiệp" hay "trinh thám - kinh dị" vẫn suy ra được style.
// Hai alias khớp cùng độ dài thì option đăng ký trước trong StyleOptions thắng — kết quả luôn tất định.
// Không khớp gì thì trả về ("", false) — caller giữ nguyên style hiện có, không đoán bừa.
func ResolveStyleFromGenre(text string) (key string, ok bool) {
	n := foldText(text)
	if n == "" {
		return "", false
	}
	for _, opt := range StyleOptions {
		for _, alias := range opt.Aliases {
			if n == foldText(alias) {
				return opt.Key, true
			}
		}
	}
	bestKey, bestLen := "", 0
	for _, opt := range StyleOptions {
		for _, alias := range opt.Aliases {
			fa := foldText(alias)
			if len(fa) > bestLen && containsWord(n, fa) {
				bestKey, bestLen = opt.Key, len(fa)
			}
		}
	}
	if bestKey != "" {
		return bestKey, true
	}
	return "", false
}

// LabelFor trả về nhãn tiếng Việt hiển thị cho style key ("wuxia" → "Võ hiệp / Tu tiên").
// Key không có trong registry (config cũ, key lạ) thì trả về "" — không lỗi,
// để caller tự fallback (ví dụ hiển thị chính key đó) và giữ tương thích ngược.
func LabelFor(key string) string {
	for _, opt := range StyleOptions {
		if opt.Key == key {
			return opt.Label
		}
	}
	return ""
}
