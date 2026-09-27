package imp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func factsN(n int) []ImportedChapterFacts {
	out := make([]ImportedChapterFacts, n)
	for i := 0; i < n; i++ {
		out[i] = ImportedChapterFacts{
			Chapter: i + 1, Title: "第" + itoa(i+1) + "章", CoreEvent: "事件", Summary: "摘要",
			HookType: "mystery", DominantStrand: "quest",
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestValidateStructure(t *testing.T) {
	ok := []ImportedVolumeRange{{Title: "卷一", Arcs: []ImportedArcRange{{StartChapter: 1, EndChapter: 3}}}}
	if err := validateStructure(ok, 3); err != nil {
		t.Fatalf("cấu trúc hợp lệ phải qua: %v", err)
	}
	gap := []ImportedVolumeRange{{Arcs: []ImportedArcRange{{StartChapter: 1, EndChapter: 2}, {StartChapter: 4, EndChapter: 5}}}}
	if err := validateStructure(gap, 5); err == nil {
		t.Fatal("có khuyết phải từ chối")
	}
	short := []ImportedVolumeRange{{Arcs: []ImportedArcRange{{StartChapter: 1, EndChapter: 2}}}}
	if err := validateStructure(short, 3); err == nil {
		t.Fatal("không phủ đủ N phải từ chối")
	}
}

func TestAssembleFoundationHappyClosed(t *testing.T) {
	facts := factsN(3)
	s := &BookSynthesis{
		Synopsis:     "无剧透简介",
		Premise:      "# 故事前提\n\n前提",
		Characters:   []domain.Character{{Name: "甲"}},
		PlanningTier: domain.PlanningTierShort,
		StoryStatus:  storyClosed,
		Compass:      domain.StoryCompass{EndingDirection: "收束"},
		Structure:    []ImportedVolumeRange{{Title: "卷一", Arcs: []ImportedArcRange{{Title: "弧一", StartChapter: 1, EndChapter: 3}}}},
	}
	f, err := AssembleFoundation(s, facts, true, "book.txt")
	if err != nil {
		t.Fatalf("lắp ráp phải thành công: %v", err)
	}
	if len(domain.FlattenOutline(f.Volumes)) != 3 {
		t.Fatal("số chương khi trải ra phải là 3")
	}
	if !f.Volumes[len(f.Volumes)-1].Final {
		t.Fatal("khi closed tập cuối phải Final")
	}
	if f.Book.Title != "book" || f.Book.Synopsis != "无剧透简介" {
		t.Fatalf("lắp thông tin tác phẩm sai: %+v", f.Book)
	}
}

func TestAssembleFoundationTitleMismatch(t *testing.T) {
	facts := factsN(2)
	facts[1].Title = "" // Phá tính nhất quán tiêu đề có fail ở kiểm tra FlattenOutline? Tiêu đề rỗng nhưng cấu trúc lấy từ facts, nên vẫn khớp.
	// Dùng chương ngoài tầm phủ của cấu trúc tạo sự không khớp thật: số chương không khớp.
	s := &BookSynthesis{
		Synopsis: "无剧透简介", Premise: "# 故事前提", Characters: []domain.Character{{Name: "甲"}},
		PlanningTier: domain.PlanningTierShort, StoryStatus: storyOpen,
		Compass:   domain.StoryCompass{EndingDirection: "x"},
		Structure: []ImportedVolumeRange{{Arcs: []ImportedArcRange{{StartChapter: 1, EndChapter: 1}}}},
	}
	if _, err := AssembleFoundation(s, facts, false, "b.txt"); err == nil {
		t.Fatal("cấu trúc chỉ phủ 1 chương mà sự thực 2 chương phải từ chối")
	}
}

func TestImportedBookTitle(t *testing.T) {
	if got := importedBookTitle("我的小说.txt"); got != "我的小说" {
		t.Fatalf("phải suy tên sách từ tên tệp: %q", got)
	}
}

func TestPlanFactRangesSplits(t *testing.T) {
	facts := factsN(20)
	one := len(compactFact(facts[0]))
	ranges := planFactRanges(facts, one*3) // mỗi khoảng chừng 3 chương
	if len(ranges) < 2 {
		t.Fatalf("phải chia nhiều khoảng, được %d", len(ranges))
	}
	if ranges[0][0] != 0 || ranges[len(ranges)-1][1] != 20 {
		t.Fatal("khoảng không phủ trọn")
	}
}

// TestToCompactCarriesEvidence canh giữ #6: character/world evidence suy ngược từng chương phải
// vào khung nhìn cô đọng của tổng hợp, nếu không bộ tổng hợp chỉ có thể bịa nhân vật và quy tắc thế
// giới chính thức từ tóm tắt.
func TestToCompactCarriesEvidence(t *testing.T) {
	f := ImportedChapterFacts{
		Chapter: 1, Title: "第一章", CoreEvent: "e", Summary: "s",
		CharacterEvidence: []ImportedCharacterFact{{Chapter: 1, Name: "甲", Note: "沉稳"}},
		WorldEvidence:     []ImportedWorldFact{{Chapter: 1, Category: "magic", Fact: "灵气充盈"}},
	}
	cv := toCompact(f)
	if len(cv.CharacterEvidence) != 1 || cv.CharacterEvidence[0].Name != "甲" {
		t.Fatalf("character evidence chưa vào khung nhìn cô đọng: %+v", cv.CharacterEvidence)
	}
	if len(cv.WorldEvidence) != 1 || cv.WorldEvidence[0].Fact != "灵气充盈" {
		t.Fatalf("world evidence chưa vào khung nhìn cô đọng: %+v", cv.WorldEvidence)
	}
}

// TestSynthesizeRejectsRangeMismatch canh giữ #4: chương đầu/cuối của tóm tắt khoảng ở giai đoạn
// Map truyện dài phải khớp yêu cầu, nếu không lúc gộp sẽ coi khoảng lệch là tóm tắt của khoảng này.
func TestSynthesizeRejectsRangeMismatch(t *testing.T) {
	err := validateRangeDigest(&RangeDigest{StartChapter: 1, EndChapter: 5, Plot: "错位区间"}, 1, 2, "range digest")
	if err == nil {
		t.Fatal("chương đầu/cuối khoảng không khớp yêu cầu phải từ chối")
	}
	if !strings.Contains(err.Error(), "phạm vi chương") {
		t.Fatalf("lỗi phải chỉ ra phạm vi khoảng không khớp, được: %v", err)
	}
}

// TestGroupDigestsByBudget canh giữ nhóm gộp #3: tóm tắt khoảng liên tiếp chia nhóm liên tục theo
// ngân sách byte, tóm tắt đơn vượt ngân sách cũng thành nhóm riêng.
func TestGroupDigestsByBudget(t *testing.T) {
	ds := []RangeDigest{
		{StartChapter: 1, EndChapter: 5, Plot: strings.Repeat("x", 200)},
		{StartChapter: 6, EndChapter: 10, Plot: strings.Repeat("y", 200)},
		{StartChapter: 11, EndChapter: 15, Plot: strings.Repeat("z", 200)},
		{StartChapter: 16, EndChapter: 20, Plot: strings.Repeat("w", 200)},
	}
	per := len(mustJSON(t, ds[0]))
	groups := groupDigestsByBudget(ds, per*2+10) // mỗi nhóm chứa chừng 2 cái
	if len(groups) != 2 || len(groups[0]) != 2 || len(groups[1]) != 2 {
		t.Fatalf("phải chia 2 nhóm mỗi nhóm 2 cái, được %v", groups)
	}
	if groups[0][0].StartChapter != 1 || groups[1][1].EndChapter != 20 {
		t.Fatal("phân nhóm không giữ độ phủ liên tục")
	}
}

// TestReduceToFitMergesUntilBudget canh giữ #3: tổng lượng tóm tắt khoảng vượt ngân sách thì gộp
// từng tầng đến khi chứa nổi, chứ không tràn không biên vào lần gọi tổng hợp cuối.
func TestReduceToFitMergesUntilBudget(t *testing.T) {
	ds := []RangeDigest{
		{StartChapter: 1, EndChapter: 5, Plot: strings.Repeat("x", 200)},
		{StartChapter: 6, EndChapter: 10, Plot: strings.Repeat("y", 200)},
		{StartChapter: 11, EndChapter: 15, Plot: strings.Repeat("z", 200)},
		{StartChapter: 16, EndChapter: 20, Plot: strings.Repeat("w", 200)},
	}
	budget := len(mustJSON(t, ds[0]))*2 + 10
	// Mỗi nhóm gộp ra một tóm tắt nhỏ: chương 1-10, chương 11-20.
	m := &mockModel{responses: []string{
		rangeDigestJSON(1, 10, "合并一"),
		rangeDigestJSON(11, 20, "合并二"),
	}}
	out, err := reduceToFit(context.Background(), m, "range", ds, budget, 4096, callProfile{})
	if err != nil {
		t.Fatalf("reduceToFit: %v", err)
	}
	if len(out) != 2 || out[0].StartChapter != 1 || out[0].EndChapter != 10 || out[1].StartChapter != 11 || out[1].EndChapter != 20 {
		t.Fatalf("phải gộp thành 2 tóm tắt khoảng liên tiếp, được %+v", out)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSynthesizeDirectWithMock(t *testing.T) {
	facts := factsN(3)
	resp := synthesisFixtureJSON(3, storyOpen)
	m := &mockModel{responses: []string{resp}}
	s, err := Synthesize(context.Background(), m, "sys", "range-sys", &Workspace{dir: t.TempDir()}, facts, 0, 4096, callProfile{})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if s.StoryStatus != storyOpen || len(s.Structure) != 1 {
		t.Fatalf("kết quả tổng hợp không khớp: %+v", s)
	}
	if _, err := AssembleFoundation(s, facts, false, "b.txt"); err != nil {
		t.Fatalf("lắp ráp phải thành công: %v", err)
	}
	_ = agentcore.StopReasonStop
}
