package imp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// testDeps dựng Deps tối thiểu với ba hàm ngữ nghĩa dùng chung một cấp mock.
func testDeps(st *store.Store, m callModel) Deps {
	c := Caller{Model: m}
	return Deps{
		Store:         st,
		CommitChapter: tools.NewCommitChapterTool(st, tools.NewStyleStatsIndex(st)),
		Segment:       c,
		Analyze:       c,
		Synthesize:    c,
		Prompts:       Prompts{Segment: "seg", Analyze: "ana", Synthesize: "syn", Range: "range"},
	}
}

// TestRunEndToEnd dùng mô hình mock điều khiển pipeline trọn vẹn
// ingest→segment→analyze→synthesize→publish, ghi xuống đĩa qua commit_chapter thật, xác nhận
// Foundation chính thức và toàn bộ chương sẵn sàng.
func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文一\n第二章\n正文二\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	seg := boundariesJSON(
		boundaryFixture("L1", "", kindChapter, "第一章"),
		boundaryFixture("L3", "", kindChapter, "第二章"),
	)
	ana := `{"chapters":[` + factsJSON(1, "第一章") + `,` + factsJSON(2, "第二章") + `]}`
	syn := synthesisFixtureJSON(2, storyClosed)
	m := &mockModel{responses: []string{seg, ana, syn}}

	ch, err := Run(context.Background(), testDeps(st, m), Options{SourcePath: src, AutoConfirm: true, ContinueAfter: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var runErr error
	var doneSeen bool
	for ev := range ch {
		if ev.Stage == StageError {
			runErr = ev.Err
		}
		if ev.Stage == StageDone {
			doneSeen = true
		}
	}
	if runErr != nil {
		t.Fatalf("pipeline thất bại: %v", runErr)
	}
	if !doneSeen {
		t.Fatal("không nhận được StageDone")
	}
	// Trạng thái chính thức sẵn sàng: thông tin tác phẩm, premise và dàn ý phẳng phủ đủ chương đã ghi
	// (world_rules rỗng là hợp lệ, không yêu cầu).
	if book, _ := st.Book.Load(); book == nil || book.Synopsis == "" {
		t.Fatalf("thông tin tác phẩm chưa ghi: %+v", book)
	}
	if p, _ := st.Outline.LoadPremise(); p == "" {
		t.Fatal("premise chưa ghi")
	}
	if o, _ := st.Outline.LoadOutline(); len(o) != 2 {
		t.Fatalf("dàn ý phẳng phải phủ 2 chương, được %d", len(o))
	}
	prog, _ := st.Progress.Load()
	if prog == nil || len(prog.CompletedChapters) != 2 {
		t.Fatalf("phải hoàn thành 2 chương: %+v", prog)
	}
	if active, done, err := ResumeStatus(st); err != nil || !active || !done {
		t.Fatalf("ResumeStatus phải là active&done, được active=%v done=%v", active, done)
	}
	// --continue: không đặt Hold hoàn tất nhập (giao cho host tự động tiếp nối).
	if meta, _ := st.RunMeta.Load(); meta != nil && meta.AdvanceHold != nil {
		t.Fatalf("--continue không nên để lại Hold hoàn tất nhập: %+v", meta.AdvanceHold)
	}
}

// TestRunSetsCompletionHold xác nhận sau khi nhập hoàn tất (không --continue) thì đặt boundary
// Hold (RFC §12.4). Hold là bảo đảm duy nhất "sau nhập không viết tiếp nhầm", phải bền hóa trong
// đường công bố.
func TestRunSetsCompletionHold(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文一\n第二章\n正文二\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seg := boundariesJSON(
		boundaryFixture("L1", "", kindChapter, "第一章"),
		boundaryFixture("L3", "", kindChapter, "第二章"),
	)
	ana := `{"chapters":[` + factsJSON(1, "第一章") + `,` + factsJSON(2, "第二章") + `]}`
	syn := synthesisFixtureJSON(2, storyClosed)
	m := &mockModel{responses: []string{seg, ana, syn}}

	ch, err := Run(context.Background(), testDeps(st, m), Options{SourcePath: src, AutoConfirm: true}) // không --continue
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for ev := range ch {
		if ev.Stage == StageError {
			t.Fatalf("pipeline thất bại: %v", ev.Err)
		}
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		t.Fatalf("load run meta: %v", err)
	}
	if meta == nil || meta.AdvanceHold == nil {
		t.Fatalf("nhập hoàn tất phải đặt boundary Hold, được %+v", meta)
	}
}

// TestRunRejectsDifferentSource canh giữ chặn đổi nguồn (RFC §12.1/§18.2): workspace đang tiến
// hành mà truyền tệp nguồn nội dung khác thì phải báo lỗi rõ ràng — ingest chỉ thi hành khi không có
// workspace, không so sánh sẽ im lặng tiếp tục từ checkpoint sách cũ, công bố xong sách cũ mà tệp
// mới không đọc được một byte. Truyền lặp đường dẫn cùng tệp là thói quen khôi phục thường, so theo
// tóm tắt nội dung thì phê duyệt.
func TestRunRejectsDifferentSource(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(a, []byte("第一章\n正文一\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Ingest(dir, a, Options{}.intent()); err != nil {
		t.Fatalf("dựng workspace: %v", err)
	}
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(b, []byte("完全不同的另一本书\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := Run(context.Background(), testDeps(st, &mockModel{responses: []string{"{}"}}), Options{SourcePath: b})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var runErr error
	for ev := range ch {
		if ev.Stage == StageError {
			runErr = ev.Err
		}
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "nội dung khác") {
		t.Fatalf("tệp nguồn khác phải bị từ chối rõ ràng, được %v", runErr)
	}
}

// TestConfirmNotesGate canh giữ ngưỡng dung sai của --yes: cấu trúc phân tách từng xảy ra dung
// sai ngữ nghĩa (Notes khác rỗng) đã bị sửa lại tất định, không do --yes chưa xem trước phê duyệt
// mù; nhấn y sau khi TUI xem trước (AcceptSegmentation) thì phê duyệt, phương thức xác nhận ghi
// user_confirmed để truy nguồn.
func TestConfirmNotesGate(t *testing.T) {
	newRunner := func(opts Options, notes []string) *runner {
		ws := &Workspace{dir: t.TempDir()}
		if err := ws.writeJSON(fileIntent, Intent{}); err != nil {
			t.Fatal(err)
		}
		seg := Segmentation{Chapters: []ChapterSpan{{Number: 1, Title: "第一章", End: 10}}, Notes: notes}
		if err := writeArtifact(ws, fileSegmentation, "d", seg); err != nil {
			t.Fatal(err)
		}
		return &runner{opts: opts, events: make(chan Event, 8), ws: ws}
	}
	r := newRunner(Options{AutoConfirm: true}, []string{"空正文占位并入前段"})
	if r.confirm() {
		t.Fatal("--yes không nên phê duyệt phân tách kèm ghi chú dung sai")
	}
	if ev := <-r.events; !strings.Contains(ev.Message, "không tự động phê duyệt") {
		t.Fatalf("bản xem trước phải nêu lý do không phê duyệt: %q", ev.Message)
	}
	if !newRunner(Options{AutoConfirm: true}, nil).confirm() {
		t.Fatal("--yes phải phê duyệt phân tách không có ghi chú dung sai")
	}
	r = newRunner(Options{AcceptSegmentation: true}, []string{"空正文占位并入前段"})
	if !r.confirm() {
		t.Fatal("y thủ công sau khi xem trước phải phê duyệt phân tách kèm ghi chú dung sai")
	}
	conf, err := readArtifact[Confirmation](r.ws, fileConfirmation)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Payload.Method != confirmMethodUser {
		t.Fatalf("xác nhận thủ công phải ghi user_confirmed, được %q", conf.Payload.Method)
	}
}

// TestStoryChoiceIgnoresStaleResolution canh giữ #5: tổng hợp lại thì phán định truyện cũ vô
// hiệu, storyChoice không được im lặng khoán open/closed cũ lên synthesis mới (nếu không người
// dùng sẽ không được hỏi lại).
func TestStoryChoiceIgnoresStaleResolution(t *testing.T) {
	ws := OpenWorkspace(t.TempDir())
	if err := ws.writeJSON(fileIntent, Intent{}); err != nil {
		t.Fatal(err)
	}
	if err := writeArtifact(ws, fileSynthesis, "d", BookSynthesis{Premise: "p1", StoryStatus: storyUncertain}); err != nil {
		t.Fatal(err)
	}
	raw, _ := ws.readBytes(fileSynthesis)
	if err := writeArtifact(ws, fileStoryResolve, Digest(raw), StoryResolution{Choice: storyClosed}); err != nil {
		t.Fatal(err)
	}
	r := &runner{ws: ws}
	if got, err := r.storyChoice(); err != nil || got != storyClosed {
		t.Fatalf("phán định ràng buộc synthesis hiện tại phải trả closed, được %q", got)
	}
	// Tổng hợp lại: sửa artifact synthesis → phán định cũ InputDigest mất khớp, phải bị bỏ qua, về "cần hỏi lại" (trả rỗng).
	if err := writeArtifact(ws, fileSynthesis, "d", BookSynthesis{Premise: "p2", StoryStatus: storyUncertain}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.storyChoice(); err != nil || got != "" {
		t.Fatalf("sau tổng hợp lại phán định cũ phải vô hiệu trả rỗng, được %q", got)
	}
}

// TestBudgetsFromDepsPerTier canh giữ núm cấp (RFC §13.1): ngân sách từng hàm ngữ nghĩa suy theo
// cấp riêng của mình, cửa sổ nhỏ của cấp rẻ chỉ ràng buộc hàm của nó, không kéo lê giai đoạn khác.
func TestBudgetsFromDepsPerTier(t *testing.T) {
	small := ModelRuntime{ContextTokens: 32000, MaxOutputTokens: 4000}
	big := ModelRuntime{ContextTokens: 200000, MaxOutputTokens: 16000}
	b := budgetsFromDeps(Deps{
		Segment:    Caller{Runtime: small},
		Analyze:    Caller{Runtime: big},
		Synthesize: Caller{Runtime: big},
	})
	if b.SegmentChunkBytes >= b.Analyze.ContextBytes {
		t.Fatalf("cửa sổ cấp nhỏ của segment phải chỉ ràng buộc chính nó: seg=%d analyze=%d", b.SegmentChunkBytes, b.Analyze.ContextBytes)
	}
	if b.Analyze.MaxOutputTokens != 16000 || b.SegmentMaxTokens != 4000 {
		t.Fatalf("ngân sách đầu ra phải lấy trần cấp riêng: analyze=%d segment=%d", b.Analyze.MaxOutputTokens, b.SegmentMaxTokens)
	}
}

// TestRunSavesFailureOnContractViolation canh giữ §14.2: vi phạm hợp đồng Schema native phải lộ
// ra ngay lập tức, và ghi phản hồi gốc cùng metadata vào failures/.
func TestRunSavesFailureOnContractViolation(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &nativeImportModel{mockModel: &mockModel{responses: []string{"这不是 JSON"}}}
	ch, err := Run(context.Background(), testDeps(st, m), Options{SourcePath: src, AutoConfirm: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var failed bool
	for ev := range ch {
		if ev.Stage == StageError {
			failed = true
		}
	}
	if !failed {
		t.Fatal("đầu ra bất hợp lệ phải kết thúc bằng StageError")
	}
	ws := OpenWorkspace(dir)
	if !ws.has("failures/last-response.txt") {
		t.Fatal("phải lưu phản hồi mô hình gốc lần cuối")
	}
	var meta FailureMeta
	if err := ws.readJSON("failures/last.json", &meta); err != nil {
		t.Fatalf("đọc metadata thất bại: %v", err)
	}
	if meta.Stage != string(ActionSegment) {
		t.Fatalf("metadata thất bại phải ghi chú giai đoạn segment, được %q", meta.Stage)
	}
}

// TestRunGuidanceResegments canh giữ §18.3: khôi phục mang --guide làm phân tách cũ tự nhiên mất
// khớp, nhận diện lại theo hướng dẫn mới và dừng lần nữa ở chỗ xác nhận; InputDigest phân tách mới
// ràng buộc văn bản hướng dẫn.
func TestRunGuidanceResegments(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文一\n第二章\n正文二\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drain := func(ch <-chan Event) (awaiting bool) {
		for ev := range ch {
			if ev.Stage == StageError {
				t.Fatalf("pipeline thất bại: %v", ev.Err)
			}
			if ev.Stage == StageAwaitingConfirmation {
				awaiting = true
			}
		}
		return awaiting
	}
	// Nhập tương tác lần đầu: mô hình cắt cả sách thành 1 chương, dừng ở xác nhận.
	one := boundariesJSON(boundaryFixture("L1", "", kindChapter, "第一章"))
	ch, err := Run(context.Background(), testDeps(st, &mockModel{responses: []string{one}}), Options{SourcePath: src})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !drain(ch) {
		t.Fatal("lượt nhập đầu phải dừng ở xác nhận phân tách")
	}
	// Khôi phục kèm hướng dẫn: phân tách cũ mất khớp → nhận diện lại thành 2 chương, dừng lần nữa ở xác nhận.
	two := boundariesJSON(
		boundaryFixture("L1", "", kindChapter, "第一章"),
		boundaryFixture("L3", "", kindChapter, "第二章"),
	)
	guidance := "第二章也是独立章节"
	ch2, err := Run(context.Background(), testDeps(st, &mockModel{responses: []string{two}}), Options{Guidance: guidance})
	if err != nil {
		t.Fatalf("Run khôi phục: %v", err)
	}
	if !drain(ch2) {
		t.Fatal("sau nhận diện lại phải dừng lần nữa ở xác nhận phân tách")
	}
	ws := OpenWorkspace(dir)
	art, err := readArtifact[Segmentation](ws, fileSegmentation)
	if err != nil {
		t.Fatalf("đọc artifact phân tách: %v", err)
	}
	if len(art.Payload.Chapters) != 2 {
		t.Fatalf("phải cắt thành 2 chương theo hướng dẫn, được %d", len(art.Payload.Chapters))
	}
	norm, _ := ws.LoadSource()
	if art.InputDigest != segmentInputDigest(Digest(norm), guidance, segmentPromptVersion) {
		t.Fatal("InputDigest phân tách mới phải ràng buộc văn bản hướng dẫn")
	}
}

// TestBudgetsFromRuntime xác nhận hai ngân sách phình theo dung lượng thật của mô hình, khả năng
// không biết thì lùi về mặc định bảo thủ (RFC §9.2/§21).
func TestBudgetsFromRuntime(t *testing.T) {
	if got := budgetsFromRuntime(ModelRuntime{}); got != DefaultRunBudgets() {
		t.Fatal("khả năng không biết phải lùi về mặc định bảo thủ")
	}
	small := budgetsFromRuntime(ModelRuntime{ContextTokens: 32000, MaxOutputTokens: 4000})
	big := budgetsFromRuntime(ModelRuntime{ContextTokens: 200000, MaxOutputTokens: 16000})
	if big.Analyze.ContextBytes <= small.Analyze.ContextBytes {
		t.Fatalf("context lớn hơn phải phình ngân sách đầu vào analyze: small=%d big=%d", small.Analyze.ContextBytes, big.Analyze.ContextBytes)
	}
	if big.Analyze.MaxOutputTokens != 16000 {
		t.Fatalf("ngân sách đầu ra phải lấy trần completion của mô hình, được %d", big.Analyze.MaxOutputTokens)
	}
}

// TestProfileForKeyPolicy canh giữ phạm vi gộp sự kiện: backoff yêu cầu (kèm mốc hết hạn) cùng
// Key nhấp nháy tại chỗ; hỏi lại sau kiểm tra là sự kiện ngữ nghĩa xuyên lời gọi, không mang Key,
// mỗi cái một dòng — phân tách gọi từng khối, dùng chung Key sẽ khiến khối sau đè khối trước, bảng
// chỉ còn một dòng unit_id không ngừng đổi, manh mối điều tra mất sạch; step là sự kiện tiến độ
// thường (không cấp cảnh báo).
func TestProfileForKeyPolicy(t *testing.T) {
	r := &runner{events: make(chan Event, 3)}
	prof := r.profileFor(Caller{}, StageSegmenting)
	prof.notify("backoff", time.Now().Add(time.Second))
	prof.notify("hỏi lại", time.Time{})
	prof.step(2, 12, "phân tách khối %d/%d...", 2, 12)
	backoff, reask, step := <-r.events, <-r.events, <-r.events
	if backoff.Key == "" || backoff.Level != "warn" || backoff.RetryAt.IsZero() {
		t.Fatalf("backoff yêu cầu phải là sự kiện warn mang Key và mốc hết hạn: %+v", backoff)
	}
	if reask.Key != "" || reask.Level != "warn" {
		t.Fatalf("hỏi lại sau kiểm tra phải là sự kiện warn không Key (một dòng riêng): %+v", reask)
	}
	if step.Level != "" || step.Current != 2 || step.Total != 12 {
		t.Fatalf("step phải là sự kiện tiến độ thường: %+v", step)
	}
}

// TestCallProfileOptions xác nhận callProfile chỉ chịu trách nhiệm ngân sách đầu ra và thinking;
// response_format do callStructured chọn theo sự thật mô hình và Contract, không được lắp lại
// trong Profile.
func TestCallProfileOptions(t *testing.T) {
	if got := (callProfile{}).callOptions(100); len(got) != 1 {
		t.Fatalf("giá trị 0 chỉ nên mang maxTokens, được %d option", len(got))
	}
	if got := (callProfile{thinking: "high"}).callOptions(100); len(got) != 2 {
		t.Fatalf("thinking phải mang 2 option, được %d", len(got))
	}
}
