package imp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"maps"
	"os"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// analysisSchemaVersion là phiên bản schema sự thực từng chương, đưa vào InputDigest.
const analysisSchemaVersion = 2

// ImportedCharacterFact / ImportedWorldFact là quan sát cô đọng dùng cho tổng hợp toàn sách,
// không ghi trực tiếp thành nhân vật hay quy tắc thế giới chính thức. Ít nhất mang số chương,
// để kết quả tổng hợp có nguồn gốc ổn định (RFC §9.1).
type ImportedCharacterFact struct {
	Chapter int    `json:"chapter"`
	Name    string `json:"name"`
	Note    string `json:"note,omitempty"`
}

type ImportedWorldFact struct {
	Chapter  int    `json:"chapter"`
	Category string `json:"category,omitempty"`
	Fact     string `json:"fact"`
}

// ImportedChapterFacts là sản phẩm có cấu trúc suy ngược từ một chương (RFC §9.1).
type ImportedChapterFacts struct {
	Chapter             int                        `json:"chapter"`
	Title               string                     `json:"title"`
	Summary             string                     `json:"summary"`
	KeyEvents           []string                   `json:"key_events"`
	CoreEvent           string                     `json:"core_event"`
	Hook                string                     `json:"hook,omitempty"`
	Scenes              []string                   `json:"scenes,omitempty"`
	Characters          []string                   `json:"characters,omitempty"`
	CharacterEvidence   []ImportedCharacterFact    `json:"character_evidence,omitempty"`
	WorldEvidence       []ImportedWorldFact        `json:"world_evidence,omitempty"`
	TimelineEvents      []domain.TimelineEvent     `json:"timeline_events,omitempty"`
	ForeshadowUpdates   []domain.ForeshadowUpdate  `json:"foreshadow_updates,omitempty"`
	RelationshipChanges []domain.RelationshipEntry `json:"relationship_changes,omitempty"`
	StateChanges        []domain.StateChange       `json:"state_changes,omitempty"`
	HookType            string                     `json:"hook_type"`
	DominantStrand      string                     `json:"dominant_strand"`
}

// AnalysisBatchResult là kết quả có cấu trúc của một lần gọi lô, mỗi phần tử là sự thực một chương.
type AnalysisBatchResult struct {
	Chapters []ImportedChapterFacts `json:"chapters"`
}

// ChapterAnalysisPayload là payload artifact phân tích một chương; các chương cùng lô ghi cùng BatchStart/BatchEnd.
type ChapterAnalysisPayload struct {
	BatchStart int                  `json:"batch_start"`
	BatchEnd   int                  `json:"batch_end"`
	Facts      ImportedChapterFacts `json:"facts"`
}

// AnalyzeBudget là ngân sách kép đầu vào/đầu ra của phân tích từng chương (RFC §9.2).
// Đầu vào xấp xỉ context window bằng byte; đầu ra xấp xỉ trần completion bằng dự phòng sự thực bảo thủ mỗi chương.
type AnalyzeBudget struct {
	ContextBytes     int // ngân sách đầu vào (chính văn + ledger + overhead)
	MaxOutputTokens  int // ngân sách đầu ra khả kiến (trần completion)
	PerChapterOutput int // dự phòng bảo thủ mỗi chương
	PromptOverhead   int // overhead đầu vào cố định của system/ledger (byte)
}

func analysisPath(chapter int) string {
	return fmt.Sprintf("%s/%06d.json", dirAnalyses, chapter)
}

// analyzedChapters trả về số artifact phân tích liên tục từ chương 1 mà InputDigest khớp với
// danh tính phân tách / phiên bản / chính văn hiện tại (RFC §9.6). Thiếu, lỗi phân tích hay digest
// mất khớp đều cắt ngang ở đây, khiến thay đổi thượng nguồn (phân tách lại, đổi phiên bản prompt/schema)
// tự nhiên vô hiệu phân tích hạ nguồn.
func analyzedChapters(w *Workspace, seg *Segmentation, normalized []byte, segIdentity, promptVersion string) int {
	n := 0
	for c := 1; c <= len(seg.Chapters); c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if err != nil {
			break
		}
		if a.InputDigest != chapterInputDigest(segIdentity, promptVersion, seg, normalized, c-1) {
			break
		}
		n++
	}
	return n
}

// analyzedChaptersStrict cùng ngữ nghĩa tươi mới với analyzedChapters, nhưng phơi bày artifact
// sẵn có bị hỏng hoặc không đọc được. Khôi phục trạng thái dùng bản strict, tránh coi lỗi đọc thật
// là "chưa phân tích" rồi ghi đè làm lại.
func analyzedChaptersStrict(w *Workspace, seg *Segmentation, normalized []byte, segIdentity, promptVersion string) (int, error) {
	n := 0
	for c := 1; c <= len(seg.Chapters); c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return n, fmt.Errorf("đọc artifact phân tích chương %d: %w", c, err)
		}
		if a.InputDigest != chapterInputDigest(segIdentity, promptVersion, seg, normalized, c-1) {
			break
		}
		n++
	}
	return n, nil
}

// discardAnalysesAfter xóa artifact phân tích từng chương có số chương > keep, để "phân tích lại
// chương nào thì vô hiệu toàn bộ phân tích phía sau" thành lập (#4a). Khi phân tích tiến thuận
// bình thường thì sau keep vốn không còn artifact, là no-op idempotent; chỉ dọn đuôi cũ khi phân
// tích lại giữa chừng (vượt qua tiền tố tươi). Lỗi xóa phải lan truyền: đây là điểm thi hành duy
// nhất của bất biến đó, nuốt lỗi sẽ khiến đuôi cũ (digest từng chương luôn khớp) bị coi là tiền
// tố tươi mà tái sử dụng, tổng hợp sẽ tiêu thụ sự thực trộn mới cũ mà không báo lỗi gì.
func discardAnalysesAfter(w *Workspace, keep, total int) error {
	for c := keep + 1; c <= total; c++ {
		if err := os.Remove(w.path(analysisPath(c))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("dọn artifact phân tích cũ %s: %w", analysisPath(c), err)
		}
	}
	return nil
}

// loadPriorFacts đọc sự thực đã ghi của chương 1..count, để dựng ledger.
func loadPriorFacts(w *Workspace, count int) []ImportedChapterFacts {
	var out []ImportedChapterFacts
	for c := 1; c <= count; c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if err != nil {
			break
		}
		out = append(out, a.Payload.Facts)
	}
	return out
}

func loadPriorFactsStrict(w *Workspace, count int) ([]ImportedChapterFacts, error) {
	out := make([]ImportedChapterFacts, 0, count)
	for c := 1; c <= count; c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if err != nil {
			return out, fmt.Errorf("đọc sự thực phân tích chương %d: %w", c, err)
		}
		out = append(out, a.Payload.Facts)
	}
	return out, nil
}

// buildLedger suy ngữ cảnh liên tục cô đọng từ các chương đã phân tích: biệt danh nhân vật +
// ID phục bút đang hoạt động + trạng thái gần đây.
func buildLedger(prior []ImportedChapterFacts) string {
	if len(prior) == 0 {
		return ""
	}
	names := map[string]bool{}
	active := map[string]string{} // foreshadow id -> desc
	var recent []string
	for _, f := range prior {
		for _, c := range f.Characters {
			names[c] = true
		}
		for _, fu := range f.ForeshadowUpdates {
			switch fu.Action {
			case "plant", "advance":
				if fu.Description != "" {
					active[fu.ID] = fu.Description
				} else if _, ok := active[fu.ID]; !ok {
					active[fu.ID] = ""
				}
			case "resolve":
				delete(active, fu.ID)
			}
		}
	}
	if len(prior) > 0 {
		last := prior[len(prior)-1]
		for _, sc := range last.StateChanges {
			recent = append(recent, fmt.Sprintf("%s.%s=%s", sc.Entity, sc.Field, sc.NewValue))
		}
	}
	var b strings.Builder
	if len(names) > 0 {
		b.WriteString("Nhân vật đã biết: ")
		b.WriteString(strings.Join(slices.Sorted(maps.Keys(names)), ", "))
		b.WriteString("\n")
	}
	if len(active) > 0 {
		b.WriteString("Phục bút đang hoạt động (tái sử dụng ID, đừng bịa mới):\n")
		for _, id := range slices.Sorted(maps.Keys(active)) {
			fmt.Fprintf(&b, "- %s: %s\n", id, active[id])
		}
	}
	if len(recent) > 0 {
		b.WriteString("Trạng thái gần đây: ")
		b.WriteString(strings.Join(recent, "; "))
		b.WriteString("\n")
	}
	return b.String()
}

// planBatch từ chương start, theo ngân sách kép đầu vào/đầu ra trả về điểm cuối lô liên tiếp end
// ([start,end), chỉ số chương từ 0). Ít nhất 1 chương; chương đơn dù vượt ngân sách cũng thành lô
// riêng, bên thi hành báo dung lượng không đủ khi bị cắt ngắn (RFC §9.2).
func planBatch(chapters []ChapterSpan, start, ledgerBytes int, b AnalyzeBudget) int {
	end := start + 1
	if b.ContextBytes <= 0 || b.MaxOutputTokens <= 0 || b.PerChapterOutput <= 0 {
		return end // ngân sách chưa cấu hình: từng chương một
	}
	inAcc := ledgerBytes + b.PromptOverhead + chapterBytes(chapters, start)
	outAcc := b.PerChapterOutput
	for end < len(chapters) {
		cb := chapterBytes(chapters, end)
		if inAcc+cb > b.ContextBytes {
			break
		}
		if outAcc+b.PerChapterOutput > b.MaxOutputTokens {
			break
		}
		inAcc += cb
		outAcc += b.PerChapterOutput
		end++
	}
	return end
}

func chapterBytes(chapters []ChapterSpan, i int) int {
	return chapters[i].End - chapters[i].Start
}

// chapterInputDigest ràng buộc danh tính artifact phân tích theo từng chương: danh tính phân tách
// + phiên bản prompt/schema + số chương + chính văn đơn chương. Ràng theo từng chương thay vì theo
// lô — cách chia lô là chi tiết thi hành thay đổi theo khả năng mô hình, không nên để đổi mô hình
// làm vô hiệu cả loạt chương đã phân tích; ràng segIdentity (InputDigest của artifact segmentation)
// đảm bảo phân tách lại thì mọi phân tích tự mất khớp (RFC §9.1/§6.3).
func chapterInputDigest(segIdentity, promptVersion string, seg *Segmentation, normalized []byte, i int) string {
	var b strings.Builder
	b.WriteString("analyze\x00")
	b.WriteString(promptVersion)
	fmt.Fprintf(&b, "\x00v%d\x00", analysisSchemaVersion)
	b.WriteString(segIdentity)
	fmt.Fprintf(&b, "\x00ch%d\x00", seg.Chapters[i].Number)
	b.WriteString(seg.Content(normalized, i))
	return Digest([]byte(b.String()))
}

// validateBatch kiểm tra hai tầng: tầng lô liên tục không thiếu không trùng, tầng từng chương về
// miền giá trị và tham chiếu (RFC §9.4).
func validateBatch(r *AnalysisBatchResult, seg *Segmentation, start, end int) error {
	want := end - start
	if len(r.Chapters) != want {
		return fmt.Errorf("số chương của lô %d != kỳ vọng %d", len(r.Chapters), want)
	}
	for i, f := range r.Chapters {
		want := seg.Chapters[start+i]
		if f.Chapter != want.Number {
			return fmt.Errorf("mục %d của lô có số chương %d != %d", i, f.Chapter, want.Number)
		}
		if strings.TrimSpace(f.Summary) == "" || strings.TrimSpace(f.CoreEvent) == "" {
			return fmt.Errorf("chương %d summary/core_event không được để trống", f.Chapter)
		}
		if !domain.ValidHookType(strings.ToLower(f.HookType)) {
			return fmt.Errorf("chương %d hook_type bất hợp lệ: %q", f.Chapter, f.HookType)
		}
		if !domain.ValidDominantStrand(strings.ToLower(f.DominantStrand)) {
			return fmt.Errorf("chương %d dominant_strand bất hợp lệ: %q", f.Chapter, f.DominantStrand)
		}
		for j, fu := range f.ForeshadowUpdates {
			if fu.Action == "plant" && strings.TrimSpace(fu.Description) == "" {
				return fmt.Errorf("chương %d foreshadow[%d] plant cần description", f.Chapter, j)
			}
		}
		// Enum kiểm tra theo chữ thường thì ghi xuống đĩa theo chữ thường: commit_chapter không
		// kiểm tra lại enum, biến thể hoa/thường sẽ đi thẳng vào trạng thái chính thức (HookHistory
		// v.v. tiêu thụ theo chuỗi chính xác, biến thể bị coi là loại lạ), qua kiểm tra thì chuẩn hóa.
		r.Chapters[i].HookType = strings.ToLower(f.HookType)
		r.Chapters[i].DominantStrand = strings.ToLower(f.DominantStrand)
	}
	return nil
}

// AnalyzeNext gộp một lô từ bản phân tích thiếu đầu tiên và ghi nguyên tử xuống đĩa, trả về số
// chương nộp lần này. Bị cắt ngắn thì "thất bại + thu nhỏ tổ hợp lại lô" (mặc định, §9.5); lô đã
// co còn một chương mà vẫn bị cắt thì báo rõ dung lượng không đủ.
func AnalyzeNext(ctx context.Context, m callModel, systemPrompt string, w *Workspace, normalized []byte, seg *Segmentation, segIdentity, promptVersion string, budget AnalyzeBudget, prof callProfile) (int, error) {
	total := len(seg.Chapters)
	start := analyzedChapters(w, seg, normalized, segIdentity, promptVersion)
	if start >= total {
		return 0, nil
	}
	ledger := buildLedger(loadPriorFacts(w, start))
	end := planBatch(seg.Chapters, start, len(ledger), budget)

	for {
		payload := buildAnalyzePayload(normalized, seg, ledger, start, end)
		res, err := callStructured[AnalysisBatchResult](ctx, m, analysisContract, systemPrompt, payload, budget.MaxOutputTokens, prof, func(r *AnalysisBatchResult) error {
			return validateBatch(r, seg, start, end)
		})
		if err != nil {
			var tr *errTruncated
			if errors.As(err, &tr) {
				// Bị cắt ngắn thì ưu tiên cứu vớt tiền tố hợp lệ liên tục lớn nhất tính từ chương đầu
				// lô, phần đã nộp không làm lại (§9.5).
				if salvaged := salvagePrefix(tr.Raw, seg, start); len(salvaged) > 0 {
					for i, f := range salvaged {
						ch := start + i + 1
						digest := chapterInputDigest(segIdentity, promptVersion, seg, normalized, start+i)
						art := ChapterAnalysisPayload{BatchStart: start + 1, BatchEnd: end, Facts: f}
						if werr := writeArtifact(w, analysisPath(ch), digest, art); werr != nil {
							return i, fmt.Errorf("ghi chương cứu vớt %d: %w", ch, werr)
						}
					}
					w.writeFailure(FailureMeta{Stage: "analyze", Detail: fmt.Sprintf("lô %d-%d bị cắt ngắn độ dài", start+1, end),
						StopReason: "length", PrefixSalvage: fmt.Sprintf("available:%d", len(salvaged))}, tr.Raw)
					prof.logger().Info("imp phân tích bị cắt ngắn, cứu vớt tiền tố liên tiếp", "batch_start", start+1, "salvaged", len(salvaged))
					echoChapterFacts(prof, salvaged)
					return len(salvaged), nil
				}
				// Không có tiền tố cứu vớt được: ghi là không khả dụng và "thất bại + thu nhỏ tổ hợp
				// lại lô", một chương mà vẫn cắt thì báo dung lượng không đủ.
				w.writeFailure(FailureMeta{Stage: "analyze", Detail: fmt.Sprintf("lô %d-%d bị cắt ngắn độ dài, không có tiền tố cứu vớt được", start+1, end),
					StopReason: "length", PrefixSalvage: "unavailable"}, tr.Raw)
				if end-start > 1 {
					prof.logger().Warn("imp phân tích bị cắt ngắn, thu nhỏ tổ hợp lại lô", "batch", fmt.Sprintf("%d-%d", start+1, end), "prefix_salvage", "unavailable")
					end = start + (end-start)/2
					// Dòng tiến độ không Key: vừa cho người dùng thấy hành động thu nhỏ lô, vừa ngăn
					// dòng backoff của hai lần gọi độc lập trước sau bị gộp nhầm cùng Key (hợp đồng
					// Key chỉ bao backoff nhất thời trong cùng một lần gọi).
					prof.step(0, 0, "đầu ra bị cắt ngắn độ dài và không có tiền tố cứu vớt, thu nhỏ lô còn chương %d-%d rồi thử lại", start+1, end)
					continue
				}
				return 0, fmt.Errorf("chương %d lô đơn chương vẫn bị cắt ngắn độ dài, khả năng đầu ra khả kiến của mô hình không đủ", start+1)
			}
			return 0, err
		}
		for i, f := range res.Chapters {
			ch := start + i + 1
			digest := chapterInputDigest(segIdentity, promptVersion, seg, normalized, start+i)
			payloadArt := ChapterAnalysisPayload{BatchStart: start + 1, BatchEnd: end, Facts: f}
			if err := writeArtifact(w, analysisPath(ch), digest, payloadArt); err != nil {
				return i, fmt.Errorf("ghi phân tích chương %d: %w", ch, err)
			}
		}
		echoChapterFacts(prof, res.Chapters)
		return end - start, nil
	}
}

// echoChapterFacts hiển thị lại lên bảng hiểu cốt lõi của mô hình về mỗi chương — người dùng
// cần thấy mô hình đã đọc hiểu gì, chứ không chỉ số đếm lô máy móc (§14.1).
func echoChapterFacts(prof callProfile, facts []ImportedChapterFacts) {
	for _, f := range facts {
		prof.step(0, 0, "Chương %d \"%s\": %s", f.Chapter, snippet(f.Title, 24), snippet(f.CoreEvent, 60))
	}
}

// buildAnalyzePayload lắp đầu vào lô: nguyên văn các chương liên tiếp + ledger trước lô.
func buildAnalyzePayload(normalized []byte, seg *Segmentation, ledger string, start, end int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hãy phân tích chương %d-%d, trả về {\"chapters\":[mỗi chương một đối tượng sự thực]}, thứ tự mảng theo đúng số chương.\n\n", start+1, end)
	if ledger != "" {
		b.WriteString("## Ledger liên tục (tham khảo)\n\n")
		b.WriteString(ledger)
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		c := seg.Chapters[i]
		fmt.Fprintf(&b, "## Chương %d: %s\n\n", c.Number, c.Title)
		b.WriteString(seg.Content(normalized, i))
		b.WriteString("\n\n---\n\n")
	}
	return b.String()
}

// salvagePrefix phân tích từ phản hồi lô bị cắt ngắn độ dài tiền tố hợp lệ liên tục lớn nhất
// (RFC §9.5). Chỉ lưu các đối tượng liên tục từ chương đầu lô, qua kiểm tra từng chương; gặp đối
// tượng đầu tiên không trọn vẹn/bất hợp lệ/bỏ số thì dừng, các byte sau không diễn giải. Hàm thuần,
// do AnalyzeNext ưu tiên gọi khi bị cắt ngắn dung lượng, tránh vứt các chương tiền tố đã sinh trọn vẹn.
func salvagePrefix(raw string, seg *Segmentation, start int) []ImportedChapterFacts {
	arr := extractChaptersArray(raw)
	if arr == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(arr))
	if _, err := dec.Token(); err != nil { // tiêu thụ '['
		return nil
	}
	var out []ImportedChapterFacts
	for dec.More() {
		var f ImportedChapterFacts
		if err := dec.Decode(&f); err != nil {
			break // đối tượng đầu tiên không trọn vẹn, dừng
		}
		idx := start + len(out)
		if idx >= len(seg.Chapters) || f.Chapter != seg.Chapters[idx].Number {
			break // bỏ số/ra ngoài biên
		}
		one := AnalysisBatchResult{Chapters: []ImportedChapterFacts{f}}
		if err := validateBatch(&one, seg, idx, idx+1); err != nil {
			break
		}
		out = append(out, one.Chapters[0]) // validateBatch đã chuẩn hóa enum tại chỗ, lấy giá trị sau kiểm tra
	}
	return out
}

// extractChaptersArray cắt lấy văn bản mảng JSON sau "chapters" (có thể bị cắt ở đuôi).
func extractChaptersArray(raw string) string {
	i := strings.Index(raw, "\"chapters\"")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(raw[i:], '[')
	if j < 0 {
		return ""
	}
	return raw[i+j:]
}
