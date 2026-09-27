package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestUpgradeProjectMigratesLegacyBook(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	premise := "# 《寿元账》\n\n## 核心冲突\n\n凡人以寿元换取灵性，在求生与守住人性之间挣扎。\n\n## 主角目标\n\n活下去。"
	if err := st.Outline.SavePremise(premise); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	progress := []byte(`{"novel_name":"寿元账"}`)
	if err := os.WriteFile(filepath.Join(dir, "meta", "progress.json"), progress, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := upgradeProject(st); err != nil {
		t.Fatalf("upgradeProject: %v", err)
	}
	book, err := st.Book.Load()
	if err != nil {
		t.Fatalf("Load book: %v", err)
	}
	if book == nil || book.Title != "寿元账" || book.Synopsis != "凡人以寿元换取灵性，在求生与守住人性之间挣扎。" {
		t.Fatalf("unexpected migrated book: %+v", book)
	}
	if checkpoint := st.Checkpoints.LatestByStep(domain.GlobalScope(), "book"); checkpoint == nil {
		t.Fatal("book checkpoint was not recorded")
	}
	version, err := st.LoadProjectFormatVersion()
	if err != nil || version != storepkg.CurrentProjectFormatVersion {
		t.Fatalf("format version = %d, err = %v", version, err)
	}
}

func TestInterventionStopsWhenPersistenceFails(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.RunMeta.Init("default", "test", "model"); err != nil {
		t.Fatalf("RunMeta.Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "run.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st, events: make(chan Event, 4)}
	err := h.doIntervention("修改主角性格", false)
	if err == nil || !strings.Contains(err.Error(), "Persist can thiệp thất bại") {
		t.Fatalf("expected persistence error, got %v", err)
	}
	// Steer công cộng phải đợi tác vụ async và trả lại cùng một lỗi nghiệp vụ cho TUI; không thể chỉ biểu thị goroutine
	// khởi động thành công, nếu không giao diện mãi mãi không nhận được thất bại thật.
	err = h.Steer("修改主角性格")
	if err == nil || !strings.Contains(err.Error(), "Persist can thiệp thất bại") {
		t.Fatalf("Steer should return persistence error, got %v", err)
	}
}

func TestCloseWaitsForRegisteredAsyncWork(t *testing.T) {
	h := &Host{
		observer: &observer{},
		engine:   &engine{},
		events:   make(chan Event, 1),
		streamCh: make(chan string, 1),
		done:     make(chan struct{}, 1),
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if !h.launchAsync(func() {
		close(started)
		<-release
	}) {
		t.Fatal("launchAsync unexpectedly refused")
	}
	<-started
	closed := make(chan struct{})
	go func() {
		h.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned before async work finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after async work finished")
	}
}

// v2→v3: sách tiếng Việt đang viết dở có ChapterWordCounts đếm theo rune phải được tính lại
// theo từ, sách tiếng Trung giữ nguyên.
func TestUpgradeProjectRecountsVietnameseWords(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.SaveProjectFormatVersion(2); err != nil {
		t.Fatalf("SaveProjectFormatVersion: %v", err)
	}
	vi := "Hắn chậm rãi bước qua cánh cổng đá."
	zh := "他缓缓走过石门。"
	if err := st.Drafts.SaveFinalChapter(1, vi); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(2, zh); err != nil {
		t.Fatal(err)
	}
	legacy := map[int]int{1: len([]rune(vi)), 2: len([]rune(zh))}
	if err := st.Progress.Save(&domain.Progress{
		CompletedChapters: []int{1, 2},
		ChapterWordCounts: legacy,
		TotalWordCount:    legacy[1] + legacy[2],
	}); err != nil {
		t.Fatal(err)
	}

	if err := upgradeProject(st); err != nil {
		t.Fatalf("upgradeProject: %v", err)
	}
	p, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p.ChapterWordCounts[1] != 8 || p.ChapterWordCounts[2] != legacy[2] || p.TotalWordCount != 8+legacy[2] {
		t.Errorf("counts = %+v total=%d", p.ChapterWordCounts, p.TotalWordCount)
	}
	if v, _ := st.LoadProjectFormatVersion(); v != storepkg.CurrentProjectFormatVersion {
		t.Errorf("format version = %d", v)
	}
}
