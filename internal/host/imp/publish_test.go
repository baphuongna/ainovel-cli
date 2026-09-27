package imp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// spyCommitter ghi số lần gọi Execute, cho test idempotent/khôi phục của công bố.
type spyCommitter struct{ calls int }

func (s *spyCommitter) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	s.calls++
	return json.RawMessage(`{}`), nil
}

func TestCheckFoundationConflictsNormalizesBookMetadata(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "测试书", Synopsis: "测试简介"}); err != nil {
		t.Fatal(err)
	}
	f := &Foundation{Book: domain.BookMetadata{Title: " 测试书 ", Synopsis: " 测试简介 "}}
	if err := checkFoundationConflicts(st, f); err != nil {
		t.Fatalf("thông tin tác phẩm giống nhau sau chuẩn hóa không nên xung đột: %v", err)
	}
}

// TestPublishChapterHandlesStalePendingCommit canh giữ khôi phục cửa sổ sập khi công bố: sập
// rơi vào giữa MarkChapterComplete và ClearPendingCommit sẽ để sót pending_commit trỏ vào chương
// này. Chương đã hoàn thành mà nhảy qua trực tiếp sẽ né nhánh dọn dẹp của công cụ commit, chương
// kế Execute bị từ chối với ErrToolConflict, lượt nhập mỗi lần chạy lại đều chết tại một chỗ —
// gặp sót thì bắt buộc vẫn đi một lần đường idempotent của công cụ.
func TestPublishChapterHandlesStalePendingCommit(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 100, "mystery", "quest"); err != nil {
		t.Fatal(err)
	}
	f := ImportedChapterFacts{Chapter: 1, Summary: "s", CoreEvent: "c", HookType: "mystery", DominantStrand: "quest"}

	// Không có sót: chương đã hoàn thành bỏ qua với chi phí 0, không kích hoạt commit.
	spy := &spyCommitter{}
	if err := publishChapter(context.Background(), st, spy, 1, "正文", f); err != nil {
		t.Fatalf("chương đã hoàn thành phải bỏ qua idempotent: %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("không có sót thì không nên gọi commit, được %d lần", spy.calls)
	}

	// Sót trỏ vào chương này: bắt buộc đi một lần đường idempotent của commit để dọn dẹp.
	if err := st.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 1}); err != nil {
		t.Fatal(err)
	}
	if err := publishChapter(context.Background(), st, spy, 1, "正文", f); err != nil {
		t.Fatalf("đường dọn sót không nên thất bại: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("trúng sót phải gọi commit đúng một lần, được %d lần", spy.calls)
	}
}
