// Package stylestat thống kê phong cách cấp toàn sách trên chính văn đã viết, chỉ xuất ra sự thật thuần túy.
//
// Động cơ: cửa sổ xem xét theo cung (~10 chương) vốn mù bẩm sinh với sự cố định
// mô hình cấp toàn sách — tic câu trường xuất hiện vài chục lần mỗi chương, hình thái
// cuối chương đồng nhất, lặp nguyên văn xuyên chương; nhìn riêng từng chương thì chỗ
// nào cũng "bình thường", chỉ thống kê toàn sách mới bộc lộ được. Thống kê thuộc về
// code (xác định, không ảo giác), phán định thuộc về LLM (editor chấm điểm chiều cạnh
// theo con số, writer dựa vào đó để tự né). Compute dùng cho đánh giá offline tính
// toàn lượng một lần; runtime dùng Tracker bảo trì tăng dần theo từng chương.
package stylestat

import (
	"regexp"
	"sort"
	"strings"
)

// minChapters ít hơn số chương này thì không xuất thống kê — mẫu quá nhỏ, tần suất vô nghĩa.
const minChapters = 5

// phraseWindow khai thác cụm từ động chỉ nhìn N chương gần nhất: thứ writer cần tránh là "câu cửa miệng hiện tại".
const phraseWindow = 20

// Input đầu vào thống kê. Chapters theo số chương tăng dần; Stopwords là tên riêng như tên nhân vật,
// bị bỏ qua khi khai thác cụm từ động (tên nhân vật xuất hiện vốn dĩ tần suất cao, không phải vấn đề văn phong).
type Input struct {
	Chapters  []string
	Titles    []string
	Stopwords []string
}

// Stats kết quả thống kê phong cách toàn sách. Mọi trường đều là đếm sự thật, không chứa phán định hay chỉ lệnh nào.
type Stats struct {
	Chapters          int            `json:"chapters"`
	Patterns          []PatternStat  `json:"patterns,omitempty"`
	TopPhrases        []PhraseStat   `json:"top_phrases,omitempty"`
	RepeatedSentences []SentenceStat `json:"repeated_sentences,omitempty"`
	Ending            EndingStat     `json:"ending"`
	OpeningTimeRate   float64        `json:"opening_time_rate"`
	TitleFormats      *TitleStat     `json:"title_formats,omitempty"`
}

// PatternStat số đếm toàn sách của lớp mẫu câu cố định (tic văn phong AI phổ biến).
type PatternStat struct {
	Name       string  `json:"name"`
	Total      int     `json:"total"`
	PerChapter float64 `json:"per_chapter"`
}

// PhraseStat cụm từ tần suất cao được khai thác trong phraseWindow chương gần nhất.
type PhraseStat struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

// SentenceStat câu dài lặp nguyên văn xuyên chương (bằng chứng trực tiếp của việc lặp lại phần kể).
type SentenceStat struct {
	Text     string `json:"text"`
	Chapters int    `json:"chapters"`
	Count    int    `json:"count"`
}

// EndingStat phân bố hình thái dòng cuối chương. Kết thúc ngắn tự nó hợp pháp, đồng nhất toàn sách mới là vấn đề.
type EndingStat struct {
	ShortRatio  float64 `json:"short_ratio"`
	MedianRunes int     `json:"median_runes"`
}

// TitleStat đếm việc trộn lẫn tiền tố "第N章" trong tiêu đề chương (trộn lẫn = dấu vết cơ chế lộ ra sản phẩm).
type TitleStat struct {
	WithPrefix    int `json:"with_prefix"`
	WithoutPrefix int `json:"without_prefix"`
}

// patternDefs mẫu câu văn phong AI phổ biến. Số đếm là gần đúng (regex không phân tích ngữ pháp),
// mục đích là so sánh cơ sở dọc của chính cuốn sách, độ chính xác tuyệt đối không quan trọng.
var patternDefs = []struct {
	name string
	re   *regexp.Regexp
	// exclude (tùy chọn) trừ bớt các lần khớp nằm trong ngữ cảnh vô hại, thay cho
	// lookbehind mà regexp của Go không hỗ trợ.
	exclude *regexp.Regexp
}{
	{name: "Câu hiệu chỉnh \"不是…(而)是…\"", re: regexp.MustCompile(`不是[^。！？\n]{1,24}?[，、]?(?:而)?是`)},
	{name: "Lượng từ thời gian \"X息/X瞬\"", re: regexp.MustCompile(`[一两二三四五六七八九十几数半][息瞬]`)},
	{name: "Ví von trực tiếp \"像一/仿佛/如同/宛如\"", re: regexp.MustCompile(`像一|仿佛|如同|宛如`)},
	{name: "Nhịp im lặng \"沉默了/没有说话/没有回头\"", re: regexp.MustCompile(`沉默了|没有说话|没有回头`)},
	{name: "Mẫu thần thái \"眼中闪过/嘴角勾起/咬了咬唇\"", re: regexp.MustCompile(`眼[中底]闪过|目光一凝|瞳孔一缩|眼眶微红|嘴角[微轻一]?[勾扬翘]|咬了咬唇|不可置信`)},
	{name: "Phản ứng thân thể \"心头一紧/身子一颤/倒吸凉气\"", re: regexp.MustCompile(`心头一[紧沉颤]|身子一[颤震僵]|倒吸(?:了)?一口凉气`)},
	{name: "Dấu hiệu tư duy \"心想/意识到/感到/觉得\"", re: regexp.MustCompile(`心想|意识到|感到|觉得`)},
	{name: "Sáo ngữ trừu tượng \"一种说不出的/的意义在于\"", re: regexp.MustCompile(`一种说不出的|说不清[的道]|的意义在于|真正的[^。！？\n]{1,10}是`)},
	// Mẫu tic văn phong AI tiếng Việt (sản phẩm mặc định language=vi — văn dịch/dở
	// hay lặp các cụm dưới với mật độ cao). Regex không phân biệt hoa thường vì
	// tiếng Việt viết hoa chữ đầu câu; chuỗi zh phía trên sẽ không khớp văn vi
	// và ngược lại — hai bộ cùng tồn tại, mỗi sách chỉ đếm đúng ngôn ngữ của nó.
	{name: "Câu hiệu chỉnh \"không phải… mà là…\"", re: regexp.MustCompile(`(?i)không phải [^.!?\n。！？]{1,80}? mà là`)},
	// "như một" trần khớp cả "coi như một", "xem như một" (không phải ví von) → loại trừ.
	{name: "Ví von trực tiếp \"như một/tựa như/như thể\"",
		re:      regexp.MustCompile(`(?i)như một|tựa như|tựa hồ|như thể|hệt như`),
		exclude: regexp.MustCompile(`(?i)(?:coi|xem|gần|hầu) như một`)},
	// "im lặng" làm danh từ/tính từ miêu tả cảnh ("sự im lặng", "căn phòng im lặng",
	// "trong im lặng") là văn bình thường; chỉ tic hành động lặng thinh mới đáng đếm.
	{name: "Nhịp im lặng \"im lặng/không nói gì\"",
		re:      regexp.MustCompile(`(?i)im lặng|không nói gì|không quay đầu`),
		exclude: regexp.MustCompile(`(?i)(?:sự|trong|vào|giữa|phá vỡ|bầu|khoảng|không gian|căn phòng|căn nhà|xung quanh|bốn bề|màn đêm|khu rừng|con phố) im lặng|im lặng (?:bao trùm|phủ|tuyệt đối|đến đáng sợ)`)},
	{name: "Mẫu thần thái \"khẽ nhíu mày/khóe môi khẽ\"", re: regexp.MustCompile(`(?i)khẽ nhíu mày|khẽ cau mày|khóe môi (?:khẽ|nhếch)|ánh mắt (?:lóe|khẽ)|sắc mặt (?:khẽ|đổi)|đôi mắt khẽ`)},
	{name: "Phản ứng thân thể \"trái tim thắt/rùng mình\"", re: regexp.MustCompile(`(?i)(?:tim|trái tim|lồng ngực|ngực|cổ họng|dạ dày) (?:chợt |bỗng |khẽ )?thắt lại|trái tim thắt|rùng mình|hụt (?:mất )?một nhịp|lạnh sống lưng|quặn (?:đau|lòng)`)},
	{name: "Dấu hiệu tư duy \"nghĩ thầm/trong lòng nghĩ\"", re: regexp.MustCompile(`(?i)nghĩ thầm|trong lòng nghĩ|ý thức được`)},
	{name: "Sáo ngữ trừu tượng \"một cảm giác khó tả\"", re: regexp.MustCompile(`(?i)một cảm giác (?:khó tả|khó nói|không tên)|không nói nên lời|(?:thời gian|không gian) như ngừng lại`)},
}

var (
	// Tách câu hỗ trợ cả dấu câu Việt/Anh (.!?) lẫn Trung (。！？) — truyện vi dùng ASCII.
	sentenceSplit = regexp.MustCompile(`[。！？\n.!?]+`)
	// Từ chỉ thời điểm mở màn: tiếng Trung + tiếng Việt (không phân biệt hoa thường,
	// vì tiếng Việt viết hoa chữ đầu câu).
	openingTimeRe = regexp.MustCompile(`(?i)(?:夜|清晨|黎明|天亮|醒来|晨光|一整夜|đêm|bình minh|rạng sáng|trời sáng|thức dậy|thức giấc|ban mai|sáng sớm|hoàng hôn|chạng vạng)`)
	// Tiêu đề chương: 第N章 (zh) hoặc Chương N / Chapter N (vi/en, không hoa thường).
	titlePrefixRe = regexp.MustCompile(`^#{0,2}\s*(?:第[零〇一二三四五六七八九十百千万\d]+章|(?i:chương|chapter)\s+\d+)`)
)

// shortEndingRunes dòng cuối không vượt quá số ký tự này thì tính là "kết thúc ngắn".
const shortEndingRunes = 30

// Compute tính thống kê phong cách toàn sách; không đủ số chương thì trả về nil.
func Compute(in Input) *Stats {
	n := len(in.Chapters)
	if n < minChapters {
		return nil
	}
	chapters := make([]string, len(in.Chapters))
	for i, c := range in.Chapters {
		chapters[i] = NormalizeText(c)
	}
	in.Chapters = chapters
	all := strings.Join(in.Chapters, "\n")

	s := &Stats{Chapters: n}
	for _, def := range patternDefs {
		total := countPattern(def.re, def.exclude, all)
		if total == 0 {
			continue
		}
		s.Patterns = append(s.Patterns, PatternStat{
			Name:       def.name,
			Total:      total,
			PerChapter: round1(float64(total) / float64(n)),
		})
	}
	s.TopPhrases = minePhrases(recentWindow(in.Chapters), in.Stopwords)
	s.RepeatedSentences = repeatedSentences(in.Chapters)
	s.Ending = endingShape(in.Chapters)
	s.OpeningTimeRate = openingTimeRate(in.Chapters)
	s.TitleFormats = titleFormats(in.Titles)
	return s
}

func recentWindow(chapters []string) []string {
	if len(chapters) <= phraseWindow {
		return chapters
	}
	return chapters[len(chapters)-phraseWindow:]
}

// minePhrases khai thác cụm từ tần suất cao trong cửa sổ: văn chữ Hán dùng n-gram 3-6 ký tự,
// văn tiếng Việt / chữ Latin dùng n-gram 3-5 âm tiết (xem viet.go).
// Lọc: chứa dấu câu/khoảng trắng, hư từ đầu/cuối, trúng tên riêng; khử trùng: cụm nào là chuỗi con của cụm đã chọn thì bỏ.
func minePhrases(chapters []string, stopwords []string) []PhraseStat {
	text := strings.Join(chapters, "\n")
	if !hanDominant(text) {
		return mineWordPhrases(chapters, stopwords)
	}
	runes := []rune(text)
	threshold := max(8, len(chapters)/2)

	counts := make(map[string]int)
	for size := 3; size <= 6; size++ {
		for i := 0; i+size <= len(runes); i++ {
			gram := runes[i : i+size]
			if !validGram(gram) {
				continue
			}
			counts[string(gram)]++
		}
	}

	stopGrams := stopwordBigrams(stopwords)
	type cand struct {
		text  string
		count int
	}
	var cands []cand
	for g, c := range counts {
		if c < threshold || hitStopword(g, stopGrams) {
			continue
		}
		cands = append(cands, cand{g, c})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		// Cùng tần số lấy cụm dài hơn (nhiều thông tin hơn), rồi sắp xếp ổn định theo thứ tự từ điển
		if len(cands[i].text) != len(cands[j].text) {
			return len(cands[i].text) > len(cands[j].text)
		}
		return cands[i].text < cands[j].text
	})

	var out []PhraseStat
	for _, c := range cands {
		if len(out) >= 8 {
			break
		}
		dup := false
		for _, picked := range out {
			if strings.Contains(picked.Text, c.text) || strings.Contains(c.text, picked.Text) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, PhraseStat{Text: c.text, Count: c.count})
		}
	}
	return out
}

// gramEdgeStop n-gram có đầu/cuối là các hư từ/đại từ này không phải cụm văn phong, bỏ qua.
const gramEdgeStop = "的了着是在和与就也都还又把被他她它我你这那"

func validGram(gram []rune) bool {
	for _, r := range gram {
		if r < 0x4E00 || r > 0x9FFF { // chỉ nhận đoạn thuần chữ Hán
			return false
		}
	}
	if strings.ContainsRune(gramEdgeStop, gram[0]) || strings.ContainsRune(gramEdgeStop, gram[len(gram)-1]) {
		return false
	}
	return true
}

// stopwordBigrams tách tên riêng thành các đoạn 2 ký tự: tên người thường lọt vào văn dưới dạng
// một phần ("九渊负手" chứa "九渊"), khớp theo nguyên tên đầy đủ sẽ bỏ sót. Thà lọc chặt — thiếu
// một sự thật cụm từ không ngại, tên người lẫn vào danh sách câu cửa miệng mới là nhiễu.
func stopwordBigrams(stopwords []string) []string {
	var grams []string
	for _, w := range stopwords {
		runes := []rune(strings.TrimSpace(w))
		if len(runes) < 2 {
			continue
		}
		for i := 0; i+2 <= len(runes); i++ {
			grams = append(grams, string(runes[i:i+2]))
		}
	}
	return grams
}

func hitStopword(gram string, stopGrams []string) bool {
	for _, g := range stopGrams {
		if strings.Contains(gram, g) {
			return true
		}
	}
	return false
}

// repeatedSentences tìm câu ≥12 ký tự lặp nguyên văn xuyên ≥3 chương, lấy top 5 theo số lần.
func repeatedSentences(chapters []string) []SentenceStat {
	type rec struct {
		count    int
		chapters map[int]struct{}
	}
	seen := make(map[string]*rec)
	for ci, text := range chapters {
		for sent, count := range chapterSentenceCounts(text) {
			r := seen[sent]
			if r == nil {
				r = &rec{chapters: make(map[int]struct{})}
				seen[sent] = r
			}
			r.count += count
			r.chapters[ci] = struct{}{}
		}
	}

	var out []SentenceStat
	for sent, r := range seen {
		if len(r.chapters) < 3 {
			continue
		}
		out = append(out, SentenceStat{Text: truncateRunes(sent, 40), Chapters: len(r.chapters), Count: r.count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Text < out[j].Text
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// trimWrappedQuotes bóc dấu ngoặc bọc: cùng một câu thoại có/không có dấu ngoặc mở không nên tính thành hai.
func trimWrappedQuotes(sentence string) string {
	return strings.Trim(strings.TrimSpace(sentence), `"“”‘’「」『』`)
}

func endingShape(chapters []string) EndingStat {
	var lengths []int
	short := 0
	for _, text := range chapters {
		line := lastNonEmptyLine(text)
		if line == "" {
			continue
		}
		n := len([]rune(line))
		lengths = append(lengths, n)
		if n <= shortEndingRunes {
			short++
		}
	}
	if len(lengths) == 0 {
		return EndingStat{}
	}
	sort.Ints(lengths)
	return EndingStat{
		ShortRatio:  round2(float64(short) / float64(len(lengths))),
		MedianRunes: lengths[len(lengths)/2],
	}
}

func openingTimeRate(chapters []string) float64 {
	hit := 0
	for _, text := range chapters {
		if openingTimeRe.MatchString(firstParagraph(text)) {
			hit++
		}
	}
	return round2(float64(hit) / float64(len(chapters)))
}

func titleFormats(titles []string) *TitleStat {
	if len(titles) == 0 {
		return nil
	}
	t := &TitleStat{}
	for _, title := range titles {
		if strings.TrimSpace(title) == "" {
			continue
		}
		if titlePrefixRe.MatchString(title) {
			t.WithPrefix++
		} else {
			t.WithoutPrefix++
		}
	}
	// Chỉ có trộn lẫn mới đáng báo cáo; định dạng thống nhất không phải vấn đề theo nghĩa sự thật
	if t.WithPrefix == 0 || t.WithoutPrefix == 0 {
		return nil
	}
	return t
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// firstParagraph lấy dòng đầu tiên khác rỗng và không phải tiêu đề Markdown (dòng đầu file chương thường là tiêu đề #).
func firstParagraph(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// countPattern đếm số lần khớp của re, trừ đi các lần khớp của exclude (nếu có).
func countPattern(re, exclude *regexp.Regexp, text string) int {
	n := len(re.FindAllStringIndex(text, -1))
	if exclude != nil {
		n -= len(exclude.FindAllStringIndex(text, -1))
	}
	return max(n, 0)
}
