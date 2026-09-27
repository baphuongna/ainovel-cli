package styles

import "testing"

func TestResolveStyleFromGenre(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		// Các ca bắt buộc theo đặc tả.
		{name: "tiên hiệp", text: "tiên hiệp", want: "wuxia", ok: true},
		{name: "xianxia", text: "xianxia", want: "wuxia", ok: true},
		{name: "fantasy", text: "fantasy", want: "fantasy", ok: true},
		{name: "ngôn tình", text: "ngôn tình", want: "romance", ok: true},
		{name: "không khớp", text: "unknown genre", want: "", ok: false},

		// Hoa thường + bỏ dấu: cùng một alias viết nhiều kiểu vẫn ra cùng key.
		{name: "HOA toàn bộ", text: "TIÊN HIỆP", want: "wuxia", ok: true},
		{name: "Hoa đầu từ", text: "Tien Hiep", want: "wuxia", ok: true},
		{name: "tiếng Việt đủ dấu hoa", text: "Tu Tiên", want: "wuxia", ok: true},
		{name: "huyền huyễn không dấu", text: "huyen huyen", want: "wuxia", ok: true},
		{name: "KINH DỊ hoa", text: "KINH DỊ", want: "suspense", ok: true},
		{name: "kỳ ảo không dấu", text: "ky ao", want: "fantasy", ok: true},
		{name: "chung", text: "chung", want: "default", ok: true},

		// Genre tự do là câu dài: fallback khớp alias theo ranh giới từ.
		{name: "genre có tiền tố", text: "truyện tiên hiệp", want: "wuxia", ok: true},
		// "ngon tinh" và "kiem hiep" cùng 9 ký tự sau fold — hoà độ dài thì option
		// đăng ký trước trong StyleOptions (romance) thắng, theo đúng doc của hàm.
		{name: "genre nhiều alias hoà độ dài", text: "ngôn tình, kiếm hiệp", want: "romance", ok: true},
		{name: "genre chứa phụ đờ", text: "thể loại võ hiệp cổ trang", want: "wuxia", ok: true},
		{name: "genre có dấu câu", text: "Trinh thám - Kinh dị", want: "suspense", ok: true},

		// Không khớp: trả ("", false), không đoán bừa.
		{name: "rỗng", text: "", want: "", ok: false},
		{name: "chỉ khoảng trắng", text: "   ", want: "", ok: false},
		{name: "từ khác hoàn toàn", text: "hài hước văn phòng", want: "", ok: false},
		{name: "alias bị cắt giữa từ", text: "xianxiaworld", want: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveStyleFromGenre(tt.text)
			if got != tt.want || ok != tt.ok {
				t.Errorf("ResolveStyleFromGenre(%q) = (%q, %v), want (%q, %v)", tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestLabelFor(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"wuxia", "Võ hiệp / Tu tiên"},
		{"default", "Chung (không đặc thù)"},
		{"fantasy", "Kỳ ảo / Fantasy"},
		{"romance", "Ngôn tình"},
		{"suspense", "Trinh thám / Kinh dị"},
		// Tương thích ngược: key lạ/config cũ trả "", không panic, không lỗi.
		{"", ""},
		{"khong-ton-tai", ""},
	}
	for _, tt := range tests {
		if got := LabelFor(tt.key); got != tt.want {
			t.Errorf("LabelFor(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestStyleOptionsContract(t *testing.T) {
	// Registry là nguồn chân lý dùng chung cho setup/config/genre: key phải duy nhất,
	// nhãn không rỗng, và mỗi alias phải khớp đúng về style của nó.
	seen := map[string]bool{}
	for _, opt := range StyleOptions {
		if opt.Key == "" {
			t.Fatal("StyleOptions có Key rỗng")
		}
		if seen[opt.Key] {
			t.Errorf("Key trùng lặp trong registry: %q", opt.Key)
		}
		seen[opt.Key] = true
		if opt.Label == "" || opt.Description == "" {
			t.Errorf("style %q thiếu Label hoặc Description", opt.Key)
		}
		for _, alias := range opt.Aliases {
			if key, ok := ResolveStyleFromGenre(alias); !ok || key != opt.Key {
				t.Errorf("alias %q của style %q không resolve về chính nó: (%q, %v)", alias, opt.Key, key, ok)
			}
		}
	}
}
