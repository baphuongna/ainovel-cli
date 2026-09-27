package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

func newTestCommitChapterTool(st *store.Store) *CommitChapterTool {
	return NewCommitChapterTool(st, NewStyleStatsIndex(st))
}

func saveTestChapterRecord(t *testing.T, st *store.Store, chapter int, content string) {
	t.Helper()
	if _, err := st.ChapterRecords.Accept(chapter, domain.ChapterOriginGenerated, content, domain.ChapterFacts{
		Title: fmt.Sprintf("第%d章", chapter), Summary: "既有摘要", KeyEvents: []string{"既有事件"},
	}, domain.StyleDelta{}); err != nil {
		t.Fatalf("SaveChapterRecord %d: %v", chapter, err)
	}
}

func TestCommitChapterSchemaDescribesFeedbackAsObject(t *testing.T) {
	tool := newTestCommitChapterTool(store.NewStore(t.TempDir()))
	if !tool.StrictSchema() {
		t.Fatal("commit_chapter must use strict schema")
	}
	if err := llmcontract.ValidateStrictReady(tool.Schema()); err != nil {
		t.Fatalf("commit_chapter schema is not strict-ready: %v", err)
	}
	schema := tool.Schema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema["properties"])
	}
	feedback, ok := props["feedback"].(map[string]any)
	if !ok {
		t.Fatalf("feedback schema missing: %#v", props["feedback"])
	}
	desc, _ := feedback["description"].(string)
	if !strings.Contains(desc, "JSON object") || !strings.Contains(desc, "stringify") {
		t.Fatalf("feedback description should warn against stringified JSON, got %q", desc)
	}
	if got := fmt.Sprint(feedback["type"]); got != "[object null]" {
		t.Fatalf("feedback type = %v, want nullable object", feedback["type"])
	}
}

func TestCommitChapterRejectsUnknownForeshadowReferenceBeforePending(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter": 1, "title": "第一章", "summary": "推进", "characters": []string{"主角"}, "key_events": []string{"发现线索"},
		"foreshadow_updates": []map[string]any{{"id": "missing", "action": "resolve"}},
	})
	if _, err := newTestCommitChapterTool(s).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Fatalf("expected unknown foreshadow rejection, got %v", err)
	}
	if pending, err := s.Signals.LoadPendingCommit(); err != nil || pending != nil {
		t.Fatalf("invalid args must not create pending commit: pending=%+v err=%v", pending, err)
	}
}

func TestCommitChapterRejectsSkippedNormalChapter(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{
		"chapter": 2, "title": "第二章", "summary": "跳过第一章", "characters": []string{"主角"}, "key_events": []string{"事件"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := newTestCommitChapterTool(s).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "chỉ được nộp chương kế tiếp 1") {
		t.Fatalf("expected skipped chapter rejection, got %v", err)
	}
	if pending, err := s.Signals.LoadPendingCommit(); err != nil || pending != nil {
		t.Fatalf("rejected commit must not create pending state, pending=%+v err=%v", pending, err)
	}
}

func TestCommitChapterRejectsInvalidNestedFields(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter": 1, "title": "第一章", "summary": "推进", "characters": []string{"主角"}, "key_events": []string{"发现线索"},
		"relationship_changes": []map[string]any{{"character_a": "主角", "character_b": "", "relation": "敌对"}},
	})
	if _, err := newTestCommitChapterTool(s).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "relationship_changes[0]") {
		t.Fatalf("expected nested field rejection, got %v", err)
	}
}

func TestCommitChapterRejectsNonPendingRewrite(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init(10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := store.Progress.SetPendingRewrites([]int{2}, "测试重写"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.Drafts.SaveDraft(3, "这是错误章节的正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := newTestCommitChapterTool(store)
	args, err := json.Marshal(map[string]any{
		"chapter":         3,
		"title":           "第三章",
		"summary":         "错误提交",
		"characters":      []string{"主角"},
		"key_events":      []string{"误提交"},
		"timeline_events": []any{},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected commit to be rejected during rewrite flow")
	}

	if _, err := os.Stat(dir + "/chapters/03.md"); !os.IsNotExist(err) {
		t.Fatalf("chapter should not be persisted, stat err=%v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if len(progress.CompletedChapters) != 1 || progress.CompletedChapters[0] != 2 {
		t.Fatalf("completed chapters should only contain original chapter 2, got %v", progress.CompletedChapters)
	}
	if progress.CurrentChapter != 3 {
		t.Fatalf("current chapter should not advance beyond original progress, got %d", progress.CurrentChapter)
	}
}

func TestCommitChapterAllowsPendingRewrite(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init(10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := store.Progress.SetPendingRewrites([]int{2}, "测试重写"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.Drafts.SaveDraft(2, "这是正确待重写章节的正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := newTestCommitChapterTool(store)
	args, err := json.Marshal(map[string]any{
		"chapter":         2,
		"title":           "第二章",
		"summary":         "正确提交",
		"characters":      []string{"主角"},
		"key_events":      []string{"完成重写"},
		"timeline_events": []any{},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := os.Stat(dir + "/chapters/02.md"); err != nil {
		t.Fatalf("chapter should be persisted: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if len(progress.CompletedChapters) != 1 || progress.CompletedChapters[0] != 2 {
		t.Fatalf("unexpected completed chapters: %v", progress.CompletedChapters)
	}
	pending, err := store.Signals.LoadPendingCommit()
	if err != nil {
		t.Fatalf("LoadPendingCommit: %v", err)
	}
	if pending != nil {
		t.Fatalf("expected pending commit cleared, got %+v", pending)
	}
}

// TestCommitChapterRewriteKeepsOwnForeshadowPlant chốt chết issue #112: khi viết lại "chương gieo" của phục bút,
// Writer thấy phục bút đó đã có trong sổ cái, đương nhiên chỉ ghi advance; cài đặt cũ ghi đè nguyên cả bản ghi chương, plant theo đó mất,
// Projector phát lại toàn lượng thì báo "triển khai phục bút không rõ" và khóa cứng hàng đợi viết lại. Dữ kiện gieo phải được giữ lại.
func TestCommitChapterRewriteKeepsOwnForeshadowPlant(t *testing.T) {
	const foreshadowID = "f_spillway_photo"
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	// Trạng thái ghi đĩa sau lần nộp đầu của chương 2: bản ghi là plant, sổ cái đã dựng mục.
	if _, err := s.ChapterRecords.Accept(2, domain.ChapterOriginGenerated, "旧版正文。", domain.ChapterFacts{
		Title: "第二章", Summary: "埋下线索", KeyEvents: []string{"发现旧照"},
		ForeshadowUpdates: []domain.ForeshadowUpdate{{ID: foreshadowID, Action: "plant", Description: "泄洪道旧照"}},
	}, domain.StyleDelta{}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: foreshadowID, Description: "泄洪道旧照", PlantedAt: 2, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "测试重写"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "这是重写后的第二章正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	args, err := json.Marshal(map[string]any{
		"chapter": 2, "title": "第二章", "summary": "重写后埋线索",
		"characters": []string{"主角"}, "key_events": []string{"重新发现旧照"},
		"foreshadow_updates": []map[string]any{{"id": foreshadowID, "action": "advance"}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := newTestCommitChapterTool(s).Execute(context.Background(), args); err != nil {
		t.Fatalf("viết lại chương gieo phục bút không được thất bại: %v", err)
	}

	record, err := s.ChapterRecords.Load(2)
	if err != nil || record == nil {
		t.Fatalf("Load record: %+v err=%v", record, err)
	}
	var planted bool
	for _, u := range record.Facts.ForeshadowUpdates {
		if u.ID == foreshadowID && u.Action == "plant" {
			planted = true
		}
	}
	if !planted {
		t.Fatalf("sau viết lại phải giữ plant của chương này, thực tế %+v", record.Facts.ForeshadowUpdates)
	}

	ledger, err := s.World.LoadForeshadowLedger()
	if err != nil {
		t.Fatalf("LoadForeshadowLedger: %v", err)
	}
	if len(ledger) != 1 || ledger[0].ID != foreshadowID || ledger[0].PlantedAt != 2 {
		t.Fatalf("sổ cái phải giữ dữ kiện gieo của chương 2, thực tế %+v", ledger)
	}

	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if len(progress.PendingRewrites) != 0 {
		t.Fatalf("hàng đợi viết lại phải đã drain cạn, thực tế %v", progress.PendingRewrites)
	}
}

func TestCommitChapterRewriteRepairsPlantLostByPreviousFailure(t *testing.T) {
	const foreshadowID = "F24_LICENSE_SUSPENSION"
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatal(err)
	}
	// Mô phỏng trạng thái sau lần viết lại đầu thất bại của phiên bản cũ: sổ cái phái sinh vẫn giữ dữ kiện gieo đúng,
	// nhưng bản ghi chương đã bị advance ghi đè, thiếu plant cùng chương.
	oldContent := "旧版本失败后留下的终稿。"
	if _, err := s.ChapterRecords.Accept(2, domain.ChapterOriginGenerated, oldContent, domain.ChapterFacts{
		Title: "第二章", Summary: "损坏记录", KeyEvents: []string{"推进线索"},
		ForeshadowUpdates: []domain.ForeshadowUpdate{{ID: foreshadowID, Action: "advance"}},
	}, domain.StyleDelta{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveFinalChapter(2, oldContent); err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{{
		ID: foreshadowID, Description: "执照暂停线索", PlantedAt: 2, Status: "planted",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "恢复旧版本损坏记录"); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(2, "修复后的第二章正文。"); err != nil {
		t.Fatal(err)
	}

	args, err := json.Marshal(map[string]any{
		"chapter": 2, "title": "第二章", "summary": "恢复伏笔链",
		"characters": []string{"主角"}, "key_events": []string{"推进线索"},
		"foreshadow_updates": []map[string]any{{"id": foreshadowID, "action": "advance"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newTestCommitChapterTool(s).Execute(context.Background(), args); err != nil {
		t.Fatalf("plant cùng chương bị phiên bản cũ làm mất phải khôi phục được tất định: %v", err)
	}

	record, err := s.ChapterRecords.Load(2)
	if err != nil || record == nil {
		t.Fatalf("Load record: %+v err=%v", record, err)
	}
	if got := record.Facts.ForeshadowUpdates; len(got) != 2 || got[0].Action != "plant" || got[0].ID != foreshadowID || got[1].Action != "advance" {
		t.Fatalf("repaired foreshadow chain = %+v", got)
	}
	ledger, err := s.World.LoadForeshadowLedger()
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 1 || ledger[0].Status != "advanced" || ledger[0].PlantedAt != 2 {
		t.Fatalf("reprojected ledger = %+v", ledger)
	}
}

func TestCommitChapterRewriteValidatesRecordSetBeforeWriting(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChapterRecords.Accept(1, domain.ChapterOriginGenerated, "第一章终稿。", domain.ChapterFacts{
		Title: "第一章", Summary: "损坏基线", KeyEvents: []string{"错误推进"},
		ForeshadowUpdates: []domain.ForeshadowUpdate{{ID: "missing", Action: "advance"}},
	}, domain.StyleDelta{}); err != nil {
		t.Fatal(err)
	}
	oldContent := "第二章旧终稿。"
	if _, err := s.ChapterRecords.Accept(2, domain.ChapterOriginGenerated, oldContent, domain.ChapterFacts{
		Title: "第二章", Summary: "原摘要", KeyEvents: []string{"原事件"},
	}, domain.StyleDelta{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveFinalChapter(2, oldContent); err != nil {
		t.Fatal(err)
	}
	for _, chapter := range []int{1, 2} {
		if err := s.Progress.MarkChapterComplete(chapter, 3000, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "测试写前校验"); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(2, "第二章新正文。"); err != nil {
		t.Fatal(err)
	}

	args, err := json.Marshal(map[string]any{
		"chapter": 2, "title": "第二章", "summary": "新摘要",
		"characters": []string{"主角"}, "key_events": []string{"新事件"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = newTestCommitChapterTool(s).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "đã bỏ đóng băng và chưa ghi kết quả viết lại") {
		t.Fatalf("expected preflight projection error, got %v", err)
	}
	if strings.Contains(err.Error(), errs.ErrStoreWrite.Error()) {
		t.Fatalf("projection invariant must not be classified as a store write: %v", err)
	}
	final, err := s.Drafts.LoadChapterText(2)
	if err != nil || final != oldContent {
		t.Fatalf("final chapter changed before validation: %q err=%v", final, err)
	}
	record, err := s.ChapterRecords.Load(2)
	if err != nil || record == nil || record.Revision != 1 || record.Content != oldContent {
		t.Fatalf("chapter record changed before validation: %+v err=%v", record, err)
	}
	if pending, err := s.Signals.LoadPendingCommit(); err != nil || pending != nil {
		t.Fatalf("invalid frozen commit must be cleared: %+v err=%v", pending, err)
	}
}

// TestCommitChapterRewriteRejectsForwardForeshadowReference cùng nguồn với ca trước (issue #112):
// sổ cái là phép chiếu cả cuốn sách, khi viết lại chương trước vẫn còn nằm đó phục bút do chương sau mới gieo. Cài đặt cũ cho qua → Projector
// phát lại theo thứ tự chương thì báo "triển khai phục bút không rõ", mà lúc đó bản ghi chương đã bị ghi đè, hàng đợi viết lại từ đó khóa cứng.
// Phải chặn trước khi ghi đĩa, và nói rõ "gieo ở chương mấy", model mới sửa được.
func TestCommitChapterRewriteRejectsForwardForeshadowReference(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	for _, ch := range []int{2, 7} {
		if _, err := s.ChapterRecords.Accept(ch, domain.ChapterOriginGenerated, "旧版正文。", domain.ChapterFacts{
			Title: fmt.Sprintf("第%d章", ch), Summary: "摘要", KeyEvents: []string{"事件"},
		}, domain.StyleDelta{}); err != nil {
			t.Fatalf("Accept %d: %v", ch, err)
		}
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete %d: %v", ch, err)
		}
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "f_late", Description: "第七章才埋的线", PlantedAt: 7, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "测试重写"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "这是重写后的第二章正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	args, err := json.Marshal(map[string]any{
		"chapter": 2, "title": "第二章", "summary": "s",
		"characters": []string{"主角"}, "key_events": []string{"e"},
		"foreshadow_updates": []map[string]any{{"id": "f_late", "action": "advance"}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	_, err = newTestCommitChapterTool(s).Execute(context.Background(), args)
	if err == nil {
		t.Fatal("tham chiếu phục bút do chương sau mới gieo phải bị từ chối")
	}
	if !strings.Contains(err.Error(), "được gieo ở chương 7") {
		t.Fatalf("báo lỗi phải nêu rõ chương gieo, model mới tự sửa được, thực tế: %v", err)
	}
	// Mấu chốt: chặn trước khi ghi đĩa — bản ghi chương và hàng đợi viết lại đều không được bẩn vì lần thất bại này.
	if pending, err := s.Signals.LoadPendingCommit(); err != nil || pending != nil {
		t.Fatalf("kiểm tra thất bại không được để lại pending commit: pending=%+v err=%v", pending, err)
	}
	record, err := s.ChapterRecords.Load(2)
	if err != nil || record == nil {
		t.Fatalf("Load record: %+v err=%v", record, err)
	}
	if record.Content != "旧版正文。" {
		t.Fatalf("kiểm tra thất bại không được ghi đè bản ghi chương, thực tế %q", record.Content)
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if len(p.PendingRewrites) != 1 || p.PendingRewrites[0] != 2 {
		t.Fatalf("hàng đợi viết lại phải giữ nguyên chờ thử lại: %v", p.PendingRewrites)
	}
}

func TestCommitChapterClearsInvalidLegacyRewritePending(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatal(err)
	}
	oldContent := "旧终稿。"
	if _, err := s.ChapterRecords.Accept(2, domain.ChapterOriginGenerated, oldContent, domain.ChapterFacts{
		Title: "第二章", Summary: "旧摘要", KeyEvents: []string{"旧事件"},
	}, domain.StyleDelta{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveFinalChapter(2, oldContent); err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{{
		ID: "f_late", Description: "后续伏笔", PlantedAt: 7, Status: "planted",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "恢复旧冻结提交"); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"chapter": 2, "title": "第二章", "summary": "非法旧提交",
		"characters": []string{"主角"}, "key_events": []string{"提前推进"},
		"foreshadow_updates": []map[string]any{{"id": "f_late", "action": "advance"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Signals.SavePendingCommit(domain.PendingCommit{
		Chapter: 2, Stage: domain.CommitStageStarted, Rewrite: true,
		Payload: payload, DraftContent: "冻结正文。",
	}); err != nil {
		t.Fatal(err)
	}

	_, err = newTestCommitChapterTool(s).Execute(context.Background(), payload)
	if err == nil || !strings.Contains(err.Error(), "đã bỏ đóng băng") || !strings.Contains(err.Error(), "được gieo ở chương 7") {
		t.Fatalf("expected actionable legacy pending error, got %v", err)
	}
	if pending, err := s.Signals.LoadPendingCommit(); err != nil || pending != nil {
		t.Fatalf("invalid legacy pending must be cleared: %+v err=%v", pending, err)
	}
	record, err := s.ChapterRecords.Load(2)
	if err != nil || record == nil || record.Revision != 1 || record.Content != oldContent {
		t.Fatalf("clearing pending changed chapter record: %+v err=%v", record, err)
	}
}

func TestCommitChapterRefreshesSharedStyleStatsAfterRewrite(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	completed := []int{1, 2, 3, 4, 5}
	for _, chapter := range completed {
		content := fmt.Sprintf("# 第%d章\n普通正文。\n故事继续。", chapter)
		if err := s.Drafts.SaveFinalChapter(chapter, content); err != nil {
			t.Fatal(err)
		}
		saveTestChapterRecord(t, s, chapter, content)
		if err := s.Progress.MarkChapterComplete(chapter, 100, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	styleStats := NewStyleStatsIndex(s)
	before, err := styleStats.Snapshot(completed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before == nil {
		t.Fatal("expected initialized style stats")
	}

	if err := s.Progress.SetPendingRewrites([]int{2}, "测试增量风格统计"); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(2, "# 第二章\n他不是退缩，而是在等待。\n改写后的故事继续。"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"title":      "第二章",
		"summary":    "完成增量统计测试重写",
		"characters": []string{"主角"},
		"key_events": []string{"完成重写"},
	})
	if _, err := NewCommitChapterTool(s, styleStats).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}

	after, err := styleStats.Snapshot(completed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pattern := range after.Patterns {
		if strings.HasPrefix(pattern.Name, "Câu hiệu chỉnh") && pattern.Total == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rewrite did not refresh shared style stats: %+v", after.Patterns)
	}
}

func TestCommitChapterRewriteRecoveryUsesFrozenDraft(t *testing.T) {
	s := store.NewStore(t.TempDir())
	s.SetLanguage("zh")
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, "第二章旧终稿"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, 100, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "测试重写恢复"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	persistedArgs, err := json.Marshal(map[string]any{
		"chapter": 2, "title": "冻结标题", "summary": "冻结摘要", "characters": []string{"主角"}, "key_events": []string{"冻结事件"},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := s.Signals.SavePendingCommit(domain.PendingCommit{
		Chapter: 2, Stage: domain.CommitStageStarted, Rewrite: true, RewriteMode: "rewrite",
		Payload: persistedArgs, DraftContent: "第二章已经完成的重写正文",
	}); err != nil {
		t.Fatalf("SavePendingCommit: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "重启后被错误覆盖的草稿"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := newTestCommitChapterTool(s)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":2,"summary":"新参数不得采用"}`)); err != nil {
		t.Fatalf("Execute recovery: %v", err)
	}
	final, err := s.Drafts.LoadChapterText(2)
	if err != nil {
		t.Fatalf("LoadChapterText: %v", err)
	}
	if final != "# 第 2 章: 冻结标题\n\n第二章已经完成的重写正文" {
		t.Fatalf("rewrite recovery used overwritten draft: %q", final)
	}
	summary, err := s.Summaries.LoadSummary(2)
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if summary == nil || summary.Summary != "冻结摘要" {
		t.Fatalf("rewrite recovery used regenerated args: %+v", summary)
	}
}

// TestCommitChapterUpdatesCastLedger kiểm chứng: commit_chapter cộng dồn characters của chương này vào cast_ledger,
// brief_role do cast_intros cung cấp được áp dụng, và nhân vật cốt lõi trong characters.json không vào ledger.
func TestCommitChapterUpdatesCastLedger(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	// Thiết lập hồ sơ nhân vật cốt lõi (những cái này không nên vào cast_ledger)
	if err := s.Characters.Save([]domain.Character{
		{Name: "林墨", Role: "主角", Tier: "core"},
		{Name: "李清砚", Role: "导师", Tier: "important"},
	}); err != nil {
		t.Fatalf("Save core characters: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "第一章正文，林墨遇到客栈老板老周与小厮阿云。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := newTestCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    1,
		"title":      "第一章",
		"summary":    "林墨入住客栈",
		"characters": []string{"林墨", "李清砚", "老周", "阿云"},
		"key_events": []string{"入住"},
		"cast_intros": []any{
			map[string]any{"name": "老周", "brief_role": "客栈老板"},
			map[string]any{"name": "阿云", "brief_role": "客栈小厮"},
		},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	summary, err := s.Summaries.LoadSummary(1)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary.Title != "第一章" {
		t.Fatalf("committed title = %+v", summary)
	}

	entries, err := s.Cast.Load()
	if err != nil {
		t.Fatalf("Cast.Load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 ledger entries (老周/阿云), got %d: %+v", len(entries), entries)
	}
	byName := map[string]domain.CastEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if e, ok := byName["老周"]; !ok || e.BriefRole != "客栈老板" || e.FirstSeenChapter != 1 {
		t.Errorf("老周 entry wrong: %+v", e)
	}
	if e, ok := byName["阿云"]; !ok || e.BriefRole != "客栈小厮" || e.AppearanceCount != 1 {
		t.Errorf("阿云 entry wrong: %+v", e)
	}
	if _, ok := byName["林墨"]; ok {
		t.Errorf("nhân vật cốt lõi 林墨 không nên vào ledger")
	}
	if _, ok := byName["李清砚"]; ok {
		t.Errorf("nhân vật cốt lõi 李清砚 không nên vào ledger")
	}
}

func TestCommitChapterReplayAfterPartialCommitDoesNotDuplicateWorldState(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "第一章正文，林墨遇到黑影并突破。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	timeline := []domain.TimelineEvent{{
		Chapter:    1,
		Time:       "清晨",
		Event:      "林墨遇到黑影",
		Characters: []string{"林墨"},
	}}
	stateChanges := []domain.StateChange{{
		Chapter:  1,
		Entity:   "林墨",
		Field:    "realm",
		OldValue: "凡人",
		NewValue: "练气期",
	}}
	foreshadow := []domain.ForeshadowUpdate{{
		ID:          "f1",
		Action:      "plant",
		Description: "黑影身份",
	}}

	// Mô phỏng commit_chapter đã ghi trạng thái thế giới nhưng process crash trước khi MarkChapterComplete.
	if err := s.World.AppendTimelineEvents(timeline); err != nil {
		t.Fatalf("AppendTimelineEvents seed: %v", err)
	}
	if err := s.World.AppendStateChanges(stateChanges); err != nil {
		t.Fatalf("AppendStateChanges seed: %v", err)
	}
	if err := s.World.UpdateForeshadow(1, foreshadow); err != nil {
		t.Fatalf("UpdateForeshadow seed: %v", err)
	}
	persistedArgs, _ := json.Marshal(map[string]any{
		"chapter":            1,
		"title":              "第一章",
		"summary":            "林墨遇到黑影并突破",
		"characters":         []string{"林墨"},
		"key_events":         []string{"遇到黑影", "突破"},
		"timeline_events":    timeline,
		"state_changes":      stateChanges,
		"foreshadow_updates": foreshadow,
	})
	if err := s.Signals.SavePendingCommit(domain.PendingCommit{
		Chapter:      1,
		Stage:        domain.CommitStageStarted,
		Summary:      "半提交摘要",
		Payload:      persistedArgs,
		DraftContent: "第一章正文，林墨遇到黑影并突破。",
	}); err != nil {
		t.Fatalf("SavePendingCommit: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "重启后被新 Worker 覆盖、绝不能混入旧提交的正文。"); err != nil {
		t.Fatalf("overwrite draft: %v", err)
	}

	tool := newTestCommitChapterTool(s)
	// Mô phỏng Writer sau khi khởi động lại sinh ra tham số khác; khôi phục phải bỏ qua nó, dùng persistedArgs.
	args, _ := json.Marshal(map[string]any{
		"chapter":         1,
		"title":           "错误标题",
		"summary":         "错误的新摘要",
		"characters":      []string{"林墨"},
		"key_events":      []string{"错误事件"},
		"timeline_events": []domain.TimelineEvent{{Time: "夜晚", Event: "不应写入的新事件"}},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute replay: %v", err)
	}

	events, _ := s.World.LoadTimeline()
	if len(events) != 1 {
		t.Fatalf("timeline duplicated after replay, got %d: %+v", len(events), events)
	}
	changes, _ := s.World.LoadStateChanges()
	if len(changes) != 1 {
		t.Fatalf("state changes duplicated after replay, got %d: %+v", len(changes), changes)
	}
	ledger, _ := s.World.LoadForeshadowLedger()
	if len(ledger) != 1 {
		t.Fatalf("foreshadow duplicated after replay, got %d: %+v", len(ledger), ledger)
	}
	pending, _ := s.Signals.LoadPendingCommit()
	if pending != nil {
		t.Fatalf("pending commit should be cleared, got %+v", pending)
	}
	if cp := s.Checkpoints.LatestByStep(domain.ChapterScope(1), "commit"); cp == nil {
		t.Fatal("commit checkpoint should be written")
	}
	final, err := s.Drafts.LoadChapterText(1)
	if err != nil {
		t.Fatalf("LoadChapterText: %v", err)
	}
	if final != "# 第一章\n\n第一章正文，林墨遇到黑影并突破。" {
		t.Fatalf("recovery used overwritten draft: %q", final)
	}
}

func TestCommitChapterRecoversProgressMarkedWindowWithExactOutput(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	if err := s.Progress.StartChapter(1); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(1, "第一章终稿"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Title: "第一章", Summary: "摘要"}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if _, err := s.ChapterRecords.Accept(1, domain.ChapterOriginGenerated, "第一章终稿", domain.ChapterFacts{
		Title: "第一章", Summary: "摘要", KeyEvents: []string{"事件"},
	}, domain.StyleDelta{}); err != nil {
		t.Fatalf("SaveChapterRecord: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, 100, "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	want := json.RawMessage(`{"chapter":1,"committed":true,"recovered":"exact"}`)
	if err := s.Signals.SavePendingCommit(domain.PendingCommit{
		Chapter: 1,
		Stage:   domain.CommitStageProgressMarked,
		Output:  want,
	}); err != nil {
		t.Fatalf("SavePendingCommit: %v", err)
	}

	tool := newTestCommitChapterTool(s)
	got, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("Execute recovery: %v", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, got); err != nil {
		t.Fatalf("compact recovered output: %v", err)
	}
	if compact.String() != string(want) {
		t.Fatalf("recovered output = %s, want exact document %s", got, want)
	}
	if pending, err := s.Signals.LoadPendingCommit(); err != nil || pending != nil {
		t.Fatalf("pending commit should be cleared, pending=%+v err=%v", pending, err)
	}
	if cp := s.Checkpoints.LatestByStep(domain.ChapterScope(1), "commit"); cp == nil {
		t.Fatal("commit checkpoint should be repaired")
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if p.InProgressChapter != 0 {
		t.Fatalf("in-progress chapter should be cleared, got %d", p.InProgressChapter)
	}
}

// TestCommitChapterRejectsPolishWithoutDraftChange kiểm chứng: sau khi chương đã hoàn thành vào hàng đợi đánh bóng/viết lại,
// nếu nội dung và tiêu đề đều không thay đổi, commit_chapter phải từ chối bản viết lại rỗng.
// TestCommitChapterNonLayeredRecompletesAfterRework kiểm chứng sách không phân tầng sau khi hoàn bản qua reopen viết lại,
// sửa xong chương commit, hàng đợi drain cạn thì tự quay lại được complete (bù nhánh không phân tầng của việc xét hoàn thành sau drain).
func TestCommitChapterNonLayeredRecompletesAfterRework(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// Hai chương viết xong và hoàn thành. Chương 2 soạn đủ drafts/chapters, cho bản nộp viết lại.
	ch1 := "第一章原始正文。"
	ch2 := "第二章原始正文，用于模拟已提交终稿。"
	if err := s.Drafts.SaveFinalChapter(1, ch1); err != nil {
		t.Fatalf("SaveFinalChapter(1): %v", err)
	}
	if err := s.Drafts.SaveDraft(2, ch2); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, ch2); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	saveTestChapterRecord(t, s, 1, ch1)
	saveTestChapterRecord(t, s, 2, ch2)
	if err := s.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete(1): %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(ch2)), "", ""); err != nil {
		t.Fatalf("MarkChapterComplete(2): %v", err)
	}
	if err := s.Progress.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	// reopen chương 2 → phase về writing, PendingRewrites=[2], flow=rewriting
	if err := s.Progress.Reopen([]int{2}, "返工"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}

	// Nộp bản viết lại (bản nháp phải khác chính văn bản cuối mới được qua)
	if err := s.Drafts.SaveDraft(2, ch2+"\n\n返工新增段落。"); err != nil {
		t.Fatalf("SaveDraft (reworked): %v", err)
	}
	tool := newTestCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"title":      "第二章",
		"summary":    "返工后摘要",
		"characters": []string{"主角"},
		"key_events": []string{"清理"},
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute rework commit: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["book_complete"] != true {
		t.Errorf("book_complete = %v, want true", payload["book_complete"])
	}

	p, _ := s.Progress.Load()
	if p.Phase != domain.PhaseComplete {
		t.Errorf("phase = %s, want complete (phải tự kết thúc lại)", p.Phase)
	}
	if len(p.PendingRewrites) != 0 {
		t.Errorf("PendingRewrites = %v, want empty", p.PendingRewrites)
	}
}

// TestCommitChapterLayeredReopenRecompletesDespiteOpenThread kiểm chứng điểm kết: sách phân tầng qua reopen
// viết lại xong, dù compass vẫn còn mối tuyến chưa thu (viết lại có thể xáo trộn), sau khi drain cạn vẫn hoàn thành lại theo "cấu trúc đầy đủ" —
// không kẹt ở writing, trừ bỏ vòng lặp chết viết vượt biên ở cuối volume cuối (họ §6.5 / known_outline_exhaustion).
// Phản chứng: nếu nhánh reopen vẫn dùng layeredBookComplete tầng chất lượng, open thread trong ca này sẽ khiến nó trả false,
// book_complete thành giả, test tức thất bại.
func TestCommitChapterLayeredReopenRecompletesDespiteOpenThread(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// Một volume một cung hai chương, mở rộng hết
	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{
					{"title": "首章", "core_event": "起", "hook": "续"},
					{"title": "次章", "core_event": "承", "hook": "终"},
				},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}

	// Hai chương viết xong ghi đĩa và hoàn thành
	ch2 := "第二章原始正文，模拟已提交终稿。"
	for ch, body := range map[int]string{1: "第一章正文。", 2: ch2} {
		if err := s.Drafts.SaveDraft(ch, body); err != nil {
			t.Fatalf("SaveDraft %d: %v", ch, err)
		}
		if err := s.Drafts.SaveFinalChapter(ch, body); err != nil {
			t.Fatalf("SaveFinalChapter %d: %v", ch, err)
		}
		saveTestChapterRecord(t, s, ch, body)
		if err := s.Progress.MarkChapterComplete(ch, len([]rune(body)), "", ""); err != nil {
			t.Fatalf("MarkChapterComplete %d: %v", ch, err)
		}
	}
	if err := s.Progress.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	// Mô phỏng "viết lại xáo trộn mối tuyến": compass vẫn còn open thread chưa thu
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "主角归乡", OpenThreads: []string{"宿敌未除"}}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}

	// reopen chương 2 → nộp bản viết lại (bản nháp phải khác chính văn bản cuối mới được qua)
	if err := s.Progress.Reopen([]int{2}, "返工"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, ch2+"\n\n返工新增段落。"); err != nil {
		t.Fatalf("SaveDraft reworked: %v", err)
	}
	tool := newTestCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter": 2, "title": "第二章", "summary": "返工摘要", "characters": []string{"主角"}, "key_events": []string{"清理"},
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute rework commit: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if bc, _ := out["book_complete"].(bool); !bc {
		t.Error("sau khi drain cạn bản viết lại reopen phải hoàn thành lại theo cấu trúc đầy đủ (dù mối tuyến chưa thu)")
	}
	p, _ := s.Progress.Load()
	if p.Phase != domain.PhaseComplete {
		t.Errorf("phase = %s, want complete", p.Phase)
	}
	if p.ReopenedFromComplete {
		t.Error("sau khi hoàn thành lại ReopenedFromComplete phải được dọn")
	}
}

func TestCommitChapterRejectsPolishWithoutDraftChange(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// Mô phỏng chương 2 đã hoàn thành bình thường: drafts và chapters cùng nội dung.
	original := "第二章原始正文内容，用于模拟已提交终稿。"
	if err := s.Drafts.SaveDraft(2, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(original)), "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 2, Title: "第二章", Summary: "原摘要"}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	// Vào hàng đợi đánh bóng: Flow=Polishing, PendingRewrites=[2]
	if err := s.Progress.SetPendingRewrites([]int{2}, "测试打磨"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	tool := newTestCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"title":      "第二章",
		"summary":    "假装打磨了",
		"characters": []string{"主角"},
		"key_events": []string{"无改动"},
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected commit to be rejected when drafts equals final content")
	}

	// Viết thêm một bản nháp khác → phải được qua
	polished := original + "\n\n打磨后新增段落。"
	if err := s.Drafts.SaveDraft(2, polished); err != nil {
		t.Fatalf("SaveDraft (polished): %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute after real polish: %v", err)
	}
}

func TestCommitChapterAllowsTitleOnlyRewrite(t *testing.T) {
	s := store.NewStore(t.TempDir())
	s.SetLanguage("zh")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatal(err)
	}
	body := "正文无需修改，只有标题需要打磨。"
	if err := s.Drafts.SaveDraft(2, body); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveFinalChapter(2, body); err != nil {
		t.Fatal(err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 2, Title: "旧标题", Summary: "原摘要"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Checkpoints.AppendArtifacts(
		domain.ChapterScope(2), "commit", "chapters/02.md", "summaries/02.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(body)), "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "优化标题"); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{
		"chapter": 2, "title": "更准确的新标题", "summary": "原摘要",
		"characters": []string{"主角"}, "key_events": []string{"既有事件"},
	})
	if _, err := newTestCommitChapterTool(s).Execute(context.Background(), args); err != nil {
		t.Fatalf("title-only rewrite failed: %v", err)
	}
	summary, err := s.Summaries.LoadSummary(2)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary.Title != "更准确的新标题" {
		t.Fatalf("committed title = %+v", summary)
	}
	after := s.Checkpoints.LatestByStep(domain.ChapterScope(2), "commit")
	if after == nil || after.Seq <= before.Seq {
		t.Fatalf("title-only rewrite did not produce a new checkpoint: before=%+v after=%+v", before, after)
	}
	final, err := s.Drafts.LoadChapterText(2)
	if err != nil {
		t.Fatal(err)
	}
	// Tiêu đề do engine render vào dòng đầu nội dung, nên chỉ sửa tiêu đề thì file cũng đổi — cái đổi phải chỉ là dòng tiêu đề.
	if final != "# 第 2 章: 更准确的新标题\n\n"+body {
		t.Fatalf("title-only rewrite changed body: %q", final)
	}
}

// TestCommitChapterLayeredRejectsOutOfRangeChapter kiểm chứng ở chế độ phân tầng,
// commit có số chương vượt ra ngoài layered_outline phải thất bại cứng, chứ không phải slog.Warn cho qua.
// Đây là phanh vật lý chặn "writer trần trụi chạy một mạch sau khi phán định sai" (ca 《凡骨》ch204..347).
func TestCommitChapterLayeredRejectsOutOfRangeChapter(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// Dựng một layered_outline, chỉ 1 volume 1 cung 1 chương
	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{
					{"title": "首章", "core_event": "起", "hook": "续"},
				},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	// Commit của chương 2 vượt biên phải thất bại cứng
	if err := s.Drafts.SaveDraft(2, "越界章节正文，必须被拦下。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := newTestCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"title":      "第二章",
		"summary":    "越界章节",
		"characters": []string{"主角"},
		"key_events": []string{"不该被允许"},
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected commit to fail when chapter out of layered outline range")
	}

	// File chương không được ghi đĩa, Progress không được tiến
	if _, statErr := os.Stat(dir + "/chapters/02.md"); !os.IsNotExist(statErr) {
		t.Fatalf("chapter 2 should not be persisted, stat err=%v", statErr)
	}
	progress, _ := s.Progress.Load()
	if len(progress.CompletedChapters) != 0 {
		t.Fatalf("CompletedChapters should stay empty, got %v", progress.CompletedChapters)
	}
}

// TestCommitChapterLayeredAutoCompletesWhenDone kiểm chứng lớp fallback hoàn thành tất định của chế độ phân tầng:
// dàn ý mở rộng hết và viết xong + không còn cung khung + không có bản viết lại + phục bút đang hoạt động bằng không + mối tuyến la bàn đã thu,
// commit chương cuối tự đẩy Phase=Complete, không phụ thuộc kiến trúc sư chủ động gọi complete_book.
// Đây là bản sửa livelock do 9bf26a5 xóa auto-complete phân tầng gây ra (cuối volume cuối model chẳng append
// cũng chẳng complete → writer trần trụi chạy vượt biên vòng lặp chết).
func TestCommitChapterLayeredAutoCompletesWhenDone(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// Một volume một cung hai chương, mở rộng hết (không còn cung khung)
	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{
					{"title": "首章", "core_event": "起", "hook": "续"},
					{"title": "次章", "core_event": "承", "hook": "终"},
				},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}
	// Mối tuyến la bàn đã thu (OpenThreads rỗng)
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "主角归乡"}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	tool := newTestCommitChapterTool(s)
	commit := func(ch int) map[string]any {
		if err := s.Drafts.SaveDraft(ch, fmt.Sprintf("第 %d 章正文内容，用于测试确定性完结。", ch)); err != nil {
			t.Fatalf("SaveDraft %d: %v", ch, err)
		}
		args, _ := json.Marshal(map[string]any{
			"chapter": ch, "title": fmt.Sprintf("第%d章", ch), "summary": "摘要", "characters": []string{"主角"}, "key_events": []string{"事件"},
		})
		raw, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("Execute ch%d: %v", ch, err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("Unmarshal ch%d: %v", ch, err)
		}
		return out
	}

	// Chương 1: chưa viết xong, không được hoàn thành
	if bc, _ := commit(1)["book_complete"].(bool); bc {
		t.Fatal("viết xong chương 1 không được kích hoạt hoàn thành")
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseComplete {
		t.Fatal("viết xong chương 1 phase không được là complete")
	}

	// Chương 2 (chương cuối): phải tự hoàn thành
	if bc, _ := commit(2)["book_complete"].(bool); !bc {
		t.Fatal("viết xong chương cuối phải tự hoàn thành")
	}
	if p, _ := s.Progress.Load(); p.Phase != domain.PhaseComplete {
		t.Fatalf("expected phase=complete, got %s", p.Phase)
	}
}

// TestCommitChapterFinaleVolumeCompletesDespiteOpenThreads kiểm chứng toàn chuỗi volume kết cục:
// đã tuyên bố volume kết cục (append_volume kèm final:true) rồi —
//  1. commit chương cuối không hoàn thành: hoàn thành không được diễn ra trước bộ ba kết thúc cuối volume (xem xét cung/tóm tắt cung/tóm tắt volume),
//     đoạn kết phải qua cổng chất lượng của editor;
//  2. bộ ba đủ, tóm tắt volume ghi đĩa (điểm kích hoạt save_volume_summary) tức hoàn thành, không còn đòi
//     phục bút/mối tuyến cùng về không — nếu không sách có estimated_scale ước cao sẽ mãi không hoàn bản hợp lệ.
//
// Đối chiếu lẫn nhau với NoAutoCompleteWithOpenThreads bên dưới: cùng mang mối tuyến chưa thu, chưa tuyên bố thì không hoàn thành, đã tuyên bố thì hoàn thành.
func TestCommitChapterFinaleVolumeCompletesDespiteOpenThreads(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{{"title": "首章", "core_event": "起", "hook": "续"}},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}

	// Tuyên bố volume kết cục ở cuối volume: append_volume kèm final:true
	appendArgs, _ := json.Marshal(map[string]any{
		"type":   "append_volume",
		"reason": "长线可在一卷内收完，宣告收官卷",
		"content": map[string]any{
			"index": 2, "title": "终卷", "theme": "收束", "final": true,
			"arcs": []map[string]any{{
				"index": 1, "title": "收官弧", "goal": "回收所有长线",
				"chapters": []map[string]any{{"title": "终章", "core_event": "合", "hook": "终"}},
			}},
		},
	})
	raw, err := foundation.Execute(context.Background(), appendArgs)
	if err != nil {
		t.Fatalf("Execute append_volume: %v", err)
	}
	var appendOut map[string]any
	if err := json.Unmarshal(raw, &appendOut); err != nil {
		t.Fatalf("Unmarshal append result: %v", err)
	}
	if appendOut["final_volume"] != true {
		t.Fatalf("append_volume phải trả về dữ kiện final_volume=true, got %v", appendOut)
	}

	// Mối tuyến chưa thu (khi chưa tuyên bố, điều này sẽ chặn hoàn thành, xem test đối chiếu)
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "主角归乡", OpenThreads: []string{"宿敌未除"}}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	tool := newTestCommitChapterTool(s)
	commit := func(ch int) map[string]any {
		if err := s.Drafts.SaveDraft(ch, fmt.Sprintf("第 %d 章正文内容，用于收官卷完结测试。", ch)); err != nil {
			t.Fatalf("SaveDraft %d: %v", ch, err)
		}
		args, _ := json.Marshal(map[string]any{
			"chapter": ch, "title": fmt.Sprintf("第%d章", ch), "summary": "摘要", "characters": []string{"主角"}, "key_events": []string{"事件"},
		})
		raw, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("Execute ch%d: %v", ch, err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("Unmarshal ch%d: %v", ch, err)
		}
		return out
	}

	// Chương 1 (không phải chương cuối volume cuối): không được hoàn thành
	if bc, _ := commit(1)["book_complete"].(bool); bc {
		t.Fatal("volume kết cục chưa viết xong không được hoàn thành")
	}
	// Artefact tổng hợp của volume một phải hoàn thành trước, tóm tắt volume ở cuối volume hai mới là mục tiêu hiện hành của Router.
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 1, Scope: "arc", Verdict: "accept", Summary: "第一卷评审"}); err != nil {
		t.Fatalf("SaveReview v1: %v", err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Title: "弧一", Summary: "完成", KeyEvents: []string{"起"}}); err != nil {
		t.Fatalf("SaveArcSummary v1: %v", err)
	}
	if err := s.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Title: "卷一", Summary: "完成", KeyEvents: []string{"起"}}); err != nil {
		t.Fatalf("SaveVolumeSummary v1: %v", err)
	}
	// Chương 2 (chương cuối volume kết cục): bộ ba kết thúc cuối volume chưa đủ, hoàn thành không được trước editor xem xét/tóm tắt
	if bc, _ := commit(2)["book_complete"].(bool); bc {
		t.Fatal("khi commit chương cuối mà bộ ba chưa đủ, không được hoàn thành")
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseComplete {
		t.Fatal("hoàn thành không được diễn ra trước xem xét và tóm tắt cuối volume")
	}

	// Bộ ba kết thúc cuối volume: sau khi xem xét cung + tóm tắt cung ghi đĩa, tóm tắt volume (save_volume_summary) là điểm kích hoạt hoàn thành
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 2, Scope: "arc", Verdict: "accept", Summary: "末弧评审"}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 2, Arc: 1, Title: "收官弧", Summary: "收束", KeyEvents: []string{"终局"}}); err != nil {
		t.Fatalf("SaveArcSummary: %v", err)
	}
	volTool := NewSaveVolumeSummaryTool(s)
	volArgs, _ := json.Marshal(map[string]any{
		"volume": 2, "title": "终卷", "summary": "全卷收束", "key_events": []string{"终局"},
	})
	volRaw, err := volTool.Execute(context.Background(), volArgs)
	if err != nil {
		t.Fatalf("Execute save_volume_summary: %v", err)
	}
	var volOut map[string]any
	if err := json.Unmarshal(volRaw, &volOut); err != nil {
		t.Fatalf("Unmarshal volume summary result: %v", err)
	}
	if volOut["book_complete"] != true {
		t.Fatalf("tóm tắt volume ghi đĩa phải kích hoạt hoàn thành volume kết cục và hiển thị lại book_complete, got %v", volOut)
	}
	if p, _ := s.Progress.Load(); p.Phase != domain.PhaseComplete {
		t.Fatalf("expected phase=complete, got %s", p.Phase)
	}
}

// TestCommitChapterFinaleSkeletonArcBlocksCompletion kiểm chứng cổng cấu trúc của hoàn thành volume kết cục:
// khi volume kết cục vẫn còn cung khung (nội dung hoạch định chưa viết), dù bộ ba đã đủ cũng không được hoàn thành — đây là
// tuyến phòng thủ duy nhất chống "hoàn thành quá sớm" (điều kiện 2 của layeredStructurallyComplete).
func TestCommitChapterFinaleSkeletonArcBlocksCompletion(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	foundation := NewSaveFoundationTool(s)
	// Volume kết cục: cung một mở rộng 1 chương, cung hai vẫn là khung
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "终卷", "theme": "收束", "final": true,
			"arcs": []map[string]any{
				{"index": 1, "title": "收官弧", "goal": "收线",
					"chapters": []map[string]any{{"title": "首章", "core_event": "起", "hook": "续"}}},
				{"index": 2, "title": "骨架弧", "goal": "待展开", "estimated_chapters": 5},
			},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "归乡"}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	tool := newTestCommitChapterTool(s)
	if err := s.Drafts.SaveDraft(1, "第一章正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter": 1, "title": "第一章", "summary": "摘要", "characters": []string{"主角"}, "key_events": []string{"事件"},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Bộ ba đủ cũng không cho qua: cung khung nghĩa là nội dung hoạch định chưa viết
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 1, Scope: "arc", Verdict: "accept", Summary: "弧评审"}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Title: "收官弧", Summary: "s", KeyEvents: []string{"e"}}); err != nil {
		t.Fatalf("SaveArcSummary: %v", err)
	}
	volTool := NewSaveVolumeSummaryTool(s)
	volArgs, _ := json.Marshal(map[string]any{
		"volume": 1, "title": "终卷", "summary": "s", "key_events": []string{"e"},
	})
	if _, err := volTool.Execute(context.Background(), volArgs); err == nil || !strings.Contains(err.Error(), "hiện không có artefact") {
		t.Fatalf("khi cung khung chưa mở rộng thì volume chưa kết thúc, tóm tắt volume phải bị từ chối, got %v", err)
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseComplete {
		t.Fatal("cung khung chưa mở rộng, phase không được là complete")
	}
}

// TestCommitChapterLayeredNoAutoCompleteWithOpenThreads kiểm chứng tính bảo thủ: khi vẫn còn mối tuyến đang hoạt động
// thì dù chương viết đầy cũng không tự hoàn thành, trao quyền phán định "có tiếp tục hay không" lại cho kiến trúc sư.

// TestCommitChapterLayeredNoAutoCompleteWithOpenThreads kiểm chứng tính bảo thủ: khi vẫn còn mối tuyến đang hoạt động
// thì dù chương viết đầy cũng không tự hoàn thành, trao quyền phán định "có tiếp tục hay không" lại cho kiến trúc sư.
func TestCommitChapterLayeredNoAutoCompleteWithOpenThreads(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{{"title": "首章", "core_event": "起", "hook": "续"}},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}
	// Vẫn còn mối tuyến đang hoạt động chưa thu
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "主角归乡", OpenThreads: []string{"宿敌未除"}}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	if err := s.Drafts.SaveDraft(1, "唯一一章的正文，但长线未收束。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := newTestCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter": 1, "title": "第一章", "summary": "摘要", "characters": []string{"主角"}, "key_events": []string{"事件"},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseComplete {
		t.Fatal("khi mối tuyến đang hoạt động chưa thu thì không được tự hoàn thành")
	}
}
