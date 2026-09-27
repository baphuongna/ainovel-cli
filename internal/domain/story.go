package domain

import (
	"fmt"
	"strings"
)

// BookMetadata là thông tin tác phẩm hướng đến độc giả và xuất bản.
// Thiết lập sáng tác thuộc về Foundation, tiến độ chạy thuộc về Progress, cả hai đều không mang dữ liệu này.
type BookMetadata struct {
	Title    string `json:"title"`
	Synopsis string `json:"synopsis"`
}

// Normalized trả về giá trị chuẩn hóa có thể lưu bền và so sánh được.
func (b BookMetadata) Normalized() BookMetadata {
	b.Title = strings.TrimSpace(b.Title)
	b.Synopsis = strings.TrimSpace(b.Synopsis)
	return b
}

// Validate kiểm tra các trường bắt buộc của thông tin tác phẩm.
func (b BookMetadata) Validate() error {
	b = b.Normalized()
	if b.Title == "" {
		return fmt.Errorf("book title is required")
	}
	if b.Synopsis == "" {
		return fmt.Errorf("book synopsis is required")
	}
	return nil
}

// OutlineEntry một mục dàn ý, tương ứng một chương.
type OutlineEntry struct {
	Chapter   int      `json:"chapter"`
	Title     string   `json:"title"`
	CoreEvent string   `json:"core_event"`
	Hook      string   `json:"hook"`
	Scenes    []string `json:"scenes"`
}

// Character hồ sơ nhân vật.
type Character struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"` // biệt danh/xưng hô/ngoại hiệu (vd "thiếu niên phế vật", "ca")
	Role        string   `json:"role"`
	Description string   `json:"description"`
	Arc         string   `json:"arc"`
	Traits      []string `json:"traits"`
	Tier        string   `json:"tier,omitempty"` // core / important / secondary / decorative (mặc định important)
}

// VolumeOutline dàn ý cấp tập (chế độ truyện dài phân tầng).
type VolumeOutline struct {
	Index int          `json:"index"`
	Title string       `json:"title"`
	Theme string       `json:"theme"`           // xung đột chủ đạo/chủ đề của tập này
	Final bool         `json:"final,omitempty"` // tập kết: toàn sách khép lại ở tập này (khai báo khi kiến trúc sư gọi append_volume)
	Arcs  []ArcOutline `json:"arcs"`
}

// IsExpanded xác định tập đã được mở rộng chưa (có cấu trúc cấp cung).
func (v *VolumeOutline) IsExpanded() bool { return len(v.Arcs) > 0 }

// FinaleVolume trả về số thứ tự tập kết đã khai báo, trả về 0 nếu chưa khai báo.
// Sự thật "tập kết" = "tập cuối mang cờ Final": sau khi khai báo, toàn sách vào trạng
// thái khép (quy hoạch thu dây, kết cấu tập cuối viết xong tức hoàn tất); nếu sau đó lại
// nối tập mới không mang cờ, tập mới trở thành tập cuối, trạng thái khép tự nhiên được
// giải trừ — vì vậy không cần công cụ thu hồi, trạng thái luôn suy ra được từ dữ liệu dàn ý.
func FinaleVolume(volumes []VolumeOutline) int {
	if n := len(volumes); n > 0 && volumes[n-1].Final {
		return volumes[n-1].Index
	}
	return 0
}

// StoryCompass la bàn hướng kết cục, thay thế danh sách tập khung xương cố định.
// Architect có thể cập nhật ở mỗi ranh giới tập, cho phép hướng truyện tiến hóa theo sáng tác.
type StoryCompass struct {
	EndingDirection string   `json:"ending_direction"`          // hướng kết cục (mô tả theo chủ đề)
	OpenThreads     []string `json:"open_threads,omitempty"`    // tuyến dài đang hoạt động (cần thu dây mới kết thúc được)
	EstimatedScale  string   `json:"estimated_scale,omitempty"` // quy mô mờ (vd "dự kiến 4-6 tập")
	LastUpdated     int      `json:"last_updated,omitempty"`    // số chương đã hoàn thành tại thời điểm cập nhật
}

// ArcOutline dàn ý cấp cung.
type ArcOutline struct {
	Index             int            `json:"index"` // số thứ tự cung trong tập
	Title             string         `json:"title"`
	Goal              string         `json:"goal"`                         // mục tiêu cung (khởi - thừa - chuyển - hợp)
	EstimatedChapters int            `json:"estimated_chapters,omitempty"` // số chương ước tính của cung khung xương (đưa về 0 sau khi mở rộng)
	Chapters          []OutlineEntry `json:"chapters"`
}

// IsExpanded xác định cung đã được mở rộng chưa (có chương chi tiết).
func (a *ArcOutline) IsExpanded() bool { return len(a.Chapters) > 0 }

// ArcExpansion là quy hoạch hoàn chỉnh mà Architect đưa ra cho một cung chưa viết tại ranh giới cấu trúc.
// Title/Goal không phải bản sao máy móc của khung xương: model có thể sửa đổi kế hoạch chưa xảy ra dựa trên chính văn đã hoàn thành.
type ArcExpansion struct {
	Title    string         `json:"title"`
	Goal     string         `json:"goal"`
	Chapters []OutlineEntry `json:"chapters"`
}

// EstimatedChapterCapacity tính ước lượng dung lượng nội bộ của dàn ý phân tầng: cung đã
// mở rộng tính theo số chương thật, cung khung xương tính theo EstimatedChapters. Giá trị này
// chỉ dùng cho chiến lược ngữ cảnh, không phải tổng số chương toàn sách; các chương thực sự
// đã chi tiết hóa và ghi được luôn đến từ FlattenOutline, cấm đưa giá trị này ra cho người dùng hay model.
func EstimatedChapterCapacity(volumes []VolumeOutline) int {
	n := 0
	for _, v := range volumes {
		for _, a := range v.Arcs {
			if a.IsExpanded() {
				n += len(a.Chapters)
			} else {
				n += a.EstimatedChapters
			}
		}
	}
	return n
}

// FlattenOutline mở rộng dàn ý phân tầng thành danh sách chương phẳng, giữ số chương toàn cục liên tục.
func FlattenOutline(volumes []VolumeOutline) []OutlineEntry {
	var result []OutlineEntry
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for _, e := range a.Chapters {
				e.Chapter = ch
				result = append(result, e)
				ch++
			}
		}
	}
	return result
}

// WorldRule một mục quy tắc thiết lập thế giới.
type WorldRule struct {
	Category string `json:"category"` // magic / technology / geography / society / other
	Rule     string `json:"rule"`     // mô tả quy tắc
	Boundary string `json:"boundary"` // ranh giới không được vi phạm
}

// RenumberVolumes xếp lại số thứ tự tập và cung theo vị trí, bắt đầu từ 1.
//
// Model quy hoạch viết index không đáng tin: thường đếm từ 0, thậm chí viết 0 cho
// mọi cung trong cùng một tập. Trong khi ExpandArc / ArcScope tra cứu theo giá trị
// index, giá trị trùng lặp hoặc bằng 0 khiến cung không bao giờ truy cập được —
// triệu chứng chỉ nổi lên sau vài bước khi expand_arc báo "tham số không hợp lệ",
// và thông báo lỗi chỉ sai hướng hoàn toàn.
//
// Thứ tự mảng mới là sự thật, index chỉ là tên gọi của nó, nên trước khi lưu xuống
// đĩa thống nhất ghi đè theo vị trí.
func RenumberVolumes(volumes []VolumeOutline) {
	for vi := range volumes {
		volumes[vi].Index = vi + 1
		for ai := range volumes[vi].Arcs {
			volumes[vi].Arcs[ai].Index = ai + 1
		}
	}
}

// Cùng một hook/sự kiện cốt lõi lặp đến số lần này thì kết luận dàn ý đang đi vòng.
// Lặp 2 lần có thể là lối hai vần cố ý, từ 3 lần trở đi không còn cách viết chính đáng:
// độc giả bị treo lửng cùng một nghi vấn ba chương liền mà không ai chi trả.
const (
	maxHookRepeat      = 3
	maxCoreEventRepeat = 2
)

// StalledOutline phát hiện dàn ý "đi vòng quanh chỗ cũ": tiêu đề chương mỗi bản một khác,
// nhưng hook hoặc sự kiện cốt lõi là cùng một câu nhân bản nhiều bản. Khi đó Writer vẫn
// trung thành chấp hành — mỗi chương kể lại chương trước rồi thêm chút ít, đọc như viết lại
// chứ không phải viết tiếp.
//
// Khuyết tật kiểu này cả Writer lẫn Editor đều không nhận ra: cả hai chỉ nhìn từng chương
// đơn lẻ, mà chương đơn lẻ tự nó vẫn nhất quán. Phải so khớp theo cả cuốn ngay tại nơi dàn ý
// lưu xuống đĩa mới chặn nổi.
//
// Trả về chuỗi rỗng nghĩa là đạt; nếu không là chẩn đoán có thể trả thẳng cho người quy hoạch.
func StalledOutline(entries []OutlineEntry) string {
	hooks := map[string]int{}
	events := map[string]int{}
	for _, e := range entries {
		if h := strings.TrimSpace(e.Hook); h != "" {
			hooks[h]++
		}
		if c := strings.TrimSpace(e.CoreEvent); c != "" {
			events[c]++
		}
	}
	if worst, n := mostRepeated(hooks); n >= maxHookRepeat {
		return fmt.Sprintf("Dàn ý đi vòng: cùng một hook lặp ở %d/%d chương — %q."+
			"Hook của mỗi chương phải là hệ quả mới phát sinh trong chính chương đó và được chương kế tiếp chi trả; hãy viết lại từng chương", n, len(entries), truncateRunes(worst, 40))
	}
	if worst, n := mostRepeated(events); n >= maxCoreEventRepeat {
		return fmt.Sprintf("Dàn ý đi vòng: cùng một core_event lặp ở %d chương — %q."+
			"Mỗi chương phải xảy ra sự việc khác nhau và làm thay đổi tình thế; hãy viết lại từng chương", n, truncateRunes(worst, 40))
	}
	return ""
}

func mostRepeated(m map[string]int) (string, int) {
	var key string
	best := 0
	for k, n := range m {
		if n > best {
			key, best = k, n
		}
	}
	return key, best
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// SkeletonArcs thống kê các cung khung xương chưa mở rộng, dùng cho kiểm tra tiền đề trước khi hoàn sách.
//
// Kiểm tra hoàn sách chỉ so với dàn ý phẳng, mà dàn ý phẳng do FlattenOutline suy ra từ
// "các cung đã mở rộng" — cung khung xương đóng góp 0 chương, hoàn toàn vô hình với kiểm tra
// này. Sự cố thực đo: tập 1 có hai cung khung xương tổng 38 chương chưa từng mở rộng, kiến
// trúc sư nhảy thẳng sang tập 2 viết xong 15 chương rồi tuyên bố hoàn sách, kiểm tra vì
// next(16) > len(flat)(15) mà buông lỏng.
//
// Muốn thu dây sớm vẫn còn lối ra chính đáng: append_volume kèm "final": true để khai báo tập kết.
func SkeletonArcs(volumes []VolumeOutline) []string {
	var out []string
	for vi := range volumes {
		for ai := range volumes[vi].Arcs {
			if a := &volumes[vi].Arcs[ai]; !a.IsExpanded() {
				out = append(out, fmt.Sprintf("Tập %d cung %d \"%s\"", volumes[vi].Index, a.Index, a.Title))
			}
		}
	}
	return out
}

// maxArcChapters là giới hạn số chương chi tiết của một cung.
//
// Ràng buộc đến từ xem xét cuối cung, không phải thị hiếu tự sự: Editor ở ranh giới cung
// phải đọc trọn cả cung mới ra được ý kiến thẩm duyệt. Thực đo một cung 20 chương =
// 113792 ký tự ≈ 37k token chính văn, cộng dồn dàn ý/snapshot/prompt thì không model
// khả dụng nào nuốt nổi — cửa sổ 32k cục bộ nhét không lọt, gói miễn phí mây ở 8 tok/s
// đứt stream 14 lần liên tục, cuối cùng cả pipeline kẹt cứng tại ranh giới cung.
// 8 chương ≈ 45k ký tự ≈ 15k token, hai phía đều còn dư địa.
//
// Điều này cũng tốt về cấu trúc: nhét 20 chương vào một cung vốn đã chứng tỏ mục tiêu
// cung không hội tụ.
const maxArcChapters = 8

// OversizedArc kiểm tra quy mô một cung có vượt giới hạn không, vượt thì trả về chẩn đoán
// có thể trả thẳng cho người quy hoạch. Trả về chuỗi rỗng nghĩa là đạt.
//
// chapters lấy giá trị lớn hơn giữa "số chương chi tiết" và "số chương ước tính khung xương":
// cung ghi estimated=20 ngay từ giai đoạn khung xương chắc chắn đụng cùng bức tường khi đến
// expand_arc, mà lúc đó đã là hai mươi chương sau — vấn đề cấu trúc phải báo ngay khi cấu trúc
// lưu xuống đĩa.
func OversizedArc(label string, chapters int) string {
	if chapters <= maxArcChapters {
		return ""
	}
	return fmt.Sprintf("%s có %d chương chi tiết, vượt giới hạn %d chương của một cung."+
		"Xem xét cuối cung cần đọc trọn cả cung trong một lượt, cung quá dài thì không model nào xem xét nổi (thực đo 20 chương là kẹt cứng pipeline)."+
		"Hãy tách nó thành nhiều cung có mục tiêu riêng: lần gọi này chỉ mở rộng cung đầu tiên trong %d chương đầu,"+
		"phần còn lại để làm cung khung xương, viết đến ranh giới mới mở rộng",
		label, chapters, maxArcChapters, maxArcChapters)
}
