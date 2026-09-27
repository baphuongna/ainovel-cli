package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/chapterfacts"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/revision"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CommitChapterTool nộp chương: tải nội dung → lưu chính văn bản cuối → tạo tóm tắt → cập nhật trạng thái → cập nhật tiến độ.
type CommitChapterTool struct {
	store      *store.Store
	styleStats *StyleStatsIndex
}

// NewCommitChapterTool tạo công cụ nộp. styleStats phải dùng chung với novel_context,
// bảo đảm sau khi thêm mới, viết lại và khôi phục sẽ refresh cùng một chỉ mục thống kê.
func NewCommitChapterTool(store *store.Store, styleStats *StyleStatsIndex) *CommitChapterTool {
	if styleStats == nil {
		// bất biến đấu nối fail-fast: Host luôn truyền styleStats dùng chung khi xây dựng
		// (host.go NewStyleStatsIndex), nil chỉ có thể là lỗi đấu nối — thà crash ngay lúc khởi động.
		panic("tools: NewCommitChapterTool requires StyleStatsIndex")
	}
	return &CommitChapterTool{store: store, styleStats: styleStats}
}

func (t *CommitChapterTool) chapterStyleDelta(chapter int) (domain.StyleDelta, error) {
	record, err := t.store.ChapterRecords.Load(chapter)
	if err != nil || record == nil {
		return domain.StyleDelta{}, err
	}
	return record.StyleDelta, nil
}

// commitOutput nhúng các trường mở rộng lên trên domain.CommitResult, giữ cho package domain không phụ thuộc rules.
// Vì trường nhúng sẽ được JSON marshaler nâng lên (promoted), kết quả tuần tự hóa tương đương cấu trúc phẳng.
type commitOutput struct {
	domain.CommitResult
	RuleViolations []rules.Violation `json:"rule_violations,omitempty"`
}

// commitArgs là payload cấu trúc đã chuẩn hóa của saga nộp chương. Lần thực hiện đầu tiên ghi nó cùng bản chụp nội dung vào
// PendingCommit; khôi phục sau crash luôn phát lại ý định đã đóng băng này, bỏ qua tham số và bản nháp do Worker mới sinh ra.
type commitArgs struct {
	Chapter int `json:"chapter"`
	domain.ChapterFacts
}

func (t *CommitChapterTool) Name() string { return "commit_chapter" }
func (t *CommitChapterTool) Description() string {
	return "Nộp chính văn bản cuối của chương. Tải nội dung bản nháp lưu thành chính văn bản cuối, cập nhật timeline, phục bút, quan hệ, trạng thái nhân vật và tiến độ." +
		"Trả về dữ kiện có cấu trúc: next_chapter / review_required / arc_end / volume_end / needs_expansion / book_complete / flow v.v."
}
func (t *CommitChapterTool) Label() string { return "Nộp chương" }

// Công cụ ghi (saga khôi phục được xuyên miền: payload đầy đủ → chính văn bản cuối/trạng thái → tiến độ → checkpoint), cấm đồng thời.
func (t *CommitChapterTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *CommitChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *CommitChapterTool) StrictSchema() bool                     { return true }

func (t *CommitChapterTool) Schema() map[string]any {
	props := []schema.Prop{schema.Property("chapter", schema.Int("số chương")).Required()}
	props = append(props, chapterfacts.Properties(true)...)
	return schema.Object(props...)
}

func (t *CommitChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var requested commitArgs
	if err := json.Unmarshal(args, &requested); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if requested.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	existingPending, err := t.store.Signals.LoadPendingCommit()
	if err != nil {
		return nil, fmt.Errorf("load pending commit: %w: %w", errs.ErrStoreRead, err)
	}
	if existingPending != nil && existingPending.Chapter != requested.Chapter {
		return nil, fmt.Errorf("tồn tại bản nộp chương chưa khôi phục: chương %d (giai đoạn %s), hãy khôi phục hoặc nộp lại chương đó trước: %w", existingPending.Chapter, existingPending.Stage, errs.ErrToolConflict)
	}
	if existingPending != nil {
		switch existingPending.Stage {
		case domain.CommitStageStarted, domain.CommitStageStateApplied, domain.CommitStageProgressMarked, domain.CommitStageSignalSaved:
		default:
			return nil, fmt.Errorf("giai đoạn pending commit không hợp lệ: %q: %w", existingPending.Stage, errs.ErrToolConflict)
		}
	}

	a := requested
	if existingPending != nil && existingPending.Stage != domain.CommitStageProgressMarked && existingPending.Stage != domain.CommitStageSignalSaved {
		if len(existingPending.Payload) == 0 {
			return nil, fmt.Errorf("chương %d tồn tại bản nộp dở dang của phiên bản cũ nhưng thiếu payload có thể phát lại; từ chối ghi đè bằng tham số mới sinh, hãy khôi phục từ checkpoint gần nhất hoặc tự kiểm tra meta/pending_commit.json: %w",
				existingPending.Chapter, errs.ErrToolConflict)
		}
		if err := json.Unmarshal(existingPending.Payload, &a); err != nil {
			return nil, fmt.Errorf("decode pending commit payload: %w: %w", errs.ErrStoreRead, err)
		}
		if a.Chapter != existingPending.Chapter {
			return nil, fmt.Errorf("chapter của pending commit payload không khớp: bản ghi=%d payload=%d: %w", existingPending.Chapter, a.Chapter, errs.ErrToolConflict)
		}
	}

	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil {
		return nil, fmt.Errorf("progress chưa được khởi tạo: %w", errs.ErrToolPrecondition)
	}
	completed := slices.Contains(progress.CompletedChapters, a.Chapter)
	if existingPending != nil && (existingPending.Stage == domain.CommitStageProgressMarked || existingPending.Stage == domain.CommitStageSignalSaved) {
		if !completed {
			return nil, fmt.Errorf("pending commit đã đến %s nhưng progress chưa đánh dấu chương %d hoàn thành: %w", existingPending.Stage, a.Chapter, errs.ErrToolConflict)
		}
		return t.finishPendingCommit(*existingPending, progress)
	}
	if existingPending == nil || existingPending.Stage == domain.CommitStageStarted {
		if err := t.validateCommitArgs(a); err != nil {
			// Phiên bản cũ có thể đã để lại bản nộp đóng băng trước khi phát hiện dữ kiện viết lại không hợp lệ. Giữ lại payload
			// bất biến này chỉ khiến mỗi lần thử lại lặp lại cùng một lỗi, vì vậy chủ động bỏ đóng băng,
			// để Writer có thể sửa tham số rồi nộp lại; nội dung và bản ghi chương đều không bị đổi ở đây.
			if existingPending != nil && existingPending.Rewrite &&
				(errors.Is(err, errs.ErrToolArgs) || errors.Is(err, errs.ErrToolPrecondition)) {
				if clearErr := t.store.Signals.ClearPendingCommit(); clearErr != nil {
					return nil, fmt.Errorf("kiểm tra bản nộp viết lại thất bại (%v), đồng thời dọn bản nộp đóng băng cũng thất bại: %w: %w", err, errs.ErrStoreWrite, clearErr)
				}
				return nil, fmt.Errorf("bản nộp viết lại do phiên bản cũ để lại không qua được kiểm tra, đã bỏ đóng băng; hãy sửa rồi nộp lại: %w", err)
			}
			return nil, err
		}
	}

	if existingPending != nil && existingPending.Rewrite {
		if !completed {
			return nil, fmt.Errorf("bản nộp viết lại yêu cầu chương %d đã có chính văn bản cuối: %w", a.Chapter, errs.ErrToolConflict)
		}
		return t.executeRewriteCommit(a, progress, *existingPending, true)
	}
	if existingPending == nil && completed {
		if slices.Contains(progress.PendingRewrites, a.Chapter) {
			content, err := t.validateRewriteDraft(a.Chapter, a.Title, progress)
			if err != nil {
				return nil, err
			}
			payload, err := json.Marshal(a)
			if err != nil {
				return nil, fmt.Errorf("marshal rewrite payload: %w", err)
			}
			now := time.Now().Format(time.RFC3339)
			mode := "rewrite"
			if progress.Flow == domain.FlowPolishing {
				mode = "polish"
			}
			pending := domain.PendingCommit{Chapter: a.Chapter, Stage: domain.CommitStageStarted,
				Rewrite: true, RewriteMode: mode, Payload: payload, DraftContent: content,
				Summary: a.Summary, HookType: a.HookType,
				DominantStrand: a.DominantStrand, StartedAt: now, UpdatedAt: now}
			if err := t.store.Signals.SavePendingCommit(pending); err != nil {
				return nil, fmt.Errorf("save rewrite pending commit: %w: %w", errs.ErrStoreWrite, err)
			}
			return t.executeRewriteCommit(a, progress, pending, false)
		}
		return t.buildSkipResult(a.Chapter, progress)
	}

	// Bản nộp mới phải qua kiểm tra giai đoạn hiện tại/hàng đợi viết lại; PendingCommit thường đã tồn tại là giao thức khôi phục,
	// được phép vượt qua cửa sổ gián đoạn “Progress đã ghi trước/Phase đã hoàn thành” để tiếp tục kết thúc.
	if existingPending == nil {
		if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
			// Xung đột hàng đợi giữ nguyên (đã mang phân loại ErrToolConflict); lỗi IO khác quy về Precondition.
			if errors.Is(err, errs.ErrToolConflict) {
				return nil, err
			}
			return nil, fmt.Errorf("chương hiện không được phép nộp: %w: %w", errs.ErrToolPrecondition, err)
		}
		if progress.Flow != domain.FlowRewriting && progress.Flow != domain.FlowPolishing {
			expected := progress.NextChapter()
			if a.Chapter != expected {
				return nil, fmt.Errorf("tiếp tục viết thông thường chỉ được nộp chương kế tiếp %d, nhận được chương %d: %w", expected, a.Chapter, errs.ErrToolConflict)
			}
		}
	}

	// Chặn vượt biên ở chế độ phân tầng: phải đứng trước mọi thao tác ghi, nếu không commit vượt biên sẽ phá hỏng cả file chương, tóm tắt,
	// lẫn Progress. boundary được dùng lại cho bước 6b bên dưới để tính tín hiệu cung/volume.
	var boundary *store.ArcBoundary
	if progress.Layered {
		b, bErr := t.store.Outline.CheckArcBoundary(a.Chapter)
		if bErr != nil {
			return nil, fmt.Errorf("kiểm tra biên cung thất bại chapter=%d: %w: %w", a.Chapter, errs.ErrStoreRead, bErr)
		}
		if b == nil {
			return nil, fmt.Errorf(
				"chương %d không nằm trong phạm vi dàn ý phân tầng: muốn viết phải expand_arc để mở rộng cung hoặc append_volume để thêm volume; nếu cả sách đã hoàn thành hãy gọi save_foundation type=complete_book: %w",
				a.Chapter, errs.ErrToolPrecondition)
		}
		boundary = b
	}

	// 1. Đóng băng nội dung chương. Bản nộp đầu tiên đọc từ bản nháp và ghi đĩa cùng PendingCommit; khi khôi phục
	// chỉ dùng bản chụp này, tránh Worker mới ghi đè draft trước khi thử lại rồi tạo ra “dữ kiện cũ + nội dung mới”.
	var content string
	if existingPending != nil {
		content = existingPending.DraftContent
		if content == "" {
			return nil, fmt.Errorf("bản nộp dở của chương %d thiếu draft_content, không thể chứng minh nội dung khôi phục giống bản nộp gốc: %w",
				a.Chapter, errs.ErrToolConflict)
		}
	} else {
		var loadErr error
		content, _, loadErr = t.store.Drafts.LoadChapterContent(a.Chapter)
		if loadErr != nil {
			return nil, fmt.Errorf("load chapter content: %w: %w", errs.ErrStoreRead, loadErr)
		}
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", a.Chapter, errs.ErrToolPrecondition)
	}
	// Tiêu đề do engine render: model hay thiếu dòng tiêu đề, viết sai cấp bậc hoặc giữ số chương sai từ giai đoạn hoạch định;
	// số chương và dấu hai chấm luôn do engine đóng theo ngôn ngữ tác phẩm đang cài trong store.
	content = domain.ApplyChapterHeading(content, a.Title, a.Chapter, t.store.Language())
	wordCount := utf8.RuneCountInString(content)

	var pending domain.PendingCommit
	if existingPending != nil {
		pending = *existingPending
	} else {
		payload, err := json.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("marshal commit payload: %w", err)
		}
		now := time.Now().Format(time.RFC3339)
		pending = domain.PendingCommit{
			Chapter: a.Chapter, Stage: domain.CommitStageStarted, Payload: payload, DraftContent: content,
			Summary: a.Summary, HookType: a.HookType, DominantStrand: a.DominantStrand,
			StartedAt: now, UpdatedAt: now,
		}
		if err := t.store.Signals.SavePendingCommit(pending); err != nil {
			return nil, fmt.Errorf("save pending commit: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// StageStarted có thể nghĩa là chưa ghi artefact nào, cũng có thể crash giữa chừng khi ghi gia tăng trạng thái; mọi thao tác của
	// payload đầy đủ đều phải idempotent, vì vậy phát lại thống nhất. Còn StageStateApplied thì đi thẳng sang Progress.
	if pending.Stage == domain.CommitStageStarted {
		// 2. Lưu chính văn bản cuối
		if err := t.store.Drafts.SaveFinalChapter(a.Chapter, content); err != nil {
			return nil, fmt.Errorf("save final chapter: %w: %w", errs.ErrStoreWrite, err)
		}
		style, err := t.chapterStyleDelta(a.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load chapter style: %w: %w", errs.ErrStoreRead, err)
		}
		if _, err := t.store.ChapterRecords.Accept(a.Chapter, domain.ChapterOriginGenerated, content, a.ChapterFacts, style); err != nil {
			return nil, fmt.Errorf("save chapter record: %w: %w", errs.ErrStoreWrite, err)
		}

		// 3. Lưu tóm tắt
		summary := domain.ChapterSummary{
			Chapter: a.Chapter, Title: a.Title, Summary: a.Summary, Characters: a.Characters, KeyEvents: a.KeyEvents,
		}
		if err := t.store.Summaries.SaveSummary(summary); err != nil {
			return nil, fmt.Errorf("save summary: %w: %w", errs.ErrStoreWrite, err)
		}

		// 4. Cập nhật gia tăng trạng thái
		if len(a.TimelineEvents) > 0 {
			for i := range a.TimelineEvents {
				a.TimelineEvents[i].Chapter = a.Chapter
			}
			if err := t.store.World.AppendTimelineEvents(a.TimelineEvents); err != nil {
				return nil, fmt.Errorf("append timeline: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		if len(a.ForeshadowUpdates) > 0 {
			if err := t.store.World.UpdateForeshadow(a.Chapter, a.ForeshadowUpdates); err != nil {
				return nil, fmt.Errorf("update foreshadow: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		if len(a.RelationshipChanges) > 0 {
			for i := range a.RelationshipChanges {
				a.RelationshipChanges[i].Chapter = a.Chapter
			}
			if err := t.store.World.UpdateRelationships(a.RelationshipChanges); err != nil {
				return nil, fmt.Errorf("update relationships: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		if len(a.StateChanges) > 0 {
			for i := range a.StateChanges {
				a.StateChanges[i].Chapter = a.Chapter
			}
			if err := t.store.World.AppendStateChanges(a.StateChanges); err != nil {
				return nil, fmt.Errorf("append state changes: %w: %w", errs.ErrStoreWrite, err)
			}
		}

		// 4b. Cộng dồn sổ tay nhân vật phụ: nhân vật không cốt lõi xuất hiện trong chương này vào cast_ledger, để novel_context triệu hồi.
		// Khi thất bại chỉ warn chứ không chặn commit — sổ tay là dữ liệu phụ, có thể tự lành qua commit của chương kế tiếp.
		if len(a.Characters) > 0 {
			coreNames, err := loadCoreCharacterNameSet(t.store)
			if err != nil {
				return nil, fmt.Errorf("load core characters for cast ledger: %w: %w", errs.ErrStoreRead, err)
			}
			if err := t.store.Cast.MergeAppearances(a.Chapter, a.Characters, a.CastIntros, coreNames); err != nil {
				slog.Warn("cộng dồn sổ tay nhân vật phụ thất bại, bỏ qua", "module", "commit", "chapter", a.Chapter, "err", err)
			}
		}

		pending.Stage = domain.CommitStageStateApplied
		pending.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := t.store.Signals.SavePendingCommit(pending); err != nil {
			return nil, fmt.Errorf("update pending commit stage: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 5. Cập nhật tiến độ
	if !completed {
		if err := t.store.Progress.MarkChapterComplete(a.Chapter, wordCount, a.HookType, a.DominantStrand); err != nil {
			return nil, fmt.Errorf("mark chapter complete: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 6. Xét xem có cần xem xét hay không
	progress, err = t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	completedCount := 0
	if progress != nil {
		completedCount = len(progress.CompletedChapters)
	}

	// 6b. Tín hiệu cung/volume ở chế độ truyện dài: boundary đã được kiểm tra trước ở lối vào, khi Layered bảo đảm khác nil
	var arcEnd, volumeEnd, needsExpansion, needsNewVolume bool
	var vol, arc, nextVol, nextArc int
	if progress != nil && progress.Layered && boundary != nil {
		arcEnd = boundary.IsArcEnd
		volumeEnd = boundary.IsVolumeEnd
		vol = boundary.Volume
		arc = boundary.Arc
		needsExpansion = boundary.NeedsExpansion
		needsNewVolume = boundary.NeedsNewVolume
		nextVol = boundary.NextVolume
		nextArc = boundary.NextArc
		if err := t.store.Progress.UpdateVolumeArc(vol, arc); err != nil {
			return nil, fmt.Errorf("update volume/arc: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	var reviewRequired bool
	var reviewReason string
	if progress != nil && progress.Layered {
		reviewRequired, reviewReason = domain.ShouldArcReview(arcEnd, volumeEnd, vol, arc)
	} else {
		reviewRequired, reviewReason = domain.ShouldReview(completedCount)
	}

	// 7. Dựng tín hiệu có cấu trúc
	result := domain.CommitResult{
		Chapter:        a.Chapter,
		Committed:      true,
		WordCount:      wordCount,
		NextChapter:    a.Chapter + 1,
		ReviewRequired: reviewRequired,
		ReviewReason:   reviewReason,
		HookType:       a.HookType,
		DominantStrand: a.DominantStrand,
		Feedback:       a.Feedback,
		// (feedback đồng thời được lưu vào hồ phản hồi, xem persistFeedback bên dưới — giá trị trả về chỉ là bản phản chiếu,
		// architect tiêu thụ dữ kiện store qua novel_context)
		ArcEnd:         arcEnd,
		VolumeEnd:      volumeEnd,
		Volume:         vol,
		Arc:            arc,
		NeedsExpansion: needsExpansion,
		NeedsNewVolume: needsNewVolume,
		NextVolume:     nextVol,
		NextArc:        nextArc,
	}

	// 8. Xét trạng thái hoàn thành: không phân tầng viết xong chương cuối / phân tầng chương cuối của volume cuối → MarkComplete
	bookComplete, err := t.applyCompletion(&result, progress)
	if err != nil {
		return nil, err
	}
	if bookComplete {
		result.BookComplete = true
	}
	latestProgress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress after completion: %w: %w", errs.ErrStoreRead, err)
	}
	if latestProgress != nil {
		result.Flow = string(latestProgress.Flow)
	}

	// 8.5 Hồ phản hồi là dữ kiện bền vững cho hoạch định sau, do Architect tiêu thụ ở thao tác cấu trúc kế tiếp.
	if a.Feedback != nil && (strings.TrimSpace(a.Feedback.Deviation) != "" || strings.TrimSpace(a.Feedback.Suggestion) != "") {
		if err := t.store.Outline.AppendOutlineFeedback(store.ChapterFeedback{
			Chapter: a.Chapter, Deviation: a.Feedback.Deviation, Suggestion: a.Feedback.Suggestion,
		}); err != nil {
			return nil, fmt.Errorf("persist outline feedback: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// Quy tắc cơ khí là một phần của đầu ra, phải được cố hóa trước ProgressMarked, khi khôi phục trả lại nguyên đầu ra đó.
	violations := t.checkRules(content, a.Chapter)
	output, err := json.Marshal(commitOutput{CommitResult: result, RuleViolations: violations})
	if err != nil {
		return nil, fmt.Errorf("marshal commit output: %w", err)
	}

	pending.Stage = domain.CommitStageProgressMarked
	pending.Result = &result
	pending.Output = output
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("update pending commit result: %w: %w", errs.ErrStoreWrite, err)
	}

	// 9. Thêm checkpoint. Phải đứng trước khi xóa pending_commit, bảo đảm pending_commit
	// nhìn thấy được sau khi khởi động lại luôn dẫn tới chạy lại để bù checkpoint còn thiếu.
	if err := t.appendCommitCheckpoint(a.Chapter); err != nil {
		return nil, fmt.Errorf("checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
	}
	pending.Stage = domain.CommitStageSignalSaved
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("update pending commit checkpoint stage: %w: %w", errs.ErrStoreWrite, err)
	}

	// 10. Dọn trạng thái trung gian của tiến độ
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, fmt.Errorf("clear in-progress: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, fmt.Errorf("clear pending commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// Lưu dữ kiện vi phạm: editor xem xét qua novel_context (giá trị trả về chỉ là bản phản chiếu —
	// writer dừng cứng ngay sau commit, không ai đọc được giá trị trả về). best-effort.
	if err := t.store.World.SaveRuleViolations(a.Chapter, violations); err != nil {
		slog.Warn("lưu vi phạm cơ khí xuống đĩa thất bại", "module", "tools", "chapter", a.Chapter, "err", err)
	}
	t.refreshStyleStats(a.Chapter, content)
	return output, nil
}

// finishPendingCommit kết thúc cửa sổ gián đoạn ProgressMarked/SignalSaved. Việc thêm checkpoint idempotent theo
// digest; chỉ xóa bản ghi khôi phục khi cả checkpoint lẫn việc dọn trạng thái trung gian đều thành công.
func (t *CommitChapterTool) finishPendingCommit(pending domain.PendingCommit, progress *domain.Progress) (json.RawMessage, error) {
	if pending.Stage == domain.CommitStageProgressMarked {
		if err := t.appendCommitCheckpoint(pending.Chapter); err != nil {
			return nil, fmt.Errorf("checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
		}
		pending.Stage = domain.CommitStageSignalSaved
		pending.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := t.store.Signals.SavePendingCommit(pending); err != nil {
			return nil, fmt.Errorf("update pending commit checkpoint stage: %w: %w", errs.ErrStoreWrite, err)
		}
	}
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, fmt.Errorf("clear in-progress: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, fmt.Errorf("clear pending commit: %w: %w", errs.ErrStoreWrite, err)
	}
	t.refreshStyleStats(pending.Chapter, pending.DraftContent)
	if len(pending.Output) > 0 {
		return append(json.RawMessage(nil), pending.Output...), nil
	}
	if pending.Result != nil {
		return json.Marshal(pending.Result)
	}
	return t.buildSkipResult(pending.Chapter, progress)
}

func (t *CommitChapterTool) validateRewriteDraft(chapter int, title string, progress *domain.Progress) (string, error) {
	content, _, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil {
		return "", fmt.Errorf("rewrite: load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return "", fmt.Errorf("no content found for chapter %d: %w", chapter, errs.ErrToolPrecondition)
	}
	changed, err := t.rewriteChanged(chapter, content, title)
	if err != nil {
		return "", err
	}
	if changed {
		return content, nil
	}
	mode := "viết lại"
	if progress != nil && progress.Flow == domain.FlowPolishing {
		mode = "đánh bóng"
	}
	return "", fmt.Errorf("nội dung và tiêu đề chương %d đều không thay đổi, không phát hiện thay đổi %s: %w",
		chapter, mode, errs.ErrToolPrecondition)
}

func (t *CommitChapterTool) rewriteChanged(chapter int, content, title string) (bool, error) {
	existingFinal, err := t.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return false, fmt.Errorf("rewrite: load final chapter: %w: %w", errs.ErrStoreRead, err)
	}
	if existingFinal != content {
		return true, nil
	}
	summary, err := t.store.Summaries.LoadSummary(chapter)
	if err != nil {
		return false, fmt.Errorf("rewrite: load chapter summary: %w: %w", errs.ErrStoreRead, err)
	}
	return summary == nil || strings.TrimSpace(summary.Title) != strings.TrimSpace(title), nil
}

func (t *CommitChapterTool) appendCommitCheckpoint(chapter int) error {
	_, err := t.store.Checkpoints.AppendArtifacts(
		domain.ChapterScope(chapter), "commit",
		fmt.Sprintf("chapters/%02d.md", chapter),
		fmt.Sprintf("summaries/%02d.json", chapter),
		store.ChapterRecordPath(chapter),
	)
	return err
}

// checkRules kiểm tra cơ khí nội dung chương: Lint nền sản phẩm nội tại (cặn dư cơ chế, luôn chạy)
// + Check quy tắc người dùng (đọc structured từ bản chụp của sách; thiếu bản chụp thì lùi về mặc định nội tại, bảo đảm nền cơ khí luôn có).
func (t *CommitChapterTool) checkRules(text string, chapter int) []rules.Violation {
	violations := rules.Lint(text)
	structured := rules.SystemDefaults().Structured
	if snap, err := t.store.UserRules.Load(); err == nil && snap != nil {
		structured = snap.Structured
	}
	violations = append(violations, rules.Check(text, structured)...)
	return append(violations, rules.CheckAgainstPrevious(text, t.previousChapters(chapter))...)
}

// previousChapters đọc chính văn bản cuối của vài chương liền trước, để so sánh "chương mới có chép nguyên chương trước không".
// Đơn chương đọc lỗi hoặc rỗng thì bỏ qua hết: đây là phép đo bổ sung, không để nó kéo sập trục chính của việc nộp.
func (t *CommitChapterTool) previousChapters(chapter int) []string {
	const lookback = 2
	var out []string
	for ch := chapter - 1; ch >= 1 && ch >= chapter-lookback; ch-- {
		text, err := t.store.Drafts.LoadChapterText(ch)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

// executeRewriteCommit xử lý nộp chương đánh bóng/viết lại: ghi đè chính văn bản cuối và tóm tắt, cập nhật số từ, drain hàng đợi.
// Bỏ qua mọi thao tác thêm trạng thái thế giới (timeline / foreshadow / relationship / state_changes) và kiểm tra biên cung,
// vì những thứ đó đã được áp dụng từ lần chương nộp gốc.
func (t *CommitChapterTool) executeRewriteCommit(a commitArgs, progress *domain.Progress, pending domain.PendingCommit, recovering bool) (json.RawMessage, error) {
	chapter := a.Chapter
	// 1. Chỉ dùng nội dung viết lại đã đóng băng từ lần nộp đầu, khôi phục sau crash không được dùng draft bị ghi đè sau đó.
	content := pending.DraftContent
	if content == "" {
		return nil, fmt.Errorf("bản nộp viết lại của chương %d thiếu draft_content, không thể khôi phục an toàn: %w", chapter, errs.ErrToolConflict)
	}
	content = domain.ApplyChapterHeading(content, a.Title, chapter, t.store.Language())
	wordCount := utf8.RuneCountInString(content)

	// 2. Nội dung hoặc tiêu đề phải có ít nhất một cái thay đổi; đánh bóng tiêu đề không cần ngụy tạo thay đổi nội dung.
	if !recovering {
		changed, err := t.rewriteChanged(chapter, content, a.Title)
		if err != nil {
			return nil, err
		}
		if !changed {
			mode := "viết lại"
			if progress != nil && progress.Flow == domain.FlowPolishing {
				mode = "đánh bóng"
			}
			return nil, fmt.Errorf("nội dung và tiêu đề chương %d đều không thay đổi, không phát hiện thay đổi %s: %w",
				chapter, mode, errs.ErrToolPrecondition)
		}
	}

	if pending.Stage == domain.CommitStageStarted {
		// 3. Dựng trước bộ bản ghi ứng viên hoàn chỉnh rồi phát lại để kiểm tra. Cài đặt cũ ghi đè bản ghi trước rồi mới dựng lại phép chiếu,
		// một khi chuỗi dữ kiện không khép kín thì sẽ để payload thất bại nằm trên đĩa, các lần thử sau mãi mãi đọc phải baseline hỏng.
		existing, err := t.store.ChapterRecords.Load(chapter)
		if err != nil {
			return nil, fmt.Errorf("rewrite: load chapter record: %w: %w", errs.ErrStoreRead, err)
		}
		var existingUpdates []domain.ForeshadowUpdate
		var style domain.StyleDelta
		if existing != nil {
			existingUpdates = existing.Facts.ForeshadowUpdates
			style = existing.StyleDelta
		}
		recovered, err := t.restoreRewritePlants(chapter, existingUpdates, &a.ChapterFacts)
		if err != nil {
			return nil, err
		}
		candidate, err := t.store.ChapterRecords.Prepare(
			chapter, domain.ChapterOriginGenerated, content, a.ChapterFacts, style,
		)
		if err != nil {
			return nil, fmt.Errorf("rewrite: prepare chapter record: %w: %w", errs.ErrStoreRead, err)
		}
		chapters := slices.Clone(progress.CompletedChapters)
		slices.Sort(chapters)
		records := make([]domain.ChapterRecord, 0, len(chapters))
		for _, completedChapter := range chapters {
			if completedChapter == chapter {
				records = append(records, *candidate)
				continue
			}
			record, err := t.store.ChapterRecords.Load(completedChapter)
			if err != nil {
				return nil, fmt.Errorf("rewrite: load chapter record %d: %w: %w", completedChapter, errs.ErrStoreRead, err)
			}
			if record == nil {
				return nil, fmt.Errorf("rewrite: chương %d thiếu bản ghi tiếp nhận: %w", completedChapter, errs.ErrToolConflict)
			}
			records = append(records, *record)
		}
		if err := revision.ValidateRecords(records); err != nil {
			if clearErr := t.store.Signals.ClearPendingCommit(); clearErr != nil {
				return nil, fmt.Errorf("rewrite: kiểm tra chuỗi dữ kiện chương thất bại (%v), đồng thời dọn bản nộp đóng băng cũng thất bại: %w: %w", err, errs.ErrStoreWrite, clearErr)
			}
			return nil, fmt.Errorf("rewrite: kiểm tra chuỗi dữ kiện chương thất bại, đã bỏ đóng băng và chưa ghi kết quả viết lại: %w: %w", errs.ErrToolPrecondition, err)
		}

		// 4. Sau khi kiểm tra qua mới ghi đè bản ghi uy quyền và chính văn bản cuối; cùng một payload đóng băng có thể phát lại an toàn.
		if err := t.store.Drafts.SaveFinalChapter(chapter, content); err != nil {
			return nil, fmt.Errorf("rewrite: save final chapter: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.ChapterRecords.Save(*candidate); err != nil {
			return nil, fmt.Errorf("rewrite: save chapter record: %w: %w", errs.ErrStoreWrite, err)
		}
		if len(recovered) > 0 {
			slog.Warn("đã khôi phục từ sổ cái phục bút dữ kiện gieo bị phiên bản cũ làm mất", "module", "commit", "chapter", chapter, "foreshadows", recovered)
		}
		if err := revision.NewProjector(t.store).Apply(records); err != nil {
			return nil, fmt.Errorf("rewrite: rebuild chapter projections: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Summaries.SaveSummary(domain.ChapterSummary{
			Chapter: chapter, Title: a.Title, Summary: a.Summary, Characters: a.Characters, KeyEvents: a.KeyEvents,
		}); err != nil {
			return nil, fmt.Errorf("rewrite: save summary: %w: %w", errs.ErrStoreWrite, err)
		}
		pending.Stage = domain.CommitStageStateApplied
		pending.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := t.store.Signals.SavePendingCommit(pending); err != nil {
			return nil, fmt.Errorf("rewrite: update pending state stage: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 5. Cập nhật số từ (MarkChapterComplete idempotent với chương đã hoàn thành: replaces word count, slice.Contains chống vào hàng đợi trùng)
	if progress.Phase != domain.PhaseComplete {
		if err := t.store.Progress.MarkChapterComplete(chapter, wordCount, a.HookType, a.DominantStrand); err != nil {
			return nil, fmt.Errorf("rewrite: update word count: %w: %w", errs.ErrStoreWrite, err)
		}

		// 6. Drain hàng đợi chờ xử lý; khi hàng đợi rỗng CompleteRewrite sẽ tự động chuyển flow về writing
		if err := t.store.Progress.CompleteRewrite(chapter); err != nil {
			return nil, fmt.Errorf("rewrite: complete rewrite: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 7. Đọc bản chụp Progress sau drain, trả về như dữ kiện
	mode := pending.RewriteMode
	if mode == "" {
		mode = "rewrite"
	}
	latest, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("rewrite: load progress after drain: %w: %w", errs.ErrStoreRead, err)
	}
	remaining := []int{}
	nextChapter := chapter + 1
	flow := string(domain.FlowWriting)
	if latest != nil {
		remaining = append(remaining, latest.PendingRewrites...)
		nextChapter = latest.NextChapter()
		flow = string(latest.Flow)
	}
	drained := len(remaining) == 0

	// Chỉ xét hoàn thành sau khi hàng đợi rỗng: bản nộp viết lại không đi qua applyCompletion của nhánh chính, hoàn thành chỉ có thể kích hoạt ở đây.
	//   - Phân tầng + viết thuận: tổng phán định layeredComplete (volume kết cục đã viết xong cấu trúc / chưa tuyên bố thì đi tầng chất lượng).
	//   - Phân tầng + reopen viết lại (ReopenedFromComplete): viết lại chỉ sửa chương sẵn có, không thêm bớt cấu trúc, theo cấu trúc đầy đủ
	//     tức hoàn thành lại — nếu vì viết lại làm xáo trộn mối tuyến nào đó mà kẹt ở writing, cuối volume cuối sẽ rơi vào vòng lặp chết viết vượt biên.
	//   - Không phân tầng: viết đủ TotalChapters tức hoàn thành (viết lại không thêm bớt số chương, vốn đã đủ).
	bookComplete := false
	if drained && latest != nil {
		reComplete := false
		switch {
		case latest.Layered && latest.ReopenedFromComplete:
			reComplete, err = layeredStructurallyComplete(t.store, latest)
		case latest.Layered:
			reComplete, err = layeredComplete(t.store, latest)
		default:
			reComplete = latest.TotalChapters > 0 && len(latest.CompletedChapters) >= latest.TotalChapters
		}
		if err != nil {
			return nil, fmt.Errorf("rewrite: evaluate completion: %w: %w", errs.ErrStoreRead, err)
		}
		if reComplete {
			if err := t.store.Progress.MarkComplete(); err != nil {
				return nil, fmt.Errorf("rewrite: mark complete: %w: %w", errs.ErrStoreWrite, err)
			}
			bookComplete = true
			p, err := t.store.Progress.Load()
			if err != nil {
				return nil, fmt.Errorf("rewrite: reload completed progress: %w: %w", errs.ErrStoreRead, err)
			}
			if p != nil {
				flow = string(p.Flow)
			}
		}
	}

	// Giống nhánh chính: rewrite/polish cũng kiểm tra cơ khí và lưu bền vững (sau viết lại ghi bản ghi mới, vi phạm cũ coi như đã sạch)
	violations := t.checkRules(content, a.Chapter)
	output, err := json.Marshal(map[string]any{
		"chapter": chapter, "rewritten": true, "mode": mode, "word_count": wordCount,
		"remaining_queue": remaining, "queue_drained": drained, "next_chapter": nextChapter,
		"flow": flow, "book_complete": bookComplete, "rule_violations": violations,
	})
	if err != nil {
		return nil, fmt.Errorf("rewrite: marshal output: %w", err)
	}
	pending.Stage = domain.CommitStageProgressMarked
	pending.Output = output
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("rewrite: update pending progress stage: %w: %w", errs.ErrStoreWrite, err)
	}

	// 8. Sau checkpoint mới đánh dấu signal_saved, cuối cùng dọn PendingCommit.
	if err := t.appendCommitCheckpoint(chapter); err != nil {
		return nil, fmt.Errorf("rewrite: checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
	}
	pending.Stage = domain.CommitStageSignalSaved
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("rewrite: update pending checkpoint stage: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, fmt.Errorf("rewrite: clear in-progress: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, fmt.Errorf("rewrite: clear pending commit: %w: %w", errs.ErrStoreWrite, err)
	}

	if err := t.store.World.SaveRuleViolations(chapter, violations); err != nil {
		slog.Warn("lưu vi phạm cơ khí xuống đĩa thất bại", "module", "tools", "chapter", chapter, "err", err)
	}
	t.refreshStyleStats(chapter, content)
	return output, nil
}

// restoreRewritePlants chỉ sửa một dạng hỏng duy nhất mà phiên bản cũ đã gây ra: sổ cái phục bút vẫn ghi plant của chương này,
// nhưng bản ghi tiếp nhận chương đã bị lần viết lại thất bại ghi đè. Sổ cái cho đầy đủ id, mô tả và chương gieo,
// nên có thể khôi phục tất định; các không nhất quán khác tiếp tục báo lỗi tường minh, không đoán dữ kiện cốt truyện.
func (t *CommitChapterTool) restoreRewritePlants(chapter int, existing []domain.ForeshadowUpdate, facts *domain.ChapterFacts) ([]string, error) {
	planted := make(map[string]struct{}, len(existing)+len(facts.ForeshadowUpdates))
	for _, update := range existing {
		if update.Action == "plant" {
			planted[update.ID] = struct{}{}
		}
	}
	for _, update := range facts.ForeshadowUpdates {
		if update.Action == "plant" {
			planted[update.ID] = struct{}{}
		}
	}

	ledger, err := t.store.World.LoadForeshadowLedger()
	if err != nil {
		return nil, fmt.Errorf("rewrite: load foreshadow ledger for recovery: %w: %w", errs.ErrStoreRead, err)
	}
	var restored []domain.ForeshadowUpdate
	var ids []string
	for _, entry := range ledger {
		if entry.PlantedAt != chapter {
			continue
		}
		if _, ok := planted[entry.ID]; ok {
			continue
		}
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Description) == "" {
			return nil, fmt.Errorf("rewrite: sổ cái phục bút của chương %d thiếu id hoặc description có thể khôi phục: %w", chapter, errs.ErrToolConflict)
		}
		planted[entry.ID] = struct{}{}
		restored = append(restored, domain.ForeshadowUpdate{
			ID: entry.ID, Action: "plant", Description: entry.Description,
		})
		ids = append(ids, entry.ID)
	}
	if len(restored) > 0 {
		facts.ForeshadowUpdates = append(restored, facts.ForeshadowUpdates...)
	}
	return ids, nil
}

func (t *CommitChapterTool) refreshStyleStats(chapter int, content string) {
	if content == "" {
		var err error
		content, err = t.store.Drafts.LoadChapterText(chapter)
		if err != nil {
			slog.Error("cập nhật chỉ mục thống kê văn phong thất bại", "module", "tools", "chapter", chapter, "err", err)
			return
		}
		if content == "" {
			slog.Error("cập nhật chỉ mục thống kê văn phong thất bại", "module", "tools", "chapter", chapter, "err", errors.New("chính văn bản cuối không tồn tại"))
			return
		}
	}
	t.styleStats.ChapterCommitted(chapter, content)
}

// buildSkipResult dựng dữ kiện trả về cho "bản nộp lặp của chương đã hoàn thành", thẳng hàng với commit thường.
// Bộ điều phối dựa vào đó ra quyết định kế tiếp (giao việc writer/editor/architect), chứ không bị ảo giác vì nhận được gợi ý dạng prose.
func (t *CommitChapterTool) buildSkipResult(chapter int, progress *domain.Progress) (json.RawMessage, error) {
	_, wordCount, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil {
		return nil, fmt.Errorf("load completed chapter: %w: %w", errs.ErrStoreRead, err)
	}

	result := domain.CommitResult{
		Chapter:     chapter,
		Committed:   true,
		WordCount:   wordCount,
		NextChapter: chapter + 1,
	}

	if progress != nil && progress.Layered {
		boundary, err := t.store.Outline.CheckArcBoundary(chapter)
		if err != nil {
			return nil, fmt.Errorf("check completed chapter boundary: %w: %w", errs.ErrStoreRead, err)
		}
		if boundary != nil {
			result.ArcEnd = boundary.IsArcEnd
			result.VolumeEnd = boundary.IsVolumeEnd
			result.Volume = boundary.Volume
			result.Arc = boundary.Arc
			result.NeedsExpansion = boundary.NeedsExpansion
			result.NeedsNewVolume = boundary.NeedsNewVolume
			result.NextVolume = boundary.NextVolume
			result.NextArc = boundary.NextArc
		}
		result.ReviewRequired, result.ReviewReason = domain.ShouldArcReview(result.ArcEnd, result.VolumeEnd, result.Volume, result.Arc)
	} else if progress != nil {
		result.ReviewRequired, result.ReviewReason = domain.ShouldReview(len(progress.CompletedChapters))
	}

	if progress != nil {
		if progress.Phase == domain.PhaseComplete {
			result.BookComplete = true
		}
		result.Flow = string(progress.Flow)
	}

	return json.Marshal(result)
}

// loadCoreCharacterNameSet tải tập hợp tên nhân vật sẵn có trong characters.json (gồm cả biệt danh).
// Dùng làm tập lọc "cốt lõi đã biết" cho cast_ledger — nhân vật cốt lõi không vào sổ tay phụ.
// Khi tải thất bại trả nil (lúc merge mọi characters đều vào ledger, chấp nhận được).
func loadCoreCharacterNameSet(s *store.Store) (map[string]bool, error) {
	chars, err := s.Characters.Load()
	if err != nil {
		return nil, err
	}
	if len(chars) == 0 {
		return nil, nil
	}
	set := make(map[string]bool, len(chars)*2)
	for _, c := range chars {
		if c.Name != "" {
			set[c.Name] = true
		}
		for _, alias := range c.Aliases {
			if alias != "" {
				set[alias] = true
			}
		}
	}
	return set, nil
}

// applyCompletion phán định xem lần commit này có làm cả sách hoàn thành hay không, nếu có thì MarkComplete và trả true.
//   - Không phân tầng: viết xong tổng số chương đã thỏa thuận tức hoàn thành.
//   - Phân tầng: kiến trúc sư chủ động save_foundation type=complete_book là nhánh chính; ở đây thêm một lớp
//     fallback tất định (xem layeredComplete) — ngăn model ở điểm cuối chẳng append_volume cũng chẳng
//     complete_book, dẫn đến livelock "writer trần trụi chạy vượt biên chương → guard chặn vượt biên → thử lại lặp lại"
//     (nguyên nhân gốc của ca 《凡骨》ch204..347).
func (t *CommitChapterTool) applyCompletion(result *domain.CommitResult, progress *domain.Progress) (bool, error) {
	if progress == nil {
		return false, nil
	}
	if progress.Phase == domain.PhaseComplete {
		return true, nil
	}
	if progress.Layered {
		complete, err := layeredComplete(t.store, progress)
		if err != nil {
			return false, fmt.Errorf("evaluate layered completion: %w: %w", errs.ErrStoreRead, err)
		}
		if complete {
			if err := t.store.Progress.MarkComplete(); err != nil {
				return false, fmt.Errorf("mark book complete: %w: %w", errs.ErrStoreWrite, err)
			}
			return true, nil
		}
		return false, nil
	}
	if progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters {
		if err := t.store.Progress.MarkComplete(); err != nil {
			return false, fmt.Errorf("mark book complete: %w: %w", errs.ErrStoreWrite, err)
		}
		return true, nil
	}
	return false, nil
}

// ── Phán định hoàn thành phân tầng (cấp package: hai điểm kích hoạt commit_chapter và save_volume_summary dùng chung) ──
//
// Kiểm tra hoàn thành luôn diễn ra trong công cụ nơi "viên gạch dữ kiện cuối cùng" rơi xuống đĩa:
//   - Chưa tuyên bố kết cục: commit chương cuối (layeredBookComplete tầng chất lượng)
//   - Đã tuyên bố kết cục: viên gạch cuối của nhánh chính thuận chiều là bộ ba kết thúc cuối volume (xem xét → tóm tắt cung → tóm tắt volume),
//     nên điểm kích hoạt nằm ở save_volume_summary; khi drain viết lại xong mà bộ ba đã đủ thì do commit kích hoạt.

// layeredStructurallyComplete phán định truyện phân tầng dài có "viết xong về mặt cấu trúc" hay không: hàng đợi viết lại rỗng + không còn cung khung chờ mở rộng
// + mọi chương đã mở rộng đều đã viết. Đây là dữ kiện trạng thái cuối tất định, không chứa phán đoán ngữ nghĩa như phục bút/mối tuyến — dùng làm lưới an toàn
// "chống vòng lặp chết trạng thái cuối" (dựa vào đó hoàn thành lại sau khi hàng đợi viết lại được drained cạn).
func layeredStructurallyComplete(st *store.Store, progress *domain.Progress) (bool, error) {
	// 1. Hàng đợi viết lại phải rỗng
	if len(progress.PendingRewrites) > 0 {
		return false, nil
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return false, fmt.Errorf("load layered outline: %w", err)
	}
	if len(volumes) == 0 {
		return false, nil
	}
	// 2. Không được còn cung khung chờ mở rộng (trong kế hoạch vẫn còn nội dung phải viết)
	for i := range volumes {
		for j := range volumes[i].Arcs {
			if !volumes[i].Arcs[j].IsExpanded() {
				return false, nil
			}
		}
	}
	// 3. Mọi chương đã mở rộng phải viết xong hết
	expanded := len(domain.FlattenOutline(volumes))
	return expanded > 0 && len(progress.CompletedChapters) >= expanded, nil
}

// finaleWrapped kiểm tra bộ ba kết thúc cuối volume của volume kết cục (xem xét cung/tóm tắt cung/tóm tắt volume) đã đủ hay chưa.
// Hoàn thành ở volume kết cục không đòi phục bút/mối tuyến về không, nhưng phải đợi cung cuối vượt qua cổng chất lượng của editor — đoạn kết là phần quan trọng nhất cả cuốn sách,
// hoàn thành không được diễn ra trước editor xem xét (có thể đưa vào hàng đợi viết lại) và trước khi tóm tắt được ghi đĩa.
func finaleWrapped(st *store.Store, progress *domain.Progress) (bool, error) {
	last := progress.LatestCompleted()
	if last <= 0 {
		return false, nil
	}
	b, err := st.Outline.CheckArcBoundary(last)
	if err != nil {
		return false, fmt.Errorf("check finale boundary: %w", err)
	}
	if b == nil || !b.IsArcEnd {
		return false, nil
	}
	hasReview, err := st.World.HasArcReview(last)
	if err != nil {
		return false, fmt.Errorf("load finale review: %w", err)
	}
	hasArcSummary, err := st.Summaries.HasArcSummary(b.Volume, b.Arc)
	if err != nil {
		return false, fmt.Errorf("load finale arc summary: %w", err)
	}
	hasVolumeSummary, err := st.Summaries.HasVolumeSummary(b.Volume)
	if err != nil {
		return false, fmt.Errorf("load finale volume summary: %w", err)
	}
	return hasReview && hasArcSummary && hasVolumeSummary, nil
}

// layeredComplete tổng phán định hoàn thành cho viết thuận phân tầng:
//   - Đã tuyên bố volume kết cục (volume cuối của layered_outline mang final) → cấu trúc viết xong + bộ ba kết thúc cuối volume đủ
//     tức hoàn thành, không còn đòi phục bút/mối tuyến về không. Cả volume kết cục lấy việc thu mối tuyến làm mục tiêu (kiến trúc sư khi hoạch định đã phân
//     mối tuyến/phục bút vào các cung), sót lẻ tẻ thuộc vấn đề chất lượng biên tập, không nên giam cả cuốn sách ngoài trạng thái cuối — nếu không
//     sách có estimated_scale ước cao sẽ mãi mãi không thể hoàn bản hợp lệ (một phía nguyên nhân gốc của ca stop guard đứt mạch ở 140 chương).
//   - Chưa tuyên bố → tầng chất lượng layeredBookComplete, ngăn model chẳng kết cục cũng chẳng hoàn bản mà lại kết thúc sớm
//     ở chỗ dàn ý cạn kiệt.
func layeredComplete(st *store.Store, progress *domain.Progress) (bool, error) {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return false, fmt.Errorf("load layered outline: %w", err)
	}
	if domain.FinaleVolume(volumes) > 0 {
		structurallyComplete, err := layeredStructurallyComplete(st, progress)
		if err != nil || !structurallyComplete {
			return structurallyComplete, err
		}
		return finaleWrapped(st, progress)
	}
	return layeredBookComplete(st, progress)
}

// ReconcileLayeredCompletion bù nốt trạng thái hoàn thành của sách phân tầng theo dữ kiện bền vững hiện tại.
// Nhánh thường của save_volume_summary và khôi phục crash của Engine cùng dùng lối vào này, tránh khi tóm tắt volume đã ghi đĩa
// mà Progress chưa kịp MarkComplete thì mất vĩnh viễn điểm kích hoạt hoàn thành tự động.
func ReconcileLayeredCompletion(st *store.Store) (bool, error) {
	progress, err := st.Progress.Load()
	if err != nil {
		return false, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil || !progress.Layered {
		return false, nil
	}
	if progress.Phase == domain.PhaseComplete {
		return true, nil
	}
	if progress.Phase != domain.PhaseWriting {
		return false, nil
	}
	complete, err := layeredComplete(st, progress)
	if err != nil || !complete {
		return complete, err
	}
	if err := st.Progress.MarkComplete(); err != nil {
		return false, fmt.Errorf("mark complete: %w", err)
	}
	return true, nil
}

// layeredBookComplete dùng dữ kiện khách quan phán định truyện phân tầng dài có thực sự viết xong hay không, đối chiếu vài mục
// lượng hóa được trong checklist phán định hoàn thành của architect-long.md + dữ kiện cấu trúc. Trên nền cấu trúc đầy đủ còn đòi phục bút về không, mối tuyến thu gom — mục nào chưa đạt
// đều nhường lại cho kiến trúc sư tiếp tục expand_arc / append_volume, tuyệt đối không kết thúc khi câu chuyện chưa viết xong. Khi không có compass thì bảo thủ
// phán là chưa hoàn thành. Đây là phán định hoàn thành "tầng chất lượng" cho lúc chưa tuyên bố volume kết cục, nghiêm hơn layeredStructurallyComplete.
func layeredBookComplete(st *store.Store, progress *domain.Progress) (bool, error) {
	structurallyComplete, err := layeredStructurallyComplete(st, progress)
	if err != nil || !structurallyComplete {
		return structurallyComplete, err
	}
	// 4. Phục bút đang hoạt động phải về không (lời hứa đã được đáp)
	active, err := st.World.LoadActiveForeshadow()
	if err != nil {
		return false, fmt.Errorf("load active foreshadow: %w", err)
	}
	if len(active) > 0 {
		return false, nil
	}
	// 5. Mối tuyến đang mở của la bàn phải được thu gom (không có compass / mối tuyến chưa dọn đều trả lại kiến trúc sư phán định)
	compass, err := st.Outline.LoadCompass()
	if err != nil {
		return false, fmt.Errorf("load compass: %w", err)
	}
	if compass == nil || len(compass.OpenThreads) > 0 {
		return false, nil
	}
	return true, nil
}
