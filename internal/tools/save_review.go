package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveReviewTool lưu kết quả xem xét của Editor.
type SaveReviewTool struct {
	store *store.Store
}

func NewSaveReviewTool(store *store.Store) *SaveReviewTool {
	return &SaveReviewTool{store: store}
}

func (t *SaveReviewTool) Name() string { return "save_review" }
func (t *SaveReviewTool) Description() string {
	return "Lưu kết quả xem xét và cập nhật trạng thái luồng. verdict là một trong accept/polish/rewrite." +
		"Editor đưa ra verdict dựa trên ngữ cảnh đầy đủ, công cụ chỉ kiểm tra sự kiện và cập nhật Progress một thể nguyên tử." +
		"Trả về sự kiện có cấu trúc: verdict / affected_chapters / next_flow / next_chapter"
}
func (t *SaveReviewTool) Label() string { return "Lưu kết quả xem xét" }

// Công cụ ghi (đồng thời cập nhật reviews/ và PendingRewrites/Flow của Progress), cấm đồng thời.
func (t *SaveReviewTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveReviewTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *SaveReviewTool) StrictSchema() bool                     { return true }

func (t *SaveReviewTool) Schema() map[string]any {
	issueSchema := schema.Object(
		schema.Property("type", schema.String("Chiều vấn đề; có thể dùng chiều cơ sở trong prompt xem xét, cũng có thể viết chiều cụ thể chính xác hơn")).Required(),
		schema.Property("severity", schema.Enum("Mức nghiêm trọng", "critical", "error", "warning")).Required(),
		schema.Property("description", schema.String("Mô tả vấn đề")).Required(),
		schema.Property("evidence", schema.String("Bằng chứng: đoạn văn gốc, tình tiết cụ thể hoặc dữ liệu trạng thái")).Required(),
		schema.Property("suggestion", llmcontract.Nullable(schema.String("Gợi ý sửa; không cần gợi ý thì để null"))).Required(),
		schema.Property("chapters", schema.Array("Chương mà bằng chứng của vấn đề này thực sự nằm ở; xem xét cung phải nằm trong khoảng nhiệm vụ giao", schema.Int("Số chương"))).Required(),
		schema.Property("requires_change", schema.Bool("Vấn đề này có nên kích hoạt ngay viết lại các chương liệt kê hay không, do Editor phán đoán theo trải nghiệm đọc tổng thể")).Required(),
	)
	dimensionSchema := schema.Object(
		schema.Property("dimension", schema.String("Chiều đánh giá; do nhiệm vụ xem xét hiện tại và rubric quyết định")).Required(),
		schema.Property("score", schema.Int("Điểm số (0-100)")).Required(),
		schema.Property("comment", schema.String("Kết luận và bằng chứng tóm tắt của chiều này; mỗi chiều bắt buộc điền")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("Số chương được xem xét (xem xét toàn cục điền số chương mới nhất)")).Required(),
		schema.Property("scope", schema.Enum("Phạm vi xem xét", "chapter", "global", "arc")).Required(),
		schema.Property("dimensions", schema.Array("Điểm theo từng chiều; rubric cơ sở do prompt Editor cung cấp, có thể bổ sung chiều cụ thể hơn theo nhiệm vụ", dimensionSchema)).Required(),
		schema.Property("issues", schema.Array("Các vấn đề phát hiện", issueSchema)).Required(),
		schema.Property("contract_status", llmcontract.Nullable(schema.Enum("Mức hoàn thành hợp đồng chương; không áp dụng thì để null", "met", "partial", "missed"))).Required(),
		schema.Property("contract_misses", schema.Array("Mục contract chưa hoàn thành hoặc vi phạm; không có thì dùng mảng rỗng", schema.String(""))).Required(),
		schema.Property("contract_notes", llmcontract.Nullable(schema.String("Mô tả tóm tắt về việc thực hiện contract; không có thì để null"))).Required(),
		schema.Property("verdict", schema.Enum("Kết luận xem xét", "accept", "polish", "rewrite")).Required(),
		schema.Property("summary", schema.String("Tổng kết xem xét")).Required(),
	)
}

func (t *SaveReviewTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var r domain.ReviewEntry
	if err := json.Unmarshal(args, &r); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if r.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	boundary, err := t.normalizeReviewEntry(&r)
	if err != nil {
		return nil, err
	}
	reviewOutcome, err := reviewFlow(r.Verdict)
	if err != nil {
		return nil, err
	}

	affected := r.AffectedChapters

	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil || !slices.Contains(progress.CompletedChapters, r.Chapter) {
		return nil, fmt.Errorf("review chapter %d must be completed", r.Chapter)
	}
	scope := domain.ChapterScope(r.Chapter)
	artifact := fmt.Sprintf("reviews/%02d.json", r.Chapter)
	var existing *domain.ReviewEntry
	switch r.Scope {
	case "arc":
		scope = domain.ArcScope(boundary.Volume, boundary.Arc)
		existing, err = t.store.World.LoadReview(r.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load arc review: %w", err)
		}
		if existing != nil && existing.Scope != "arc" {
			existing = nil
		}
	case "global":
		artifact = fmt.Sprintf("reviews/%02d-global.json", r.Chapter)
		existing, err = t.store.World.LoadGlobalReview(r.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load global review: %w", err)
		}
	}
	if existing != nil {
		if !reflect.DeepEqual(*existing, r) {
			return nil, fmt.Errorf("Xem xét tổng hợp chương %d đã tồn tại và nội dung khác nhau, từ chối ghi đè: %w", r.Chapter, errs.ErrToolConflict)
		}
		return t.finishReview(r, progress, scope, artifact)
	}
	switch r.Scope {
	case "arc":
		if err := requireAggregateTarget(t.store, flow.AggregateArcReview, boundary.Volume, boundary.Arc, r.Chapter); err != nil {
			return nil, err
		}
	case "global":
		if err := requireAggregateTarget(t.store, flow.AggregateGlobalReview, 0, 0, r.Chapter); err != nil {
			return nil, err
		}
	}

	// Áp dụng trạng thái điều khiển một thể nguyên tử trước, rồi mới lưu tác phẩm xem xét. Nếu bước hai thất bại, ý định viết lại vẫn tồn tại;
	// sau khi Writer cạn hàng đợi, định tuyến sẽ thấy thiếu tác phẩm xem xét mà giao lại Editor, không bỏ qua bước xem xét.
	latest, err := t.store.Progress.ApplyReviewOutcome(reviewOutcome, affected, r.Summary)
	if err != nil {
		return nil, fmt.Errorf("apply review outcome: %w", err)
	}
	if err := t.store.World.SaveReview(r); err != nil {
		return nil, fmt.Errorf("save review: %w", err)
	}

	return t.finishReview(r, latest, scope, artifact)
}

func (t *SaveReviewTool) finishReview(
	r domain.ReviewEntry,
	progress *domain.Progress,
	scope domain.Scope,
	artifact string,
) (json.RawMessage, error) {
	if _, err := t.store.Checkpoints.AppendArtifact(scope, "review", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint review: %w", err)
	}

	// Dùng snapshot Progress do cập nhật nguyên tử trả về làm sự kiện, tránh đọc lần hai mở thêm cửa sổ thất bại mới.
	nextFlow := string(domain.FlowWriting)
	nextChapter := 0
	if progress != nil {
		nextFlow = string(progress.Flow)
		nextChapter = progress.NextChapter()
	}

	return json.Marshal(map[string]any{
		"saved":             true,
		"chapter":           r.Chapter,
		"scope":             r.Scope,
		"verdict":           r.Verdict,
		"affected_chapters": r.AffectedChapters,
		"issues":            len(r.Issues),
		"next_flow":         nextFlow,
		"next_chapter":      nextChapter,
	})
}

func (t *SaveReviewTool) normalizeReviewEntry(r *domain.ReviewEntry) (*store.ArcBoundary, error) {
	switch r.Scope {
	case "chapter", "global", "arc":
	default:
		return nil, fmt.Errorf("invalid review scope: %q", r.Scope)
	}
	if len(r.AffectedChapters) > 0 {
		return nil, fmt.Errorf("affected_chapters is derived from issues[].chapters; do not submit it")
	}
	if strings.TrimSpace(r.Summary) == "" {
		return nil, fmt.Errorf("summary is required")
	}
	if r.ContractStatus != "" && r.ContractStatus != "met" && r.ContractStatus != "partial" && r.ContractStatus != "missed" {
		return nil, fmt.Errorf("invalid contract_status: %q", r.ContractStatus)
	}
	for _, miss := range r.ContractMisses {
		if strings.TrimSpace(miss) == "" {
			return nil, fmt.Errorf("contract_misses cannot contain empty entries")
		}
	}
	var boundary *store.ArcBoundary
	if r.Scope == "arc" {
		var err error
		boundary, err = t.store.Outline.CheckArcBoundary(r.Chapter)
		if err != nil {
			return nil, fmt.Errorf("check arc scope: %w", err)
		}
		if boundary == nil || !boundary.IsArcEnd || boundary.EndChapter != r.Chapter {
			return nil, fmt.Errorf("arc review chapter must be an arc endpoint")
		}
	}

	affectedSet := make(map[int]struct{})
	for i := range r.Issues {
		issue := &r.Issues[i]
		if strings.TrimSpace(issue.Description) == "" {
			return nil, fmt.Errorf("issue description is required")
		}
		if strings.TrimSpace(issue.Evidence) == "" {
			return nil, fmt.Errorf("issue evidence is required")
		}
		switch issue.Severity {
		case "critical", "error", "warning":
		default:
			return nil, fmt.Errorf("invalid issue severity: %q", issue.Severity)
		}
		if len(issue.Chapters) == 0 && r.Scope == "chapter" {
			issue.Chapters = []int{r.Chapter}
		}
		if len(issue.Chapters) == 0 {
			return nil, fmt.Errorf("issue chapters are required when scope=%s", r.Scope)
		}
		issue.Chapters = uniqueSortedChapters(issue.Chapters)
		for _, chapter := range issue.Chapters {
			switch r.Scope {
			case "chapter":
				if chapter != r.Chapter {
					return nil, fmt.Errorf("chapter review issue must reference chapter %d, got %d", r.Chapter, chapter)
				}
			case "global":
				if chapter <= 0 || chapter > r.Chapter {
					return nil, fmt.Errorf("global review issue chapter %d outside 1-%d", chapter, r.Chapter)
				}
			case "arc":
				if chapter < boundary.StartChapter || chapter > boundary.EndChapter {
					return nil, fmt.Errorf("arc review issue chapter %d outside %d-%d", chapter, boundary.StartChapter, boundary.EndChapter)
				}
			}
			if issue.RequiresChange {
				affectedSet[chapter] = struct{}{}
			}
		}
	}
	if err := validateDimensions(r.Dimensions); err != nil {
		return nil, err
	}
	derived := make([]int, 0, len(affectedSet))
	for chapter := range affectedSet {
		derived = append(derived, chapter)
	}
	slices.Sort(derived)
	if r.Verdict == "accept" && len(derived) > 0 {
		return nil, fmt.Errorf("accept review cannot contain issues with requires_change=true")
	}
	if (r.Verdict == "rewrite" || r.Verdict == "polish") && len(derived) == 0 {
		return nil, fmt.Errorf("verdict=%s requires at least one issue with requires_change=true", r.Verdict)
	}
	r.AffectedChapters = derived
	return boundary, nil
}

func uniqueSortedChapters(chapters []int) []int {
	seen := make(map[int]struct{}, len(chapters))
	for _, chapter := range chapters {
		seen[chapter] = struct{}{}
	}
	result := make([]int, 0, len(seen))
	for chapter := range seen {
		result = append(result, chapter)
	}
	slices.Sort(result)
	return result
}

// reviewFlow là điểm ánh xạ duy nhất giữa phán định văn học và giao thức lưu trữ. verdict do Editor quyết định;
// ở đây chỉ nhận ba kết quả điều khiển mà Router phục hồi được.
func reviewFlow(verdict string) (domain.FlowState, error) {
	switch verdict {
	case "accept":
		return domain.FlowWriting, nil
	case "polish":
		return domain.FlowPolishing, nil
	case "rewrite":
		return domain.FlowRewriting, nil
	default:
		return "", fmt.Errorf("invalid review verdict: %q", verdict)
	}
}

func validateDimensions(dimensions []domain.DimensionScore) error {
	if len(dimensions) == 0 {
		return fmt.Errorf("dimensions must contain at least one evidence-based assessment")
	}

	seen := make(map[string]struct{}, len(dimensions))
	for _, dim := range dimensions {
		name := strings.TrimSpace(dim.Dimension)
		if name == "" {
			return fmt.Errorf("dimension name is required")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate dimension: %s", name)
		}
		seen[name] = struct{}{}
		if dim.Score < 0 || dim.Score > 100 {
			return fmt.Errorf("invalid score for %s: %d", dim.Dimension, dim.Score)
		}
		if strings.TrimSpace(dim.Comment) == "" {
			return fmt.Errorf("dimension comment is required: %s", dim.Dimension)
		}
	}
	return nil
}
