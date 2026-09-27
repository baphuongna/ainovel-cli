package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSaveFoundationStopsOnCorruptProgress(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "progress.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"type": "premise", "content": "# 测试"})
	if _, err := NewSaveFoundationTool(st).Execute(context.Background(), args); err == nil {
		t.Fatal("progress hỏng phải thất bại trước khi ghi premise")
	}
	if _, err := os.Stat(filepath.Join(dir, "premise.md")); !os.IsNotExist(err) {
		t.Fatalf("lỗi gọi thất bại không được ghi premise, stat err=%v", err)
	}
}

func TestSaveFoundationPersistsPlanningTier(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type":    "premise",
		"content": "# 测试书名\n\n## 题材和基调\n测试",
		"scale":   "long",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("expected run meta to exist")
	}
	if meta.PlanningTier != domain.PlanningTierLong {
		t.Fatalf("expected planning tier %q, got %q", domain.PlanningTierLong, meta.PlanningTier)
	}
}

func TestSaveFoundationPremiseDoesNotOwnBookMetadata(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init(0); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	if err := store.Book.Save(domain.BookMetadata{Title: "长夜燃灯", Synopsis: "旧城熄灯后，少年追索失踪真相。"}); err != nil {
		t.Fatalf("Save book: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type": "premise",
		"content": `# 长夜燃灯

## 题材和基调
东方玄幻，冷硬求生。`,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	book, err := store.Book.Load()
	if err != nil {
		t.Fatalf("Load book: %v", err)
	}
	if book == nil || book.Title != "长夜燃灯" {
		t.Fatalf("premise không được sửa thông tin tác phẩm: %+v", book)
	}
}

func TestSaveFoundationCanRevisePremiseAfterOutline(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init(0); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatalf("UpdatePhase outline: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"type":    "premise",
		"content": "# 新书名\n\n修订后的故事前提。",
	})
	if _, err := NewSaveFoundationTool(st).Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	p, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if p.Phase != domain.PhaseOutline {
		t.Fatalf("phase = %s, want outline", p.Phase)
	}
	if cp := st.Checkpoints.LatestByStep(domain.GlobalScope(), "premise"); cp == nil {
		t.Fatal("premise sau sửa đổi nên sinh checkpoint")
	}
}

func TestSaveFoundationRejectsFullOutlineAfterComplete(t *testing.T) {
	tests := []struct {
		name    string
		typeArg string
		content any
	}{
		{
			name: "flat", typeArg: "outline",
			content: []map[string]any{{"chapter": 1, "title": "覆盖后", "core_event": "变化", "hook": "继续", "scenes": []string{}}},
		},
		{
			name: "layered", typeArg: "layered_outline",
			content: []map[string]any{{
				"index": 1, "title": "覆盖卷", "theme": "变化",
				"arcs": []map[string]any{{
					"index": 1, "title": "覆盖弧", "goal": "变化",
					"chapters": []map[string]any{{"title": "覆盖后", "core_event": "变化", "hook": "继续", "scenes": []string{}}},
				}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := store.NewStore(t.TempDir())
			if err := s.Init(); err != nil {
				t.Fatal(err)
			}
			if err := s.Progress.Init(1); err != nil {
				t.Fatal(err)
			}
			if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "原始标题"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.Progress.MarkComplete(); err != nil {
				t.Fatal(err)
			}

			args, _ := json.Marshal(map[string]any{"type": tt.typeArg, "content": tt.content})
			if _, err := NewSaveFoundationTool(s).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "đã hoàn thành") {
				t.Fatalf("ghi đè toàn bộ sau khi hoàn thành phải bị từ chối, err=%v", err)
			}
			outline, err := s.Outline.LoadOutline()
			if err != nil {
				t.Fatal(err)
			}
			if len(outline) != 1 || outline[0].Title != "原始标题" {
				t.Fatalf("lời gọi bị từ chối đã sửa outline: %+v", outline)
			}
			if _, err := os.Stat(filepath.Join(s.Dir(), "layered_outline.json")); tt.typeArg == "layered_outline" && !os.IsNotExist(err) {
				t.Fatalf("lời gọi bị từ chối đã ghi layered_outline.json: %v", err)
			}
		})
	}
}

func TestSaveFoundationOutlineClearsLayeredStateWhenDowngrading(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewSaveFoundationTool(store)

	layeredArgs, err := json.Marshal(map[string]any{
		"type":    "layered_outline",
		"content": `[{"index":1,"title":"第一卷","theme":"主题","arcs":[{"index":1,"title":"第一弧","goal":"目标","chapters":[{"chapter":1,"title":"第一章","core_event":"开局","hook":"继续"}]}]}]`,
		"scale":   "long",
	})
	if err != nil {
		t.Fatalf("Marshal layered args: %v", err)
	}
	rawResult, err := tool.Execute(context.Background(), layeredArgs)
	if err != nil {
		t.Fatalf("Execute layered outline: %v", err)
	}
	var layeredResult map[string]any
	if err := json.Unmarshal(rawResult, &layeredResult); err != nil {
		t.Fatalf("Unmarshal layered result: %v", err)
	}
	if layeredResult["dynamic_planning"] != true || layeredResult["outlined_chapters"] != float64(1) {
		t.Fatalf("kết quả layered nên báo số chương đã chi tiết hóa hiện tại: %#v", layeredResult)
	}
	if _, exists := layeredResult["chapters"]; exists {
		t.Fatalf("kết quả layered không được lộ ước tính dung lượng nội bộ thành chapters: %#v", layeredResult)
	}

	outlineArgs, err := json.Marshal(map[string]any{
		"type":    "outline",
		"content": `[{"chapter":1,"title":"第一章","core_event":"改为中篇","hook":"继续"}]`,
		"scale":   "mid",
	})
	if err != nil {
		t.Fatalf("Marshal outline args: %v", err)
	}
	if _, err := tool.Execute(context.Background(), outlineArgs); err != nil {
		t.Fatalf("Execute outline: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress == nil {
		t.Fatal("expected progress to exist")
	}
	if progress.Layered {
		t.Fatal("expected layered mode to be disabled")
	}
	if progress.CurrentVolume != 0 || progress.CurrentArc != 0 {
		t.Fatalf("expected volume/arc reset, got volume=%d arc=%d", progress.CurrentVolume, progress.CurrentArc)
	}

	volumes, err := store.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	if len(volumes) != 0 {
		t.Fatalf("expected layered outline cleared, got %d volumes", len(volumes))
	}

	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("expected run meta to exist")
	}
	if meta.PlanningTier != domain.PlanningTierMid {
		t.Fatalf("expected planning tier %q, got %q", domain.PlanningTierMid, meta.PlanningTier)
	}
}

func TestSaveFoundationAppendVolume(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewSaveFoundationTool(s)

	// Tạo layered_outline khởi tạo trước (quyển 1)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "第一卷", "theme": "起步",
			"arcs": []map[string]any{{
				"index": 1, "title": "首弧", "goal": "目标",
				"chapters": []map[string]any{{"title": "第一章", "core_event": "开局", "hook": "继续"}},
			}},
		}},
		"scale": "long",
	})
	if _, err := tool.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}

	// append_volume: thêm quyển 2
	appendArgs, _ := json.Marshal(map[string]any{
		"type":   "append_volume",
		"reason": "主线仍有多条长线未收束，需继续第二卷",
		"content": map[string]any{
			"index": 2, "title": "第二卷", "theme": "升级",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{{"title": "新章", "core_event": "推进", "hook": "钩子"}},
			}},
		},
	})
	res, err := tool.Execute(context.Background(), appendArgs)
	if err != nil {
		t.Fatalf("Execute append_volume: %v", err)
	}
	var result map[string]any
	json.Unmarshal(res, &result)
	if result["volume"] != float64(2) {
		t.Fatalf("expected volume=2, got %v", result["volume"])
	}

	// Kiểm chứng dàn ý có 2 quyển
	volumes, _ := s.Outline.LoadLayeredOutline()
	if len(volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(volumes))
	}
	if volumes[1].Title != "第二卷" {
		t.Fatalf("expected title '第二卷', got %q", volumes[1].Title)
	}

	// Lý do phán định cuối quyển phải vào kiểm toán phán định
	recs, _ := s.Decisions.Recent(1)
	if len(recs) != 1 || recs[0].Kind != "volume_end" || recs[0].Decider != "architect" {
		t.Fatalf("append_volume nên ghi một bản kiểm toán phán định volume_end, got %+v", recs)
	}
	if recs[0].Reason == "" || !strings.Contains(string(recs[0].Decision), `"append_volume"`) {
		t.Fatalf("bản ghi kiểm toán nên chứa reason và action, got %+v", recs[0])
	}
}

func TestSaveFoundationExpandArcCalibratesTarget(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(5); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "选择",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "已完成弧", Goal: "建立同盟", Chapters: []domain.OutlineEntry{{Title: "分裂", CoreEvent: "同盟意外破裂"}}},
			{Index: 2, Title: "旧标题", Goal: "维持同盟", EstimatedChapters: 4},
		},
	}}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "expand_arc", "volume": 1, "arc": 2,
		"content": map[string]any{
			"title": "裂盟之后",
			"goal":  "让分裂后的双方以不同选择推进同一主线",
			"chapters": []map[string]any{{
				"title": "各走一边", "core_event": "双方分别追索真相", "hook": "两条线索意外重合", "scenes": []string{"分道", "追索"},
			}},
		},
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute expand_arc: %v", err)
	}
	var facts map[string]any
	if err := json.Unmarshal(result, &facts); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if facts["title"] != "裂盟之后" || facts["goal"] != "让分裂后的双方以不同选择推进同一主线" {
		t.Fatalf("expected calibrated facts, got %+v", facts)
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	if got := volumes[0].Arcs[1]; got.Title != "裂盟之后" || got.Goal != "让分裂后的双方以不同选择推进同一主线" || len(got.Chapters) != 1 {
		t.Fatalf("unexpected expanded arc: %+v", got)
	}
}

func TestSaveFoundationAppendVolumeValidation(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewSaveFoundationTool(s)

	// Quyển khởi tạo
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "第一卷", "theme": "起步",
			"arcs": []map[string]any{{
				"index": 1, "title": "首弧", "goal": "目标",
				"chapters": []map[string]any{{"title": "第一章", "core_event": "开局", "hook": "继续"}},
			}},
		}},
		"scale": "long",
	})
	tool.Execute(context.Background(), layeredArgs)

	// Index không tăng → phải thất bại (kiểm tra cấu trúc)
	appendArgs, _ := json.Marshal(map[string]any{
		"type":   "append_volume",
		"reason": "测试理由",
		"content": map[string]any{
			"index": 1, "title": "重复 Index", "theme": "x",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{{"title": "章", "core_event": "事件", "hook": "钩子"}},
			}},
		},
	})
	_, err := tool.Execute(context.Background(), appendArgs)
	if err == nil {
		t.Fatal("expected error when appending volume with non-increasing index")
	}
}

// TestSaveFoundationAppendVolumeRejectsAfterComplete kiểm chứng sau Phase=Complete không cho append_volume.
// Thay thế ngữ nghĩa cũ "quyển Final từ chối thêm mới" (trường Final đã xóa).
func TestSaveFoundationAppendVolumeRejectsAfterComplete(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	appendArgs, _ := json.Marshal(map[string]any{
		"type":   "append_volume",
		"reason": "测试理由",
		"content": map[string]any{
			"index": 1, "title": "尝试续写", "theme": "x",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧", "goal": "g",
				"chapters": []map[string]any{{"title": "章", "core_event": "e", "hook": "h"}},
			}},
		},
	})
	if _, err := tool.Execute(context.Background(), appendArgs); err == nil {
		t.Fatal("expected error when appending after Phase=Complete")
	}
}

func TestSaveFoundationUpdateCompass(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "update_compass",
		"content": map[string]any{
			"ending_direction": "主角面对最终抉择",
			"open_threads":     []string{"线索A", "关系B"},
			"estimated_scale":  "预计 4-6 卷",
		},
	})
	_, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute update_compass: %v", err)
	}

	compass, err := s.Outline.LoadCompass()
	if err != nil {
		t.Fatalf("LoadCompass: %v", err)
	}
	if compass == nil || compass.EndingDirection != "主角面对最终抉择" {
		t.Fatalf("unexpected compass: %+v", compass)
	}
	if len(compass.OpenThreads) != 2 {
		t.Fatalf("expected 2 open threads, got %d", len(compass.OpenThreads))
	}
}

func TestSaveFoundationUpdateCompassOverridesLastUpdated(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		CompletedChapters: []int{1, 2, 3, 5, 4}, // xáo trộn thứ tự, kiểm chứng lấy max chứ không phải len
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "update_compass",
		"content": map[string]any{
			"ending_direction": "主角面对最终抉择",
			"open_threads":     []string{"线索A"},
			"last_updated":     0, // LLM thường quên điền hoặc để 0
		},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute update_compass: %v", err)
	}

	compass, err := s.Outline.LoadCompass()
	if err != nil {
		t.Fatalf("LoadCompass: %v", err)
	}
	if compass.LastUpdated != 5 {
		t.Fatalf("expected LastUpdated=5 (max of CompletedChapters), got %d", compass.LastUpdated)
	}
}

func TestSaveFoundationUpdateCompassRequiresDirection(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type":    "update_compass",
		"content": map[string]any{"estimated_scale": "3 卷"},
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error when ending_direction is empty")
	}
}

func TestSaveFoundationAcceptsDirectJSONArrayContent(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type": "outline",
		"content": []map[string]any{
			{
				"chapter":    1,
				"title":      "第一章",
				"core_event": "主角登场",
				"hook":       "继续",
				"scenes":     []string{"场景一", "场景二"},
			},
		},
		"scale": "short",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	outline, err := store.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(outline) != 1 || outline[0].Title != "第一章" {
		t.Fatalf("unexpected outline: %+v", outline)
	}
}

// completeBookSetup dựng một Store tối tiểu ở giai đoạn writing, tổng 2 chương, dùng cho loạt kiểm thử complete_book
// Kiểm tra tầng công cụ (toàn bộ đếm được, vào code không vào prompt): progress đã khởi tạo,
// PendingRewrites rỗng, viết xong ít nhất một chương, dàn ý không còn chương chưa viết.
func completeBookSetup(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)
	return s
}

func TestSaveFoundationCompleteBookPushesPhaseComplete(t *testing.T) {
	s := completeBookSetup(t)
	for ch := 1; ch <= 2; ch++ {
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{},
		"reason": "两章大纲全部写完，终局命题已回答",
	})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute complete_book: %v", err)
	}
	var result map[string]any
	_ = json.Unmarshal(res, &result)
	if result["book_complete"] != true {
		t.Fatalf("expected book_complete=true, got %+v", result)
	}
	if result["phase"] != string(domain.PhaseComplete) {
		t.Fatalf("expected phase=complete, got %v", result["phase"])
	}
	progress, _ := s.Progress.Load()
	if progress.Phase != domain.PhaseComplete {
		t.Fatalf("expected progress.Phase=complete, got %s", progress.Phase)
	}

	// Lý do phán định hoàn thành phải vào kiểm toán phán định (snapshot sự kiện lấy thời điểm phán định)
	recs, _ := s.Decisions.Recent(1)
	if len(recs) != 1 || recs[0].Kind != "volume_end" || recs[0].Decider != "architect" {
		t.Fatalf("complete_book nên ghi một bản kiểm toán phán định volume_end, got %+v", recs)
	}
	if recs[0].Reason == "" || !strings.Contains(string(recs[0].Decision), `"complete_book"`) {
		t.Fatalf("bản ghi kiểm toán nên chứa reason và action, got %+v", recs[0])
	}
	if !strings.Contains(string(recs[0].Facts), `"completed_chapters":2`) {
		t.Fatalf("facts kiểm toán nên chứa tiến độ tại thời điểm phán định, got %s", recs[0].Facts)
	}
}

// TestSaveFoundationCompleteBookRejectsZeroChapters tái hiện sự cố thật: quy hoạch vừa ghi xuống đĩa
// phase tự lật sang writing, mô hình yếu tiện tay gọi nhầm complete_book — chưa viết chương nào phải từ chối,
// nếu không cả cuốn sách bị bỏ qua (0/68 chương đánh dấu hoàn thành).
func TestSaveFoundationCompleteBookRejectsZeroChapters(t *testing.T) {
	s := completeBookSetup(t)
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{},
		"reason": "测试理由",
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("complete_book khi chưa viết chương nào phải bị từ chối")
	}
	progress, _ := s.Progress.Load()
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("phase nên giữ writing, got %s", progress.Phase)
	}
}

// TestSaveFoundationCompleteBookRejectsOpenThreads canh phòng lớp công cụ của "tuyến dài chưa khép không thể hoàn thành"
// tuyến phòng thủ: khớp OpenThreads tức "phải khép mới được kết thúc", nhưng thực đo kiến trúc sư sẽ miễn trừ tuyến chưa khép thành
// "tác giả cố tình để trống" mà hoàn thành thẳng (kịch bản tiếp viết sách đã hoàn thành nhập vào, nhu cầu tiếp viết của người dùng bị khóa cứng bởi quy tắc hoàn thành). Miễn trừ phải
// ghi xuống đĩa tường minh — update_compass dọn open_threads xong mới được hoàn thành.
func TestSaveFoundationCompleteBookRejectsOpenThreads(t *testing.T) {
	s := completeBookSetup(t)
	for ch := 1; ch <= 2; ch++ {
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}
	if err := s.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: "潜在终局", OpenThreads: []string{"八十年大限走向", "精变重逢可能"},
	}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{}, "reason": "主线已闭合",
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "open_threads") {
		t.Fatalf("open_threads không rỗng nên từ chối hoàn thành và chỉ dẫn update_compass, được: %v", err)
	}
	if p, _ := s.Progress.Load(); p.Phase != domain.PhaseWriting {
		t.Fatalf("phase nên giữ writing, được %s", p.Phase)
	}
	// Khép tường minh ghi xuống đĩa (update_compass dọn open_threads) rồi mới cho qua.
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "终局已达成"}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("tuyến dài dọn xong thì hoàn thành nên cho qua: %v", err)
	}
}

// TestSaveFoundationCompleteBookRejectsUnwrittenChapters khi dàn ý còn chương chưa viết
// thì không thể hoàn thành (đường chính quy khép sớm là quyển kết thúc final).
func TestSaveFoundationCompleteBookRejectsUnwrittenChapters(t *testing.T) {
	s := completeBookSetup(t)
	if err := s.Progress.MarkChapterComplete(1, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{},
		"reason": "测试理由",
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("complete_book khi dàn ý còn chương chưa viết phải bị từ chối")
	}
	if !strings.Contains(err.Error(), "final") {
		t.Fatalf("câu từ chối nên dẫn đường quyển kết thúc final, got %v", err)
	}
	progress, _ := s.Progress.Load()
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("phase nên giữ writing, got %s", progress.Phase)
	}
}

func TestSaveFoundationCompleteBookRejectsBeforeWriting(t *testing.T) {
	// Gọi nhầm complete_book ở giai đoạn quy hoạch phải bị từ chối, nếu không sẽ bỏ qua thẳng toàn bộ việc viết.
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhasePremise)
	_ = s.Progress.UpdatePhase(domain.PhaseOutline)
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{},
		"reason": "测试理由",
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected error when phase != writing")
	}
	progress, _ := s.Progress.Load()
	if progress.Phase != domain.PhaseOutline {
		t.Fatalf("phase should remain outline, got %s", progress.Phase)
	}
}

// TestSaveFoundationVolumeEndRequiresReason ba chọn một cuối quyển phải kèm lý do phán định —
// nó là phán đoán ngữ nghĩa nặng nhất toàn sách, lý do phải thành sự kiện kiểm toán thay vì rải rác trong log phiên.
func TestSaveFoundationVolumeEndRequiresReason(t *testing.T) {
	s := completeBookSetup(t)
	tool := NewSaveFoundationTool(s)
	for _, typ := range []string{"append_volume", "complete_book"} {
		args, _ := json.Marshal(map[string]any{
			"type": typ, "content": map[string]any{},
		})
		_, err := tool.Execute(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "reason") {
			t.Fatalf("%s thiếu reason phải bị từ chối và câu từ nhắc đến reason, got %v", typ, err)
		}
	}
	if recs, _ := s.Decisions.Recent(1); len(recs) != 0 {
		t.Fatalf("lời gọi bị từ chối không nên sinh bản ghi kiểm toán, got %+v", recs)
	}
}

func TestSaveFoundationCompleteBookRejectsWithPendingRewrites(t *testing.T) {
	s := completeBookSetup(t)
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "尾章节奏过快"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{},
		"reason": "测试理由",
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected error when PendingRewrites non-empty")
	}
	progress, _ := s.Progress.Load()
	if progress.Phase == domain.PhaseComplete {
		t.Fatalf("phase should not be Complete with PendingRewrites: %s", progress.Phase)
	}
}
