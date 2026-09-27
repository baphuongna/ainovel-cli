package domain

import (
	"strings"
	"testing"
)

// Bốn dạng đầu chương đã gặp thật trong 15 chương của lần chạy vừa rồi.
func TestApplyChapterHeadingNormalisesRealCases(t *testing.T) {
	const title = "Chương 4: Bí Mật Về Linh Thảo"
	cases := map[string]string{
		"khong co tieu de": "Nửa canh giờ trước khi mặt trời lặn, sương mù còn dính trên lá.",
		"heading dung":     "# Chương 4: Bí Mật Về Linh Thảo\n\nNửa canh giờ trước khi mặt trời lặn.",
		"heading cap hai":  "## Chương 4: Bí Mật Về Linh Thảo\n\nNửa canh giờ trước khi mặt trời lặn.",
		"in dam":           "**Chương 4: Bí Mật Về Linh Thảo**\n\nNửa canh giờ trước khi mặt trời lặn.",
	}
	for name, content := range cases {
		got := ApplyChapterHeading(content, title, 4, "vi")
		if !strings.HasPrefix(got, "# "+title+"\n\n") {
			t.Errorf("%s: đầu ra sai\n%q", name, got)
		}
		if strings.Count(got, title) != 1 {
			t.Errorf("%s: tiêu đề bị lặp\n%q", name, got)
		}
	}
}

// Số chương trong tiêu đề phải theo số thật, không theo trí nhớ của model.
func TestFixChapterNumber(t *testing.T) {
	cases := []struct {
		in   string
		ch   int
		want string
	}{
		{"Chương 20: Gặp Linh Vân", 2, "Chương 2: Gặp Linh Vân"},
		{"Chương 7: Linh Thảo Bí Mật", 7, "Chương 7: Linh Thảo Bí Mật"},
		{"第 20 章 灵草秘密", 2, "第 2 章 灵草秘密"},
		{"Gốc cây thông cổ thụ", 5, "Gốc cây thông cổ thụ"},
	}
	for _, c := range cases {
		if got := FixChapterNumber(c.in, c.ch); got != c.want {
			t.Errorf("FixChapterNumber(%q,%d) = %q, mong đợi %q", c.in, c.ch, got, c.want)
		}
	}
}

// Câu văn mở đầu bằng chữ in đậm không được nuốt nhầm thành tiêu đề.
func TestApplyChapterHeadingKeepsBoldProse(t *testing.T) {
	content := "**Không ai** ngờ rằng đêm ấy cả thôn đều thức trắng chờ tin dữ."
	got := ApplyChapterHeading(content, "Chương 9: Đêm Trắng", 9, "vi")
	if !strings.Contains(got, "**Không ai** ngờ rằng") {
		t.Fatalf("câu văn bị nuốt mất:\n%q", got)
	}
}

// Tên chương không có số chương thì engine phải tự đóng số theo ngôn ngữ tác phẩm, kèm dấu hai chấm.
func TestApplyChapterHeadingAddsChapterNumberByLanguage(t *testing.T) {
	content := "Bát canh rong chợ Nam Minh chưa họp trọn đã tan."
	cases := []struct{ lang, want string }{
		{"vi", "# Chương 1: Bát canh rong"},
		{"zh", "# 第 1 章: Bát canh rong"},
		{"en", "# Chapter 1: Bát canh rong"},
		{"", "# Chapter 1: Bát canh rong"},
	}
	for _, c := range cases {
		got := ApplyChapterHeading(content, "Bát canh rong", 1, c.lang)
		if !strings.Contains(got, c.want) {
			t.Errorf("lang=%q: thiếu đầu đề %q trong\n%q", c.lang, c.want, got)
		}
	}
}

// Đề mục trùng tên chương nhưng KHÔNG mang số (model viết "# Bát canh rong") phải bị bóc ra,
// không để lặp tên chương hai lần khi engine đóng số vào đề mục.
func TestApplyChapterHeadingStripsUnnumberedModelHeading(t *testing.T) {
	content := "# Bát canh rong\n\nCanh rong đỡ nóng trong đêm gió mùa."
	got := ApplyChapterHeading(content, "Bát canh rong", 1, "vi")
	want := "# Chương 1: Bát canh rong\n\nCanh rong đỡ nóng trong đêm gió mùa."
	if got != want {
		t.Fatalf("đầu ra sai:\n%q\nmong đợi:\n%q", got, want)
	}
	if strings.Count(got, "Bát canh rong") != 1 {
		t.Errorf("tên chương bị lặp:\n%q", got)
	}
}

// Tên chương tiếng Trung đã mang số Hán ("第一章") không được nhân đôi thành "第 1 章: 第一章";
// tên có số Ả Rập thì được chuẩn hóa lại đúng định dạng ngôn ngữ.
func TestApplyChapterHeadingHanNumeralAndZhNormalisation(t *testing.T) {
	if got := ApplyChapterHeading("正文。", "第一章", 1, "zh"); !strings.HasPrefix(got, "# 第一章\n\n") {
		t.Errorf("số Hán bị nhân đôi: %q", got)
	}
	if got := ApplyChapterHeading("正文。", "第 20 章 灵草秘密", 2, "zh"); !strings.HasPrefix(got, "# 第 2 章: 灵草秘密\n\n") {
		t.Errorf("tiêu đề zh không chuẩn hóa: %q", got)
	}
	// Tiền đề số viết sai ngôn ngữ với tác phẩm cũng được đóng lại theo ngôn ngữ sách.
	if got := ApplyChapterHeading("正文。", "Chương 7: Linh Thảo", 7, "zh"); !strings.HasPrefix(got, "# 第 7 章: Linh Thảo\n\n") {
		t.Errorf("tiền tố sai ngôn ngữ không được chuẩn hóa: %q", got)
	}
}
