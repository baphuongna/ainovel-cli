package imp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

// TestDiscardAnalysesAfter canh giữ #4a: dọn artifact phân tích cũ vượt qua tiền tố tươi, bảo đảm
// "phân tích lại chương nào là vô hiệu toàn bộ phân tích phía sau", ngăn ledger cũ bị tái dùng theo
// các chương sau.
func TestDiscardAnalysesAfter(t *testing.T) {
	ws := OpenWorkspace(t.TempDir())
	for c := 1; c <= 5; c++ {
		if err := writeArtifact(ws, analysisPath(c), "d", ChapterAnalysisPayload{Facts: ImportedChapterFacts{Chapter: c}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := discardAnalysesAfter(ws, 2, 5); err != nil {
		t.Fatalf("dọn dẹp không nên thất bại: %v", err)
	}
	for c := 1; c <= 2; c++ {
		if !ws.has(analysisPath(c)) {
			t.Fatalf("chương %d trong tiền tố tươi phải được giữ", c)
		}
	}
	for c := 3; c <= 5; c++ {
		if ws.has(analysisPath(c)) {
			t.Fatalf("chương %d vượt qua tiền tố tươi phải bị dọn", c)
		}
	}
}

// analyzeFixture dựng một phân tách gồm n chương, chính văn đều rất ngắn, cho test lô/phân tích.
func analyzeFixture(t *testing.T, n int) ([]byte, *Segmentation) {
	t.Helper()
	var b strings.Builder
	for c := 1; c <= n; c++ {
		b.WriteString("第")
		b.WriteString(strings.Repeat("一", 1))
		b.WriteString("章\n正文\n")
	}
	norm := []byte(b.String())
	units := buildSourceUnits(norm, 0)
	var ds []BoundaryDecision
	for i := 0; i < len(units); i += 2 { // mỗi 2 dòng một chương (dòng tiêu đề + dòng chính văn)
		ds = append(ds, BoundaryDecision{UnitID: units[i].ID, Kind: kindChapter, Title: units[i].Text})
	}
	seg, err := resolveSegmentation(norm, units, ds)
	if err != nil {
		t.Fatalf("phân tách fixture thất bại: %v", err)
	}
	if len(seg.Chapters) != n {
		t.Fatalf("số chương fixture %d != %d", len(seg.Chapters), n)
	}
	return norm, seg
}

func TestPlanBatchOutputBudgetCaps(t *testing.T) {
	_, seg := analyzeFixture(t, 10)
	// Đầu vào rộng, nhưng ngân sách đầu ra khả kiến chỉ đủ 2 chương (canh độ hạt lô #83, §20.4.2).
	b := AnalyzeBudget{ContextBytes: 1 << 20, MaxOutputTokens: 250, PerChapterOutput: 100, PromptOverhead: 0}
	end := planBatch(seg.Chapters, 0, 0, b)
	if end != 2 {
		t.Fatalf("ngân sách đầu ra phải giới hạn lô còn 2 chương, được end=%d", end)
	}
}

func TestPlanBatchInputBudgetCaps(t *testing.T) {
	_, seg := analyzeFixture(t, 10)
	// Đầu ra rộng, nhưng ngân sách byte đầu vào chỉ đủ chừng 1 chương.
	one := chapterBytes(seg.Chapters, 0)
	b := AnalyzeBudget{ContextBytes: one + 1, MaxOutputTokens: 1 << 20, PerChapterOutput: 1, PromptOverhead: 0}
	end := planBatch(seg.Chapters, 0, 0, b)
	if end != 1 {
		t.Fatalf("ngân sách đầu vào phải giới hạn lô còn 1 chương, được end=%d", end)
	}
}

func factsJSON(chapter int, title string) string {
	f := map[string]any{
		"chapter": chapter, "title": title, "summary": "摘要", "core_event": "核心事件",
		"key_events": []string{"事件"}, "hook": nil, "scenes": []string{}, "characters": []string{},
		"character_evidence": []any{}, "world_evidence": []any{}, "timeline_events": []any{},
		"foreshadow_updates": []any{}, "relationship_changes": []any{}, "state_changes": []any{},
		"hook_type": "mystery", "dominant_strand": "quest",
	}
	data, _ := json.Marshal(f)
	return string(data)
}

func TestValidateBatchRejections(t *testing.T) {
	_, seg := analyzeFixture(t, 2)
	// Số lượng không khớp
	bad := &AnalysisBatchResult{Chapters: []ImportedChapterFacts{{Chapter: 1}}}
	if err := validateBatch(bad, seg, 0, 2); err == nil {
		t.Fatal("số lượng không khớp phải bị từ chối")
	}
	// hook_type bất hợp lệ
	var f ImportedChapterFacts
	_ = json.Unmarshal([]byte(factsJSON(1, seg.Chapters[0].Title)), &f)
	f.HookType = "bogus"
	if err := validateBatch(&AnalysisBatchResult{Chapters: []ImportedChapterFacts{f}}, seg, 0, 1); err == nil {
		t.Fatal("hook_type bất hợp lệ phải bị từ chối")
	}
	// Biến thể hoa/thường của enum: qua kiểm tra thì chuẩn hóa tại chỗ thành chữ thường —
	// commit_chapter không kiểm tra lại enum, biến thể đi thẳng vào trạng thái chính thức sẽ bị
	// logic tiêu thụ chuỗi chính xác coi là loại lạ.
	_ = json.Unmarshal([]byte(factsJSON(1, seg.Chapters[0].Title)), &f)
	f.HookType, f.DominantStrand = "Crisis", "QUEST"
	got := &AnalysisBatchResult{Chapters: []ImportedChapterFacts{f}}
	if err := validateBatch(got, seg, 0, 1); err != nil {
		t.Fatalf("biến thể hoa/thường phải qua kiểm tra: %v", err)
	}
	if got.Chapters[0].HookType != "crisis" || got.Chapters[0].DominantStrand != "quest" {
		t.Fatalf("enum phải chuẩn hóa chữ thường rồi ghi: %+v", got.Chapters[0])
	}
}

func TestAnalyzeNextPersistsWithRebatchOnTruncation(t *testing.T) {
	norm, seg := analyzeFixture(t, 2)
	book := t.TempDir()
	ws := &Workspace{dir: book}
	// Lô đầu 2 chương bị cắt: chương 1 trọn, chương 2 đứa nửa → cứu vớt tiền tố liên tiếp chương 1 (§9.5).
	truncated := `{"chapters":[` + factsJSON(1, seg.Chapters[0].Title) + `,{"chapter":2,"summary":"截断`
	m := &mockModel{
		responses: []string{truncated},
		stops:     []agentcore.StopReason{agentcore.StopReasonLength},
	}
	budget := AnalyzeBudget{ContextBytes: 1 << 20, MaxOutputTokens: 1000, PerChapterOutput: 10, PromptOverhead: 0}
	done, err := AnalyzeNext(context.Background(), m, "sys", ws, norm, seg, "segid", "v1", budget, callProfile{})
	if err != nil {
		t.Fatalf("AnalyzeNext: %v", err)
	}
	if done != 1 {
		t.Fatalf("bị cắt phải cứu vớt tiền tố liên tiếp chương 1, được %d", done)
	}
	if !ws.has(analysisPath(1)) || ws.has(analysisPath(2)) {
		t.Fatal("phải chỉ ghi chương 1")
	}
	if analyzedChapters(ws, seg, norm, "segid", "v1") != 1 {
		t.Fatal("số chương đã phân tích phải là 1")
	}
	// failures/ phải lưu phản hồi gốc và trạng thái cứu vớt (§14.2).
	if !ws.has("failures/last-response.txt") || !ws.has("failures/last.json") {
		t.Fatal("phải lưu phản hồi gốc thất bại và metadata")
	}
}

func TestSalvagePrefixContiguous(t *testing.T) {
	_, seg := analyzeFixture(t, 3)
	// 2 chương đầu trọn vẹn, chương 3 bị cắt đứt.
	raw := `{"chapters":[` +
		factsJSON(1, seg.Chapters[0].Title) + `,` +
		factsJSON(2, seg.Chapters[1].Title) + `,` +
		`{"chapter":3,"summary":"截断`
	got := salvagePrefix(raw, seg, 0)
	if len(got) != 2 {
		t.Fatalf("phải cứu vớt tiền tố liên tiếp 2 chương đầu, được %d", len(got))
	}
	if got[0].Chapter != 1 || got[1].Chapter != 2 {
		t.Fatal("số chương tiền tố không liên tiếp")
	}
}

func TestSalvagePrefixStopsAtGap(t *testing.T) {
	_, seg := analyzeFixture(t, 3)
	// Sau chương 1 nhảy thẳng đến chương 3 → cứu vớt dừng chỗ bỏ số, chỉ trả chương 1.
	raw := `{"chapters":[` + factsJSON(1, seg.Chapters[0].Title) + `,` + factsJSON(3, seg.Chapters[2].Title) + `]}`
	got := salvagePrefix(raw, seg, 0)
	if len(got) != 1 {
		t.Fatalf("phải dừng tại chỗ bỏ số, được %d", len(got))
	}
}

// TestAnalyzedChaptersInvalidatesOnUpstreamChange xác nhận danh tính phân tách hoặc phiên bản prompt
// thay đổi làm phân tích đã ghi vô hiệu (bất biến 1). Đây là lõi mà cơ chế InputDigest thật sự đáp
// đất: đổi thượng nguồn là vô hiệu hạ nguồn, chứ không phải chỉ nhìn tệp có tồn tại hay không.
func TestAnalyzedChaptersInvalidatesOnUpstreamChange(t *testing.T) {
	norm, seg := analyzeFixture(t, 2)
	ws := &Workspace{dir: t.TempDir()}
	m := &mockModel{responses: []string{
		`{"chapters":[` + factsJSON(1, seg.Chapters[0].Title) + `,` + factsJSON(2, seg.Chapters[1].Title) + `]}`,
	}}
	budget := AnalyzeBudget{ContextBytes: 1 << 20, MaxOutputTokens: 1 << 20, PerChapterOutput: 10, PromptOverhead: 0}
	if _, err := AnalyzeNext(context.Background(), m, "sys", ws, norm, seg, "segid-A", "v1", budget, callProfile{}); err != nil {
		t.Fatalf("AnalyzeNext: %v", err)
	}
	if got := analyzedChapters(ws, seg, norm, "segid-A", "v1"); got != 2 {
		t.Fatalf("cùng danh tính/phiên bản phải công nhận 2 chương, được %d", got)
	}
	if got := analyzedChapters(ws, seg, norm, "segid-B", "v1"); got != 0 {
		t.Fatalf("danh tính phân tách thay đổi phải vô hiệu toàn bộ phân tích, được %d", got)
	}
	if got := analyzedChapters(ws, seg, norm, "segid-A", "v2"); got != 0 {
		t.Fatalf("phiên bản prompt thay đổi phải vô hiệu toàn bộ phân tích, được %d", got)
	}
}
