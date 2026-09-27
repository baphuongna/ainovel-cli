package domain

import "testing"

func TestWordCount(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"Hắn chậm rãi bước qua cánh cổng đá.", 8},
		{"Ánh trăng — lạnh lẽo, nhợt nhạt — phủ xuống sân.", 9},
		{"  \n\n", 0},
		{"他缓缓走过石门。", 8}, // zh: giữ nguyên số rune
		{"Số 12 ở đường Hàng-Bạc.", 5},
	}
	for _, c := range cases {
		if got := WordCount(c.text); got != c.want {
			t.Errorf("WordCount(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}
