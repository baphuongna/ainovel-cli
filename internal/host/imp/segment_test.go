package imp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestUnitLessNumericNotLexical(t *testing.T) {
	// Thứ tự từ điển sẽ phán L900 > L1000, L1257.2 > L1800; thứ tự số phải ngược lại.
	if !unitLess(SourceUnit{Line: 900}, SourceUnit{Line: 1000}) {
		t.Fatal("L900 phải < L1000 (thứ tự số)")
	}
	if !unitLess(SourceUnit{Line: 1257, Part: 2}, SourceUnit{Line: 1800}) {
		t.Fatal("L1257.2 phải < L1800")
	}
	if !unitLess(SourceUnit{Line: 1257, Part: 1}, SourceUnit{Line: 1257, Part: 2}) {
		t.Fatal("part cùng dòng phải theo thứ tự số")
	}
	if unitLess(SourceUnit{Line: 5}, SourceUnit{Line: 5}) {
		t.Fatal("bằng nhau không nên less")
	}
}

func TestBuildSourceUnitsRoundtrip(t *testing.T) {
	norm := []byte("第一章\n正文一\n\n第二章\n正文二")
	units := buildSourceUnits(norm, 0)
	// Ghép lại: văn bản mỗi unit + '\n' giữa các dòng phải phục hồi văn bản đã chuẩn hóa.
	var b strings.Builder
	for i, u := range units {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(u.Text)
		if u.Text != string(norm[u.StartByte:u.EndByte]) {
			t.Fatalf("phạm vi byte của unit %s không khớp văn bản", u.ID)
		}
	}
	if b.String() != string(norm) {
		t.Fatalf("ghép lại không khớp: %q", b.String())
	}
	if units[0].ID != "L1" || units[3].ID != "L4" {
		t.Fatalf("ID không khớp: %s %s", units[0].ID, units[3].ID)
	}
}

func TestBuildSourceUnitsVirtualShard(t *testing.T) {
	// Một dòng dài vượt xa ngân sách → tách nhiều unit ảo, ranh giới nằm tại biên ký tự UTF-8.
	long := strings.Repeat("字", 100) // mỗi chữ 3 byte = 300 byte
	units := buildSourceUnits([]byte(long), 30)
	if len(units) < 2 {
		t.Fatalf("dòng vượt ngân sách phải được tách mảnh, được %d", len(units))
	}
	var b strings.Builder
	for _, u := range units {
		if u.Line != 1 || u.Part == 0 {
			t.Fatalf("phân mảnh ảo phải cùng Line, Part>=1: %+v", u)
		}
		b.WriteString(u.Text) // các mảnh cùng một dòng, không xuống dòng ngăn cách
	}
	if b.String() != long {
		t.Fatal("ghép lại phân mảnh ảo mất chữ")
	}
}

func TestResolveBoundaryByteAnchor(t *testing.T) {
	units := []SourceUnit{{ID: "L1", Line: 1, StartByte: 0, EndByte: 10, Text: "楔子风起楔"}}
	m := map[string]SourceUnit{"L1": units[0]}
	if _, err := resolveBoundaryByte(m, "L1", "风起"); err != nil {
		t.Fatalf("anchor duy nhất phải thành công: %v", err)
	}
	if _, err := resolveBoundaryByte(m, "L1", "楔"); err == nil {
		t.Fatal("anchor trùng lặp phải thất bại")
	}
	if _, err := resolveBoundaryByte(m, "L1", "缺失"); err == nil {
		t.Fatal("anchor không tồn tại phải thất bại")
	}
	if _, err := resolveBoundaryByte(m, "L9", ""); err == nil {
		t.Fatal("unit không tồn tại phải thất bại")
	}
}

func TestPlanChunksCoversWithoutGap(t *testing.T) {
	units := buildSourceUnits([]byte(strings.Repeat("行内容\n", 50)), 0)
	chunks := planChunks(units, 40)
	if len(chunks) < 2 {
		t.Fatalf("phải chia nhiều khối, được %d", len(chunks))
	}
	// Không khe không chồng và phủ trọn.
	if chunks[0][0] != 0 || chunks[len(chunks)-1][1] != len(units) {
		t.Fatal("không phủ trọn")
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i][0] != chunks[i-1][1] {
			t.Fatalf("khối %d không nối khối trước: %v", i, chunks)
		}
	}
}

func segFixture() ([]byte, []SourceUnit) {
	norm := []byte("前言\n感谢阅读\n第一章 风起\n正文一\n卷二\n第二章 云涌\n正文二")
	return norm, buildSourceUnits(norm, 0)
}

func TestResolveSegmentationHappy(t *testing.T) {
	norm, units := segFixture()
	// L1 前言 (front) / L3 第一章 / L5 卷二 (group) / L6 第二章  — chú thích vị trí fixture
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L5", Kind: kindGroup, Title: "卷二"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	}
	seg, err := resolveSegmentation(norm, units, decisions)
	if err != nil {
		t.Fatalf("kiểm tra độ phủ phải qua: %v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("số chương phải là 2 (group không tính), được %d", len(seg.Chapters))
	}
	if seg.Chapters[0].Number != 1 || seg.Chapters[1].Number != 2 {
		t.Fatal("số chương phải liên tiếp")
	}
	if !strings.Contains(seg.Content(norm, 0), "正文一") {
		t.Fatalf("chính văn chương 1 không khớp: %q", seg.Content(norm, 0))
	}
	// Độ phủ: đoạn đầu (front_matter) từ 0, chương cuối phủ đến cuối văn bản.
	if len(seg.Matter) == 0 || seg.Matter[0].Kind != kindFrontMatter || seg.Matter[0].Start != 0 {
		t.Fatalf("đoạn đầu phải là front_matter từ 0: %+v", seg.Matter)
	}
	if seg.Chapters[len(seg.Chapters)-1].End != len(norm) {
		t.Fatal("chương cuối phải phủ đến cuối văn bản")
	}
}

func TestResolveSegmentationRejections(t *testing.T) {
	norm, units := segFixture()
	cases := []struct {
		name string
		ds   []BoundaryDecision
	}{
		{"không có chương", []BoundaryDecision{
			{UnitID: "L1", Kind: kindFrontMatter},
		}},
		{"kind bất hợp lệ", []BoundaryDecision{
			{UnitID: "L1", Kind: "verse"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := resolveSegmentation(norm, units, c.ds); err == nil {
				t.Fatalf("phải bị từ chối: %s", c.name)
			}
		})
	}
}

// TestResolveSegmentationReordersAndDedups canh giữ kỷ luật tọa độ của phương án chặn hồi kết:
// sai thứ tự thỉnh thoảng của mô hình trong khối được khôi phục tất định bằng sắp xếp byte (thực
// đo 319 ranh giới từng thua vì 1 chỗ đảo thứ tự, và cache khối khiến thất bại tái hiện tất định);
// trùng cùng byte giữ cái xuất hiện trước và ghi Notes giao bản xem trước xác nhận.
func TestResolveSegmentationReordersAndDedups(t *testing.T) {
	norm, units := segFixture()
	seg, err := resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L1", Kind: kindChapter, Title: "开篇"}, // sai thứ tự: vị trí trước L3
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 重复"}, // trùng cùng byte
	})
	if err != nil {
		t.Fatalf("sai thứ tự/trùng lặp phải được sửa tất định thay vì từ chối: %v", err)
	}
	if len(seg.Chapters) != 3 {
		t.Fatalf("phải được 3 chương, được %d: %+v", len(seg.Chapters), seg.Chapters)
	}
	if seg.Chapters[0].Title != "开篇" || seg.Chapters[0].Start != 0 {
		t.Fatalf("sau sắp xếp chương đầu phải là ranh giới ở vị trí trước nhất: %+v", seg.Chapters[0])
	}
	if seg.Chapters[2].Title != "第二章 云涌" {
		t.Fatalf("trùng cùng byte phải giữ cái xuất hiện trước: %+v", seg.Chapters[2])
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "trùng vị trí") {
		t.Fatalf("ranh giới trùng phải ghi vào Notes: %v", seg.Notes)
	}
}

// TestResolveSegmentationAbsorbsLeadingText canh giữ sửa tất định cho bỏ sót đầu: văn bản đầu
// khác rỗng kiểu lời tựa đầu sách/quảng cáo nếu bị mô hình bỏ sót ranh giới, không được phủ quyết
// hồi kết — bỏ sót đã vào cache khối, phủ quyết sẽ khiến chạy lại không gọi model nào mà tái hiện tất
// định thất bại. Go bổ một front_matter chặn [0, first) và ghi Notes giao bản xem trước xác nhận.
func TestResolveSegmentationAbsorbsLeadingText(t *testing.T) {
	norm, units := segFixture()
	// Chỉ báo chương từ L3: văn bản L1/L2 khác rỗng chưa phân bổ.
	seg, err := resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	})
	if err != nil {
		t.Fatalf("văn bản đầu chưa phân bổ phải được thu thành front_matter thay vì từ chối: %v", err)
	}
	if len(seg.Matter) != 1 || seg.Matter[0].Kind != kindFrontMatter || seg.Matter[0].Start != 0 {
		t.Fatalf("phải bổ ra front_matter từ 0: %+v", seg.Matter)
	}
	if len(seg.Chapters) != 2 || seg.Chapters[0].Start == 0 {
		t.Fatalf("chương không được nuốt văn bản đầu: %+v", seg.Chapters)
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "chưa được mô hình phân bổ") {
		t.Fatalf("phải ghi ghi chú kiểm tra thủ công: %v", seg.Notes)
	}
}

// TestResolveSegmentationNotesDuplicateTitles canh giữ tính thấy được của chương trùng tên:
// nguồn có quy ước tiêu đề thì tên chương không nên lặp, lặp là tín hiệu tất định của "cùng một
// chương bị cắt nhầm" — chỉ ghi Notes (chặn --yes, trình bày ở bản xem trước) giao kiểm tra thủ
// công, có hợp lại hay không Go không phán định.
func TestResolveSegmentationNotesDuplicateTitles(t *testing.T) {
	norm, units := segFixture()
	seg, err := resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L6", Kind: kindChapter, Title: "第一章风起"}, // trùng tên (bỏ qua khác biệt khoảng trắng)
	})
	if err != nil {
		t.Fatalf("chương trùng tên phải được phê duyệt và ghi Notes: %v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("phải được 2 chương, được %d", len(seg.Chapters))
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "trùng tiêu đề") {
		t.Fatalf("phải ghi một ghi chú kiểm tra trùng tên: %v", seg.Notes)
	}
}

// TestChunkValidatorOwnedDiscipline canh giữ độ phủ của kiểm tra thời điểm gọi: kind bất hợp lệ
// trong vùng owned, anchor hỏng, xung đột ngữ nghĩa cùng vị trí, đầu khối chưa phân bổ đều phải
// hỏi lại kèm phản hồi ở thời điểm gọi — cho qua sẽ theo khối vào cache, đến hồi kết resolve mới
// phát hiện thì chạy lại không gọi model nào mà đọc lại cùng dữ liệu hỏng; ranh giới vùng ngữ cảnh
// chắc chắn bị cắt, không hỏi lại vì nó; trùng lặp hoàn toàn giống nhau cùng vị trí là dư máy móc,
// cho qua rồi resolve khử trùng vô thanh.
func TestChunkValidatorOwnedDiscipline(t *testing.T) {
	norm, units := segFixture()
	unitByID := map[string]SourceUnit{}
	proj, owned := map[string]bool{}, map[string]bool{}
	for _, u := range units {
		unitByID[u.ID] = u
		proj[u.ID] = true
	}
	owned["L1"], owned["L2"], owned["L3"] = true, true, true
	v := chunkValidator{projIDs: proj, ownedIDs: owned, unitByID: unitByID, normalized: norm}

	cases := []struct {
		name    string
		bs      []BoundaryDecision
		wantErr bool
	}{
		{"owned kind bất hợp lệ", []BoundaryDecision{{UnitID: "L1", Kind: "volume"}}, true},
		{"owned anchor hỏng", []BoundaryDecision{{UnitID: "L3", Kind: kindChapter, Anchor: "不存在的锚"}}, true},
		{"owned anchor hợp lệ", []BoundaryDecision{{UnitID: "L3", Kind: kindChapter, Anchor: "第一章"}}, false},
		{"kind bất hợp lệ vùng ngữ cảnh không hỏi lại", []BoundaryDecision{{UnitID: "L6", Kind: "volume"}}, false},
		{"ID ảo giác ngoài projection", []BoundaryDecision{{UnitID: "L99", Kind: kindChapter}}, true},
		{"xung đột ngữ nghĩa cùng vị trí hỏi lại", []BoundaryDecision{
			{UnitID: "L1", Kind: kindChapter, Title: "前言"},
			{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		}, true},
		{"trùng lặp hoàn toàn cùng vị trí cho qua", []BoundaryDecision{
			{UnitID: "L1", Kind: kindChapter, Title: "前言"},
			{UnitID: "L1", Kind: kindChapter, Title: "前言"},
		}, false},
		// Hiển thị lại tiêu đề: tên chương/tên tập phải thật sự tồn tại trong nguyên văn unit chứa
		// ranh giới — tiêu đề bịa của ranh giới ma bị chặn tại đây.
		{"tiêu đề chương bịa hỏi lại", []BoundaryDecision{{UnitID: "L2", Kind: kindChapter, Title: "第某章 我编的"}}, true},
		{"tiêu đề quy nạp phải uncertain mới cho qua", []BoundaryDecision{{UnitID: "L2", Kind: kindChapter, Title: "第某章 我编的", Uncertain: true}}, false},
		{"hiển thị lại dung nhan khác biệt khoảng trắng", []BoundaryDecision{{UnitID: "L3", Kind: kindChapter, Title: "第一章风起"}}, false},
		{"tên tập bịa hỏi lại", []BoundaryDecision{{UnitID: "L2", Kind: kindGroup, Title: "卷九"}}, true},
		{"tiêu đề mô tả phụ trợ không đối chiếu", []BoundaryDecision{{UnitID: "L2", Kind: kindFrontMatter, Title: "引言"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := v.validate(c.bs); (err != nil) != c.wantErr {
				t.Fatalf("wantErr=%v, được %v", c.wantErr, err)
			}
		})
	}

	// Phủ đầu khối đầu: L1/L2 khác rỗng mà không có ranh giới phân bổ → hỏi lại; bổ ranh giới điểm đầu rồi thì qua.
	vs := v
	vs.coverStart = true
	if err := vs.validate([]BoundaryDecision{{UnitID: "L3", Kind: kindChapter}}); err == nil {
		t.Fatal("đầu khối đầu chưa phân bổ phải hỏi lại")
	}
	if err := vs.validate([]BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter}, {UnitID: "L3", Kind: kindChapter},
	}); err != nil {
		t.Fatalf("điểm khởi đầu đã phủ thì phải qua: %v", err)
	}
	if err := vs.validate(nil); err == nil {
		t.Fatal("khối đầu không ranh giới phải hỏi lại (toàn bộ văn bản đầu chưa phân bổ)")
	}
}

// TestSegmentClearsChunksOnResolveFailure canh giữ van tổng "cache tái hiện tất định": khi tích
// hợp hồi kết thất bại, cache khối đã vô giá trị (digest luôn khớp, chạy lại không gọi model nào
// đọc lại cùng mẻ ranh giới rồi chết thêm lần nữa), bắt buộc xóa đổi lấy cơ hội gọi mô hình phân
// tách lại lần sau; snapshot quyết định qua errSemantic tập trung ghi failures/.
func TestSegmentClearsChunksOnResolveFailure(t *testing.T) {
	norm, units := segFixture()
	// Mô hình đánh cả sách thành front_matter: không có chương, Go không thể sửa tất định, thất bại hồi kết.
	m := &mockModel{responses: []string{boundariesJSON(boundaryFixture("L1", "", kindFrontMatter, "前言"))}}
	w := &Workspace{dir: t.TempDir()}
	_, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, w, "id-1")
	if err == nil {
		t.Fatal("không có chương phải thất bại hồi kết")
	}
	var se *errSemantic
	if !errors.As(err, &se) {
		t.Fatalf("thất bại hồi kết phải là errSemantic (tập trung ghi failures/), được %T", err)
	}
	if _, statErr := os.Stat(filepath.Join(w.dir, dirSegmentChunks)); !os.IsNotExist(statErr) {
		t.Fatalf("sau thất bại hồi kết cache khối phải bị xóa: %v", statErr)
	}
}

// mockModel trả lần lượt các phản hồi đặt sẵn, cho test hợp đồng typed-call.
// stops có thể ghi stop reason cho mỗi lần gọi; thiếu thì dùng stop hoặc StopReasonStop.
type mockModel struct {
	responses []string
	stops     []agentcore.StopReason
	i         int
	stop      agentcore.StopReason
}

func (m *mockModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	idx := m.i
	r := m.responses[idx%len(m.responses)]
	sr := m.stop
	if idx < len(m.stops) {
		sr = m.stops[idx]
	}
	if sr == "" {
		sr = agentcore.StopReasonStop
	}
	m.i++
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(r)},
		StopReason: sr,
	}}, nil
}

// TestResolveSegmentationSingleLineChapters canh giữ #9: đoạn một dòng không xuống dòng (kịch bản
// cắt bằng anchor) cả đoạn là chính văn, tiểu thuyết một dòng/một dòng nhiều chương không nên bị
// phán nhầm "chính văn rỗng" mà từ chối.
func TestResolveSegmentationSingleLineChapters(t *testing.T) {
	normalized := []byte("第一章甲的故事第二章乙的故事") // cả thiên một dòng, không xuống dòng
	units := buildSourceUnits(normalized, 0)
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindChapter, Title: "第一章"},                // không anchor → byte 0
		{UnitID: "L1", Anchor: "第二章", Kind: kindChapter, Title: "第二章"}, // anchor trong dòng cắt ra chương 2
	}
	seg, err := resolveSegmentation(normalized, units, decisions)
	if err != nil {
		t.Fatalf("nhiều chương một dòng phải được chấp nhận: %v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("phải cắt ra 2 chương, được %d", len(seg.Chapters))
	}
	if got := seg.Content(normalized, 0); got != "第一章甲的故事" {
		t.Fatalf("phạm vi chính văn chương đầu sai: %q", got)
	}
}

func TestSegmentWithMockModel(t *testing.T) {
	norm, units := segFixture()
	resp := boundariesJSON(
		boundaryFixture("L1", "", kindFrontMatter, "前言"),
		boundaryFixture("L3", "", kindChapter, "第一章 风起"),
		boundaryFixture("L5", "", kindGroup, "卷二"),
		boundaryFixture("L6", "", kindChapter, "第二章 云涌"),
	)
	m := &mockModel{responses: []string{resp}}
	seg, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, nil, "")
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("phải được 2 chương, được %d", len(seg.Chapters))
	}
}

// TestResolveSegmentationAbsorbsEmptyChapter canh giữ dung sai nguồn bẩn: nguồn truyện mạng thật
// thường có tiêu đề placeholder "đã khóa/chương trả phí" (tiêu đề có, chính văn thiếu). Ranh giới kiểu
// này không được thất bại toàn bộ — phủ quyết một phiếu ở hồi kết sẽ phí toàn bộ lời gọi mô hình của
// giai đoạn phân tách; đoạn placeholder nhập vào đoạn trước (không mất một chữ), ghi vào Notes để
// bản xem trước xác nhận trình bày kiểm tra thủ công.
func TestResolveSegmentationAbsorbsEmptyChapter(t *testing.T) {
	norm, units := segFixture()
	// Dòng L5 "卷二" bị mô hình đánh thành tiêu đề chương: span [L5,L6) của nó không chính văn → nhập vào chương 1.
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L5", Kind: kindChapter, Title: "第五章 [本章节已锁定]"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	}
	seg, err := resolveSegmentation(norm, units, decisions)
	if err != nil {
		t.Fatalf("chương placeholder không chính văn phải được hấp thụ thay vì thất bại toàn bộ: %v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("phải được 2 chương (placeholder nhập đoạn trước), được %d", len(seg.Chapters))
	}
	if got := seg.Content(norm, 0); !strings.Contains(got, "卷二") {
		t.Fatalf("đoạn placeholder phải nhập vào chương 1 (không mất chữ): %q", got)
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "已锁定") {
		t.Fatalf("phải ghi một ghi chú kiểm tra thủ công: %v", seg.Notes)
	}
	// Điểm đầu luôn là chương không chính văn: không có đoạn trước để nhập → rơi thành front_matter, cũng không thất bại.
	seg, err = resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L1", Kind: kindChapter, Title: "占位"}, // [L1,L2) tiêu đề một dòng không chính văn
		{UnitID: "L2", Kind: kindChapter, Title: "第一章"},
	})
	if err != nil {
		t.Fatalf("điểm đầu không chính văn phải rơi thành front_matter: %v", err)
	}
	if len(seg.Matter) != 1 || seg.Matter[0].Kind != kindFrontMatter {
		t.Fatalf("điểm đầu không chính văn phải là front_matter: %+v", seg.Matter)
	}
}

// TestSegmentClipsContextBoundaries canh giữ thi hành phía Go của kỷ luật tọa độ: ranh giới mô
// hình trả về trong vùng ngữ cảnh không kích hoạt hỏi lại ngữ nghĩa (mô hình yếu thường cạn 3 lần
// thử kéo sập cả khối), do code cắt bỏ trực tiếp — ranh giới đó thuộc quyền khối liền kề, khối liền
// kề sẽ tự báo nó trong khoảng owned của mình, giữ lại sẽ tạo ra trùng/sai thứ tự xuyên khối.
func TestSegmentClipsContextBoundaries(t *testing.T) {
	norm, units := segFixture()
	chunks := planChunks(units, planningBudget(40, "sys", "")) // nhất quán với quy hoạch nội bộ của Segment
	if len(chunks) < 2 {
		t.Fatalf("fixture phải chia ra ít nhất 2 khối, được %d", len(chunks))
	}
	// Phản hồi mỗi khối: một ranh giới chương ở unit đầu owned (không tiêu đề thì đi firstLine
	// fallback, né đối chiếu hiển thị lại tiêu đề — chỗ này test kỷ luật tọa độ); khối đầu mang
	// thêm một ranh giới ở unit đầu khối kế (vùng ngữ cảnh).
	responses := make([]string, len(chunks))
	for ci, owned := range chunks {
		boundaries := []map[string]any{boundaryFixture(units[owned[0]].ID, "", kindChapter, "")}
		if ci == 0 {
			boundaries = append(boundaries, boundaryFixture(units[chunks[1][0]].ID, "", kindChapter, ""))
		}
		responses[ci] = boundariesJSON(boundaries...)
	}
	// Ghi chú cắt bỏ đi hiển thị lại tiến độ thường (kỷ luật tọa độ thường lệ, không phải cảnh báo — màu warn sẽ khiến người dùng tưởng lỗi).
	var clipNotes int
	prof := callProfile{progress: func(_, _ int, s string) {
		if strings.Contains(s, "cắt bỏ") {
			clipNotes++
		}
	}}
	seg, err := Segment(context.Background(), &mockModel{responses: responses}, "sys", norm, units, "", 40, 2, 4096, prof, nil, "")
	if err != nil {
		t.Fatalf("ranh giới vùng ngữ cảnh phải bị cắt bỏ thay vì thất bại: %v", err)
	}
	if len(seg.Chapters) != len(chunks) {
		t.Fatalf("phải được %d chương (ranh giới vượt biên không tính trùng), được %d", len(chunks), len(seg.Chapters))
	}
	if clipNotes != 1 {
		t.Fatalf("phải hiển thị lại 1 dòng ghi chú cắt bỏ, được %d", clipNotes)
	}
}

// TestSegmentReusesChunkArtifacts canh giữ checkpoint cấp khối: phân tách ghi cache ranh giới
// từng khối, chạy lại thì khối digest khớp được tái dùng trực tiếp với zero lời gọi mô hình — phân
// tách là giai đoạn đắt nhất, một khối thất bại không nên trả lại tiền các khối đã xong (cùng triết
// lý với analyze/synthesize).
func TestSegmentReusesChunkArtifacts(t *testing.T) {
	norm, units := segFixture()
	chunks := planChunks(units, planningBudget(40, "sys", "")) // nhất quán với quy hoạch nội bộ của Segment
	responses := make([]string, len(chunks))
	for ci, owned := range chunks {
		responses[ci] = boundariesJSON(boundaryFixture(units[owned[0]].ID, "", kindChapter, ""))
	}
	w := &Workspace{dir: t.TempDir()}
	m1 := &mockModel{responses: responses}
	seg1, err := Segment(context.Background(), m1, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-1")
	if err != nil {
		t.Fatalf("chạy đầu: %v", err)
	}
	if m1.i != len(chunks) {
		t.Fatalf("chạy đầu phải gọi %d lần, được %d", len(chunks), m1.i)
	}
	m2 := &mockModel{responses: responses}
	seg2, err := Segment(context.Background(), m2, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-1")
	if err != nil {
		t.Fatalf("chạy lại: %v", err)
	}
	if m2.i != 0 {
		t.Fatalf("khối digest khớp phải tái dùng zero lời gọi, thực tế gọi %d lần", m2.i)
	}
	if len(seg2.Chapters) != len(seg1.Chapters) {
		t.Fatalf("kết quả tái dùng phải nhất quán: %d != %d", len(seg2.Chapters), len(seg1.Chapters))
	}
	// Danh tính đổi (đổi phiên bản prompt/hướng dẫn/nguồn) → cache tự nhiên mất khớp, làm lại tất cả.
	m3 := &mockModel{responses: responses}
	if _, err := Segment(context.Background(), m3, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-2"); err != nil {
		t.Fatalf("chạy lại sau khi danh tính đổi: %v", err)
	}
	if m3.i != len(chunks) {
		t.Fatalf("danh tính đổi phải làm lại tất cả (%d lần gọi), được %d", len(chunks), m3.i)
	}
}

// TestSegmentShrinksChunkOnTruncation canh giữ vòng ngân sách đầu ra: nhiều chương ngắn sẽ khiến
// JSON ranh giới của một khối vượt đầu ra khả kiến (stop=length), bắt buộc chia đôi khối thử lại
// thay vì thất bại toàn bộ — cùng triết lý thu nhỏ lô với analyze.
func TestSegmentShrinksChunkOnTruncation(t *testing.T) {
	norm, units := segFixture() // 7 unit, một khối [0,7), mid=3
	left := boundariesJSON(boundaryFixture("L1", "", kindChapter, ""))
	right := boundariesJSON(boundaryFixture("L6", "", kindChapter, "第二章 云涌"))
	m := &mockModel{
		responses: []string{`{"boundaries":[]}`, left, right},
		stops:     []agentcore.StopReason{agentcore.StopReasonLength}, // lần gọi đầu bị cắt, hai nửa khối bình thường
	}
	seg, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, nil, "")
	if err != nil {
		t.Fatalf("bị cắt phải thu nhỏ khối thử lại thay vì thất bại: %v", err)
	}
	if m.i != 3 {
		t.Fatalf("phải là 1 lần cắt + 2 lần gọi nửa khối, được %d", m.i)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("kết quả thu nhỏ khối phải phủ trọn (2 chương), được %d", len(seg.Chapters))
	}
}

// TestPlanningBudget canh giữ việc trừ overhead cấu trúc của ngân sách quy hoạch phân tách: chính văn owned chỉ là một phần của yêu cầu.
func TestPlanningBudget(t *testing.T) {
	if got := planningBudget(0, "sys", "g"); got != 0 {
		t.Fatalf("không ngân sách phải truyền nguyên, được %d", got)
	}
	if got := planningBudget(1000, strings.Repeat("s", 100), strings.Repeat("g", 100)); got != 600 {
		t.Fatalf("(1000-200)*3/4 phải là 600, được %d", got)
	}
	if got := planningBudget(1000, strings.Repeat("s", 2000), ""); got != 250 {
		t.Fatalf("prompt siêu dài phải kích hoạt trần dưới chunkBytes/4=250, được %d", got)
	}
}

// TestBuildProjectionContextByteCap canh giữ trần byte vùng ngữ cảnh: phân mảnh ảo của dòng siêu
// dài (một mảnh có thể đạt MaxUnitBytes) sẽ nuốt ngân sách đầu vào, ngữ cảnh chỉ là thông tin tham khảo,
// co theo trần byte thay vì nhận nguyên tất cả.
func TestBuildProjectionContextByteCap(t *testing.T) {
	_, units := segFixture()
	if _, ids := buildProjection(units, [2]int{2, 3}, 2, 1, ""); len(ids) != 1 || !ids["L3"] {
		t.Fatalf("trần byte phải cắt bỏ unit ngữ cảnh, chỉ còn owned: %v", ids)
	}
	if _, ids := buildProjection(units, [2]int{2, 3}, 2, 0, ""); len(ids) != 5 {
		t.Fatalf("không trần byte thì phải gồm trước sau mỗi bên 2 unit ngữ cảnh (tổng 5), được %v", ids)
	}
}

func TestCallStructuredTruncation(t *testing.T) {
	m := &mockModel{responses: []string{`{"boundaries":[]}`}, stop: agentcore.StopReasonLength}
	_, err := callStructured[boundaryBatch](context.Background(), m, segmentContract, "s", "p", 16, callProfile{}, nil)
	var trunc *errTruncated
	if err == nil || !asTruncated(err, &trunc) {
		t.Fatalf("cắt ngắn độ dài phải trả *errTruncated, được %v", err)
	}
}

func asTruncated(err error, target **errTruncated) bool {
	t, ok := err.(*errTruncated)
	if ok {
		*target = t
	}
	return ok
}
