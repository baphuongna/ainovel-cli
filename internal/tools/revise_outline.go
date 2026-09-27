package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ReviseOutlineTool để Architect dùng nội dung thay thế đầy đủ để sửa đoạn đuôi dàn ý chưa xảy ra.
type ReviseOutlineTool struct {
	store *store.Store
}

func NewReviseOutlineTool(store *store.Store) *ReviseOutlineTool {
	return &ReviseOutlineTool{store: store}
}

func (t *ReviseOutlineTool) Name() string  { return "revise_outline" }
func (t *ReviseOutlineTool) Label() string { return "Sửa dàn ý" }
func (t *ReviseOutlineTool) Description() string {
	return "Sửa dàn ý chưa xảy ra. Từ from_chapter, dùng replacement thay thế toàn bộ kế hoạch sau đó:" +
		"dàn ý phẳng thay thế đoạn đuôi toàn sách, dàn ý phân tầng thay thế đoạn đuôi cung chứa chương đó; chương đã hoàn thành hoặc đang viết không thể di chuyển." +
		"Các chương sau cần giữ phải đặt vào replacement luôn."
}

func (t *ReviseOutlineTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *ReviseOutlineTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *ReviseOutlineTool) StrictSchema() bool                   { return true }

func (t *ReviseOutlineTool) Schema() map[string]any {
	entry := schema.Object(
		schema.Property("title", schema.String("Tiêu đề chương")).Required(),
		schema.Property("core_event", schema.String("Sự kiện cốt lõi của chương")).Required(),
		schema.Property("hook", schema.String("Móc cuối chương")).Required(),
		schema.Property("scenes", schema.Array("Cảnh trong kế hoạch; nếu không có thì dùng mảng rỗng", schema.String(""))).Required(),
	)
	return schema.Object(
		schema.Property("from_chapter", schema.Int("Từ chương này trở đi thay thế kế hoạch chưa xảy ra")).Required(),
		schema.Property("replacement", schema.Array("Thay thế toàn bộ đoạn đuôi; các chương sau cần giữ cũng phải bao gồm", entry)).Required(),
		schema.Property("reason", schema.String("Lý do sửa lần này")).Required(),
	)
}

func (t *ReviseOutlineTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		FromChapter int                   `json:"from_chapter"`
		Replacement []domain.OutlineEntry `json:"replacement"`
		Reason      string                `json:"reason"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if input.FromChapter <= 0 {
		return nil, fmt.Errorf("from_chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if strings.TrimSpace(input.Reason) == "" {
		return nil, fmt.Errorf("reason không được để trống: %w", errs.ErrToolArgs)
	}

	total, err := t.store.ReviseOutline(input.FromChapter, input.Replacement)
	if err != nil {
		return nil, fmt.Errorf("revise outline: %w", err)
	}
	artifact := "outline.json"
	result := map[string]any{
		"revised":      true,
		"from_chapter": input.FromChapter,
		"replacement":  len(input.Replacement),
		"reason":       strings.TrimSpace(input.Reason),
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress after revise: %w: %w", errs.ErrStoreRead, err)
	}
	if progress != nil && progress.Layered {
		artifact = "layered_outline.json"
		outline, outlineErr := t.store.Outline.LoadOutline()
		if outlineErr != nil {
			return nil, fmt.Errorf("load outlined chapters after revise: %w: %w", errs.ErrStoreRead, outlineErr)
		}
		result["dynamic_planning"] = true
		result["outlined_chapters"] = len(outline)
	} else {
		result["total_chapters"] = total
	}
	if _, err := t.store.Checkpoints.AppendArtifact(domain.GlobalScope(), "revise_outline", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint revise_outline: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Outline.ClearOutlineFeedback(); err != nil {
		return nil, fmt.Errorf("clear outline feedback: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(result)
}
