package rules

import (
	"fmt"
	"maps"
	"strings"
)

// Snapshot là snapshot quy tắc người dùng sau chuẩn hóa của cuốn sách (meta/user_rules.json).
//
// Nó là nguồn sự thực duy nhất lúc runtime: khi mở sách/nhập/làm mới được hợp nhất từ các
// nguồn bằng chuẩn hóa, sau đó novel_context inject và commit_chapter kiểm tra đều chỉ đọc
// một bản này, không đọc lại file rules nhiều lần (tránh trôi dạt và hai nơi đọc phân tán).
//
// Chỉ có Structured + Preferences được inject cho model (xem Payload); Version / Status /
// Sources / Uncertain là metadata vận hành và chẩn đoán, không vào working_memory.user_rules.
type Snapshot struct {
	Version     int        `json:"version"`
	Status      Status     `json:"status"`
	Structured  Structured `json:"structured"`
	Preferences string     `json:"preferences"`
	Sources     []string   `json:"sources"`
	Uncertain   []string   `json:"uncertain"`
}

// Status đánh dấu việc chuẩn hóa snapshot có thành công trọn vẹn hay không.
type Status string

const (
	// StatusReady mọi nguồn đều chuẩn hóa thành công.
	StatusReady Status = "ready"
	// StatusDegraded ít nhất một nguồn chuẩn hóa thất bại, đã hạ cấp thành raw preferences (chi tiết xem Uncertain / log).
	StatusDegraded Status = "degraded"
)

// SnapshotVersion là phiên bản schema snapshot hiện tại, tiện cho việc di trú tương lai.
// v2: chapter_words rút khỏi structured (số chữ là ràng buộc mềm ngữ nghĩa, đi preferences).
// Snapshot v1 tải trực tiếp vẫn tương thích: trường lạ bị bỏ qua khi deserialize, lần phủ lưu
// sau tự nhiên hội tụ về v2; cố ý không làm "sai phiên bản là dựng lại" — làm vậy sẽ mất các
// quy tắc không tái sinh được do AddRuntimeRule thêm lúc chạy.
const SnapshotVersion = 2

// Candidate là kết quả ứng viên của một nguồn sau chuẩn hóa.
//
// Các nguồn sắp theo mức ưu tiên thấp→cao rồi giao cho BuildSnapshot hợp nhất tất định. LLM
// chỉ phụ trách biến ngôn ngữ tự nhiên của một nguồn duy nhất thành Structured/Preferences ứng
// viên; mức ưu tiên và phủ trường do BuildSnapshot (Go) phán định.
type Candidate struct {
	Source      string     // nhãn nguồn dễ đọc, vào Snapshot.Sources (như system_defaults / startup_prompt / global:my.md)
	Structured  Structured // trường cấu trúc ứng viên của nguồn này
	Preferences string     // văn bản sở thích ngôn ngữ tự nhiên của nguồn này
	Uncertain   []string   // mục nguồn này cố ý không nâng lên structured + lý do (chẩn đoán)
	Degraded    bool       // nguồn này chuẩn hóa thất bại, đã hạ cấp thành raw preferences
}

// Payload trả hình thái inject vào working_memory.user_rules: chỉ lộ structured + preferences.
// Kể cả đều rỗng cũng trả cấu trúc ổn định, tránh LLM thấy user_rules=null mà nhảy nhánh bất thường.
func (s Snapshot) Payload() map[string]any {
	return map[string]any{
		"structured":  s.Structured,
		"preferences": s.Preferences,
	}
}

// BuildSnapshot hợp nhất tất định các ứng viên đã sắp theo mức ưu tiên (thấp→cao) thành snapshot.
//
// Quy tắc hợp nhất (toàn bộ tất định phía Go, không giao LLM):
//   - structured: phủ theo trường, nguồn ưu tiên cao phủ nguồn ưu tiên thấp; fatigue_words cộng theo từng từ
//   - preferences: không phủ, nối theo thứ tự nguồn (ưu tiên cao ở sau), kèm tiêu đề nguồn
//   - giá trị rỗng/zero coi là thiếu trường, không phủ giá trị sẵn có (sanitizeStructured)
//   - bất kỳ nguồn nào Degraded → snapshot status=degraded
func BuildSnapshot(cands []Candidate) Snapshot {
	snap := Snapshot{
		Version: SnapshotVersion,
		Status:  StatusReady,
		Sources: make([]string, 0, len(cands)),
	}
	var prefs []string
	for _, c := range cands {
		s := sanitizeStructured(c.Structured)
		if s.Genre != "" {
			snap.Structured.Genre = s.Genre
		}
		if len(s.ForbiddenChars) > 0 {
			snap.Structured.ForbiddenChars = s.ForbiddenChars
		}
		if len(s.ForbiddenPhrases) > 0 {
			snap.Structured.ForbiddenPhrases = s.ForbiddenPhrases
		}
		if len(s.FatigueWords) > 0 {
			snap.Structured.FatigueWords = mergeFatigueWords(snap.Structured.FatigueWords, s.FatigueWords)
		}

		if p := strings.TrimSpace(c.Preferences); p != "" {
			if src := strings.TrimSpace(c.Source); src != "" {
				prefs = append(prefs, fmt.Sprintf("## [%s]\n\n%s", src, p))
			} else {
				prefs = append(prefs, p)
			}
		}
		if src := strings.TrimSpace(c.Source); src != "" {
			snap.Sources = append(snap.Sources, src)
		}
		snap.Uncertain = append(snap.Uncertain, c.Uncertain...)
		if c.Degraded {
			snap.Status = StatusDegraded
		}
	}
	snap.Preferences = strings.Join(prefs, "\n\n")
	return snap
}

// OverlaySnapshot phủ một ứng viên ưu tiên cao lên snapshot sẵn có (ứng viên thắng).
//
// Dùng cho hành động rules của Arbiter lúc chạy: không chuẩn hóa lại mọi nguồn, chỉ phủ quy tắc
// mới vào snapshot hiện tại — structured phủ theo trường, preferences thêm một đoạn,
// sources/uncertain cộng dồn, hạ cấp lan truyền.
func OverlaySnapshot(base Snapshot, cand Candidate) Snapshot {
	out := base
	out.Version = SnapshotVersion
	s := sanitizeStructured(cand.Structured)
	if s.Genre != "" {
		out.Structured.Genre = s.Genre
	}
	if len(s.ForbiddenChars) > 0 {
		out.Structured.ForbiddenChars = s.ForbiddenChars
	}
	if len(s.ForbiddenPhrases) > 0 {
		out.Structured.ForbiddenPhrases = s.ForbiddenPhrases
	}
	if len(s.FatigueWords) > 0 {
		out.Structured.FatigueWords = mergeFatigueWords(cloneFatigue(out.Structured.FatigueWords), s.FatigueWords)
	}
	if p := strings.TrimSpace(cand.Preferences); p != "" {
		section := p
		if src := strings.TrimSpace(cand.Source); src != "" {
			section = fmt.Sprintf("## [%s]\n\n%s", src, p)
		}
		if strings.TrimSpace(out.Preferences) == "" {
			out.Preferences = section
		} else {
			out.Preferences = out.Preferences + "\n\n" + section
		}
	}
	if src := strings.TrimSpace(cand.Source); src != "" {
		out.Sources = append(append([]string{}, out.Sources...), src)
	}
	if len(cand.Uncertain) > 0 {
		out.Uncertain = append(append([]string{}, out.Uncertain...), cand.Uncertain...)
	}
	if cand.Degraded {
		out.Status = StatusDegraded
	}
	return out
}

// mergeFatigueWords cộng ngưỡng từ mệt mỏi theo từng từ, src phủ ngưỡng cùng từ trong dst
// (ưu tiên gần hơn). Để người dùng chỉ cần thêm ít từ mệt mỏi mới, không phải liệt kê lại cơ sở dựng sẵn.
func mergeFatigueWords(dst, src map[string]int) map[string]int {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]int, len(src))
	}
	maps.Copy(dst, src)
	return dst
}

func cloneFatigue(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int, len(m))
	maps.Copy(out, m)
	return out
}

// SystemDefaults là cơ sở cơ học dựng sẵn trong code (nguồn mức ưu tiên thấp nhất), không đi chuẩn hóa LLM.
//
// Số liệu di chuyển từ front matter của assets/rules/default.md cũ. Căn cứ ngưỡng giữ nguyên luôn:
// từ mệt mỏi hậu đoạn (像一/沉默了/没有说话/X息) đến từ thực chứng sản phẩm chạy dài 196 chương —
// sau khi sáo câu AI truyền thống bị bảng cấm (phần trên) diệt, model chuyển sang dùng các "từ nhịp" này
// 5-7 lần trung bình mỗi chương, nới ngưỡng để dung nạp sử dụng bình thường.
func SystemDefaults() Candidate {
	return Candidate{
		Source: "system_defaults",
		Structured: Structured{
			// Sáo câu AI cố định chiều dài; checker khớp chuỗi con literal, mẫu có biến (不是X而是Y) thuộc tầng ngữ nghĩa.
			// Danh sách song ngữ: chuỗi zh chỉ khớp văn Trung, chuỗi vi chỉ khớp văn Việt —
			// cùng tồn tại an toàn, mỗi sách chỉ xét đúng ngôn ngữ của nó.
			ForbiddenPhrases: []string{
				"某种程度上", "值得注意的是", "不知为何", "五味杂陈",
				"theo một cách nào đó", "điều đáng chú ý là", "vì lý do nào đó", "muôn vàn cảm xúc",
			},
			FatigueWords: map[string]int{
				"不禁": 1, "竟然": 1, "仿佛": 2, "此外": 1, "然而": 2,
				"一丝": 2, "一抹": 2, "一缕": 2, "宛如": 1, "不由得": 1,
				"像一": 3, "沉默了": 2, "没有说话": 2, "几息": 3, "一息": 3, "数息": 2,
				// Tiếng Việt: "khẽ" là tic văn dịch đặc trưng (khẽ cười/khẽ gật) — ngưỡng
				// nới theo tần suất 5-7 lần/chương của văn dịch bình thường.
				"khẽ": 5, "tựa như": 2, "như thể": 2, "giống như một": 3,
				"một tia": 2, "một làn": 2, "một thoáng": 2,
				"ngoài ra": 1, "tuy nhiên": 2, "không ngờ": 1,
				"không kiềm được": 1, "không khỏi": 1,
				"im lặng": 2, "không nói gì": 2,
			},
		},
	}
}

// sanitizeStructured hiện thực "rỗng/zero = thiếu trường": bộ chuẩn hóa có thể nhả chỗ giữ
// kiểu genre:"" (thực đo nguyên mẫu), phải coi là chưa khai báo, tránh làm bẩn hợp nhất và kiểm tra cơ học.
func sanitizeStructured(s Structured) Structured {
	out := Structured{}
	if g := strings.TrimSpace(s.Genre); g != "" {
		out.Genre = g
	}
	out.ForbiddenChars = nonEmptyStrings(s.ForbiddenChars)
	out.ForbiddenPhrases = nonEmptyStrings(s.ForbiddenPhrases)
	out.FatigueWords = sanitizeFatigueWords(s.FatigueWords)
	return out
}

func nonEmptyStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func sanitizeFatigueWords(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int, len(m))
	for w, n := range m {
		if w = strings.TrimSpace(w); w == "" || n <= 0 {
			continue
		}
		out[w] = n
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
