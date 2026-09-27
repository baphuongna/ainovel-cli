package imp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ChapterCommitter là interface tối thiểu cần để công bố chương, do tools.CommitChapterTool đáp ứng.
// Tái sử dụng saga PendingCommit, checkpoint và kiểm tra idempotent chương hoàn thành của nó, không
// copy bộ logic nộp thứ hai (RFC §12.3).
type ChapterCommitter interface {
	Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// publishFoundation công bố Foundation theo thứ tự dependency chính thức, trùng thứ tự ghi của
// Architect trường thiên (RFC §12.2). Công bố lặp lại nội dung giống nhau là idempotent (Store ghi
// đè cùng nội dung + checkpoint khử trùng).
func publishFoundation(st *store.Store, f *Foundation) error {
	// Đối soát xung đột trước khi công bố: artifact chính thức đã tồn tại mà khác thì từ chối ghi
	// đè (§12.2 / bất biến 6). Nội dung giống nhau thì ghi tiếp theo kiểu idempotent (Store ghi đè
	// cùng nội dung + checkpoint khử trùng).
	if err := checkFoundationConflicts(st, f); err != nil {
		return err
	}
	if err := st.Book.Save(f.Book); err != nil {
		return fmt.Errorf("book: %w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "book", "meta/book.json"); err != nil {
		return fmt.Errorf("checkpoint book: %w", err)
	}
	if err := st.RunMeta.SetPlanningTier(f.PlanningTier); err != nil {
		return fmt.Errorf("planning tier: %w", err)
	}
	// premise
	if err := st.Outline.SavePremise(f.Premise); err != nil {
		return fmt.Errorf("premise: %w", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhasePremise); err != nil {
		return fmt.Errorf("phase premise: %w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "premise", "premise.md"); err != nil {
		return fmt.Errorf("checkpoint premise: %w", err)
	}
	// characters
	if err := st.Characters.Save(f.Characters); err != nil {
		return fmt.Errorf("characters: %w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "characters", "characters.json"); err != nil {
		return fmt.Errorf("checkpoint characters: %w", err)
	}
	// world rules
	if err := st.World.SaveWorldRules(f.WorldRules); err != nil {
		return fmt.Errorf("world_rules: %w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "world_rules", "world_rules.json"); err != nil {
		return fmt.Errorf("checkpoint world_rules: %w", err)
	}
	// layered outline là nguồn duy nhất, Store dựng lại flat outline đồng bộ.
	if err := st.Outline.SaveLayeredOutline(f.Volumes); err != nil {
		return fmt.Errorf("layered outline: %w", err)
	}
	// Tiến độ giai đoạn dàn ý là căn cứ engine tính lại routing (sức chứa chương/phân tầng/tập-cung
	// hiện tại), ghi thất bại sẽ để lại trạng thái đã công bố không nhất quán, phải phơi ra chứ không
	// được nuốt (RFC §12.2).
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		return fmt.Errorf("phase outline: %w", err)
	}
	if err := st.Progress.SetTotalChapters(domain.EstimatedChapterCapacity(f.Volumes)); err != nil {
		return fmt.Errorf("total chapters: %w", err)
	}
	if err := st.Progress.SetLayered(true); err != nil {
		return fmt.Errorf("set layered: %w", err)
	}
	if len(f.Volumes) > 0 && len(f.Volumes[0].Arcs) > 0 {
		if err := st.Progress.UpdateVolumeArc(f.Volumes[0].Index, f.Volumes[0].Arcs[0].Index); err != nil {
			return fmt.Errorf("volume arc: %w", err)
		}
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "layered_outline", "layered_outline.json"); err != nil {
		return fmt.Errorf("checkpoint layered outline: %w", err)
	}
	// compass
	if err := st.Outline.SaveCompass(f.Compass); err != nil {
		return fmt.Errorf("compass: %w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "compass", "meta/compass.json"); err != nil {
		return fmt.Errorf("checkpoint compass: %w", err)
	}
	// Toàn bộ ghi chính thức của Foundation nhập đã thành công, có thể vào writing tường minh.
	// Không tái dùng FoundationMissing của luồng sáng tác thường: lượt nhập cho phép world_rules
	// rỗng, coi "giá trị rỗng hợp lệ" là thiếu sẽ khiến tiến độ kẹt mãi ở outline, sau đó StartChapter
	// bị cổng chặn giai đoạn từ chối.
	p, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress: %w", err)
	}
	if p == nil {
		return fmt.Errorf("load progress: progress chưa khởi tạo")
	}
	if p.Phase != domain.PhaseWriting && p.Phase != domain.PhaseComplete {
		if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
			return fmt.Errorf("phase writing: %w", err)
		}
	}
	return nil
}

// checkFoundationConflicts kiểm tra tính nhất quán giữa Foundation sắp công bố và artifact chính
// thức sẵn có: sẵn có rỗng coi là công bố lần đầu; giống nhau coi là idempotent; khác nhau thì báo
// xung đột, không ghi đè (RFC §12.2 / bất biến 6). compass và dàn ý phẳng do dàn ý phân tầng phái
// sinh, phân tầng khớp thì phái sinh khớp, nên không kiểm tra riêng artifact phái sinh. Lỗi đọc
// không được nuốt thành "tệp không tồn tại": loader của store trả (giá trị 0, nil) khi thiếu, nên
// mọi lỗi khác nil đều là lỗi thật (hỏng/quyền/JSON bất hợp lệ), nếu coi là rỗng mà tiếp tục sẽ ghi
// đè artifact chính thức không đọc được (RFC §12.2).
func checkFoundationConflicts(st *store.Store, f *Foundation) error {
	wantBook := f.Book.Normalized()
	book, err := st.Book.Load()
	if err != nil {
		return fmt.Errorf("đọc book chính thức: %w", err)
	}
	if book != nil && !jsonEqual(book, wantBook) {
		return fmt.Errorf("book chính thức xung đột với tổng hợp nhập (đã tồn tại phiên bản khác), từ chối ghi đè")
	}
	cur, err := st.Outline.LoadPremise()
	if err != nil {
		return fmt.Errorf("đọc premise chính thức: %w", err)
	}
	if cur != "" && cur != f.Premise {
		return fmt.Errorf("premise chính thức xung đột với tổng hợp nhập (đã tồn tại phiên bản khác), từ chối ghi đè")
	}
	chars, err := st.Characters.Load()
	if err != nil {
		return fmt.Errorf("đọc characters chính thức: %w", err)
	}
	if len(chars) > 0 && !jsonEqual(chars, f.Characters) {
		return fmt.Errorf("characters chính thức xung đột với tổng hợp nhập (đã tồn tại phiên bản khác), từ chối ghi đè")
	}
	rules, err := st.World.LoadWorldRules()
	if err != nil {
		return fmt.Errorf("đọc world_rules chính thức: %w", err)
	}
	if len(rules) > 0 && !jsonEqual(rules, f.WorldRules) {
		return fmt.Errorf("world_rules chính thức xung đột với tổng hợp nhập (đã tồn tại phiên bản khác), từ chối ghi đè")
	}
	layered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return fmt.Errorf("đọc layered_outline chính thức: %w", err)
	}
	if len(layered) > 0 && !jsonEqual(layered, f.Volumes) {
		return fmt.Errorf("layered_outline chính thức xung đột với tổng hợp nhập (đã tồn tại phiên bản khác), từ chối ghi đè")
	}
	return nil
}

// jsonEqual so hai giá trị có tương đương hay không theo bytes JSON đã chuẩn hóa.
func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// publishChapter tái dùng commit_chapter để công bố một chương; chương đã hoàn thành được bỏ qua bởi kiểm tra idempotent của nó (RFC §12.3).
func publishChapter(ctx context.Context, st *store.Store, commit ChapterCommitter, chapter int, content string, f ImportedChapterFacts) error {
	completed, err := st.Progress.IsChapterCompleted(chapter)
	if err != nil {
		return fmt.Errorf("load progress ch%d: %w", chapter, err)
	}
	if completed {
		// Sập có thể rơi vào giữa MarkChapterComplete và ClearPendingCommit: pending_commit sót lại
		// trỏ vào chương này. Nhảy qua trực tiếp sẽ né nhánh dọn dẹp mà công cụ commit chuẩn bị riêng
		// cho cửa sổ này (bổ checkpoint + dọn sót), chương kế Execute sẽ từ chối với "tồn tại lệnh nộp
		// chương chưa khôi phục", lượt nhập mỗi lần chạy lại đều chết tại cùng một chỗ và phải xóa tay
		// meta/pending_commit.json mới mở khóa. Gặp sót thì vẫn đi đường idempotent của công cụ để dọn.
		pending, err := st.Signals.LoadPendingCommit()
		if err != nil {
			return fmt.Errorf("load pending commit ch%d: %w", chapter, err)
		}
		if pending != nil && pending.Chapter == chapter {
			raw, err := json.Marshal(commitArgs(chapter, f))
			if err != nil {
				return fmt.Errorf("marshal commit ch%d: %w", chapter, err)
			}
			if _, err := commit.Execute(ctx, raw); err != nil {
				return fmt.Errorf("commit ch%d: %w", chapter, err)
			}
		}
		return nil
	}
	if err := st.Drafts.SaveDraft(chapter, content); err != nil {
		return fmt.Errorf("save draft ch%d: %w", chapter, err)
	}
	if err := st.Progress.StartChapter(chapter); err != nil {
		return fmt.Errorf("start ch%d: %w", chapter, err)
	}
	raw, err := json.Marshal(commitArgs(chapter, f))
	if err != nil {
		return fmt.Errorf("marshal commit ch%d: %w", chapter, err)
	}
	if _, err := commit.Execute(ctx, raw); err != nil {
		return fmt.Errorf("commit ch%d: %w", chapter, err)
	}
	return nil
}

// commitArgs ánh xạ sự thực từng chương thành tham số vào của commit_chapter.
func commitArgs(chapter int, f ImportedChapterFacts) map[string]any {
	keyEvents := f.KeyEvents
	if len(keyEvents) == 0 {
		keyEvents = []string{f.CoreEvent} // core_event đã kiểm tra khác rỗng
	}
	args := map[string]any{
		"chapter":         chapter,
		"title":           f.Title,
		"summary":         f.Summary,
		"characters":      f.Characters,
		"key_events":      keyEvents,
		"hook_type":       f.HookType,
		"dominant_strand": f.DominantStrand,
	}
	if len(f.TimelineEvents) > 0 {
		args["timeline_events"] = f.TimelineEvents
	}
	if len(f.ForeshadowUpdates) > 0 {
		args["foreshadow_updates"] = f.ForeshadowUpdates
	}
	if len(f.RelationshipChanges) > 0 {
		args["relationship_changes"] = f.RelationshipChanges
	}
	if len(f.StateChanges) > 0 {
		args["state_changes"] = f.StateChanges
	}
	return args
}

// isPublished kiểm tra trạng thái chính thức đã phản ánh trọn vẹn lượt nhập chưa: Foundation đã
// ghi và số chương hoàn thành đạt kỳ vọng. Chỉ đối soát artifact mà lượt nhập thật sự sản xuất —
// book, premise, dàn ý phẳng phủ đủ chương, chương hoàn thành — chứ không tái dùng
// FoundationMissing(): cái sau là cổng chặn "ghi được" của luồng sáng tác thường, sẽ phán nhầm
// world_rules rỗng hợp lệ là chưa xong, khiến đối soát công bố không bao giờ hội tụ (RFC §12.3).
func isPublished(st *store.Store, expected int) (bool, error) {
	if expected == 0 {
		return false, nil
	}
	book, err := st.Book.Load()
	if err != nil {
		return false, fmt.Errorf("đọc book chính thức: %w", err)
	}
	if book == nil {
		return false, nil
	}
	p, err := st.Outline.LoadPremise()
	if err != nil {
		return false, fmt.Errorf("đọc premise chính thức: %w", err)
	}
	if p == "" {
		return false, nil
	}
	o, err := st.Outline.LoadOutline()
	if err != nil {
		return false, fmt.Errorf("đọc outline chính thức: %w", err)
	}
	if len(o) < expected {
		return false, nil
	}
	prog, err := st.Progress.Load()
	if err != nil {
		return false, fmt.Errorf("đọc progress chính thức: %w", err)
	}
	return prog != nil && len(prog.CompletedChapters) >= expected, nil
}
