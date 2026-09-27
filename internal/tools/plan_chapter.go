package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// PlanChapterTool lưu ý tưởng chương, Agent tự quyết độ mịn của quy hoạch.
type PlanChapterTool struct {
	store *store.Store
}

func NewPlanChapterTool(store *store.Store) *PlanChapterTool {
	return &PlanChapterTool{store: store}
}

func (t *PlanChapterTool) Name() string { return "plan_chapter" }
func (t *PlanChapterTool) Description() string {
	return "Lưu ý tưởng viết chương. Agent tự quyết định độ mịn quy hoạch, không bắt buộc tách cảnh"
}
func (t *PlanChapterTool) Label() string { return "Quy hoạch chương" }

// Công cụ ghi, cấm đồng thời.
func (t *PlanChapterTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *PlanChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *PlanChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("Số chương")).Required(),
		schema.Property("title", schema.String("Tiêu đề chương tạm; sau khi viết có thể chỉnh theo chính văn")).Required(),
		schema.Property("goal", schema.String("Mục tiêu của chương")).Required(),
		schema.Property("conflict", schema.String("Xung đột cốt lõi")).Required(),
		schema.Property("hook", schema.String("Móc cuối chương")).Required(),
		schema.Property("emotion_arc", schema.String("Đường cong cảm xúc")),
		schema.Property("notes", schema.String("Ghi nhớ tự do (bất cứ gì bạn thấy cần nhớ khi viết)")),
		schema.Property("required_beats", schema.Array("Mục đẩy bắt buộc phải hoàn thành trong chương", schema.String(""))),
		schema.Property("forbidden_moves", schema.Array("Mục đẩy rõ ràng không được xảy ra trong chương", schema.String(""))),
		schema.Property("continuity_checks", schema.Array("Điểm liên tục cần đối chiếu riêng trong chương", schema.String(""))),
		schema.Property("evaluation_focus", schema.Array("Mục Editor kiểm tra trọng tâm", schema.String(""))),
		schema.Property("emotion_target", schema.String("Tùy chọn: cảm xúc mong muốn người đọc cảm nhận chủ yếu ở chương này")),
		schema.Property("payoff_points", schema.Array("Tùy chọn: điểm tình tiết hoặc điểm trả thưởng mà chương then chốt muốn đáp ứng", schema.String(""))),
		schema.Property("hook_goal", schema.String("Tùy chọn: mục tiêu khát khao đọc tiếp hoặc bỏ lửng cuối chương")),
	)
}

func (t *PlanChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	plan, err := decodeChapterPlanArgs(args)
	if err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if plan.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	completed := progress != nil && slices.Contains(progress.CompletedChapters, plan.Chapter)
	// Chương nằm trong hàng đợi viết lại là ngoại lệ duy nhất: khi đã hoàn thành mà cần viết lại thì "nên viết lại thế nào" phải có nơi ghi xuống,
	// và Contract (goal / payoff_points / continuity_checks / hook_goal) chính là chỉ dẫn đó.
	// Trước đây chỗ này từ chối một luật, khiến revise_outline (chỉ cho sửa chương chưa viết), save_foundation(outline)
	// (cấm ghi đè toàn bộ trong giai đoạn viết) và công cụ này ba ngóc cùng tắc — kiến trúc sư không có chỗ ghi hướng viết lại, thực đo chạy không 4 lần thì đứt cầu dao.
	queuedForRewrite := progress != nil && slices.Contains(progress.PendingRewrites, plan.Chapter)
	if completed && !queuedForRewrite {
		return json.Marshal(map[string]any{
			"chapter":   plan.Chapter,
			"skipped":   true,
			"completed": true,
			"reason":    fmt.Sprintf("Chương %d đã nộp hoàn thành, không thể quy hoạch lại", plan.Chapter),
		})
	}
	if err := t.store.Progress.ValidateChapterWork(plan.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureChapterExpanded(t.store, plan.Chapter); err != nil {
		return nil, err
	}

	if err := t.store.Drafts.SaveChapterPlan(plan); err != nil {
		return nil, fmt.Errorf("save chapter plan: %w", err)
	}
	// Ghi lại chỉ thị viết lại không đồng nghĩa bắt đầu viết chương này: Engine đã đánh dấu tiến hành sẵn khi giao writer
	// (engine.go), draft_chapter khi xuống bút cũng đánh dấu lần nữa, nên ở đây là thừa với chương viết lại.
	// Còn StartChapter sẽ ghi đè vô điều kiện InProgressChapter và dọn CompletedScenes —
	// nếu người quy hoạch viết chỉ thị cho chương 20 trong hàng đợi trong khi writer đang ở chương 13, con trỏ sẽ bị kéo đi.
	if !queuedForRewrite || progress.InProgressChapter == plan.Chapter {
		if err := t.store.Progress.StartChapter(plan.Chapter); err != nil {
			return nil, fmt.Errorf("mark chapter in progress: %w", err)
		}
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(plan.Chapter), "plan",
		fmt.Sprintf("drafts/%02d.plan.json", plan.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint chapter plan: %w", err)
	}

	return json.Marshal(map[string]any{
		"planned":   true,
		"chapter":   plan.Chapter,
		"next_step": "Gọi ngay draft_chapter(chapter=số chương này, content=chuỗi chính văn đầy đủ) để ghi chính văn, không quy hoạch lặp cùng một chương",
	})
}

func decodeChapterPlanArgs(args json.RawMessage) (domain.ChapterPlan, error) {
	var a struct {
		Chapter          int      `json:"chapter"`
		Title            string   `json:"title"`
		Goal             string   `json:"goal"`
		Conflict         string   `json:"conflict"`
		Hook             string   `json:"hook"`
		EmotionArc       string   `json:"emotion_arc"`
		Notes            string   `json:"notes"`
		RequiredBeats    []string `json:"required_beats"`
		ForbiddenMoves   []string `json:"forbidden_moves"`
		ContinuityChecks []string `json:"continuity_checks"`
		EvaluationFocus  []string `json:"evaluation_focus"`
		EmotionTarget    string   `json:"emotion_target"`
		PayoffPoints     []string `json:"payoff_points"`
		HookGoal         string   `json:"hook_goal"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return domain.ChapterPlan{}, err
	}

	return domain.ChapterPlan{
		Chapter:    a.Chapter,
		Title:      a.Title,
		Goal:       a.Goal,
		Conflict:   a.Conflict,
		Hook:       a.Hook,
		EmotionArc: a.EmotionArc,
		Notes:      a.Notes,
		Contract: domain.ChapterContract{
			RequiredBeats:    a.RequiredBeats,
			ForbiddenMoves:   a.ForbiddenMoves,
			ContinuityChecks: a.ContinuityChecks,
			EvaluationFocus:  a.EvaluationFocus,
			EmotionTarget:    a.EmotionTarget,
			PayoffPoints:     a.PayoffPoints,
			HookGoal:         a.HookGoal,
		},
	}, nil
}
