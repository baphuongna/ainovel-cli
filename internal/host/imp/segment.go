package imp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// BoundaryDecision là phán đoán ranh giới của mô hình cho một owned range đơn (RFC §8.2).
type BoundaryDecision struct {
	UnitID    string `json:"unit_id"`
	Anchor    string `json:"anchor,omitempty"`
	Kind      string `json:"kind"` // chapter / group / front_matter / back_matter
	Title     string `json:"title,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

const (
	kindChapter     = "chapter"
	kindGroup       = "group"
	kindFrontMatter = "front_matter"
	kindBackMatter  = "back_matter"
)

// boundaryBatch là kết quả có cấu trúc của một lần gọi phân đoạn.
type boundaryBatch struct {
	Boundaries []BoundaryDecision `json:"boundaries"`
}

// ChapterSpan là một chương có thể nộp sau khi phân tách được xác nhận: tiêu đề + phạm vi byte văn
// bản đã chuẩn hóa (gồm dòng tiêu đề).
type ChapterSpan struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Start  int    `json:"start_byte"`
	End    int    `json:"end_byte"`
}

// MatterSpan là tiêu đề tập/phần hoặc khu vực phụ trợ rõ ràng.
type MatterSpan struct {
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	Start int    `json:"start_byte"`
	End   int    `json:"end_byte"`
}

// Segmentation là kết quả phân tách đã qua kiểm tra độ phủ toàn văn (thượng nguồn của confirmation và phân tích từng chương).
type Segmentation struct {
	Chapters  []ChapterSpan `json:"chapters"`
	Matter    []MatterSpan  `json:"matter,omitempty"`    // group / front / back
	Uncertain []int         `json:"uncertain,omitempty"` // số chương được đánh dấu uncertain, để nhắc trong bản xem trước
	Notes     []string      `json:"notes,omitempty"`     // ghi chú cần kiểm tra thủ công trong giai đoạn phân tách (ví dụ tiêu đề chương rỗng placeholder nhập vào đoạn trước)
}

// Content trả về chính văn đã chuẩn hóa của chương thứ i (gồm dòng tiêu đề).
func (s *Segmentation) Content(normalized []byte, i int) string {
	c := s.Chapters[i]
	return string(normalized[c.Start:c.End])
}

// resolveSegmentation ánh xạ các quyết định ranh giới có thứ tự thành Segmentation đã qua kiểm tra
// độ phủ toàn văn (RFC §8.3). Hàm thuần: tách đầu ra mô hình khỏi kiểm tra của code, "dòng nào có
// phải tiêu đề chương không" Go không phán lại, nhưng bất biến độ phủ bắt buộc phải đúng.
func resolveSegmentation(normalized []byte, units []SourceUnit, decisions []BoundaryDecision) (*Segmentation, error) {
	if len(decisions) == 0 {
		return nil, fmt.Errorf("không nhận diện được ranh giới nào")
	}
	// Hợp đồng trước: units phải xếp theo thứ tự số (Line,Part) (cấm từ điển trên ID).
	for i := 1; i < len(units); i++ {
		if !unitLess(units[i-1], units[i]) {
			return nil, fmt.Errorf("SourceUnit không xếp theo thứ tự số (Line,Part): %s đứng sau %s", units[i-1].ID, units[i].ID)
		}
	}
	unitByID := make(map[string]SourceUnit, len(units))
	for _, u := range units {
		unitByID[u.ID] = u
	}

	type point struct {
		byte int
		d    BoundaryDecision
	}
	points := make([]point, 0, len(decisions))
	for i, d := range decisions {
		switch d.Kind {
		case kindChapter, kindGroup, kindFrontMatter, kindBackMatter:
		default:
			return nil, fmt.Errorf("ranh giới[%d] kind bất hợp lệ: %q", i, d.Kind)
		}
		b, err := resolveBoundaryByte(unitByID, d.UnitID, d.Anchor)
		if err != nil {
			return nil, err
		}
		points = append(points, point{byte: b, d: d})
	}
	// Thứ tự sai và trùng lặp thỉnh thoảng của mô hình là vấn đề kỷ luật tọa độ, Go sửa tất định
	// thay vì phủ quyết ở hồi kết — sau khi mọi khối thành công mà vứt cả giai đoạn phân tách vì hai
	// ranh giới đảo thứ tự, giá quá đáng chịu không nổi (thực đo 319 ranh giới thua vì 1 chỗ đảo
	// trong khối, và cache khối khiến thất bại tái hiện tất định). Thứ tự giữa các khối do khoảng owned
	// không chồng nhau bảo đảm, sai thứ tự chỉ có thể xảy ra trong khối: sắp ổn định theo byte là khôi
	// phục thứ tự thật, mất thông tin bằng 0; trùng cùng byte thì giữ cái xuất hiện trước và ghi Notes
	// giao bản xem trước xác nhận kiểm tra thủ công.
	sort.SliceStable(points, func(i, j int) bool { return points[i].byte < points[j].byte })
	var notes []string
	uniq := points[:0]
	for _, p := range points {
		if n := len(uniq); n > 0 && uniq[n-1].byte == p.byte {
			// Trùng lặp hoàn toàn giống nhau là dư máy móc, khử trùng vô thanh; xung đột ngữ nghĩa
			// cùng vị trí (kind/tiêu đề khác) đã hỏi lại ở thời điểm gọi, đến đây chỉ có thể từ cache cũ
			// trước khi sửa — giữ cái xuất hiện trước và ghi Notes để kiểm tra thủ công.
			if prev := uniq[n-1].d; prev.Kind != p.d.Kind || boundaryLabel(prev) != boundaryLabel(p.d) {
				notes = append(notes, fmt.Sprintf("ranh giới %q và %q trùng vị trí (byte %d), đã giữ cái trước",
					boundaryLabel(prev), boundaryLabel(p.d), p.byte))
			}
			continue
		}
		uniq = append(uniq, p)
	}
	points = uniq
	// Văn bản khác rỗng trước ranh giới đầu (lời tựa đầu sách/quảng cáo v.v., mô hình bỏ sót ranh
	// giới khởi đầu) không bị phủ quyết hồi kết: Go tất định bổ một front_matter chặn [0, first), ghi
	// Notes giao bản xem trước xác nhận kiểm tra thủ công — bỏ sót đã vào cache khối, phủ quyết hồi
	// kết sẽ khiến chạy lại không gọi model nào mà tái hiện cùng thất bại (cùng triết lý hấp thụ chương
	// chính văn rỗng, RFC §8.3.5). Bản thân phán đoán ngữ nghĩa đã trả lại cho mô hình ở thời điểm gọi
	// (chunkValidator.coverStart hỏi lại), phương án chặn này chỉ chữa cache cũ.
	if head := points[0].byte; head != 0 && strings.TrimSpace(string(normalized[:head])) != "" {
		notes = append(notes, fmt.Sprintf("văn bản %d byte đầu chưa được mô hình phân bổ (%s…), đã thu thành front_matter, hãy kiểm tra có chương nào bị sót phân tách không",
			head, snippet(string(normalized[:min(head, 48)]), 24)))
		points = append([]point{{byte: 0, d: BoundaryDecision{UnitID: units[0].ID, Kind: kindFrontMatter}}}, points...)
	}

	seg := &Segmentation{Notes: notes}
	chapterNo := 0
	// absorb nhập một đoạn vào span mới sinh gần nhất (chương hoặc khu vực phụ trợ đều được), không có gì để nhập thì trả false.
	absorb := func(end int) bool {
		ci, mi := len(seg.Chapters)-1, len(seg.Matter)-1
		switch {
		case ci >= 0 && (mi < 0 || seg.Chapters[ci].Start > seg.Matter[mi].Start):
			seg.Chapters[ci].End = end
		case mi >= 0:
			seg.Matter[mi].End = end
		default:
			return false
		}
		return true
	}
	for i, p := range points {
		start := p.byte
		if i == 0 {
			start = 0 // đoạn đầu hấp thụ khoảng trắng ở điểm khởi đầu
		}
		end := len(normalized)
		if i+1 < len(points) {
			end = points[i+1].byte
		}
		title := strings.TrimSpace(p.d.Title)
		if title == "" {
			title = firstLine(normalized, p.byte, end)
		}
		switch p.d.Kind {
		case kindChapter:
			if strings.TrimSpace(bodyAfterTitle(normalized, p.byte, end)) == "" {
				// Nguồn truyện mạng thật thường có mục "đã khóa/chương trả phí": tiêu đề có, chính văn
				// thiếu. Không thất bại toàn bộ — phủ quyết một phiếu ở hồi kết sẽ phí toàn bộ lời gọi mô
				// hình của giai đoạn phân tách; dòng tiêu đề nhập vào đoạn trước (không mất một chữ), ghi
				// vào Notes để bản xem trước xác nhận trình bày, người dùng không chấp nhận thì dùng --guide
				// phán định (điểm dừng của RFC §8.4 tồn tại đúng vì việc này).
				seg.Notes = append(seg.Notes,
					fmt.Sprintf("tiêu đề chương %q không có chính văn (byte %d..%d), đã nhập vào đoạn trước (thường gặp ở chương khóa/trả phí dạng placeholder)", title, start, end))
				if !absorb(end) {
					seg.Matter = append(seg.Matter, MatterSpan{Kind: kindFrontMatter, Title: title, Start: start, End: end})
				}
				continue
			}
			chapterNo++
			seg.Chapters = append(seg.Chapters, ChapterSpan{Number: chapterNo, Title: title, Start: start, End: end})
			if p.d.Uncertain {
				seg.Uncertain = append(seg.Uncertain, chapterNo)
			}
		default:
			seg.Matter = append(seg.Matter, MatterSpan{Kind: p.d.Kind, Title: title, Start: start, End: end})
		}
	}
	if chapterNo == 0 {
		return nil, fmt.Errorf("phân tách không sinh ra chương nào (group không tính vào chương)")
	}
	// Chương trùng tên là tín hiệu tất định của "cùng một chương bị cắt nhầm" (nguồn có quy ước tiêu
	// đề thì tên chương không nên lặp), chỉ ghi Notes giao bản xem trước xác nhận kiểm tra thủ công
	// (Notes khác rỗng là chặn --yes) — có hợp lại hay không Go không phán định.
	titleAt := make(map[string]int, len(seg.Chapters))
	for _, c := range seg.Chapters {
		key := squashSpace(c.Title)
		if first, ok := titleAt[key]; ok && key != "" {
			seg.Notes = append(seg.Notes, fmt.Sprintf("chương %d và chương %d trùng tiêu đề (%q), nghi là cùng một chương bị cắt nhầm, hãy kiểm tra",
				c.Number, first, snippet(c.Title, 24)))
		} else {
			titleAt[key] = c.Number
		}
	}
	return seg, nil
}

// squashSpace bỏ toàn bộ khoảng trắng, dùng cho hiển thị lại tiêu đề và so trùng tên — khác biệt
// khoảng trắng/trang trí không tạo thành khác biệt ngữ nghĩa.
func squashSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// firstLine trả về văn bản dòng đầu trong [start,end) sau khi bỏ khoảng trắng.
func firstLine(normalized []byte, start, end int) string {
	s := string(normalized[start:end])
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// bodyAfterTitle trả về chính văn của [start,end) sau khi bỏ dòng đầu (tiêu đề). Tiêu đề chương
// nhiều dòng chiếm riêng dòng đầu, chính văn ở sau; đoạn một dòng không có xuống dòng (kịch bản cắt
// bằng anchor) thì cả đoạn là chính văn, lúc đó trả cả đoạn thay vì chuỗi rỗng — nếu không tiểu
// thuyết hợp lệ một dòng/một dòng nhiều chương sẽ bị phán nhầm "chính văn rỗng" mà từ chối (RFC §8.3).
func bodyAfterTitle(normalized []byte, start, end int) string {
	s := string(normalized[start:end])
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// planChunks cắt units thành các khoảng chỉ số owned [start,end) không chồng nhau, phủ trọn, theo
// ngân sách byte. Kích thước khối tính theo ngân sách ngữ cảnh, không theo số dòng hay số chương cố định (RFC §8.1).
func planChunks(units []SourceUnit, budgetBytes int) [][2]int {
	if len(units) == 0 {
		return nil
	}
	if budgetBytes <= 0 {
		return [][2]int{{0, len(units)}}
	}
	var chunks [][2]int
	start := 0
	acc := 0
	for i, u := range units {
		size := u.EndByte - u.StartByte
		if acc > 0 && acc+size > budgetBytes {
			chunks = append(chunks, [2]int{start, i})
			start = i
			acc = 0
		}
		acc += size
	}
	chunks = append(chunks, [2]int{start, len(units)})
	return chunks
}

// buildProjection lắp payload projection có cấu trúc của một khoảng owned (kèm chút ngữ cảnh),
// mô hình chỉ trả ranh giới cho owned. Đồng thời trả tập hợp toàn bộ unit_id trong projection
// (owned + vùng ngữ cảnh), để kiểm tra đầu ra phân biệt ảo giác với vượt biên.
func buildProjection(units []SourceUnit, owned [2]int, contextMargin, ctxBudget int, guidance string) (string, map[string]bool) {
	// Vùng ngữ cảnh co theo hai trần: số unit và byte (ctxBudget<=0 thì chỉ theo số unit): các unit
	// margin thường là dòng thường, nhưng phân mảnh ảo của dòng siêu dài có thể đạt MaxUnitBytes,
	// vài cái là nuốt sạch ngân sách đầu vào — ngữ cảnh chỉ là thông tin tham khảo, không đáng giá đó.
	lo, budget := owned[0], ctxBudget
	for lo > 0 && owned[0]-lo < contextMargin {
		if n := len(units[lo-1].Text); ctxBudget > 0 {
			if n > budget {
				break
			}
			budget -= n
		}
		lo--
	}
	hi, budget := owned[1], ctxBudget
	for hi < len(units) && hi-owned[1] < contextMargin {
		if n := len(units[hi].Text); ctxBudget > 0 {
			if n > budget {
				break
			}
			budget -= n
		}
		hi++
	}
	type projUnit struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	proj := struct {
		OwnedStart   string     `json:"owned_start"`
		OwnedEnd     string     `json:"owned_end"`
		Units        []projUnit `json:"units"`
		UserGuidance string     `json:"user_guidance,omitempty"`
	}{
		OwnedStart:   units[owned[0]].ID,
		OwnedEnd:     units[owned[1]-1].ID,
		UserGuidance: guidance,
	}
	ids := make(map[string]bool, hi-lo)
	for i := lo; i < hi; i++ {
		proj.Units = append(proj.Units, projUnit{ID: units[i].ID, Text: units[i].Text})
		ids[units[i].ID] = true
	}
	data, _ := json.MarshalIndent(proj, "", "  ")
	return string(data), ids
}

// segmentInputDigest bao trùm đầu vào ngữ nghĩa mà hành động phân đoạn thật sự tiêu thụ: nguồn đã
// chuẩn hóa, hướng dẫn người dùng, phiên bản prompt (RFC §6.3).
func segmentInputDigest(normalizedDigest, guidance, promptVersion string) string {
	return Digest([]byte(strings.Join([]string{"segment", promptVersion, normalizedDigest, guidance}, "\x00")))
}

// segmentChunkPath / segmentChunkDigest: đường dẫn artifact và danh tính của cache ranh giới cấp khối.
// Danh tính ràng buộc danh tính phân tách (nguồn+hướng dẫn+phiên bản prompt) với phạm vi unit owned
// của khối — bất kỳ thay đổi thượng nguồn nào cũng làm cache tự nhiên mất khớp.
func segmentChunkPath(owned [2]int) string {
	return fmt.Sprintf("%s/chunk-%06d-%06d.json", dirSegmentChunks, owned[0], owned[1])
}

func segmentChunkDigest(identity, loID, hiID string) string {
	return Digest([]byte(strings.Join([]string{"segment-chunk", identity, loID, hiID}, "\x00")))
}

// Segment phân tách ngữ nghĩa toàn bộ văn bản đã chuẩn hóa: gọi mô hình nhận diện ranh giới theo
// từng khoảng owned, rồi kiểm tra độ phủ toàn văn. contextMargin là số unit ngữ cảnh, chunkBytes là
// ngân sách byte của khoảng owned, maxTokens là ngân sách đầu ra mỗi lần. w khác rỗng thì ghi cache
// ranh giới từng khối (identity = segmentInputDigest): một khối có thể kéo dài nhiều phút, một khối
// thất bại không nên trả lại tiền lời gọi của các khối đã xong — cùng triết lý với analyze từng
// chương, synthesize từng khoảng; trước đây phân tách là giai đoạn đắt duy nhất không có bền hóa
// trong giai đoạn,chỗ thất bại là làm lại tất cả.
func Segment(ctx context.Context, m callModel, systemPrompt string, normalized []byte, units []SourceUnit, guidance string, chunkBytes, contextMargin, maxTokens int, prof callProfile, w *Workspace, identity string) (*Segmentation, error) {
	chunks := planChunks(units, planningBudget(chunkBytes, systemPrompt, guidance))
	unitByID := make(map[string]SourceUnit, len(units))
	for _, u := range units {
		unitByID[u.ID] = u
	}
	var decisions []BoundaryDecision
	// chunk xử lý một khoảng owned: cache trúng thì zero lời gọi; đầu ra bị cắt ngắn độ dài mà khoảng
	// còn chia được thì chia đôi khối thử lại đệ quy (nhiều chương ngắn sẽ khiến JSON ranh giới vượt đầu
	// ra khả kiến, cùng triết lý thu nhỏ lô của analyze) — nửa khối có
	// Đường dẫn cache độc lập, thành quả thử lại không phải trả lại; cắt đến cấp unit mới là thiếu
	// dung lượng thật.
	var chunk func(owned [2]int, cur, total int) ([]BoundaryDecision, error)
	chunk = func(owned [2]int, cur, total int) ([]BoundaryDecision, error) {
		lo, hi := units[owned[0]], units[owned[1]-1]
		rel, want := segmentChunkPath(owned), segmentChunkDigest(identity, lo.ID, hi.ID)
		if w != nil {
			if art, err := readArtifact[boundaryBatch](w, rel); err == nil && art.InputDigest == want {
				return art.Payload.Boundaries, nil
			}
		}
		// Một lời gọi mô hình đơn khối có thể kéo dài nhiều phút, hiển thị lại tiến độ từng khối + số
		// ranh giới lũy kế, bảng mới không im lặng cả đoạn trông như treo.
		prof.step(cur, total, "phân tách khối %d/%d (%s..%s), đã nhận diện %d ranh giới...",
			cur, total, lo.ID, hi.ID, len(decisions))
		// Trần byte vùng ngữ cảnh lấy chunkBytes/8 nhưng không thấp hơn 4096: cái cần chặn là phân
		// mảnh ảo của dòng siêu dài (một mảnh có thể đạt MaxUnitBytes) nuốt ngân sách đầu vào, chi phí margin
		// của dòng thường vốn vô hại.
		payload, projIDs := buildProjection(units, owned, contextMargin, max(chunkBytes/8, 4096), guidance)
		ownedIDs := make(map[string]bool, owned[1]-owned[0])
		for i := owned[0]; i < owned[1]; i++ {
			ownedIDs[units[i].ID] = true
		}
		v := chunkValidator{projIDs: projIDs, ownedIDs: ownedIDs, unitByID: unitByID,
			normalized: normalized, coverStart: owned[0] == 0}
		batch, err := callStructured[boundaryBatch](ctx, m, segmentContract, systemPrompt, payload, maxTokens, prof, func(b *boundaryBatch) error {
			return v.validate(b.Boundaries)
		})
		if err != nil {
			var tr *errTruncated
			if errors.As(err, &tr) && owned[1]-owned[0] > 1 {
				mid := (owned[0] + owned[1]) / 2
				prof.step(0, 0, "đầu ra ranh giới khối %s..%s bị cắt ngắn (chương quá dày), chia đôi khối rồi thử lại", lo.ID, hi.ID)
				prof.logger().Warn("imp đầu ra phân tách bị cắt ngắn, chia đôi khối", "chunk", lo.ID+".."+hi.ID)
				left, lerr := chunk([2]int{owned[0], mid}, cur, total)
				if lerr != nil {
					return nil, lerr
				}
				right, rerr := chunk([2]int{mid, owned[1]}, cur, total)
				if rerr != nil {
					return nil, rerr
				}
				return append(left, right...), nil
			}
			return nil, fmt.Errorf("phân tách khoảng %s..%s: %w", lo.ID, hi.ID, err)
		}
		// Ranh giới vùng ngữ cảnh thuộc quyền khối liền kề (nó sẽ tự báo lại một lần trong khoảng owned
		// của mình), Go cắt bỏ trực tiếp: kỷ luật tọa độ do code thi hành, thử lại ngữ nghĩa chỉ dành
		// cho thất bại ngữ nghĩa thật — hành vi cũ hỏi lại với phản hồi vượt biên, mô hình yếu thường
		// cạn sạch cả 3 lần thử kéo sập cả khối (RFC §8.1 "mô hình quản ngữ nghĩa, Go quản tọa độ").
		kept := make([]BoundaryDecision, 0, len(batch.Boundaries))
		for _, bd := range batch.Boundaries {
			if ownedIDs[bd.UnitID] {
				kept = append(kept, bd)
			}
		}
		if n := len(batch.Boundaries) - len(kept); n > 0 {
			// Kỷ luật tọa độ thường lệ chứ không phải bất thường, hiển thị lại bằng tiến độ thường —
			// màu cảnh báo sẽ khiến người dùng tưởng lỗi.
			prof.step(0, 0, "đã cắt bỏ %d ranh giới vùng ngữ cảnh báo thừa (khối liền kề tự báo, không phải lỗi)", n)
		}
		// Hiển thị lại phán đoán ngữ nghĩa của mô hình (tiêu đề nhận diện được), cho người dùng thấy mô
		// hình đọc hiểu gì, chứ không chỉ số đếm máy móc.
		if len(kept) > 0 {
			prof.step(0, 0, "mô hình nhận diện ra: %s", previewBoundaries(kept))
		}
		if w != nil {
			if err := writeArtifact(w, rel, want, boundaryBatch{Boundaries: kept}); err != nil {
				return nil, fmt.Errorf("ghi khối phân tách %s..%s: %w", lo.ID, hi.ID, err)
			}
		}
		return kept, nil
	}
	for ci, owned := range chunks {
		kept, err := chunk(owned, ci+1, len(chunks))
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, kept...)
	}
	seg, err := resolveSegmentation(normalized, units, decisions)
	if err != nil {
		// Khi tích hợp hồi kết thất bại, cache khối đã vô giá trị: digest luôn khớp sẽ khiến chạy
		// lại không gọi model nào mà đọc lại cùng mẻ ranh giới, tái hiện tất định cùng thất bại. Xóa
		// cache đổi lấy cơ hội gọi mô hình phân tách lại lần sau; snapshot quyết định qua errSemantic
		// ghi tập trung vào failures/ phục vụ điều tra sau. Xóa thất bại phải báo trung thực — nói dối
		// đã xóa sẽ khiến người dùng chạy lại lần nữa vẫn đọc cache hỏng (Debug-First).
		hint := "cache khối đã xóa, chạy lại sẽ phân tách lại"
		if w != nil {
			if cerr := w.clearDir(dirSegmentChunks); cerr != nil {
				hint = fmt.Sprintf("xóa cache khối thất bại: %v, trước khi chạy lại hãy xóa tay meta/import/segment-chunks/", cerr)
			}
		}
		raw, _ := json.MarshalIndent(decisions, "", "  ")
		return nil, &errSemantic{Raw: string(raw), Err: fmt.Errorf("tích hợp phân tách toàn sách thất bại (%s): %w", hint, err)}
	}
	return seg, nil
}

// planningBudget trừ overhead cấu trúc của yêu cầu khỏi ngân sách đầu vào: system prompt và hướng
// dẫn trừ theo độ dài thật, phần còn lại quy 3/4 cho sự phình của bao bọc JSON projection (id/quote/escape
// ≈ 1/3 chính văn) — chính văn owned chỉ là một phần yêu cầu, quy hoạch theo trần đủ sẽ vượt ngân sách
// đầu vào thật khi prompt dài hoặc vùng ngữ cảnh lớn. Trần dưới chunkBytes/4 chống prompt siêu dài ép
// ngân sách thành âm; chunkBytes<=0 nghĩa là không ngân sách (một khối), truyền nguyên.
func planningBudget(chunkBytes int, systemPrompt, guidance string) int {
	if chunkBytes <= 0 {
		return chunkBytes
	}
	b := (chunkBytes - len(systemPrompt) - len(guidance)) * 3 / 4
	return max(b, chunkBytes/4)
}

// boundaryLabel đưa cho quyết định ranh giới một định danh dễ đọc: ưu tiên tiêu đề, không tiêu đề
// thì lùi về kind@unit_id.
func boundaryLabel(d BoundaryDecision) string {
	if t := strings.TrimSpace(d.Title); t != "" {
		return t
	}
	return d.Kind + "@" + d.UnitID
}

// previewBoundaries nén một mẻ quyết định ranh giới thành một dòng xem trước tiêu đề (tối đa 3 cái
// + đếm), để bảng hiển thị lại.
func previewBoundaries(bs []BoundaryDecision) string {
	titles := make([]string, 0, 3)
	for _, b := range bs {
		titles = append(titles, snippet(boundaryLabel(b), 24))
		if len(titles) == 3 {
			break
		}
	}
	s := strings.Join(titles, " / ")
	if len(bs) > len(titles) {
		s += fmt.Sprintf("(tổng %d chỗ)", len(bs))
	}
	return s
}

// chunkValidator mang ngữ cảnh kiểm tra thời điểm gọi của một lần gọi phân tách: unit_id ngoài
// projection là ảo giác; ranh giới vùng owned còn phải kind hợp lệ, anchor giải được, cùng vị trí
// không xung đột ngữ nghĩa; khối đầu phải có ranh giới chặn điểm đầu văn bản. Các giá trị hỏng này
// không chặn ở thời điểm gọi sẽ theo khối rơi vào cache — digest luôn khớp, chạy lại không gọi model
// nào mà đọc lại cùng dữ liệu hỏng, thất bại tái hiện tất định (RFC §8.3). Phán đoán ngữ nghĩa (giữ
// cái nào, mở đầu là gì) qua hỏi lại trả cho mô hình, Go không trả thay; ranh giới vùng ngữ cảnh
// chắc chắn bị kỷ luật tọa độ cắt, không hỏi lại vì nó.
type chunkValidator struct {
	projIDs, ownedIDs map[string]bool
	unitByID          map[string]SourceUnit
	normalized        []byte
	coverStart        bool // khối đầu: văn bản khác rỗng trước điểm đầu văn bản phải có ranh giới phân bổ
}

func (v chunkValidator) validate(bs []BoundaryDecision) error {
	seen := make(map[int]BoundaryDecision)
	first := -1
	for _, b := range bs {
		if b.UnitID == "" {
			return fmt.Errorf("ranh giới thiếu unit_id")
		}
		if !v.projIDs[b.UnitID] {
			return fmt.Errorf("ranh giới unit_id %q không tồn tại trong projection lần này", b.UnitID)
		}
		if !v.ownedIDs[b.UnitID] {
			continue
		}
		switch b.Kind {
		case kindChapter, kindGroup, kindFrontMatter, kindBackMatter:
		default:
			return fmt.Errorf("ranh giới %s kind bất hợp lệ: %q (chỉ có thể là chapter/group/front_matter/back_matter)", b.UnitID, b.Kind)
		}
		at, err := resolveBoundaryByte(v.unitByID, b.UnitID, b.Anchor)
		if err != nil {
			return err
		}
		// Hiển thị lại tiêu đề: tiêu đề của chapter/group phải thật sự tồn tại trong nguyên văn unit
		// chứa ranh giới (bỏ qua khác biệt khoảng trắng) — tiêu đề bịa bị sự thật chặn tại đây (thực đo
		// một nguồn 157 chương thì 67 chương là mô hình tạo ranh giới trên văn nối giữa chương + bịa
		// tiêu đề). Quyền cân nhắc ngữ nghĩa vẫn về mô hình: nguồn thật sự không có quy ước tiêu đề thì đặt
		// uncertain để giữ tiêu đề quy nạp; tiêu đề mô tả của front/back matter rủi ro thấp, không đối chiếu.
		if (b.Kind == kindChapter || b.Kind == kindGroup) && !b.Uncertain {
			if t := squashSpace(b.Title); t != "" && !strings.Contains(squashSpace(v.unitByID[b.UnitID].Text), t) {
				return fmt.Errorf("không tìm thấy tiêu đề %q của ranh giới %s trong nguyên văn unit: nếu đây là chính văn nối tiếp của chương trước, đừng đặt ranh giới cho nó (do ranh giới trước phân bổ, boundaries có thể rỗng); nếu nguồn đúng là không có dòng tiêu đề ở đây mà tiêu đề là bạn quy nạp, hãy đặt uncertain=true",
					b.UnitID, snippet(b.Title, 24))
			}
		}
		// Xung đột cùng vị trí (kind/tiêu đề khác) là vấn đề ngữ nghĩa, giữ cái nào Go không phán
		// định; trùng lặp hoàn toàn giống nhau là dư máy móc, cho qua rồi resolve khử trùng vô thanh.
		if prev, ok := seen[at]; ok {
			if prev.Kind != b.Kind || boundaryLabel(prev) != boundaryLabel(b) {
				return fmt.Errorf("ranh giới %q và %q rơi vào cùng một vị trí (%s), xung đột ngữ nghĩa, chỉ giữ lại một cái đúng",
					boundaryLabel(prev), boundaryLabel(b), b.UnitID)
			}
		} else {
			seen[at] = b
		}
		if first < 0 || at < first {
			first = at
		}
	}
	if v.coverStart {
		head := first
		if head < 0 {
			head = len(v.normalized) // khối đầu không báo một ranh giới owned nào: toàn bộ văn bản đầu chưa phân bổ
		}
		if head > 0 && strings.TrimSpace(string(v.normalized[:head])) != "" {
			return fmt.Errorf("văn bản %d byte đầu (%s…) chưa được phân bổ cho ranh giới nào, hãy bổ sung ranh giới cho mở đầu văn bản (front_matter/chapter/group)",
				head, snippet(string(v.normalized[:min(head, 48)]), 24))
		}
	}
	return nil
}
