package stylestat

import (
	"strings"
	"testing"
)

func chapterWith(body string) string {
	return "# 标题\n" + body
}

func TestComputeBelowMinChapters(t *testing.T) {
	in := Input{Chapters: []string{"a", "b", "c", "d"}}
	if Compute(in) != nil {
		t.Fatal("below minChapters should return nil")
	}
}

func TestComputePatterns(t *testing.T) {
	body := "他不是愤怒，而是恐惧。沉默了几息。像一盏灯。她眼中闪过慌乱，心头一紧。他觉得这是一种说不出的寒意。\n正文。\n"
	chapters := make([]string, 6)
	for i := range chapters {
		chapters[i] = chapterWith(body)
	}
	s := Compute(Input{Chapters: chapters})
	if s == nil {
		t.Fatal("expected stats")
	}
	want := map[string]int{
		"Câu hiệu chỉnh \"不是…(而)是…\"":          6,
		"Lượng từ thời gian \"X息/X瞬\"":         6,
		"Ví von trực tiếp \"像一/仿佛/如同/宛如\"":     6,
		"Nhịp im lặng \"沉默了/没有说话/没有回头\"":       6,
		"Mẫu thần thái \"眼中闪过/嘴角勾起/咬了咬唇\"":     6,
		"Phản ứng thân thể \"心头一紧/身子一颤/倒吸凉气\"": 6,
		"Dấu hiệu tư duy \"心想/意识到/感到/觉得\"":     6,
		"Sáo ngữ trừu tượng \"一种说不出的/的意义在于\"":  6,
	}
	for _, p := range s.Patterns {
		if w, ok := want[p.Name]; ok && p.Total != w {
			t.Errorf("%s total: got %d want %d", p.Name, p.Total, w)
		}
		if p.PerChapter != 1.0 {
			t.Errorf("%s per_chapter: got %v want 1.0", p.Name, p.PerChapter)
		}
	}
	if len(s.Patterns) != len(want) {
		t.Errorf("expected %d pattern classes, got %d: %+v", len(want), len(s.Patterns), s.Patterns)
	}
}

func TestComputeTopPhrasesWithStopwords(t *testing.T) {
	// "青云山巅" xuất hiện tần suất cao; "陆九渊" là tên nhân vật phải bị lọc bỏ
	line := "众人望向青云山巅，陆九渊负手而立。\n"
	chapters := make([]string, 10)
	for i := range chapters {
		chapters[i] = chapterWith(strings.Repeat(line, 3))
	}
	s := Compute(Input{Chapters: chapters, Stopwords: []string{"陆九渊"}})
	if s == nil {
		t.Fatal("expected stats")
	}
	var hasMountain, hasName bool
	for _, p := range s.TopPhrases {
		if strings.Contains(p.Text, "青云山") {
			hasMountain = true
		}
		if strings.Contains(p.Text, "九渊") || strings.Contains(p.Text, "陆九") {
			hasName = true
		}
	}
	if !hasMountain {
		t.Errorf("mong đợi cụm 青云山 được khai thác, nhận %+v", s.TopPhrases)
	}
	if hasName {
		t.Errorf("tên nhân vật phải bị lọc, nhận %+v", s.TopPhrases)
	}
}

func TestComputeRepeatedSentences(t *testing.T) {
	motto := "此生未能远行，望你替我看看远方的山海。"
	chapters := make([]string, 6)
	for i := range chapters {
		body := "平常正文，没有什么重复。\n"
		if i%2 == 0 {
			body += motto + "\n"
		}
		chapters[i] = chapterWith(body)
	}
	s := Compute(Input{Chapters: chapters})
	if s == nil {
		t.Fatal("expected stats")
	}
	if len(s.RepeatedSentences) == 0 {
		t.Fatalf("expected repeated sentence, got none")
	}
	got := s.RepeatedSentences[0]
	if got.Chapters != 3 || got.Count != 3 {
		t.Errorf("repeated sentence: %+v", got)
	}
	if !strings.HasPrefix(got.Text, "此生未能远行") {
		t.Errorf("text: %q", got.Text)
	}
}

func TestComputeEndingAndOpening(t *testing.T) {
	short := chapterWith("一整夜没有睡。\n正文很长很长很长。\n他走了。")
	long := chapterWith("白天的事。\n正文。\n这是一个非常非常非常长的结尾句子，远远超过三十个字符的阈值长度，用来测试中位数。")
	chapters := []string{short, short, short, long, long}
	s := Compute(Input{Chapters: chapters})
	if s == nil {
		t.Fatal("expected stats")
	}
	if s.Ending.ShortRatio != 0.6 {
		t.Errorf("short_ratio: got %v want 0.6", s.Ending.ShortRatio)
	}
	if s.OpeningTimeRate != 0.6 {
		t.Errorf("opening_time_rate: got %v want 0.6", s.OpeningTimeRate)
	}
}

func TestComputeTitleFormats(t *testing.T) {
	chapters := make([]string, 5)
	for i := range chapters {
		chapters[i] = chapterWith("正文。")
	}
	// Trộn lẫn → báo lên
	s := Compute(Input{Chapters: chapters, Titles: []string{"第一章 风起", "云涌", "第3章 雷动"}})
	if s.TitleFormats == nil || s.TitleFormats.WithPrefix != 2 || s.TitleFormats.WithoutPrefix != 1 {
		t.Errorf("title formats: %+v", s.TitleFormats)
	}
	// Thống nhất → không báo lên
	s = Compute(Input{Chapters: chapters, Titles: []string{"风起", "云涌"}})
	if s.TitleFormats != nil {
		t.Errorf("uniform titles should not report: %+v", s.TitleFormats)
	}
}

// Truyện tiếng Việt (mặc định sản phẩm): pattern vi phải đếm được, pattern zh
// không khớp (0-match bị lọc khỏi Patterns), regex tiêu đề/thời điểm mở màn nhận văn vi.
func TestComputeVietnamesePatterns(t *testing.T) {
	body := "Đêm đó anh khẽ nhíu mày, trái tim thắt lại. Anh im lặng như một bức tượng. Đó là một cảm giác khó tả, không nói nên lời.\nCô không nói gì.\n"
	chapters := make([]string, 6)
	for i := range chapters {
		chapters[i] = chapterWith(body)
	}
	s := Compute(Input{Chapters: chapters})
	if s == nil {
		t.Fatal("expected stats")
	}
	got := map[string]int{}
	for _, p := range s.Patterns {
		got[p.Name] = p.Total
	}
	want := map[string]int{
		`Mẫu thần thái "khẽ nhíu mày/khóe môi khẽ"`:   6,
		`Phản ứng thân thể "trái tim thắt/rùng mình"`: 6,
		`Ví von trực tiếp "như một/tựa như/như thể"`:  6,
		`Nhịp im lặng "im lặng/không nói gì"`:         12,
		`Sáo ngữ trừu tượng "một cảm giác khó tả"`:    12,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s total: got %d want %d (all=%+v)", name, got[name], w, got)
		}
	}
	// Pattern zh phải bị lọc khỏi kết quả của văn vi (0-match).
	for _, p := range s.Patterns {
		if strings.Contains(p.Name, "不是") {
			t.Errorf("pattern zh không được đếm trên văn vi: %+v", p)
		}
	}
	if s.OpeningTimeRate <= 0 {
		t.Errorf("câu mở đầu 'Đêm đó…' phải được tính là opening time, got %v", s.OpeningTimeRate)
	}
	// Regex tiêu đề chương nhận cả "Chương N" lẫn "第N章".
	if !titlePrefixRe.MatchString("Chương 12") || !titlePrefixRe.MatchString("## Chương 3") {
		t.Error("titlePrefixRe phải match 'Chương N'")
	}
	if !titlePrefixRe.MatchString("第12章") {
		t.Error("titlePrefixRe vẫn phải match '第N章' cho sách zh")
	}
	if !sentenceSplit.MatchString("Anh im lặng. Cô không nói gì!") || !sentenceSplit.MatchString("他沉默了。") {
		t.Error("sentenceSplit phải tách cả câu vi (.!?) lẫn zh (。！？)")
	}
}
