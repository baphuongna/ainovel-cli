package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveVolumeSummaryTool lưu tóm tắt cấp quyển, Editor gọi khi kết thúc quyển.
type SaveVolumeSummaryTool struct {
	store *store.Store
}

func NewSaveVolumeSummaryTool(store *store.Store) *SaveVolumeSummaryTool {
	return &SaveVolumeSummaryTool{store: store}
}

func (t *SaveVolumeSummaryTool) Name() string { return "save_volume_summary" }
func (t *SaveVolumeSummaryTool) Description() string {
	return "Lưu tóm tắt cấp quyển (chế độ trường thiên, gọi khi kết thúc quyển)"
}
func (t *SaveVolumeSummaryTool) Label() string { return "Lưu tóm tắt quyển" }

// Công cụ ghi, cấm đồng thời.
func (t *SaveVolumeSummaryTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveVolumeSummaryTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveVolumeSummaryTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("volume", schema.Int("Số quyển")).Required(),
		schema.Property("title", schema.String("Tiêu đề quyển")).Required(),
		schema.Property("summary", schema.String("Tóm tắt quyển (trong 500 chữ)")).Required(),
		schema.Property("key_events", schema.Array("Sự kiện then chốt trong quyển", schema.String(""))).Required(),
	)
}

func (t *SaveVolumeSummaryTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Volume    int      `json:"volume"`
		Title     string   `json:"title"`
		Summary   string   `json:"summary"`
		KeyEvents []string `json:"key_events"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Volume <= 0 {
		return nil, fmt.Errorf("volume must be > 0")
	}
	if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Summary) == "" {
		return nil, fmt.Errorf("title and summary are required: %w", errs.ErrToolArgs)
	}
	volSummary := domain.VolumeSummary{
		Volume:    a.Volume,
		Title:     a.Title,
		Summary:   a.Summary,
		KeyEvents: a.KeyEvents,
	}
	existing, err := t.store.Summaries.LoadVolumeSummary(a.Volume)
	if err != nil {
		return nil, fmt.Errorf("load volume summary: %w: %w", errs.ErrStoreRead, err)
	}
	if existing != nil {
		if !reflect.DeepEqual(*existing, volSummary) {
			return nil, fmt.Errorf("Tóm tắt quyển %d đã tồn tại và nội dung khác nhau, từ chối ghi đè: %w", a.Volume, errs.ErrToolConflict)
		}
	} else {
		if err := requireAggregateTarget(t.store, flow.AggregateVolumeSummary, a.Volume, 0, 0); err != nil {
			return nil, err
		}
		if err := t.store.Summaries.SaveVolumeSummary(volSummary); err != nil {
			return nil, fmt.Errorf("save volume summary: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.VolumeScope(a.Volume), "volume_summary",
		fmt.Sprintf("summaries/vol-v%02d.json", a.Volume),
	); err != nil {
		return nil, fmt.Errorf("checkpoint volume summary: %w", err)
	}

	result := map[string]any{"saved": true, "type": "volume_summary", "volume": a.Volume}
	// Điểm kích hoạt hoàn thành của đường dẫn chính kết thúc: mảnh ghép cuối của bộ ba thu hồi cuối quyển là tóm tắt quyển, sau khi ghi xuống đĩa nếu toàn sách đã
	// thỏa điều kiện hoàn thành thì MarkComplete ngay tại chỗ (kiểm tra hoàn thành luôn xảy ra trong công cụ mà mảnh sự kiện cuối cùng ghi xuống đĩa,
	// cùng một dạng với commit_chapter; vị từ xem layeredComplete trong commit_chapter.go).
	complete, err := ReconcileLayeredCompletion(t.store)
	if err != nil {
		return nil, fmt.Errorf("reconcile book completion: %w", err)
	}
	if complete {
		result["book_complete"] = true
	}

	return json.Marshal(result)
}
