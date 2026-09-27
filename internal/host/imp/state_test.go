package imp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func mustLoadState(t *testing.T, w *Workspace) Facts {
	t.Helper()
	f, err := LoadState(w)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	return f
}

func TestNextActionChain(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want Action
	}{
		{"trống", Facts{}, ActionIngest},
		{"đã dựng workspace chờ phân tách", Facts{WorkspaceReady: true}, ActionSegment},
		{"đã phân tách chờ xác nhận", Facts{WorkspaceReady: true, Segmented: true}, ActionAwaitConfirmation},
		{"đã xác nhận chờ phân tích", Facts{WorkspaceReady: true, Segmented: true, Confirmed: true, ExpectedChapters: 3}, ActionAnalyze},
		{"phân tích chưa đủ", Facts{WorkspaceReady: true, Segmented: true, Confirmed: true, ExpectedChapters: 3, AnalyzedChapters: 2}, ActionAnalyze},
		{"phân tích đủ chờ tổng hợp", Facts{WorkspaceReady: true, Segmented: true, Confirmed: true, ExpectedChapters: 3, AnalyzedChapters: 3}, ActionSynthesize},
		{"sau tổng hợp uncertain chờ phán định", Facts{WorkspaceReady: true, Segmented: true, Confirmed: true, ExpectedChapters: 3, AnalyzedChapters: 3, Synthesized: true, StoryUncertain: true}, ActionAwaitStoryResolution},
		{"uncertain đã phán định chờ công bố", Facts{WorkspaceReady: true, Segmented: true, Confirmed: true, ExpectedChapters: 3, AnalyzedChapters: 3, Synthesized: true, StoryUncertain: true, StoryResolved: true}, ActionPublish},
		{"trạng thái rõ ràng chờ công bố", Facts{WorkspaceReady: true, Segmented: true, Confirmed: true, ExpectedChapters: 3, AnalyzedChapters: 3, Synthesized: true}, ActionPublish},
		{"toàn bộ nhất quán", Facts{WorkspaceReady: true, Segmented: true, Confirmed: true, ExpectedChapters: 3, AnalyzedChapters: 3, Synthesized: true, Published: true}, ActionDone},
		{"công bố trạng thái cuối nối tắt thượng nguồn hết tươi", Facts{Published: true}, ActionDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NextAction(c.f)
			if got != c.want {
				t.Fatalf("NextAction=%s want=%s", got, c.want)
			}
			// Hằng số với cùng một snapshot sự thật.
			if NextAction(c.f) != got {
				t.Fatal("NextAction không hằng số với cùng một Facts")
			}
		})
	}
}

func TestLoadStateReflectsWorkspace(t *testing.T) {
	book := t.TempDir()
	// Chưa dựng workspace: không hoạt động → ingest.
	w := OpenWorkspace(book)
	if NextAction(mustLoadState(t, w)) != ActionIngest {
		t.Fatal("sách trống phải ingest trước")
	}
	// Sau khi dựng workspace: workspace ready, chưa phân tách → segment.
	src := filepath.Join(book, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _, err := Ingest(book, src, Intent{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	f := mustLoadState(t, ws)
	if !f.WorkspaceReady || f.Segmented {
		t.Fatalf("sự thật sau khi dựng workspace không khớp: %+v", f)
	}
	if NextAction(f) != ActionSegment {
		t.Fatal("sau khi dựng workspace phải segment")
	}
}

func TestLoadStateReportsCorruptArtifact(t *testing.T) {
	book := t.TempDir()
	src := filepath.Join(book, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _, err := Ingest(book, src, Intent{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.writeAtomic(fileSegmentation, []byte("{")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(ws); err == nil || !strings.Contains(err.Error(), "artifact phân tách") {
		t.Fatalf("artifact hỏng không được ngụy trang thành chưa phân tách: %v", err)
	}
}

func TestIngestSnapshotConsistent(t *testing.T) {
	book := t.TempDir()
	src := filepath.Join(book, "book.txt")
	content := "第一章\r\n正文一\r\n\r\n第二章\r\n正文二"
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, m, err := Ingest(book, src, Intent{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if m.Encoding != encodingUTF8 || m.SourceName != "book.txt" {
		t.Fatalf("manifest không khớp: %+v", m)
	}
	snap, err := ws.LoadSource()
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot nguồn phải đã chuẩn hóa, và tóm tắt khớp manifest.
	if string(snap) != "第一章\n正文一\n\n第二章\n正文二" {
		t.Fatalf("snapshot nguồn chưa chuẩn hóa: %q", snap)
	}
	if Digest(snap) != m.NormalizedSHA256 {
		t.Fatal("tóm tắt snapshot nguồn không khớp manifest")
	}
}

// TestGuidanceChangeInvalidatesSegmentation canh giữ §18.3: hướng dẫn phân tách là đầu vào ngữ
// nghĩa của segmentation, hướng dẫn thay đổi làm phân tách cũ (và toàn bộ hạ nguồn) tự nhiên mất
// khớp rồi làm lại, không cần quy tắc vô hiệu thủ công.
func TestGuidanceChangeInvalidatesSegmentation(t *testing.T) {
	book := t.TempDir()
	src := filepath.Join(book, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _, err := Ingest(book, src, Intent{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	norm, err := ws.LoadSource()
	if err != nil {
		t.Fatal(err)
	}
	seg := Segmentation{Chapters: []ChapterSpan{{Number: 1, Title: "第一章", Start: 0, End: len(norm)}}}
	if err := writeArtifact(ws, fileSegmentation, segmentInputDigest(Digest(norm), "", segmentPromptVersion), seg); err != nil {
		t.Fatal(err)
	}
	if !mustLoadState(t, ws).Segmented {
		t.Fatal("không có hướng dẫn thì phân tách phải hữu hiệu")
	}
	if err := ws.writeAtomic(fileGuidance, []byte("幕间也是独立章节")); err != nil {
		t.Fatal(err)
	}
	if mustLoadState(t, ws).Segmented {
		t.Fatal("sau khi hướng dẫn đổi, phân tách cũ phải vô hiệu (cần nhận diện lại)")
	}
}

// TestResumeSummary canh giữ nhắc khởi động §18.2: không có workspace trả chuỗi rỗng; kẹt nửa
// đường thì đưa mô tả theo giai đoạn, để người dùng không phải đợi sáng tác bị cổng chặn từ chối
// mới phát hiện cuốn sách kẹt nửa đường nhập.
func TestResumeSummary(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSummary(st); got != "" {
		t.Fatalf("không có workspace nhập phải trả chuỗi rỗng, được %q", got)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _, err := Ingest(dir, src, Intent{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if got := ResumeSummary(st); !strings.Contains(got, "chưa hoàn thành phân tách") {
		t.Fatalf("vừa dựng workspace phải nhắc chưa hoàn thành phân tách, được %q", got)
	}
	// Phân tách + xác nhận sẵn sàng, phân tích 0/1 → nhắc tiến độ phân tích.
	norm, _ := ws.LoadSource()
	seg := Segmentation{Chapters: []ChapterSpan{{Number: 1, Title: "第一章", Start: 0, End: len(norm)}}}
	if err := writeArtifact(ws, fileSegmentation, segmentInputDigest(Digest(norm), "", segmentPromptVersion), seg); err != nil {
		t.Fatal(err)
	}
	raw, _ := ws.readBytes(fileSegmentation)
	if err := writeArtifact(ws, fileConfirmation, Digest(raw), Confirmation{Method: confirmMethodAuto, Chapters: 1}); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSummary(st); !strings.Contains(got, "đã phân tích 0/1 chương") {
		t.Fatalf("phải nhắc tiến độ phân tích, được %q", got)
	}
}

// TestResumeStatusPublishedIsTerminal canh giữ trạng thái cuối công bố (sự cố thực đo): sách đã
// công bố toàn lượng, nâng cấp segmentPromptVersion làm artifact phân tách của workspace hết tươi,
// ResumeStatus không được dựa vào đó mà phán sách trở về "nửa đường nhập" — nếu không cổng chặn
// cross-restart của startEngine sẽ từ chối vĩnh viễn việc viết tiếp cho sách đã công bố.
func TestResumeStatusPublishedIsTerminal(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _, err := Ingest(dir, src, Intent{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	norm, _ := ws.LoadSource()
	// Ghi phân tách bằng số phiên bản cũ: mô phỏng digest mất khớp do prompt nâng cấp sau công bố.
	seg := Segmentation{Chapters: []ChapterSpan{{Number: 1, Title: "第一章", Start: 0, End: len(norm)}}}
	if err := writeArtifact(ws, fileSegmentation, segmentInputDigest(Digest(norm), "", "seg-v0"), seg); err != nil {
		t.Fatal(err)
	}
	// Chưa công bố + phân tách hết tươi: vẫn là nhập nửa đường, cổng chặn phải chặn.
	if active, done, err := ResumeStatus(st); err != nil || !active || done {
		t.Fatalf("workspace hết tươi chưa công bố phải bị phán chưa hoàn thành (active=%v done=%v)", active, done)
	}
	// Kho chính thức đã ghi toàn lượng theo phân tách đó → đối soát công bố qua, trạng thái cuối không bị ảnh hưởng bởi thượng nguồn hết tươi.
	if err := st.Book.Save(domain.BookMetadata{Title: "测试书", Synopsis: "测试简介"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("前提"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "第一章"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{CompletedChapters: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if active, done, err := ResumeStatus(st); err != nil || !active || !done {
		t.Fatalf("sách đã công bố phải bị phán nhập hoàn thành (active=%v done=%v)", active, done)
	}
	if got := ResumeSummary(st); got != "" {
		t.Fatalf("sách đã công bố không nên nhắc lượt nhập chưa hoàn thành, được %q", got)
	}
}

func TestImportPreconditions(t *testing.T) {
	// Sách trống thì qua.
	empty := store.NewStore(t.TempDir())
	if err := checkImportPreconditions(empty); err != nil {
		t.Fatalf("sách trống phải qua kiểm tra trước: %v", err)
	}
	// Có chương hoàn thành thì bị từ chối.
	nonEmpty := store.NewStore(t.TempDir())
	if err := nonEmpty.Progress.Save(&domain.Progress{CompletedChapters: []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := checkImportPreconditions(nonEmpty); err == nil {
		t.Fatal("sách không trống phải bị từ chối nhập")
	}
	withBook := store.NewStore(t.TempDir())
	if err := withBook.Book.Save(domain.BookMetadata{Title: "已有作品", Synopsis: "已有简介"}); err != nil {
		t.Fatal(err)
	}
	if err := checkImportPreconditions(withBook); err == nil {
		t.Fatal("đã có thông tin tác phẩm thì phải bị từ chối nhập")
	}
}
